import { NextResponse } from "next/server";
import * as oidc from "openid-client";

import { getWebConfig } from "@/lib/config";
import { clientConfiguration } from "@/lib/oidc";
import { isSameOrigin } from "@/lib/origin";
import { clearSession, readRawSession } from "@/lib/session";

export const dynamic = "force-dynamic";

/**
 * RP-initiated logout: destroy the local session unconditionally, then send
 * the browser to the provider's end-session endpoint (metadata-derived) so
 * the IdP session dies too. The local session is cleared even when provider
 * metadata is unreachable — logout must never depend on logout succeeding
 * somewhere else.
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

  let idToken: string | undefined;
  try {
    idToken = (await readRawSession())?.idToken;
  } catch {
    // An unusable session still logs out locally; no hint is required.
  }
  await clearSession();

  try {
    const endSession = oidc.buildEndSessionUrl(await clientConfiguration(), {
      ...(idToken ? { id_token_hint: idToken } : {}),
      post_logout_redirect_uri: new URL("/", cfg.webUrl).toString(),
    });
    return NextResponse.redirect(endSession.toString());
  } catch (err) {
    console.error("ants-web: end-session URL unavailable", err instanceof Error ? err.message : err);
    return NextResponse.redirect(new URL("/login", cfg.webUrl));
  }
}
