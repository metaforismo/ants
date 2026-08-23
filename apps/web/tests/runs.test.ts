import { describe, expect, it } from "vitest";

import type { RunPage } from "@ants/contracts";
import type { Run } from "@ants/contracts";

import {
  collectRunHistory,
  latestRun,
} from "@/lib/runs";

function run(id: string): Run {
  return {
    id,
    thread_id: "thr_test",
    spec_id: "",
    status: "completed",
    idempotency_key: `key-${id}`,
    task_ids: [],
    principal: "prn_test",
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as unknown as Run;
}

/** A deterministic oldest-first server: page size 2, fixed total. */
function fixedServer(ids: string[], total = ids.length) {
  const calls: number[] = [];
  const fetchPage = async (after: number): Promise<RunPage> => {
    calls.push(after);
    return { runs: ids.slice(after, after + 2).map(run), total };
  };
  return { fetchPage, calls };
}

describe("collectRunHistory", () => {
  it("walks multiple bounded pages in order and ends on the true latest run", async () => {
    const ids = ["r1", "r2", "r3", "r4", "r5"];
    const server = fixedServer(ids);

    const history = await collectRunHistory(server.fetchPage);

    expect(history.runs.map((r) => r.id)).toEqual(ids);
    expect(history.total).toBe(5);
    expect(latestRun(history.runs)?.id).toBe("r5");
    expect(server.calls).toEqual([0, 2, 4]);
  });

  it("terminates exactly at an exact-multiple boundary without a wasted call", async () => {
    const server = fixedServer(["r1", "r2", "r3", "r4"]);

    const history = await collectRunHistory(server.fetchPage);

    expect(server.calls).toEqual([0, 2]);
    expect(history.total).toBe(4);
  });

  it("returns an empty history for a runless thread with one probe", async () => {
    const server = fixedServer([]);

    const history = await collectRunHistory(server.fetchPage);

    expect(history.runs).toEqual([]);
    expect(history.total).toBe(0);
    expect(server.calls).toEqual([0]);
  });

  it("tolerates the total growing mid-traversal and consumes the grown tail", async () => {
    // Page size 1: the server holds [r1, r2] when the walk starts, and a
    // concurrent start appends r3 while the reader is between pages.
    let collected = ["r1", "r2"];
    let total = 2;
    const calls: number[] = [];
    const fetchPage = async (after: number): Promise<RunPage> => {
      calls.push(after);
      if (after === 1) {
        collected = [...collected, "r3"];
        total = 3;
      }
      return { runs: collected.slice(after, after + 1).map(run), total };
    };

    const history = await collectRunHistory(fetchPage);

    expect(history.total).toBe(3);
    expect(history.runs.map((r) => r.id)).toEqual(["r1", "r2", "r3"]);
    expect(latestRun(history.runs)?.id).toBe("r3");
  });

  it("refuses to loop when a page comes back empty before the end", async () => {
    const fetchPage = async (): Promise<RunPage> => ({ runs: [], total: 5 });

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      name: "RunHistoryTraversalError",
      code: "no_progress",
    });
  });

  it("refuses overlapping pages instead of silently deduplicating them", async () => {
    // A reshuffled (non-positional) server resends r2 on the second page.
    const fetchPage = async (after: number): Promise<RunPage> =>
      after === 0
        ? { runs: [run("r1"), run("r2")], total: 3 }
        : { runs: [run("r2")], total: 3 };

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      name: "RunHistoryTraversalError",
      code: "duplicate_run",
    });
  });

  it("refuses a total that drops below the entries observed", async () => {
    let calls = 0;
    const fetchPage = async (): Promise<RunPage> => {
      calls++;
      if (calls === 1) return { runs: [run("r1"), run("r2")], total: 4 };
      // The page still serves r3, but claims a total that cannot hold it.
      return { runs: [run("r3")], total: 2 };
    };

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      name: "RunHistoryTraversalError",
      code: "history_shrank",
    });
  });

  it("refuses non-integer garbage totals instead of looping on NaN", async () => {
    const fetchPage = async (): Promise<RunPage> => ({
      runs: [],
      total: Number.NaN,
    });

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      code: "history_shrank",
    });
  });

  it("stops a pathological writer that grows the total faster than it is consumed", async () => {
    let served = 0;
    const fetchPage = async (): Promise<RunPage> => {
      served++;
      // Every page serves one real entry but claims there is always one
      // more outstanding, so the walk never catches up on progress alone.
      return { runs: [run(`r${served}`)], total: served + 1 };
    };

    await expect(
      collectRunHistory(fetchPage, { maxGrowthSteps: 3 }),
    ).rejects.toMatchObject({ code: "unbounded_traversal" });
  });

  it("honours the absolute page cap as the final backstop", async () => {
    const fetchPage = async (after: number): Promise<RunPage> => ({
      runs: [run(`r${after}`)],
      total: Number.MAX_SAFE_INTEGER,
    });

    await expect(
      collectRunHistory(fetchPage, { maxPages: 5 }),
    ).rejects.toMatchObject({ code: "unbounded_traversal" });
  });
});
