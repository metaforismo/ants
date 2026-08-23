import { NextResponse } from "next/server";
import * as oidc from "openid-client";

import { getWebConfig } from "@/lib/config";
import { clientConfiguration } from "@/lib/oidc";
import { isSameOrigin } from "@/lib/origin";
import { clearSession } from "@/lib/session";

export const dynamic = "force-dynamic";

/**
 * RP-initiated logout: destroy the local session unconditionally, then send
 * the browser to the provider's end-session endpoint (metadata-derived) so
 * the IdP session dies too. The local session is cleared even when provider
 * metadata is unreachable — logout must never depend on logout succeeding
 * somewhere else. No id_token_hint is sent: the ID token is deliberately not
 * stored (cookie budget, ADR-0020), so the provider may ask the user to
 * confirm ending its session.
 */
export async function POST(request: Request): Promise<Response> {
  const cfg = getWebConfig();
  if (!isSameOrigin(request, [cfg.webUrl.origin])) {
    return NextResponse.json(
      {
        type: "about:blank",
        code: "csrf_rejected",
        title: "Cross-origin request refused",
        status: 403,
      },
      { status: 403 },
    );
  }

  await clearSession();

  try {
    const endSession = oidc.buildEndSessionUrl(await clientConfiguration(), {
      post_logout_redirect_uri: new URL("/", cfg.webUrl).toString(),
    });
    return NextResponse.redirect(endSession.toString());
  } catch (err) {
    console.error("ants-web: end-session URL unavailable", err instanceof Error ? err.message : err);
    return NextResponse.redirect(new URL("/login", cfg.webUrl));
  }
}
