import { NextResponse } from "next/server";

import { loadFreshSession, SessionExpiredError } from "@/lib/session";

export const dynamic = "force-dynamic";

/**
 * Session status probe for the browser: identity metadata only. Tokens,
 * tenant ids, and provider details never leave the server; the client uses
 * this to distinguish authenticated / expired / anonymous states.
 */
export async function GET(): Promise<Response> {
  try {
    const { session } = await loadFreshSession();
    return NextResponse.json({
      status: "authenticated" as const,
      username: session.username,
      tenantSlug: session.tenantSlug,
      tokenExpiresAt: session.tokenExpiresAt,
    });
  } catch (err) {
    if (err instanceof SessionExpiredError) {
      return NextResponse.json({ status: "expired" as const }, { status: 200 });
    }
    console.error("ants-web: session probe failed", err instanceof Error ? err.message : err);
    return NextResponse.json({ status: "unavailable" as const }, { status: 503 });
  }
}
