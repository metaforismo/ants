import { AppShell } from "@/components/app-shell";
import { requireSession } from "@/lib/auth-gate";

import { WorkspaceView } from "./workspace-view";
import { AppProviders } from "../../providers";

export const dynamic = "force-dynamic";

export const metadata = { title: "Thread" };

export default async function ThreadWorkspacePage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const session = await requireSession(`/threads/${encodeURIComponent(id)}`);
  return (
    <AppShell username={session.username} tenantSlug={session.tenantSlug}>
      <AppProviders>
        <WorkspaceView threadId={id} />
      </AppProviders>
    </AppShell>
  );
}
