// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RunHistoryNavigator } from "@/app/threads/[id]/run-history";
import { resolveSelectedRun } from "@/lib/runs";
import type { Run } from "@ants/contracts";

function run(
  id: string,
  seq: number,
  status: Run["status"],
  createdAt = "2026-01-01T00:00:00Z",
): Run {
  return {
    id,
    thread_id: "thr_test",
    spec_id: "",
    status,
    idempotency_key: `key-${id}`,
    seq,
    task_ids: [],
    principal: "prn_test",
    version: 1,
    created_at: createdAt,
    updated_at: createdAt,
  } as unknown as Run;
}

function history(count: number): Run[] {
  return Array.from({ length: count }, (_, i) => run(`r${i + 1}`, i + 1, "completed"));
}

function HistoryHarness({ runs }: { runs: Run[] }) {
  const [pinnedRunId, setPinnedRunId] = useState<string | undefined>();
  const selected = resolveSelectedRun(runs, pinnedRunId);
  const selectionMode = pinnedRunId === undefined ? "follow-latest" : "pinned";

  return (
    <>
      <output data-testid="selected-run">{selected?.id ?? "none"}</output>
      <RunHistoryNavigator
        runs={runs}
        selectedRunId={selected?.id}
        selectionMode={selectionMode}
        onSelect={setPinnedRunId}
      />
    </>
  );
}

afterEach(() => {
  cleanup();
});

describe("RunHistoryNavigator", () => {
  it("bounds a long history to 25 DOM rows and reaches every page", () => {
    const runs = history(250);
    const { container } = render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r250"
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );

    const rows = () => container.querySelectorAll<HTMLButtonElement>(".run-row");
    expect(rows()).toHaveLength(25);
    expect(rows()[0]?.textContent).toContain("#250");
    expect(rows()[24]?.textContent).toContain("#226");
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 1 of 10");
    expect(screen.getByText("250")).toBeTruthy();

    const older = screen.getByRole("button", {
      name: "Show older runs",
    }) as HTMLButtonElement;
    for (let page = 2; page <= 10; page += 1) {
      fireEvent.click(older);
      expect(screen.getByTestId("page-indicator").textContent).toBe(`Page ${page} of 10`);
      expect(rows().length).toBeLessThanOrEqual(25);
    }
    expect(rows()[0]?.textContent).toContain("#25");
    expect(rows()[24]?.textContent).toContain("#1");
    expect(older.disabled).toBe(true);

    const newer = screen.getByRole("button", {
      name: "Show newer runs",
    }) as HTMLButtonElement;
    for (let page = 9; page >= 1; page -= 1) {
      fireEvent.click(newer);
      expect(screen.getByTestId("page-indicator").textContent).toBe(`Page ${page} of 10`);
    }
    expect(newer.disabled).toBe(true);
  });

  it.each([
    [0, 0],
    [25, 25],
  ])("keeps the %i-run boundary on one page with %i rows", (count, expectedRows) => {
    const runs = history(count);
    const { container } = render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId={runs.at(-1)?.id}
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );

    expect(container.querySelectorAll(".run-row")).toHaveLength(expectedRows);
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 1 of 1");
    expect(
      (screen.getByRole("button", { name: "Show newer runs" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Show older runs" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.queryByTestId("newer-runs-notice")).toBeNull();
  });

  it("browses the 26th run without changing the selected run", () => {
    const onSelect = vi.fn();
    const runs = history(26);
    const { container } = render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r26"
        selectionMode="follow-latest"
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Show older runs" }));
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 2 of 2");
    expect(container.querySelectorAll(".run-row")).toHaveLength(1);
    expect(container.querySelector(".run-row")?.textContent).toContain("#1");
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Show newer runs" }));
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 1 of 2");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("follows a newly discovered latest run and returns to its first page", () => {
    const first = history(26);
    const { rerender } = render(<HistoryHarness runs={first} />);

    fireEvent.click(screen.getByRole("button", { name: "Show older runs" }));
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 2 of 2");

    rerender(<HistoryHarness runs={history(27)} />);
    expect(screen.getByTestId("selected-run").textContent).toBe("r27");
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 1 of 2");
    expect(screen.getByRole("button", { name: /#27/ }).getAttribute("aria-current")).toBe("true");
  });

  it("keeps an explicit pin through polling growth and derives its shifted page", () => {
    const { rerender } = render(<HistoryHarness runs={history(26)} />);

    fireEvent.click(screen.getByRole("button", { name: "Show older runs" }));
    fireEvent.click(screen.getByRole("button", { name: /^#1completed/ }));
    expect(screen.getByTestId("selected-run").textContent).toBe("r1");
    expect(screen.getByTestId("newer-runs-notice").textContent).toContain("25 newer runs");

    rerender(<HistoryHarness runs={history(51)} />);
    expect(screen.getByTestId("selected-run").textContent).toBe("r1");
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 3 of 3");
    expect(
      screen.getByRole("button", { name: /^#1completed/ }).getAttribute("aria-current"),
    ).toBe("true");
    expect(screen.getByTestId("newer-runs-notice").textContent).toContain("50 newer runs");
  });

  it("keeps an explicit browse request stable while a pinned history grows", () => {
    const { rerender } = render(<HistoryHarness runs={history(51)} />);

    fireEvent.click(screen.getByRole("button", { name: "Show older runs" }));
    fireEvent.click(screen.getByRole("button", { name: "Show older runs" }));
    fireEvent.click(screen.getByRole("button", { name: /^#1completed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Show newer runs" }));
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 2 of 3");

    rerender(<HistoryHarness runs={history(52)} />);
    expect(screen.getByTestId("selected-run").textContent).toBe("r1");
    expect(screen.getByTestId("page-indicator").textContent).toBe("Page 2 of 3");
    expect(screen.queryByRole("button", { name: /^#1completed/ })).toBeNull();
  });

  it("keeps native pager focus while moving between pages", () => {
    render(
      <RunHistoryNavigator
        runs={history(26)}
        selectedRunId="r26"
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );
    const older = screen.getByRole("button", { name: "Show older runs" });
    older.focus();
    fireEvent.click(older);
    expect(document.activeElement).toBe(older);
    expect(screen.getByTestId("page-indicator").getAttribute("aria-live")).toBe("polite");
  });

  it("distinguishes status without color alone: every row carries label and glyph", () => {
    const runs = [
      run("r1", 1, "failed"),
      run("r2", 2, "cancelled"),
      run("r3", 3, "completed"),
      run("r4", 4, "executing_tasks"),
    ];
    render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r4"
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );

    for (const label of ["failed", "cancelled", "completed", "executing tasks"]) {
      expect(screen.getByText(label)).toBeTruthy();
    }
    expect(document.querySelectorAll(".status-dot")).toHaveLength(4);
  });

  it("exposes an accessible name per row and marks the selection with aria-current", () => {
    const runs = [run("r1", 1, "failed"), run("r2", 2, "completed")];
    render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r1"
        selectionMode="pinned"
        onSelect={() => {}}
      />,
    );

    const oldest = screen.getByRole("button", { name: /#1.*failed/ });
    expect(oldest.getAttribute("aria-current")).toBe("true");
    expect(
      screen.getByRole("button", { name: /#2.*completed/ }).getAttribute("aria-current"),
    ).toBeNull();
  });

  it("selects a historical run on click", () => {
    const onSelect = vi.fn();
    const runs = [run("r1", 1, "completed"), run("r2", 2, "failed")];
    render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r2"
        selectionMode="follow-latest"
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /#1/ }));
    expect(onSelect).toHaveBeenCalledWith("r1");
  });

  it("surfaces discovered newer runs while pinned and releases to latest", () => {
    const onSelect = vi.fn();
    const runs = [
      run("r1", 1, "failed"),
      run("r2", 2, "completed"),
      run("r3", 3, "executing_tasks"),
    ];
    render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r1"
        selectionMode="pinned"
        onSelect={onSelect}
      />,
    );

    const notice = screen.getByTestId("newer-runs-notice");
    expect(notice.textContent).toContain("2 newer runs");
    fireEvent.click(screen.getByTestId("view-latest-run"));
    expect(onSelect).toHaveBeenCalledWith(undefined);
  });

  it("uses singular copy for exactly one newer run", () => {
    const runs = [run("r1", 1, "failed"), run("r2", 2, "completed")];
    render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r1"
        selectionMode="pinned"
        onSelect={() => {}}
      />,
    );
    expect(screen.getByTestId("newer-runs-notice").textContent).toContain(
      "A newer run exists.",
    );
  });

  it("shows no notice while following latest or when the newest run is pinned", () => {
    const runs = [run("r1", 1, "failed"), run("r2", 2, "completed")];
    const { rerender } = render(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r2"
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByTestId("newer-runs-notice")).toBeNull();
    rerender(
      <RunHistoryNavigator
        runs={runs}
        selectedRunId="r2"
        selectionMode="pinned"
        onSelect={() => {}}
      />,
    );
    expect(screen.queryByTestId("newer-runs-notice")).toBeNull();
  });

  it("renders relative finished/started times with absolute tooltips, never raw clocks", () => {
    const now = Date.now();
    const fiveMinAgo = new Date(now - 5 * 60_000).toISOString();
    render(
      <RunHistoryNavigator
        runs={[run("r1", 1, "completed", fiveMinAgo)]}
        selectedRunId="r1"
        selectionMode="follow-latest"
        onSelect={() => {}}
      />,
    );
    const time = document.querySelector("time");
    expect(time?.textContent).toBe("5m ago");
    expect(time?.getAttribute("title")).toBeTruthy();
    expect(document.querySelector(".run-list")?.textContent).not.toContain(fiveMinAgo);
  });
});
