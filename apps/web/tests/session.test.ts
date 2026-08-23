import { describe, expect, it } from "vitest";
import type { TokenEndpointResponse, TokenEndpointResponseHelpers } from "openid-client";

import { accessTokenClaims, identityFromTokenSet } from "@/lib/oidc";
import { epochAfter, nowSeconds, selectFreshSession, sessionFromTokens } from "@/lib/session";

process.env.ANTS_WEB_URL ??= "http://127.0.0.1:3100";
process.env.ANTS_API_BASE_URL ??= "http://127.0.0.1:8081";
process.env.ANTS_OIDC_ISSUER_URL ??= "http://127.0.0.1:54331/realms/ants";
process.env.ANTS_OIDC_CLIENT_ID ??= "ants-web";
process.env.ANTS_SESSION_KEY ??= Buffer.alloc(32, 1).toString("base64");

type TokenSet = TokenEndpointResponse & TokenEndpointResponseHelpers;

function tokenSet(
  idClaims: Record<string, unknown>,
  accessPayload: Record<string, unknown>,
): TokenSet {
  const encode = (obj: unknown) => Buffer.from(JSON.stringify(obj)).toString("base64url");
  return {
    access_token: `${encode({ alg: "RS256" })}.${encode(accessPayload)}.sig`,
    token_type: "Bearer",
    expires_in: 900,
    refresh_token: "refresh-fixture",
    scope: "openid",
    claims: () => idClaims,
    expiresIn: () => 900,
  } as unknown as TokenSet;
}

describe("accessTokenClaims", () => {
  it("decodes only structurally valid JWT payloads", () => {
    const payload = { sub: "u1", ants_tenant: "acme" };
    const jwt = [
      Buffer.from(JSON.stringify({ alg: "RS256" })).toString("base64url"),
      Buffer.from(JSON.stringify(payload)).toString("base64url"),
      "signature",
    ].join(".");
    expect(accessTokenClaims(jwt)).toMatchObject(payload);
    expect(() => accessTokenClaims("not-a-jwt")).toThrow(/not a JWT/);
  });
});

describe("identityFromTokenSet", () => {
  it("derives identity from the validated id-token subject and access-token tenant", () => {
    const tokens = tokenSet({ sub: "user-1", preferred_username: "alice" }, { ants_tenant: "acme" });
    expect(identityFromTokenSet(tokens)).toEqual({
      sub: "user-1",
      username: "alice",
      tenantSlug: "acme",
    });
  });

  it("falls back to the subject when preferred_username is absent", () => {
    const identity = identityFromTokenSet(tokenSet({ sub: "user-9" }, { ants_tenant: "acme" }));
    expect(identity.username).toBe("user-9");
  });

  it("refuses a token set without a subject", () => {
    const noSubject = { ...tokenSet({}, { ants_tenant: "acme" }), claims: () => ({}) };
    expect(() => identityFromTokenSet(noSubject as unknown as TokenSet)).toThrow(/no subject/);
  });
});

describe("sessionFromTokens", () => {
  it("builds a session bound to the provider expiry", () => {
    const before = nowSeconds();
    const session = sessionFromTokens(
      { sub: "user-1", username: "alice", tenantSlug: "acme" },
      tokenSet({}, { ants_tenant: "acme" }),
    );
    expect(session.tenantSlug).toBe("acme");
    expect(session.tokenExpiresAt).toBeGreaterThan(before);
    expect(session.refreshToken).toBe("refresh-fixture");
    expect(session.createdAt).toBeGreaterThanOrEqual(before);
  });

  it("refuses tenant claims outside the cookie-safe grammar", () => {
    expect(() =>
      sessionFromTokens(
        { sub: "user-1", username: "alice", tenantSlug: "bad;path=/ value" },
        tokenSet({}, {}),
      ),
    ).toThrow(/grammar/);
  });

  it("keeps renewal leeway strictly positive even without provider expiry", () => {
    expect(epochAfter(undefined)).toBeGreaterThanOrEqual(nowSeconds() + 1);
    expect(epochAfter(0)).toBe(nowSeconds() + 1);
  });
});

describe("selectFreshSession", () => {
  const base = {
    accessToken: "a",
    refreshToken: "r",
    sub: "s",
    username: "u",
    tenantSlug: "t",
    createdAt: 1000,
  };
  const now = 5000;

  it("uses the request cookie while it is comfortably valid", () => {
    const d = selectFreshSession({ ...base, tokenExpiresAt: now + 300 }, undefined, now);
    expect(d).toEqual({ kind: "fresh", session: { ...base, tokenExpiresAt: now + 300 }, adopted: false });
  });

  it("renews inside the leeway window", () => {
    const d = selectFreshSession({ ...base, tokenExpiresAt: now + 30 }, undefined, now);
    expect(d.kind).toBe("renew");
  });

  it("expires without a refresh token once the access token is stale", () => {
    const d = selectFreshSession(
      { ...base, refreshToken: undefined, tokenExpiresAt: now + 10 },
      undefined,
      now,
    );
    expect(d).toEqual({ kind: "expired" });
  });

  it("adopts a strictly newer cached state instead of replaying a rotated refresh token", () => {
    const raw = { ...base, tokenExpiresAt: now + 10 };
    const cached = { ...base, tokenExpiresAt: now + 800 };
    const d = selectFreshSession(raw, cached, now);
    expect(d).toEqual({ kind: "fresh", session: cached, adopted: true });
  });

  it("ignores cache entries that are not newer than the cookie", () => {
    const raw = { ...base, tokenExpiresAt: now + 60 };
    const cached = { ...base, tokenExpiresAt: raw.tokenExpiresAt - 1 };
    const d = selectFreshSession(raw, cached, now);
    expect(d.kind).toBe("fresh");
    if (d.kind === "fresh") expect(d.session).toEqual(raw);
  });
});
