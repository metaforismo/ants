// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { StatusBadge } from "@/components/status-badge";
import { ProblemNote } from "@/components/problem-note";
import { RelativeTime } from "@/components/relative-time";
import type { components } from "@ants/contracts";

type ThreadStatus = components["schemas"]["ThreadStatus"];
type RunStatus = components["schemas"]["RunStatus"];

function withProviders(ui: React.ReactElement): React.ReactElement {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{ui}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("StatusBadge", () => {
  it("renders a text label alongside the shape glyph (never color alone)", () => {
    render(<StatusBadge label="executing" kind="live" />);
    expect(screen.getByText("executing")).toBeTruthy();
    expect(document.querySelector('[data-status="running"]')).toBeTruthy();
  });

  it("maps every thread and run status to a presentation kind", async () => {
    const { threadKind, runKind } = await import("@/lib/status");
    const threadStatuses: ThreadStatus[] = [
      "idle", "planning", "awaiting_input", "ready_to_execute", "executing",
      "waiting_external", "needs_attention", "reviewing", "fixing",
      "ready_for_review", "merged", "failed", "archived",
    ];
    for (const status of threadStatuses) {
      expect(threadKind(status)).toBeDefined();
    }
    const runStatuses: RunStatus[] = [
      "pending", "planning", "executing_tasks", "integrating", "verifying",
      "reporting", "completed", "failed", "cancelled",
    ];
    for (const status of runStatuses) {
      expect(runKind(status)).toBeDefined();
    }
    // Live states are visually distinct from attention states.
    expect(threadKind("executing")).toBe("live");
    expect(threadKind("needs_attention")).toBe("attention");
    expect(threadKind("merged")).toBe("done");
    expect(threadKind("failed")).toBe("failed");
  });
});

describe("ProblemNote", () => {
  it("keeps cross-tenant refusals uniform (no existence oracle)", () => {
    for (const status of [403, 404]) {
      cleanup();
      render(
        withProviders(
          <ProblemNote problem={{ type: "about:blank", code: "thread_not_found", title: "Thread not found", status }} />,
        ),
      );
      expect(screen.getByText("Not available")).toBeTruthy();
      expect(screen.queryByText(/thread/i, { exact: false })).toBeNull();
    }
  });

  it("names the failure and the recovery for other problems", () => {
    render(
      withProviders(
        <ProblemNote problem={{ type: "about:blank", code: "network_failure", title: "Network unreachable", status: 0 }}>
        </ProblemNote>,
      ),
    );
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByText("Check your connection. The console resumes automatically and continues where it left off."))
      .toBeTruthy();
  });
});

describe("RelativeTime", () => {
  it("never renders negative durations when the server clock is ahead", () => {
    const now = Date.now();
    const future = new Date(now + 60_000).toISOString();
    const { container } = render(<RelativeTime at={future} now={now} />);
    expect(container.textContent).toBe("just now");
  });

  it("renders coarse relative buckets with the absolute timestamp as tooltip", () => {
    const now = Date.now();
    const twoHoursAgo = new Date(now - 2 * 3600_000).toISOString();
    render(<RelativeTime at={twoHoursAgo} now={now} />);
    const el = document.querySelector("time")!;
    expect(el.textContent).toBe("2h ago");
    expect(el.getAttribute("title")).toBeTruthy();
  });
});
