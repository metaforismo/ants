import type { Run, RunPage } from "@ants/contracts";

const TERMINAL_RUN_STATUSES = new Set(["completed", "failed", "cancelled"]);

export function isTerminalRun(run: Pick<Run, "status">): boolean {
  return TERMINAL_RUN_STATUSES.has(run.status);
}

/**
 * The thread's newest run. The list endpoint serves the stable
 * oldest-first order, so a fully consumed history ends with the
 * live/latest run.
 */
export function latestRun(runs: Run[]): Run | undefined {
  return runs.at(-1);
}

/**
 * Why a history traversal refused to continue. Every variant names a
 * violated invariant of the positional oldest-first pagination contract,
 * never a transient network condition (those surface as ApiClientError).
 */
export type RunHistoryTraversalCode =
  | "no_progress"
  | "duplicate_run"
  | "history_shrank"
  | "unbounded_traversal";

export class RunHistoryTraversalError extends Error {
  readonly code: RunHistoryTraversalCode;
  constructor(code: RunHistoryTraversalCode, message: string) {
    super(message);
    this.name = "RunHistoryTraversalError";
    this.code = code;
  }
}

/** Backstops for the traversal below; overridable in tests. */
export const RUN_HISTORY_MAX_PAGES = 10_000;
export const RUN_HISTORY_MAX_GROWTH_STEPS = 64;

export type CollectRunHistoryOptions = {
  maxPages?: number;
  maxGrowthSteps?: number;
};

/**
 * Walk the thread's run-history pages until the authoritative `total` is
 * consumed, returning the full oldest-first history whose last item is the
 * true latest run.
 *
 * The server serves bounded positional pages in a stable order; runs are
 * never deleted and new runs append only at the tail, so resuming at the
 * count of already-consumed entries can neither duplicate nor skip — even
 * when `total` grows between page requests, which simply extends the walk
 * (bounded by maxGrowthSteps so a pathological writer cannot spin the loop
 * forever). Guards refuse to loop on contract violations instead: an empty
 * page before the end (no_progress), a repeated entry (duplicate_run), or a
 * total that drops below what was already collected (history_shrank).
 */
export async function collectRunHistory(
  fetchPage: (after: number) => Promise<RunPage>,
  options: CollectRunHistoryOptions = {},
): Promise<RunPage> {
  const maxPages = options.maxPages ?? RUN_HISTORY_MAX_PAGES;
  const maxGrowthSteps = options.maxGrowthSteps ?? RUN_HISTORY_MAX_GROWTH_STEPS;

  const runs: Run[] = [];
  const seenIds = new Set<string>();
  let total = 0;
  let growthSteps = 0;

  for (let page = 1; ; page++) {
    if (page > maxPages) {
      throw new RunHistoryTraversalError(
        "unbounded_traversal",
        `run history did not terminate after ${maxPages} pages`,
      );
    }
    const result = await fetchPage(runs.length);
    const consumed = runs.length + result.runs.length;
    if (!Number.isSafeInteger(result.total) || result.total < consumed) {
      throw new RunHistoryTraversalError(
        "history_shrank",
        `run history total ${result.total} is below the ${consumed} entries observed`,
      );
    }
    if (result.total > total) {
      growthSteps++;
      if (growthSteps > maxGrowthSteps) {
        throw new RunHistoryTraversalError(
          "unbounded_traversal",
          `run history kept growing beyond ${maxGrowthSteps} observed growth steps during one traversal`,
        );
      }
      total = result.total;
    }
    for (const run of result.runs) {
      if (seenIds.has(run.id)) {
        throw new RunHistoryTraversalError(
          "duplicate_run",
          `run ${run.id} appeared twice while walking stable positional pages`,
        );
      }
      seenIds.add(run.id);
      runs.push(run);
    }
    if (result.runs.length === 0 && runs.length < total) {
      throw new RunHistoryTraversalError(
        "no_progress",
        `server returned an empty page at after=${runs.length} with ${total - runs.length} runs outstanding`,
      );
    }
    if (runs.length >= total) {
      return { runs, total };
    }
  }
}
