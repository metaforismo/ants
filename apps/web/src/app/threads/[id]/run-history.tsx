"use client";

import { useState } from "react";

import { RelativeTime } from "@/components/relative-time";
import { StatusBadge } from "@/components/status-badge";
import type { Run } from "@ants/contracts";

import { humanStatus } from "@/app/threads/threads-view";
import { isTerminalRun } from "@/lib/runs";
import { runKind } from "@/lib/status";

/** Rows per visual page. The full history stays in state (server truth);
 * only this window renders, so a 250-run thread costs 250 array slots and
 * 25 rows of DOM. */
const PAGE_SIZE = 25;

type SelectionMode = "follow-latest" | "pinned";

type BrowseRequest = {
  page: number;
  selectedRunId: string | undefined;
  selectionMode: SelectionMode;
};

/**
 * Run-history navigator: the thread's run history, newest first, at most
 * PAGE_SIZE rows per visual page, driven entirely by server truth
 * (GET /v1/threads/{id}/runs walked to the authoritative total). Selecting
 * a row opens that run's tasks, events, and report below; the workspace
 * decides which run that is.
 *
 * Selection semantics are the workspace's single deterministic rule set:
 * following the latest run is the default and tracks runs discovered by
 * polling, an explicit selection pins one exact run until the operator
 * releases it, and a pin that vanishes from server truth resets to latest
 * (see resolveSelectedRun).
 *
 * Paging model: an explicit browse request belongs to the selection and mode
 * that created it. A selection change invalidates that request by derivation,
 * without effect-driven state resets. Follow-latest returns to page one when
 * polling discovers a new latest id. A pin with no browse request follows its
 * recomputed page as history grows; a pinned operator who deliberately browses
 * away stays on that requested page (clamped if history shrinks). Older/Newer
 * never change the selected run and remain native keyboard-reachable buttons.
 */
export function RunHistoryNavigator({
  runs,
  selectedRunId,
  selectionMode,
  onSelect,
}: {
  runs: Run[];
  /** The resolved selection (never a stale id): marks the row aria-current. */
  selectedRunId: string | undefined;
  /** Whether the workspace follows server latest or holds an explicit pin. */
  selectionMode: SelectionMode;
  /** Pass undefined to release an explicit selection and follow the latest run. */
  onSelect: (runId: string | undefined) => void;
}) {
  const [browseRequest, setBrowseRequest] = useState<BrowseRequest | null>(null);

  // History arrives oldest-first (append-stable keyset order); operators scan
  // newest-first, so display order reverses it.
  const selectedIndex = runs.findIndex((run) => run.id === selectedRunId);
  const selectedDisplayIndex = selectedIndex >= 0 ? runs.length - 1 - selectedIndex : -1;
  const selectionPage =
    selectedDisplayIndex >= 0 ? Math.floor(selectedDisplayIndex / PAGE_SIZE) + 1 : 1;
  // History is oldest-first: everything after the selected index is newer.
  const newerCount = selectedIndex >= 0 ? runs.length - 1 - selectedIndex : 0;
  const viewingHistory = selectionMode === "pinned" && newerCount > 0;

  const pageCount = Math.max(1, Math.ceil(runs.length / PAGE_SIZE));
  const requestMatchesSelection =
    browseRequest?.selectedRunId === selectedRunId &&
    browseRequest?.selectionMode === selectionMode;
  const derivedPage = selectionMode === "follow-latest" ? 1 : selectionPage;
  const requestedPage = requestMatchesSelection ? browseRequest.page : derivedPage;
  const activePage = Math.min(Math.max(requestedPage, 1), pageCount);

  const displayRuns = [...runs].reverse();
  const pageRows = displayRuns.slice((activePage - 1) * PAGE_SIZE, activePage * PAGE_SIZE);

  return (
    <section
      aria-label="Run history"
      className="card"
      style={{ padding: 16 }}
      data-testid="run-history"
    >
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "baseline",
          gap: 12,
          flexWrap: "wrap",
          marginBottom: 8,
        }}
      >
        <h2 style={{ margin: 0 }}>
          Runs{" "}
          <span className="mono" style={{ color: "var(--ink-3)", fontSize: 12 }}>
            {runs.length}
          </span>
        </h2>
        <span className="row-meta">newest first</span>
      </header>

      {viewingHistory ? (
        <div role="status" className="banner banner-info" style={{ marginBottom: 8 }} data-testid="newer-runs-notice">
          <span>
            {newerCount === 1
              ? "A newer run exists."
              : `${newerCount} newer runs exist since this one.`}
          </span>
          <button type="button" className="btn" data-testid="view-latest-run" onClick={() => onSelect(undefined)}>
            View latest run
          </button>
        </div>
      ) : null}

      <ol className="run-list" style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {pageRows.map((run) => (
          <li key={run.id}>
            <button
              type="button"
              className="run-row"
              aria-current={run.id === selectedRunId ? "true" : undefined}
              onClick={() => onSelect(run.id)}
            >
              <span className="mono row-meta">#{run.seq}</span>
              <StatusBadge label={humanStatus(run.status)} kind={runKind(run.status)} />
              <span className="row-meta">
                {isTerminalRun(run) ? "finished" : "started"} <RelativeTime at={run.created_at} />
              </span>
              <span className="mono run-row-id">{run.id}</span>
            </button>
          </li>
        ))}
      </ol>

      <nav
        aria-label="Run history pages"
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "flex-end",
          gap: 8,
          marginTop: 8,
        }}
        data-testid="run-history-pager"
      >
        <button
          type="button"
          className="btn"
          aria-label="Show newer runs"
          disabled={activePage <= 1}
          onClick={() =>
            setBrowseRequest({
              page: activePage - 1,
              selectedRunId,
              selectionMode,
            })
          }
        >
          Newer
        </button>
        {/* Polite live region: page moves are announced without stealing focus. */}
        <span className="row-meta" aria-live="polite" data-testid="page-indicator">
          Page {activePage} of {pageCount}
        </span>
        <button
          type="button"
          className="btn"
          aria-label="Show older runs"
          disabled={activePage >= pageCount}
          onClick={() =>
            setBrowseRequest({
              page: activePage + 1,
              selectedRunId,
              selectionMode,
            })
          }
        >
          Older
        </button>
      </nav>
    </section>
  );
}
