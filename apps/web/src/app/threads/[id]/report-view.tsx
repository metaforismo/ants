"use client";

import { useQuery } from "@tanstack/react-query";

import { api, errorCode } from "@/lib/client-api";

/**
 * Terminal report: evidence first (DESIGN.md). Verification evidence with
 * real exit codes leads; findings, tasks, budget, and artifacts follow.
 */
export function ReportView({ runId }: { runId: string }) {
  const reportQuery = useQuery({
    queryKey: ["report", runId],
    queryFn: () => api.getRunReport(runId),
    retry: 1,
  });

  if (reportQuery.isPending) {
    return (
      <div style={{ marginTop: 16 }}>
        <h3 style={{ fontSize: 13 }}>Report</h3>
        <div className="skeleton-row" aria-hidden="true" />
      </div>
    );
  }

  if (reportQuery.isError) {
    if (errorCode(reportQuery.error) === "session_expired") {
      return (
        <div role="alert" className="banner banner-attention" style={{ marginTop: 12 }}>
          <span>Sign in again to read the report.</span>
        </div>
      );
    }
    return (
      <div role="alert" className="state-panel card" style={{ marginTop: 12 }} data-testid="report-error">
        <p className="state-title">Could not load the report</p>
        <button type="button" className="btn" onClick={() => reportQuery.refetch()}>
          Retry
        </button>
      </div>
    );
  }

  const report = reportQuery.data;
  // Go-side nil slices marshal as JSON null even where the generated type
  // says array; normalize once here instead of trusting every emitter.
  const evidence = report.verification?.evidence ?? [];
  const findings = report.findings ?? [];
  const tasks = report.tasks ?? [];
  const artifacts = report.artifacts ?? [];
  return (
    <article data-testid="run-report" style={{ marginTop: 16, borderTop: "1px solid var(--hairline)", paddingTop: 12 }}>
      <header style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap", marginBottom: 8 }}>
        <h3 style={{ fontSize: 13, margin: 0 }}>Report</h3>
        <span
          className="mono"
          style={{
            fontSize: 11,
            fontWeight: 600,
            padding: "2px 8px",
            borderRadius: 6,
            background: report.ready_for_review ? "var(--accent-wash)" : "#fbeae9",
            color: report.ready_for_review ? "var(--accent)" : "var(--danger)",
          }}
        >
          {report.ready_for_review ? "ready for review" : "not ready"}
        </span>
      </header>

      <p style={{ marginTop: 0 }}>{report.summary}</p>

      <section>
        <h4 style={{ marginBottom: 4 }}>Spec outcome</h4>
        <p style={{ color: "var(--ink-2)", marginTop: 0 }}>{report.spec.outcome}</p>
      </section>

      <section>
        <h4>Verification</h4>
        {evidence.length === 0 ? (
          <p style={{ color: "var(--ink-2)" }}>No verification evidence recorded.</p>
        ) : (
          <table className="evidence-table">
            <thead>
              <tr>
                <th scope="col">Criterion</th>
                <th scope="col">Command</th>
                <th scope="col">Exit</th>
                <th scope="col">Result</th>
              </tr>
            </thead>
            <tbody>
              {evidence.map((row) => (
                <tr key={`${row.criterion}-${row.at}`}>
                  <td>{row.criterion}</td>
                  <td className="mono">{row.command.join(" ")}</td>
                  <td className="mono">{row.exit_code}</td>
                  <td className={`pass-${row.passed ? "true" : "false"}`}>
                    {row.passed ? "passed" : "failed"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {findings.length > 0 ? (
        <section>
          <h4>Findings</h4>
          <ul style={{ margin: 0, paddingLeft: 18 }}>
            {findings.map((finding) => (
              <li key={`${finding.category}-${finding.location}`} style={{ marginBottom: 4 }}>
                <strong>{finding.severity}</strong> · {finding.category} at{" "}
                <span className="mono">{finding.location}</span> — {finding.scenario}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section>
        <h4>Tasks</h4>
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          {tasks.map((task) => (
            <li key={task.id} style={{ marginBottom: 2 }}>
              {task.name} — {task.status.replaceAll("_", " ")}
              {task.attempts > 1 ? ` (attempt ${task.attempts})` : null}
              {task.branch ? (
                <>
                  {" "}
                  on <span className="mono">{task.branch}</span>
                </>
              ) : null}
            </li>
          ))}
        </ul>
      </section>

      <p className="row-meta mono" style={{ marginTop: 8 }}>
        budget {report.budget.tasks_used}/{report.budget.max_tasks} tasks ·{" "}
        {report.budget.exec_ops_used}/{report.budget.max_exec_ops} exec ops ·{" "}
        {artifacts.length} artifact{artifacts.length === 1 ? "" : "s"}
      </p>
    </article>
  );
}
