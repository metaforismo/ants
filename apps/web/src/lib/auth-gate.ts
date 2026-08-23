import { redirect } from "next/navigation";

import { safeRedirectPath } from "@/lib/origin";
import { readRawSession, type SessionData } from "@/lib/session";

/**
 * Server-side gate for authenticated pages. Read-only by design: RSC renders
 * cannot mutate cookies, so token renewal stays exclusively inside route
 * handlers (the BFF renews on demand when the client calls through it).
 */
export async function requireSession(nextPath: string): Promise<SessionData> {
  let session: SessionData | undefined;
  try {
    session = await readRawSession();
  } catch {
    session = undefined;
  }
  if (!session) {
    redirect(`/login?next=${encodeURIComponent(safeRedirectPath(nextPath) ?? "/threads")}`);
  }
  return session;
}
