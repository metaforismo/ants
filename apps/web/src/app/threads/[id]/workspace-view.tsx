"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { Composer } from "@/app/threads/[id]/composer";
import { MessageList } from "@/app/threads/[id]/message-list";
import { RunPanel } from "@/app/threads/[id]/run-panel";
import { ExpiredNotice, humanStatus } from "@/app/threads/threads-view";
import { StatusBadge } from "@/components/status-badge";
import { useActiveRun } from "@/hooks/use-active-run";
import { api, errorCode } from "@/lib/client-api";
import { threadKind } from "@/lib/status";

const THREAD_LIVE = new Set(["planning", "executing", "reviewing", "fixing"]);

/**
 * Thread workspace: the one screen where the conversation, its runs, the
 * live event trail, and the terminal report are legible together.
 */
export function WorkspaceView({ threadId }: { threadId: string }) {
  const queryClient = useQueryClient();
  const { runId, setActiveRun } = useActiveRun(threadId);

  const threadQuery = useQuery({
    queryKey: ["thread", threadId],
    queryFn: () => api.getThread(threadId),
    refetchInterval: 5000,
  });

  const messagesQuery = useQuery({
    queryKey: ["messages", threadId],
    queryFn: () => api.listMessages(threadId, 0),
    refetchInterval: THREAD_LIVE.has(threadQuery.data?.status ?? "") ? 3000 : false,
  });

  const appendMessage = useMutation({
    mutationFn: (content: string) => api.appendMessage(threadId, content),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["messages", threadId] });
      await queryClient.invalidateQueries({ queryKey: ["thread", threadId] });
    },
  });

  if (threadQuery.isPending && messagesQuery.isPending) {
    return (
      <div aria-hidden="true" data-testid="workspace-loading">
        <div className="skeleton-row" />
        <div className="skeleton-row" />
        <div className="skeleton-row" />
      </div>
    );
  }

  if (threadQuery.isError) {
    return <ThreadError error={threadQuery.error} onRetry={() => threadQuery.refetch()} />;
  }

  const thread = threadQuery.data;
  if (!thread) {
    return null;
  }

  const messages = messagesQuery.data?.messages ?? [];
  const threadLive = THREAD_LIVE.has(thread.status);

  return (
    <div style={{ display: "grid", gap: 20 }}>
      <header className="page-head" style={{ marginBottom: 0 }}>
        <div style={{ minWidth: 0 }}>
          <h1>{thread.title}</h1>
          <p className="mono" style={{ color: "var(--ink-3)", margin: "2px 0 0", fontSize: 12 }}>
            {thread.id}
          </p>
        </div>
        <StatusBadge label={humanStatus(thread.status)} kind={threadKind(thread.status)} />
      </header>

      {runId ? (
        <RunPanel runId={runId} />
      ) : (
        <section aria-label="Start a run" className="card" style={{ padding: 16 }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 12,
              flexWrap: "wrap",
            }}
          >
            <p style={{ margin: 0, color: "var(--ink-2)" }}>
              {messages.length === 0
                ? "Describe the outcome below; then start a run to delegate it."
                : threadLive
                  ? "A run is executing for this thread in another view. Its panel reattaches here once this tab has seen it."
                  : "The described outcome is ready to be delegated to a run."}
            </p>
            {!threadLive ? (
              <StartRunButton threadId={threadId} hasMessages={messages.length > 0} onStarted={setActiveRun} />
            ) : null}
          </div>
        </section>
      )}

      <section aria-label="Conversation" style={{ display: "grid", gap: 12 }}>
        <h2>Conversation</h2>
        <MessageList
          messages={messages.map((m) => ({
            id: m.id,
            seq: m.seq,
            role: m.role,
            content: m.content,
            createdAt: m.created_at,
          }))}
          loading={messagesQuery.isPending}
          error={
            errorCode(messagesQuery.error) === "session_expired"
              ? ("session_expired" as const)
              : messagesQuery.isError
                ? ("error" as const)
                : undefined
          }
          onRetry={() => messagesQuery.refetch()}
        />
        <Composer disabled={appendMessage.isPending} onSubmit={(content) => appendMessage.mutateAsync(content)} />
      </section>
    </div>
  );
}

function ThreadError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const status =
    error != null && typeof error === "object" && "status" in error
      ? (error as { status?: number }).status
      : undefined;
  if (status === 403 || status === 404) {
    // Uniform copy: no existence oracle across tenants (ADR-0004).
    return (
      <div role="note" className="state-panel card" data-testid="workspace-unavailable">
        <p className="state-title">Not available</p>
        <p>This resource is not available in this workspace.</p>
        <a className="btn" href="/threads">
          Back to threads
        </a>
      </div>
    );
  }
  if (errorCode(error) === "session_expired") {
    return <ExpiredNotice />;
  }
  return (
    <div role="alert" className="state-panel card" data-testid="workspace-error">
      <p className="state-title">Could not load this thread</p>
      <button type="button" className="btn" onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

function StartRunButton({
  threadId,
  hasMessages,
  onStarted,
}: {
  threadId: string;
  hasMessages: boolean;
  onStarted: (runId: string) => void;
}) {
  const queryClient = useQueryClient();
  // The idempotency key identifies one logical intent ("start THE run for
  // this attempt"): retries of an ambiguous request replay the same run
  // instead of double-starting. A new intent after success gets a new key.
  const keyRef = useRef<string | undefined>(undefined);
  const [starting, setStarting] = useState(false);

  async function start() {
    setStarting(true);
    try {
      keyRef.current ??= crypto.randomUUID();
      const run = await api.startRun(threadId, keyRef.current);
      keyRef.current = undefined;
      onStarted(run.id);
      await queryClient.invalidateQueries({ queryKey: ["thread", threadId] });
    } catch {
      // The refused intent stays retryable with a fresh key; typed problem
      // rendering is owned by the surrounding surfaces.
      keyRef.current = undefined;
    } finally {
      setStarting(false);
    }
  }

  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
      {!hasMessages ? (
        <span style={{ fontSize: 12, color: "var(--ink-3)" }}>Needs at least one message</span>
      ) : null}
      <button
        type="button"
        className="btn btn-primary"
        data-testid="start-run"
        disabled={!hasMessages || starting}
        onClick={() => void start()}
      >
        Start run
      </button>
    </span>
  );
}
