package domain

import (
	"time"
)

// Evidence is one observed fact produced by executing a check. It records
// what ran and what happened; it never encodes an agent's opinion.
type Evidence struct {
	Criterion     string     `json:"criterion"`
	Command       []string   `json:"command"`
	ExitCode      int        `json:"exit_code"`
	Passed        bool       `json:"passed"`
	LogArtifactID ArtifactID `json:"log_artifact_id,omitempty"`
	At            time.Time  `json:"at"`
}

// Severity follows the plan's review model. Only Blocker gates readiness.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
	SeverityNote    Severity = "note"
)

// Confidence is deliberately coarse: precise-looking numbers would be fake
// precision for a deterministic reviewer.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
)

type Finding struct {
	Category   string     `json:"category"`
	Severity   Severity   `json:"severity"`
	Confidence Confidence `json:"confidence"`
	Location   string     `json:"location"`
	Scenario   string     `json:"scenario"`
}

func (f Finding) Validate() error {
	switch f.Severity {
	case SeverityBlocker, SeverityWarning, SeverityNote:
	default:
		return Invalidf("finding_severity", "severity %q is not supported", f.Severity)
	}
	if f.Category == "" || f.Scenario == "" {
		return Invalidf("finding_shape", "findings need category and failure scenario")
	}
	return nil
}

func (f Finding) Blocking() bool { return f.Severity == SeverityBlocker }

type ReportTask struct {
	ID        TaskID       `json:"id"`
	Name      string       `json:"name"`
	Branch    string       `json:"branch"`
	CommitSHA string       `json:"commit_sha,omitempty"`
	Status    TaskStatus   `json:"status"`
	Attempts  int          `json:"attempts"`
	Failure   *FailureInfo `json:"failure,omitempty"`
}

type ReportIntegration struct {
	Branch    string   `json:"branch"`
	SHA       string   `json:"sha,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

type ReportVerification struct {
	Passed   bool       `json:"passed"`
	Evidence []Evidence `json:"evidence"`
}

type ReportArtifactRef struct {
	ID     ArtifactID   `json:"id"`
	Kind   ArtifactKind `json:"kind"`
	Digest string       `json:"digest"`
}

type BudgetSummary struct {
	TasksUsed   int `json:"tasks_used"`
	MaxTasks    int `json:"max_tasks"`
	ExecOpsUsed int `json:"exec_ops_used"`
	MaxExecOps  int `json:"max_exec_ops"`
}

// RunReport is the durable outcome of a run: what was planned, what was built,
// what proves it works, what risks remain, and what it cost.
type RunReport struct {
	RunID          RunID               `json:"run_id"`
	ThreadID       ThreadID            `json:"thread_id"`
	SpecID         SpecID              `json:"spec_id"`
	Spec           SpecContent         `json:"spec"`
	Tasks          []ReportTask        `json:"tasks"`
	Integration    ReportIntegration   `json:"integration"`
	Verification   ReportVerification  `json:"verification"`
	Findings       []Finding           `json:"findings"`
	Artifacts      []ReportArtifactRef `json:"artifacts"`
	Budget         BudgetSummary       `json:"budget"`
	ReadyForReview bool                `json:"ready_for_review"`
	Summary        string              `json:"summary"`
	StartedAt      time.Time           `json:"started_at"`
	FinishedAt     time.Time           `json:"finished_at"`
}
