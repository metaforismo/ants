import { describe, expect, it } from "vitest";

import type { Run } from "@ants/contracts";

import { isTerminalRun, latestRun } from "@/lib/runs";

function run(id: string, status: Run["status"]): Run {
  return {
    id,
    tenant_id: "ten_test",
    thread_id: "thr_test",
    status,
    idempotency_key: "k",
    task_ids: [],
    version: 1,
    created_at: "2026-08-23T00:00:00Z",
    updated_at: "2026-08-23T00:00:00Z",
  };
}

describe("latestRun", () => {
  it("returns the last entry of the server's oldest-first order", () => {
    const runs = [run("run_a", "completed"), run("run_b", "executing_tasks")];
    expect(latestRun(runs)?.id).toBe("run_b");
  });

  it("returns undefined for an empty history", () => {
    expect(latestRun([])).toBeUndefined();
  });
});

describe("isTerminalRun", () => {
  it.each(["completed", "failed", "cancelled"] as const)("treats %s as terminal", (status) => {
    expect(isTerminalRun(run("run_x", status))).toBe(true);
  });

  it.each(["pending", "planning", "executing_tasks", "integrating", "verifying", "reporting"] as const)(
    "treats %s as live",
    (status) => {
      expect(isTerminalRun(run("run_x", status))).toBe(false);
    },
  );
});
