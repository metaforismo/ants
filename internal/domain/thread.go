package domain

import "time"

// ThreadStatus mirrors the master plan thread lifecycle. The visual status a UI
// shows is derived from activity; this field is the durable operational state.
type ThreadStatus string

const (
	ThreadIdle            ThreadStatus = "idle"
	ThreadPlanning        ThreadStatus = "planning"
	ThreadAwaitingInput   ThreadStatus = "awaiting_input"
	ThreadReadyToExecute  ThreadStatus = "ready_to_execute"
	ThreadExecuting       ThreadStatus = "executing"
	ThreadWaitingExternal ThreadStatus = "waiting_external"
	ThreadNeedsAttention  ThreadStatus = "needs_attention"
	ThreadReviewing       ThreadStatus = "reviewing"
	ThreadFixing          ThreadStatus = "fixing"
	ThreadReadyForReview  ThreadStatus = "ready_for_review"
	ThreadMerged          ThreadStatus = "merged"
	ThreadFailed          ThreadStatus = "failed"
	ThreadArchived        ThreadStatus = "archived"
)

var AllThreadStatuses = []ThreadStatus{
	ThreadIdle,
	ThreadPlanning,
	ThreadAwaitingInput,
	ThreadReadyToExecute,
	ThreadExecuting,
	ThreadWaitingExternal,
	ThreadNeedsAttention,
	ThreadReviewing,
	ThreadFixing,
	ThreadReadyForReview,
	ThreadMerged,
	ThreadFailed,
	ThreadArchived,
}

// threadTransitions implements the plan's thread state machine. Every state
// may archive (dormant: events accumulate but nothing wakes it).
var threadTransitions = transitionTable[ThreadStatus]{
	ThreadIdle:            {ThreadPlanning, ThreadArchived},
	ThreadPlanning:        {ThreadAwaitingInput, ThreadReadyToExecute, ThreadFailed, ThreadArchived},
	ThreadAwaitingInput:   {ThreadPlanning, ThreadReadyToExecute, ThreadArchived},
	ThreadReadyToExecute:  {ThreadExecuting, ThreadNeedsAttention, ThreadArchived},
	ThreadExecuting:       {ThreadWaitingExternal, ThreadNeedsAttention, ThreadReviewing, ThreadFailed, ThreadArchived},
	ThreadWaitingExternal: {ThreadExecuting, ThreadFailed, ThreadArchived},
	ThreadNeedsAttention:  {ThreadExecuting, ThreadArchived},
	ThreadReviewing:       {ThreadFixing, ThreadReadyForReview, ThreadFailed, ThreadArchived},
	ThreadFixing:          {ThreadReviewing, ThreadArchived},
	ThreadReadyForReview:  {ThreadMerged, ThreadIdle, ThreadArchived},
	ThreadMerged:          {ThreadArchived},
	ThreadFailed:          {ThreadIdle, ThreadArchived},
	ThreadArchived:        {},
}

func init() {
	if err := checkTransitionTable(AllThreadStatuses, threadTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionThread(from, to ThreadStatus) bool {
	return threadTransitions.allows(from, to)
}

func ThreadEdgesFrom(from ThreadStatus) []ThreadStatus {
	return threadTransitions.edgesFrom(from)
}

type ThreadRole string

const (
	RoleUser   ThreadRole = "user"
	RoleAgent  ThreadRole = "agent"
	RoleSystem ThreadRole = "system"
)

type DeliveryMode string

const (
	DeliveryImmediate DeliveryMode = "immediate"
	DeliveryQueue     DeliveryMode = "queue"
	DeliverySteer     DeliveryMode = "steer"
)

type Message struct {
	ID           MessageID    `json:"id"`
	TenantID     TenantID     `json:"tenant_id"`
	ThreadID     ThreadID     `json:"thread_id"`
	Seq          int64        `json:"seq"`
	Role         ThreadRole   `json:"role"`
	DeliveryMode DeliveryMode `json:"delivery_mode"`
	Content      string       `json:"content"`
	CreatedAt    time.Time    `json:"created_at"`
}

func (m *Message) Validate() error {
	switch m.Role {
	case RoleUser, RoleAgent, RoleSystem:
	default:
		return Invalidf("message_role", "message role %q is not supported", m.Role)
	}
	switch m.DeliveryMode {
	case DeliveryImmediate, DeliveryQueue, DeliverySteer:
	default:
		return Invalidf("message_delivery_mode", "delivery mode %q is not supported", m.DeliveryMode)
	}
	if m.Content == "" {
		return Invalidf("message_content", "message content must not be empty")
	}
	return nil
}

type Thread struct {
	ID        ThreadID     `json:"id"`
	TenantID  TenantID     `json:"tenant_id"`
	ProjectID ProjectID    `json:"project_id"`
	Title     string       `json:"title"`
	Status    ThreadStatus `json:"status"`
	CreatorID PrincipalID  `json:"creator_id"`
	Version   int64        `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func NewThread(id ThreadID, tenantID TenantID, projectID ProjectID, title string, creator PrincipalID, now time.Time) (*Thread, error) {
	if _, err := ParseThreadID(string(id)); err != nil {
		return nil, err
	}
	if title == "" {
		return nil, Invalidf("thread_title", "thread title must not be empty")
	}
	return &Thread{
		ID:        id,
		TenantID:  tenantID,
		ProjectID: projectID,
		Title:     title,
		Status:    ThreadIdle,
		CreatorID: creator,
		Version:   1,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}, nil
}

// TransitionTo applies a lifecycle transition. Callers persist the entity with
// the returned value and the pre-read version for optimistic concurrency.
func (t *Thread) TransitionTo(next ThreadStatus) error {
	if t.Status == next {
		return NewInvalidTransitionError(t.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionThread(t.Status, next) {
		return NewInvalidTransitionError(t.Status, next)
	}
	t.Status = next
	t.Version++
	return nil
}
