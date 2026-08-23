import type { Problem } from "@/lib/problem";

/**
 * Renders a typed API problem with its recovery, per DESIGN.md voice: name
 * the problem, then the recovery. Uniform copy for 403/404 keeps the
 * tenant isolation posture (no existence oracle).
 */
export function ProblemNote({ problem }: { problem: Problem }) {
  if (problem.status === 403 || problem.status === 404) {
    return (
      <div role="note" className="state-panel card">
        <p className="state-title">Not available</p>
        <p>This resource is not available in this workspace.</p>
      </div>
    );
  }
  const recovery = RECOVERIES[problem.code] ?? DEFAULT_RECOVERY;
  return (
    <div role="alert" className="state-panel card">
      <p className="state-title">{problem.title}</p>
      <p>{recovery}</p>
      <p className="mono" style={{ color: "var(--ink-3)", fontSize: 11 }}>
        {problem.code}
      </p>
    </div>
  );
}

const DEFAULT_RECOVERY = "Retry the action; if it persists, the incident is already visible to operators.";

const RECOVERIES: Record<string, string> = {
  network_failure: "Check your connection. The console resumes automatically and continues where it left off.",
  api_unreachable: "The Ants API is unreachable right now. Requests resume automatically once it returns.",
  rate_limited: "Too many requests. Wait a moment before retrying; in-flight work is not lost.",
  session_expired: "Your session expired. Sign in again to continue where you left off.",
  unknown_tenant: "Your account is not linked to a workspace yet. Contact your administrator.",
  validation_failed: "Adjust the input and try again.",
};
