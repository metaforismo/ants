"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { EventTrail } from "@/app/threads/[id]/event-trail";
import { ReportView } from "@/app/threads/[id]/report-view";
import { StatusBadge } from "@/components/status-badge";
import { humanStatus } from "@/app/threads/threads-view";
import { api, errorCode } from "@/lib/client-api";
import { runKind, taskKind } from "@/lib/status";

const TERMINAL_RUN = new Set(["completed", "failed", "cancelled"]);

/**
 * Live run panel. While the run executes it polls the durable record on a
 * bounded interval; the event stream resumes from its own `seq` cursor so a
 * reload or reconnect never replays or skips history.
 *
 * `/v1` exposes runs by id only (no list-by-thread yet), so the panel is
 * anchored to the run id captured from this console's own start action,
 * kept per browser tab in sessionStorage. Reopening elsewhere shows the
 * truthful thread status while the run completes without a live panel;
 * closing this gap is named in the tranche evidence, not papered over.
 */
export function RunPanel({ runId }: { runId: string }) {
  const runQuery = useQuery({
    queryKey: ["run", runId],
    queryFn: () => api.getRunWithTasks(runId),
    refetchInterval: 1500,
  });

  if (runQuery.isPending) {
    return (
      <section aria-label="Run" className="card" style={{ padding: 16 }}>
        <div aria-hidden="true">
          <div className="skeleton-row" />
        </div>
      </section>
    );
  }

  if (runQuery.isError) {
    if (errorCode(runQuery.error) === "session_expired") {
      return <SessionCard />;
    }
    return (
      <section aria-label="Run" className="card state-panel" role="alert">
        <p className="state-title">Could not load the run</p>
        <button type="button" className="btn" onClick={() => runQuery.refetch()}>
          Retry
        </button>
      </section>
    );
  }

  const data = runQuery.data;
  const run = data?.run;

  return (
    <section aria-label="Run" className="card" style={{ padding: 16 }} data-testid="run-panel">
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: 12,
          flexWrap: "wrap",
          marginBottom: 12,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
          <h2 style={{ margin: 0 }}>Run</h2>
          <span className="mono" style={{ color: "var(--ink-3)", fontSize: 12 }}>
            {runId}
          </span>
          {run ? (
            <StatusBadge label={humanStatus(run.status)} kind={runKind(run.status)} />
          ) : null}
        </div>
        {run && !TERMINAL_RUN.has(run.status) ? <CancelButton runId={run.id} /> : null}
      </header>

      {(data.tasks ?? []).length > 0 ? (
        <ul
          data-testid="task-list"
          style={{ listStyle: "none", margin: "0 0 12px", padding: 0, display: "grid", gap: 6 }}
        >
          {(data.tasks ?? []).map((task) => (
            <li
              key={task.id}
              style={{
                display: "grid",
                gridTemplateColumns: "minmax(0,1fr) auto auto",
                gap: 8,
                alignItems: "center",
                borderBottom: "1px solid var(--surface-sunken)",
                paddingBottom: 6,
              }}
            >
              <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {task.name}
              </span>
              <StatusBadge label={humanStatus(task.status)} kind={taskKind(task.status)} />
              <span className="row-meta mono">
                attempt {task.attempts}/{task.max_attempts}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        <p style={{ color: "var(--ink-2)" }}>Planning the work…</p>
      )}

      <EventTrail runId={runId} active={run ? !TERMINAL_RUN.has(run.status) : false} />

      {run && TERMINAL_RUN.has(run.status) ? <ReportView runId={run.id} /> : null}
    </section>
  );
}

function SessionCard() {
  return (
    <div className="state-panel card" data-testid="session-expired">
      <p className="state-title">Your session expired</p>
      <p>Sign in again to continue observing this run.</p>
      <a className="btn btn-primary" href="/api/auth/login?next=%2Fthreads" style={{ textDecoration: "none" }}>
        Sign in again
      </a>
    </div>
  );
}

function CancelButton({ runId }: { runId: string }) {
  const [requested, setRequested] = useState(false);
  const cancelRun = useMutation({
    mutationFn: () => api.cancelRun(runId),
    onSuccess: () => setRequested(true),
  });
  return (
    <button
      type="button"
      className="btn btn-danger"
      data-testid="cancel-run"
      disabled={cancelRun.isPending || requested}
      onClick={() => cancelRun.mutate()}
    >
      {requested ? "Cancellation requested…" : "Cancel run"}
    </button>
  );
}
