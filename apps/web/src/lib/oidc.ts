import * as oidc from "openid-client";

import { getWebConfig } from "@/lib/config";
let cachedConfiguration: Promise<oidc.Configuration> | undefined;

/**
 * Discovery runs once per process. The transport rule was already enforced
 * by configuration validation (https unless literal loopback); insecure
 * HTTP requests are enabled exactly when the validated issuer is plain
 * HTTP so the local Keycloak fixture works without weakening production
 * posture.
 */
export function clientConfiguration(): Promise<oidc.Configuration> {
  cachedConfiguration ??= discover();
  return cachedConfiguration;
}

async function discover(): Promise<oidc.Configuration> {
  const cfg = getWebConfig();
  const issuer = new URL(cfg.issuerUrl);
  const options =
    issuer.protocol === "http:"
      ? ({ execute: [oidc.allowInsecureRequests] } satisfies oidc.DiscoveryRequestOptions)
      : undefined;
  return oidc.discovery(issuer, cfg.clientId, undefined, undefined, options);
}

export type LoginTransaction = {
  state: string;
  nonce: string;
  codeVerifier: string;
  /** Relative path inside this app to land on after login. Pre-validated. */
  redirectTo: string;
  createdAt: number;
};

/**
 * Builds the IdP authorization redirect (Authorization Code + PKCE, S256).
 * The returned URL is always derived from validated discovery metadata,
 * never assembled by hand.
 */
export async function buildLoginUrl(
  tx: LoginTransaction,
): Promise<{ redirectUrl: URL; codeChallenge: string }> {
  const cfg = getWebConfig();
  const config = await clientConfiguration();
  const codeChallenge = await oidc.calculatePKCECodeChallenge(tx.codeVerifier);
  const redirectUrl = oidc.buildAuthorizationUrl(config, {
    redirect_uri: new URL("/api/auth/callback", cfg.webUrl).toString(),
    scope: cfg.scopes,
    state: tx.state,
    nonce: tx.nonce,
    code_challenge: codeChallenge,
    code_challenge_method: "S256",
  });
  return { redirectUrl, codeChallenge };
}

/**
 * Decodes the payload of an access token this process received directly
 * from the token endpoint (never from user input). Used only for the
 * tenant slug and display name; every security decision about the token
 * itself remains with the API's verified verification pipeline.
 */
export function accessTokenClaims(accessToken: string): Record<string, unknown> {
  const parts = accessToken.split(".");
  if (parts.length !== 3 || !parts[1]) {
    throw new Error("access token from provider is not a JWT");
  }
  let json: string;
  try {
    json = Buffer.from(parts[1], "base64url").toString("utf8");
  } catch {
    throw new Error("access token payload is not base64url");
  }
  const parsed: unknown = JSON.parse(json);
  if (!parsed || typeof parsed !== "object") {
    throw new Error("access token payload is not an object");
  }
  return parsed as Record<string, unknown>;
}

export type IdentityFromTokens = {
  sub: string;
  /** Always present: preferred_username claim, or the subject as fallback. */
  username: string;
  tenantSlug: string;
};

/**
 * Extracts the Ants identity from a freshly received token set: subject
 * comes from the library-validated ID token; the tenant slug comes from
 * the configured claim inside the access token issued seconds ago by the
 * same provider response.
 */
export function identityFromTokenSet(
  tokens: oidc.TokenEndpointResponse & oidc.TokenEndpointResponseHelpers,
): IdentityFromTokens {
  const idClaims = tokens.claims();
  const sub = idClaims?.sub;
  if (!sub) {
    throw new Error("provider token response carries no subject");
  }
  const accessClaims = accessTokenClaims(tokens.access_token);
  const tenantRaw = accessClaims[getWebConfig().tenantClaim];
  if (typeof tenantRaw !== "string" || tenantRaw.length === 0) {
    throw new Error("access token carries no usable tenant claim");
  }
  const username =
    typeof idClaims?.preferred_username === "string" ? idClaims.preferred_username : sub;
  return { sub, username, tenantSlug: tenantRaw };
}
