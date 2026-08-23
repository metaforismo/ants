import type { Run } from "@ants/contracts";

const TERMINAL_RUN_STATUSES = new Set(["completed", "failed", "cancelled"]);

export function isTerminalRun(run: Pick<Run, "status">): boolean {
  return TERMINAL_RUN_STATUSES.has(run.status);
}

/**
 * The thread's newest run. The list endpoint serves the stable
 * oldest-first order, so the live/latest run is always the last entry.
 */
export function latestRun(runs: Run[]): Run | undefined {
  return runs.at(-1);
}
