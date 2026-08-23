import { NextResponse } from "next/server";
import * as oidc from "openid-client";

import { buildLoginUrl } from "@/lib/oidc";
import { safeRedirectPath } from "@/lib/origin";
import { writeAuthTransaction } from "@/lib/session";

export const dynamic = "force-dynamic";

export async function GET(request: Request): Promise<Response> {
  const redirectTo = safeRedirectPath(new URL(request.url).searchParams.get("next")) ?? "/threads";
  try {
    const tx = {
      state: oidc.randomState(),
      nonce: oidc.randomNonce(),
      codeVerifier: oidc.randomPKCECodeVerifier(),
      redirectTo,
      createdAt: Math.floor(Date.now() / 1000),
    };
    await writeAuthTransaction(tx);
    const { redirectUrl } = await buildLoginUrl(tx);
    return NextResponse.redirect(redirectUrl.toString());
  } catch (err) {
    // Discovery or configuration failure: back to the login screen with a
    // typed key. Provider details stay in server logs, never in the URL.
    console.error("ants-web: login redirect failed", err instanceof Error ? err.message : err);
    return NextResponse.redirect(new URL("/login?error=provider_unavailable", request.url));
  }
}

