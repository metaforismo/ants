import { describe, expect, it } from "vitest";

import type { RunPage } from "@ants/contracts";
import type { Run } from "@ants/contracts";

import {
  collectRunHistory,
  latestRun,
  resolveSelectedRun,
} from "@/lib/runs";

function run(id: string, seq: number, createdAt = "2026-01-01T00:00:00Z"): Run {
  return {
    id,
    thread_id: "thr_test",
    spec_id: "",
    status: "completed",
    idempotency_key: `key-${id}`,
    seq,
    task_ids: [],
    principal: "prn_test",
    version: 1,
    created_at: createdAt,
    updated_at: createdAt,
  } as unknown as Run;
}

/**
 * A deterministic keyset server: entries carry dense sequences 1..n in
 * list order; `after` is a sequence value and only strictly-greater
 * sequences are served (page size 2).
 */
function fixedServer(entries: Run[], total = entries.length) {
  const calls: number[] = [];
  const fetchPage = async (after: number): Promise<RunPage> => {
    calls.push(after);
    const page = entries.filter((r) => r.seq > after).slice(0, 2);
    return { runs: page, total };
  };
  return { fetchPage, calls };
}

describe("collectRunHistory", () => {
  it("walks multiple bounded pages in order and ends on the true latest run", async () => {
    const ids = ["r1", "r2", "r3", "r4", "r5"];
    const server = fixedServer(ids.map((id, i) => run(id, i + 1)));

    const history = await collectRunHistory(server.fetchPage);

    expect(history.runs.map((r) => r.id)).toEqual(ids);
    expect(history.total).toBe(5);
    expect(latestRun(history.runs)?.id).toBe("r5");
    // Cursors are the last consumed run's sequence value.
    expect(server.calls).toEqual([0, 2, 4]);
  });

  it("terminates exactly at an exact-multiple boundary without a wasted call", async () => {
    const server = fixedServer(["r1", "r2", "r3", "r4"].map((id, i) => run(id, i + 1)));

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
    // The server holds [r1(seq 1), r2(seq 2)] when the walk starts, and a
    // concurrent start appends r3(seq 3) while the reader is between pages.
    let entries = [run("r1", 1), run("r2", 2)];
    let total = 2;
    const fetchPage = async (after: number): Promise<RunPage> => {
      if (after === 1) {
        entries = [...entries, run("r3", 3)];
        total = 3;
      }
      return { runs: entries.filter((r) => r.seq > after).slice(0, 1), total };
    };

    const history = await collectRunHistory(fetchPage);

    expect(history.total).toBe(3);
    expect(history.runs.map((r) => r.id)).toEqual(["r1", "r2", "r3"]);
    expect(latestRun(history.runs)?.id).toBe("r3");
  });

  it("keeps a clock-rollback run at the tail instead of duplicating consumed history", async () => {
    // r3 was created after r1/r2 but its created_at is BACKDATED by a clock
    // rollback. Its sequence still sorts it last, so the walk must neither
    // reorder nor duplicate anything — the regression test for the
    // positional-offset defect this traversal replaced.
    const entries = [
      run("r1", 1, "2026-01-01T00:00:10Z"),
      run("r2", 2, "2026-01-01T00:00:20Z"),
      run("r3", 3, "2026-01-01T00:00:05Z"),
    ];
    const server = fixedServer(entries);

    const history = await collectRunHistory(server.fetchPage);

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
    // A misbehaving server resends the same row on the second page.
    const fetchPage = async (after: number): Promise<RunPage> =>
      after === 0
        ? { runs: [run("r1", 1), run("r2", 2)], total: 3 }
        : { runs: [run("r2", 2)], total: 3 };

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      name: "RunHistoryTraversalError",
      code: "duplicate_run",
    });
  });

  it("refuses a server that reshuffles already-consumed sequences between pages", async () => {
    // Sequence numbers are immutable per the contract; a server that
    // prepends a run and renumbers everything afterwards drags an already
    // collected entry back above the cursor, exactly like a head insertion
    // broke positional offsets. The walk must refuse, not deduplicate.
    let shifted = false;
    const fetchPage = async (after: number): Promise<RunPage> => {
      if (!shifted) {
        shifted = true;
        return { runs: [run("r0", 1), run("r1", 2)], total: 3 };
      }
      const renumbered = [run("rx", 1), run("r0", 2), run("r1", 3)];
      return { runs: renumbered.filter((r) => r.seq > after).slice(0, 2), total: 3 };
    };

    await expect(collectRunHistory(fetchPage)).rejects.toMatchObject({
      name: "RunHistoryTraversalError",
      code: "duplicate_run",
    });
  });

  it("refuses a total that drops below the entries observed", async () => {
    let calls = 0;
    const fetchPage = async (): Promise<RunPage> => {
      calls++;
      if (calls === 1) return { runs: [run("r1", 1), run("r2", 2)], total: 4 };
      // The page still serves r3, but claims a total that cannot hold it.
      return { runs: [run("r3", 3)], total: 2 };
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
      return { runs: [run(`r${served}`, served)], total: served + 1 };
    };

    await expect(
      collectRunHistory(fetchPage, { maxGrowthSteps: 3 }),
    ).rejects.toMatchObject({ code: "unbounded_traversal" });
  });

  it("honours the absolute page cap as the final backstop", async () => {
    const fetchPage = async (after: number): Promise<RunPage> => ({
      runs: [run(`r${after}`, after + 1)],
      total: Number.MAX_SAFE_INTEGER,
    });

    await expect(
      collectRunHistory(fetchPage, { maxPages: 5 }),
    ).rejects.toMatchObject({ code: "unbounded_traversal" });
  });
});

describe("resolveSelectedRun", () => {
  const history = [run("r1", 1), run("r2", 2), run("r3", 3)];

  it("follows the newest run when the operator has not selected history", () => {
    expect(resolveSelectedRun(history)?.id).toBe("r3");
    expect(resolveSelectedRun([], undefined)).toBeUndefined();
  });

  it("keeps an explicit historical selection pinned while a newer run is discovered", () => {
    // Polling appended r3 after the operator pinned r2: the pin must not move.
    expect(resolveSelectedRun(history, "r2")?.id).toBe("r2");
  });

  it("pins the newest run exactly instead of silently switching to follow mode", () => {
    // Pinning the current latest, then a newer run appearing, leaves the
    // original pin selected — same rule as any historical run.
    expect(resolveSelectedRun([...history, run("r4", 4)], "r3")?.id).toBe("r3");
  });

  it("resets to the newest run when the pinned id left server truth", () => {
    // Unreachable under today's append-only contract; guards against a
    // future retention change pointing the panel at a ghost.
    expect(resolveSelectedRun(history, "rx")?.id).toBe("r3");
  });
});
