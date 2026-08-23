import type { TokenEndpointResponse, TokenEndpointResponseHelpers } from "openid-client";
import { cookies } from "next/headers";
import * as oidc from "openid-client";

import { AUTH_TX_COOKIE, SESSION_COOKIE, getWebConfig } from "@/lib/config";
import { clientConfiguration, type IdentityFromTokens } from "@/lib/oidc";
import { seal, unseal } from "@/lib/seal";

/**
 * Server-side session handling. The browser holds one opaque sealed cookie;
 * access and refresh tokens exist only inside server memory for the duration
 * of a request and inside the AES-256-GCM sealed cookie value. No token ever
 * reaches client JavaScript, localStorage, or a URL.
 */

/** Absolute session lifetime; renewal refreshes tokens but not this window. */
const SESSION_TTL_SECONDS = 8 * 60 * 60;
/** Refresh the access token this many seconds before its real expiry. */
const RENEW_LEEWAY_SECONDS = 45;
/**
 * Browsers silently refuse cookies beyond ~4 KiB, which would surface as a
 * mysteriously sessionless console. The budget leaves room for name and
 * attributes; writeSession fails loudly instead of overflowing it. The ID
 * token is therefore NOT stored (it exists only as an optional logout hint)
 * — see ADR-0020.
 */
const MAX_COOKIE_VALUE_BYTES = 3800;

export type SessionData = {
  accessToken: string;
  refreshToken?: string;
  /** Epoch seconds at which the access token expires (provider-issued). */
  tokenExpiresAt: number;
  sub: string;
  username: string;
  tenantSlug: string;
  createdAt: number;
};

export class SessionExpiredError extends Error {
  constructor() {
    super("session expired or invalid");
    this.name = "SessionExpiredError";
  }
}

type TokenSet = TokenEndpointResponse & TokenEndpointResponseHelpers;

function cookieOptions(maxAge: number) {
  return {
    httpOnly: true,
    secure: getWebConfig().secureCookies,
    sameSite: "lax" as const,
    path: "/",
    maxAge,
  };
}

export async function writeSession(session: SessionData): Promise<void> {
  const sealed = seal(JSON.stringify(session), getWebConfig().sessionKey);
  if (Buffer.byteLength(sealed, "utf8") > MAX_COOKIE_VALUE_BYTES) {
    // Fail loudly: a silently dropped cookie would present as an endless
    // login loop, never as an actionable operator error.
    throw new Error(
      `sealed session is ${Buffer.byteLength(sealed, "utf8")} bytes, over the ${MAX_COOKIE_VALUE_BYTES}-byte cookie budget; shorten provider token lifetimes or claims`,
    );
  }
  const store = await cookies();
  store.set(SESSION_COOKIE, sealed, {
    ...cookieOptions(SESSION_TTL_SECONDS),
  });
}

export async function clearSession(): Promise<void> {
  const store = await cookies();
  store.set(SESSION_COOKIE, "", { ...cookieOptions(0) });
}

export async function readRawSession(): Promise<SessionData | undefined> {
  const store = await cookies();
  const raw = store.get(SESSION_COOKIE)?.value;
  if (!raw) return undefined;
  try {
    const parsed: unknown = JSON.parse(unseal(raw, getWebConfig().sessionKey));
    if (!isSessionData(parsed)) throw new SessionExpiredError();
    return parsed;
  } catch (err) {
    if (err instanceof SessionExpiredError) throw err;
    // Tampered, stale-format, or wrong-key cookie: an unusable session.
    throw new SessionExpiredError();
  }
}

function isSessionData(value: unknown): value is SessionData {
  if (!value || typeof value !== "object") return false;
  const s = value as Partial<SessionData>;
  return (
    typeof s.accessToken === "string" &&
    s.accessToken.length > 0 &&
    typeof s.tokenExpiresAt === "number" &&
    typeof s.sub === "string" &&
    typeof s.username === "string" &&
    typeof s.tenantSlug === "string" &&
    typeof s.createdAt === "number" &&
    (s.refreshToken === undefined || typeof s.refreshToken === "string")
  );
}

/**
 * Serializes all renewals process-wide. Keycloak rotates refresh tokens on
 * every use; two concurrent BFF requests refreshing the same session would
 * race and one would invalidate the other's rotation. Single-flight makes
 * the second request wait instead of firing its own grant.
 */
let renewQueue: Promise<unknown> = Promise.resolve();

function enqueueRenew<T>(task: () => Promise<T>): Promise<T> {
  const run = renewQueue.then(task, task);
  renewQueue = run.catch(() => undefined);
  return run;
}

export type LoadedSession = {
  session: SessionData;
  renewed: boolean;
};

/**
 * Renewal results are also kept in a process-local map keyed by immutable
 * session identity (sub + tenant + creation time). A cookie is frozen at
 * the moment its request arrived: without this map, a second concurrent
 * request re-reads its own stale cookie and replays the pre-rotation
 * refresh token, which the provider rejects — destroying the very session
 * the first request just renewed. Processes exchange nothing here; each
 * converges through its own renewals (ADR-0020 consequence).
 */
const renewedSessions = new Map<string, SessionData>();
const RENEWED_CACHE_LIMIT = 512;

function sessionKey(s: SessionData): string {
  return `${s.sub}\u0000${s.tenantSlug}\u0000${s.createdAt}`;
}

function rememberRenewed(next: SessionData): void {
  if (!renewedSessions.has(sessionKey(next)) && renewedSessions.size >= RENEWED_CACHE_LIMIT) {
    const oldest = renewedSessions.keys().next().value;
    if (oldest !== undefined) renewedSessions.delete(oldest);
  }
  renewedSessions.set(sessionKey(next), next);
}

export type FreshDecision =
  | { kind: "fresh"; session: SessionData; adopted: boolean }
  | { kind: "renew"; session: SessionData & { refreshToken: string } }
  | { kind: "expired" };

/**
 * Pure renewal decision for one request: prefer a strictly newer cached
 * state over this request's frozen cookie, then decide between using it,
 * refreshing, or declaring the session dead. Exported for tests.
 */
export function selectFreshSession(
  raw: SessionData,
  cached: SessionData | undefined,
  now: number,
): FreshDecision {
  const adopted = cached !== undefined && cached.tokenExpiresAt > raw.tokenExpiresAt;
  const current = adopted ? cached : raw;
  if (current.tokenExpiresAt - RENEW_LEEWAY_SECONDS > now) {
    return { kind: "fresh", session: current, adopted };
  }
  if (!current.refreshToken) return { kind: "expired" };
  return { kind: "renew", session: { ...current, refreshToken: current.refreshToken } };
}

/** Best-effort cookie convergence after adopting cached state. */
async function persistAdopted(session: SessionData): Promise<void> {
  try {
    await writeSession(session);
  } catch (err) {
    console.error(
      "ants-web: session cookie convergence skipped",
      err instanceof Error ? err.message : err,
    );
  }
}

/**
 * Loads the current session, silently renewing the access token when it is
 * near expiry and a refresh token exists. Renewal failure with the provider
 * rejecting the grant converts to SessionExpiredError so callers render the
 * re-authentication state instead of retrying forever.
 */
export async function loadFreshSession(): Promise<LoadedSession> {
  const raw = await readRawSession();
  if (!raw) throw new SessionExpiredError();

  let decision = selectFreshSession(raw, renewedSessions.get(sessionKey(raw)), nowSeconds());
  if (decision.kind === "expired") throw new SessionExpiredError();

  if (decision.kind === "fresh" && !decision.adopted) {
    return { session: decision.session, renewed: false };
  }
  if (decision.kind === "fresh") {
    await persistAdopted(decision.session);
    return { session: decision.session, renewed: true };
  }

  // Near expiry: serialize behind the queue, then re-derive the decision —
  // a sibling request may have renewed while we waited.
  return enqueueRenew(async () => {
    const latest = await readRawSession();
    if (!latest) throw new SessionExpiredError();
    decision = selectFreshSession(latest, renewedSessions.get(sessionKey(latest)), nowSeconds());

    switch (decision.kind) {
      case "expired":
        throw new SessionExpiredError();
      case "fresh": {
        if (decision.adopted) await persistAdopted(decision.session);
        return { session: decision.session, renewed: decision.adopted };
      }
      case "renew": {
        const current = decision.session;
        let tokens: TokenSet;
        try {
          tokens = await oidc.refreshTokenGrant(await clientConfiguration(), current.refreshToken);
        } catch (err) {
          if (isInvalidGrant(err)) throw new SessionExpiredError();
          throw err;
        }
        const next: SessionData = {
          ...current,
          accessToken: tokens.access_token,
          refreshToken:
            typeof tokens.refresh_token === "string" ? tokens.refresh_token : current.refreshToken,
          tokenExpiresAt: epochAfter(tokens.expiresIn()),
        };
        rememberRenewed(next);
        await writeSession(next);
        return { session: next, renewed: true };
      }
    }
  });
}

/** openid-client surfaces OAuth error responses as errors carrying `error`. */
function isInvalidGrant(err: unknown): boolean {
  return (
    err instanceof Error &&
    "error" in err &&
    typeof (err as { error?: unknown }).error === "string" &&
    (err as { error: string }).error === "invalid_grant"
  );
}

export function epochAfter(seconds: number | undefined): number {
  return nowSeconds() + Math.max(1, Math.trunc(seconds ?? 300));
}

export function nowSeconds(): number {
  return Math.floor(Date.now() / 1000);
}

/** Builds the initial session from a freshly received provider token set. */
export function sessionFromTokens(identity: IdentityFromTokens, tokens: TokenSet): SessionData {
  // Grammar bound: refuse to persist a tenant slug that could smuggle cookie
  // structure; the API grammar-checks it again on every request anyway.
  if (!/^[\w][\w.-]{0,62}$/.test(identity.tenantSlug)) {
    throw new Error("provider returned a tenant claim outside the accepted grammar");
  }
  return {
    accessToken: tokens.access_token,
    refreshToken: typeof tokens.refresh_token === "string" ? tokens.refresh_token : undefined,
    tokenExpiresAt: epochAfter(tokens.expiresIn()),
    sub: identity.sub,
    username: identity.username,
    tenantSlug: identity.tenantSlug,
    createdAt: nowSeconds(),
  };
}

// ---- login transaction (short-lived pre-auth state) ----

export async function writeAuthTransaction(tx: Record<string, string | number>): Promise<void> {
  const store = await cookies();
  store.set(AUTH_TX_COOKIE, seal(JSON.stringify(tx), getWebConfig().sessionKey), {
    ...cookieOptions(600),
  });
}

export async function readAuthTransaction<
  T extends Record<string, string | number>,
>(): Promise<T | undefined> {
  const store = await cookies();
  const raw = store.get(AUTH_TX_COOKIE)?.value;
  if (!raw) return undefined;
  try {
    const parsed: unknown = JSON.parse(unseal(raw, getWebConfig().sessionKey));
    if (!parsed || typeof parsed !== "object") return undefined;
    return parsed as T;
  } catch {
    // Tampered or expired transaction: login must restart cleanly.
    return undefined;
  }
}

export async function clearAuthTransaction(): Promise<void> {
  const store = await cookies();
  store.set(AUTH_TX_COOKIE, "", { ...cookieOptions(0) });
}
