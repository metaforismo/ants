package domain

import "time"

// RunStatus is the lifecycle of one orchestration pass over a thread's task
// graph: request -> plan -> parallel tasks -> integration -> verification ->
// report.
type RunStatus string

const (
	RunPending     RunStatus = "pending"
	RunPlanning    RunStatus = "planning"
	RunExecuting   RunStatus = "executing_tasks"
	RunIntegrating RunStatus = "integrating"
	RunVerifying   RunStatus = "verifying"
	RunReporting   RunStatus = "reporting"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
)

var AllRunStatuses = []RunStatus{
	RunPending,
	RunPlanning,
	RunExecuting,
	RunIntegrating,
	RunVerifying,
	RunReporting,
	RunCompleted,
	RunFailed,
	RunCancelled,
}

var runTransitions = transitionTable[RunStatus]{
	RunPending:     {RunPlanning, RunCancelled},
	RunPlanning:    {RunExecuting, RunFailed, RunCancelled},
	RunExecuting:   {RunIntegrating, RunVerifying, RunFailed, RunCancelled},
	RunIntegrating: {RunVerifying, RunFailed, RunCancelled},
	RunVerifying:   {RunReporting, RunFailed, RunCancelled},
	RunReporting:   {RunCompleted, RunFailed, RunCancelled},
	RunCompleted:   {},
	RunFailed:      {},
	RunCancelled:   {},
}

func init() {
	if err := checkTransitionTable(AllRunStatuses, runTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionRun(from, to RunStatus) bool {
	return runTransitions.allows(from, to)
}

func RunEdgesFrom(from RunStatus) []RunStatus {
	return runTransitions.edgesFrom(from)
}

func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled
}

type Run struct {
	ID             RunID     `json:"id"`
	TenantID       TenantID  `json:"tenant_id"`
	ThreadID       ThreadID  `json:"thread_id"`
	SpecID         SpecID    `json:"spec_id,omitempty"`
	Status         RunStatus `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	// Seq is the store-assigned, per-thread monotonic cursor used for stable
	// run-history pagination. It is allocated once at insert time and never
	// updated, so it orders runs by true creation order regardless of what
	// any clock did between insertions; clients pass the last observed Seq
	// back as `after` to resume a history walk exactly there.
	Seq        int64        `json:"seq"`
	Principal  PrincipalID  `json:"principal,omitempty"`
	TaskIDs    []TaskID     `json:"task_ids"`
	Report     *RunReport   `json:"report,omitempty"`
	Version    int64        `json:"version"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Failure    *FailureInfo `json:"failure,omitempty"`
}

const MaxIdempotencyKeyLen = 256

func NewRun(id RunID, tenantID TenantID, threadID ThreadID, idempotencyKey string, now time.Time) (*Run, error) {
	if _, err := ParseRunID(string(id)); err != nil {
		return nil, err
	}
	if idempotencyKey == "" {
		return nil, Invalidf("run_idempotency_key", "idempotency key must not be empty")
	}
	if len(idempotencyKey) > MaxIdempotencyKeyLen {
		return nil, Invalidf("run_idempotency_key", "idempotency key longer than %d characters", MaxIdempotencyKeyLen)
	}
	now = now.UTC()
	return &Run{
		ID:             id,
		TenantID:       tenantID,
		ThreadID:       threadID,
		Status:         RunPending,
		IdempotencyKey: idempotencyKey,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// WithPrincipal attributes the run to its originating principal. Kept off
// the constructor so existing call sites stay honest about who started what.
func (r *Run) WithPrincipal(p PrincipalID) *Run {
	r.Principal = p
	return r
}

func (r *Run) TransitionTo(next RunStatus) error {
	if r.Status == next {
		return NewInvalidTransitionError(r.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionRun(r.Status, next) {
		return NewInvalidTransitionError(r.Status, next)
	}
	r.Status = next
	r.Version++
	return nil
}

// Finish marks a terminal state exactly once and stamps the finish time.
func (r *Run) Finish(next RunStatus, at time.Time, failure *FailureInfo) error {
	switch next {
	case RunCompleted:
	case RunFailed:
		if failure == nil {
			return Invalidf("run_failure", "finishing a run as failed requires failure info")
		}
	case RunCancelled:
	default:
		return Invalidf("run_finish_state", "%q is not a terminal run state", next)
	}
	if err := r.TransitionTo(next); err != nil {
		return err
	}
	r.Failure = failure
	ts := at.UTC()
	r.FinishedAt = &ts
	return nil
}
