import type { components } from "@ants/contracts";

type ThreadStatus = components["schemas"]["ThreadStatus"];
type RunStatus = components["schemas"]["RunStatus"];
type TaskStatus = components["schemas"]["TaskStatus"];

/**
 * Status presentation. Status is never color alone (DESIGN.md): every state
 * renders a shape-coded icon plus its text label.
 */

export type StatusKind = "idle" | "live" | "attention" | "done" | "failed";

const THREAD_KINDS: Record<ThreadStatus, StatusKind> = {
  idle: "idle",
  planning: "live",
  awaiting_input: "attention",
  ready_to_execute: "idle",
  executing: "live",
  waiting_external: "attention",
  needs_attention: "attention",
  reviewing: "live",
  fixing: "live",
  ready_for_review: "attention",
  merged: "done",
  failed: "failed",
  archived: "done",
};

const RUN_KINDS: Record<RunStatus, StatusKind> = {
  pending: "idle",
  planning: "live",
  executing_tasks: "live",
  integrating: "live",
  verifying: "live",
  reporting: "live",
  completed: "done",
  failed: "failed",
  cancelled: "failed",
};

const TASK_KINDS: Record<TaskStatus, StatusKind> = {
  draft: "idle",
  queued: "idle",
  provisioning: "live",
  working: "live",
  verifying: "live",
  integrating: "live",
  done: "done",
  waiting_external: "attention",
  blocked: "attention",
  cancelled: "failed",
  failed: "failed",
};

export function threadKind(status: ThreadStatus): StatusKind {
  return THREAD_KINDS[status];
}

export function runKind(status: RunStatus): StatusKind {
  return RUN_KINDS[status];
}

export function taskKind(status: TaskStatus): StatusKind {
  return TASK_KINDS[status];
}

export const KIND_COLOR: Record<StatusKind, string> = {
  idle: "var(--ink-3)",
  live: "var(--run-live)",
  attention: "var(--attention)",
  done: "var(--accent)",
  failed: "var(--danger)",
};
