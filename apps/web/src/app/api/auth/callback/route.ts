import { NextResponse } from "next/server";
import * as oidc from "openid-client";

import { getWebConfig } from "@/lib/config";
import { clientConfiguration, identityFromTokenSet } from "@/lib/oidc";
import { safeRedirectPath } from "@/lib/origin";
import {
  clearAuthTransaction,
  readAuthTransaction,
  sessionFromTokens,
  writeSession,
} from "@/lib/session";
import { bootstrapTenant } from "@/lib/onboarding";

export const dynamic = "force-dynamic";

/**
 * Authorization response handler. Validates state (CSRF), exchanges the
 * code with the PKCE verifier, validates the ID token (issuer, audience,
 * nonce — enforced by openid-client), then establishes the sealed session.
 * First login on a new tenant claim bootstraps the tenant through the API's
 * documented open endpoint before the first authenticated call.
 */
export async function GET(request: Request): Promise<Response> {
  const cfg = getWebConfig();
  // All protocol redirects stay on the validated configured origin. Next
  // may normalize the request's Host (loopback forms collapse to
  // "localhost"), and following request.url would bounce the browser across
  // cookie-scoped origins mid-flow.
  const fail = (key: string) =>
    NextResponse.redirect(new URL(`/login?error=${encodeURIComponent(key)}`, cfg.webUrl));

  // The authorization request carried redirect_uri from cfg.webUrl; the
  // exchange must present byte-identical redirect_uri or the provider
  // refuses with invalid_grant. Rebuilding from the validated origin (not
  // request.url, whose host may be normalized) keeps both sides equal.
  const callbackUrl = new URL("/api/auth/callback", cfg.webUrl);
  const incoming = new URL(request.url);
  for (const [key, value] of incoming.searchParams) {
    callbackUrl.searchParams.set(key, value);
  }

  const tx = await readAuthTransaction<{
    state: string;
    nonce: string;
    codeVerifier: string;
    redirectTo: string;
    createdAt: number;
  }>();
  await clearAuthTransaction();
  if (!tx) return fail("login_expired");

  try {
    const config = await clientConfiguration();
    const tokens = await oidc.authorizationCodeGrant(config, callbackUrl, {
      pkceCodeVerifier: tx.codeVerifier,
      expectedState: tx.state,
      expectedNonce: tx.nonce,
    });
    const identity = identityFromTokenSet(tokens);
    const session = sessionFromTokens(identity, tokens);

    // The tenant named by the verified token must exist before any
    // authenticated call succeeds; create it when missing (open bootstrap
    // endpoint, documented limitation until memberships ship).
    await bootstrapTenant(cfg.apiBaseUrl, session.accessToken, identity.tenantSlug);

    await writeSession(session);
    return NextResponse.redirect(new URL(safeRedirectPath(tx.redirectTo) ?? "/threads", cfg.webUrl));
  } catch (err) {
    console.error(
      "ants-web: authorization callback failed",
      err instanceof Error ? err.message : err,
    );
    return fail(callbackErrorKey(err));
  }
}

function callbackErrorKey(err: unknown): string {
  if (err instanceof Error) {
    if ("error" in err && (err as { error?: unknown }).error === "invalid_grant") {
      return "code_exchange_rejected";
    }
    if (err.message.includes("state mismatch")) return "state_mismatch";
    if (err.message.startsWith("access token carries no usable tenant")) {
      return "missing_tenant_claim";
    }
    if (err.message.includes("tenant bootstrap")) return "tenant_bootstrap_failed";
  }
  return "provider_unavailable";
}
