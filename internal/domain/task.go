package domain

import (
	"time"
)

// TaskStatus mirrors the plan's task lifecycle. failed is retryable up to a
// bounded attempt count enforced by the orchestrator; cancelled and done are
// terminal.
type TaskStatus string

const (
	TaskDraft           TaskStatus = "draft"
	TaskQueued          TaskStatus = "queued"
	TaskProvisioning    TaskStatus = "provisioning"
	TaskWorking         TaskStatus = "working"
	TaskVerifying       TaskStatus = "verifying"
	TaskIntegrating     TaskStatus = "integrating"
	TaskDone            TaskStatus = "done"
	TaskWaitingExternal TaskStatus = "waiting_external"
	TaskBlocked         TaskStatus = "blocked"
	TaskCancelled       TaskStatus = "cancelled"
	TaskFailed          TaskStatus = "failed"
)

var AllTaskStatuses = []TaskStatus{
	TaskDraft,
	TaskQueued,
	TaskProvisioning,
	TaskWorking,
	TaskVerifying,
	TaskIntegrating,
	TaskDone,
	TaskWaitingExternal,
	TaskBlocked,
	TaskCancelled,
	TaskFailed,
}

var taskTransitions = transitionTable[TaskStatus]{
	TaskDraft:           {TaskQueued, TaskCancelled},
	TaskQueued:          {TaskProvisioning, TaskBlocked, TaskCancelled},
	TaskProvisioning:    {TaskWorking, TaskWaitingExternal, TaskBlocked, TaskFailed, TaskCancelled},
	TaskWorking:         {TaskVerifying, TaskWaitingExternal, TaskBlocked, TaskFailed, TaskCancelled},
	TaskVerifying:       {TaskIntegrating, TaskWorking, TaskFailed, TaskCancelled},
	TaskIntegrating:     {TaskDone, TaskFailed, TaskCancelled},
	TaskDone:            {},
	TaskWaitingExternal: {TaskWorking, TaskCancelled},
	TaskBlocked:         {TaskQueued, TaskCancelled},
	TaskCancelled:       {},
	TaskFailed:          {TaskQueued, TaskCancelled},
}

func init() {
	if err := checkTransitionTable(AllTaskStatuses, taskTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionTask(from, to TaskStatus) bool {
	return taskTransitions.allows(from, to)
}

func TaskEdgesFrom(from TaskStatus) []TaskStatus {
	return taskTransitions.edgesFrom(from)
}

// FailureInfo records why a task left its happy path. Transient failures are
// eligible for orchestrator retries; terminal ones surface to the user.
type FailureInfo struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Transient bool   `json:"transient"`
}

type TaskKind string

const (
	TaskKindCodeChange   TaskKind = "code_change"
	TaskKindIntegration  TaskKind = "integration"
	TaskKindVerification TaskKind = "verification"
)

func (k TaskKind) Valid() bool {
	switch k {
	case TaskKindCodeChange, TaskKindIntegration, TaskKindVerification:
		return true
	default:
		return false
	}
}

type Task struct {
	ID          TaskID       `json:"id"`
	TenantID    TenantID     `json:"tenant_id"`
	RunID       RunID        `json:"run_id"`
	ThreadID    ThreadID     `json:"thread_id"`
	Name        string       `json:"name"`
	Kind        TaskKind     `json:"kind"`
	Status      TaskStatus   `json:"status"`
	Depth       int          `json:"depth"`
	DependsOn   []TaskID     `json:"depends_on"`
	Attempts    int          `json:"attempts"`
	MaxAttempts int          `json:"max_attempts"`
	Failure     *FailureInfo `json:"failure,omitempty"`
	Version     int64        `json:"version"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

func NewTask(id TaskID, tenantID TenantID, runID RunID, threadID ThreadID, name string, kind TaskKind, depth int, dependsOn []TaskID, maxAttempts int, now time.Time) (*Task, error) {
	if _, err := ParseTaskID(string(id)); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, Invalidf("task_name", "task name must not be empty")
	}
	if !kind.Valid() {
		return nil, Invalidf("task_kind", "task kind %q is not supported", kind)
	}
	if depth < 0 {
		return nil, Invalidf("task_depth", "task depth must not be negative")
	}
	if maxAttempts < 1 || maxAttempts > 10 {
		return nil, Invalidf("task_max_attempts", "task max attempts %d outside allowed range [1,10]", maxAttempts)
	}
	deps := make([]TaskID, len(dependsOn))
	copy(deps, dependsOn)
	now = now.UTC()
	return &Task{
		ID:          id,
		TenantID:    tenantID,
		RunID:       runID,
		ThreadID:    threadID,
		Name:        name,
		Kind:        kind,
		Status:      TaskDraft,
		Depth:       depth,
		DependsOn:   deps,
		MaxAttempts: maxAttempts,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (t *Task) TransitionTo(next TaskStatus) error {
	if t.Status == next {
		return NewInvalidTransitionError(t.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionTask(t.Status, next) {
		return NewInvalidTransitionError(t.Status, next)
	}
	t.Status = next
	t.Version++
	return nil
}

// BeginAttempt records a retry. It refuses to exceed MaxAttempts so budgeted
// retry loops cannot be bypassed by callers.
func (t *Task) BeginAttempt() error {
	if t.Attempts >= t.MaxAttempts {
		return &Error{
			Kind:    ErrKindBudgetExhausted,
			Code:    "task_attempts_exhausted",
			Message: "task exhausted its retry attempts",
			Details: map[string]any{"task_id": string(t.ID), "attempts": t.Attempts, "max_attempts": t.MaxAttempts},
		}
	}
	t.Attempts++
	return nil
}
