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

export type SessionData = {
  accessToken: string;
  refreshToken?: string;
  idToken?: string;
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
  const store = await cookies();
  store.set(SESSION_COOKIE, seal(JSON.stringify(session), getWebConfig().sessionKey), {
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
    (s.refreshToken === undefined || typeof s.refreshToken === "string") &&
    (s.idToken === undefined || typeof s.idToken === "string")
  );
}

/**
 * Serializes all renewals process-wide. Keycloak rotates refresh tokens on
 * every use; two concurrent BFF requests refreshing the same session would
 * race and one would invalidate the other's rotation. Single-flight makes
 * the second request observe the already-renewed session instead.
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
 * Loads the current session, silently renewing the access token when it is
 * near expiry and a refresh token exists. Renewal failure with the provider
 * rejecting the grant converts to SessionExpiredError so callers render the
 * re-authentication state instead of retrying forever.
 */
export async function loadFreshSession(): Promise<LoadedSession> {
  const session = await readRawSession();
  if (!session) throw new SessionExpiredError();

  const needsRenewal =
    session.tokenExpiresAt - RENEW_LEEWAY_SECONDS <= nowSeconds() &&
    session.refreshToken !== undefined;

  if (!needsRenewal) return { session, renewed: false };

  return enqueueRenew(async () => {
    // Re-read after acquiring the lock: another request may have renewed
    // this same session while we waited in the queue.
    const latest = await readRawSession();
    if (!latest) throw new SessionExpiredError();
    if (latest.tokenExpiresAt - RENEW_LEEWAY_SECONDS > nowSeconds()) {
      return { session: latest, renewed: true };
    }
    if (!latest.refreshToken) throw new SessionExpiredError();
    let tokens: TokenSet;
    try {
      tokens = await oidc.refreshTokenGrant(await clientConfiguration(), latest.refreshToken);
    } catch (err) {
      if (isInvalidGrant(err)) throw new SessionExpiredError();
      throw err;
    }
    const next: SessionData = {
      ...latest,
      accessToken: tokens.access_token,
      refreshToken:
        typeof tokens.refresh_token === "string" ? tokens.refresh_token : latest.refreshToken,
      idToken: typeof tokens.id_token === "string" ? tokens.id_token : latest.idToken,
      tokenExpiresAt: epochAfter(tokens.expiresIn()),
    };
    await writeSession(next);
    return { session: next, renewed: true };
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
    idToken: typeof tokens.id_token === "string" ? tokens.id_token : undefined,
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
