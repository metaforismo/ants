import { AppShell } from "@/components/app-shell";
import { requireSession } from "@/lib/auth-gate";

import { AppProviders } from "../providers";
import { ThreadsView } from "./threads-view";

export const dynamic = "force-dynamic";

export const metadata = { title: "Threads" };

export default async function ThreadsPage() {
  const session = await requireSession("/threads");
  return (
    <AppShell username={session.username} tenantSlug={session.tenantSlug}>
      <AppProviders>
        <ThreadsView />
      </AppProviders>
    </AppShell>
  );
}
