import { redirect } from "next/navigation";

import { readRawSession } from "@/lib/session";

export const dynamic = "force-dynamic";

/** Entry point: authenticated sessions land on the thread list. */
export default async function Home(): Promise<never> {
  const authenticated = await readRawSession()
    .then((session) => session !== undefined)
    .catch(() => false);
  redirect(authenticated ? "/threads" : "/login");
}
