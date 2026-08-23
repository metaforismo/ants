package domain

import "time"

// EventType names are versioned per the plan's event envelope contract.
// Appending a new type requires a new ".vN" suffix, never an in-place change.
type EventType string

const (
	EventTenantCreated             EventType = "tenant.created.v1"
	EventProjectCreated            EventType = "project.created.v1"
	EventThreadCreated             EventType = "thread.created.v1"
	EventThreadStatusChanged       EventType = "thread.status.changed.v1"
	EventSpecRecorded              EventType = "spec.recorded.v1"
	EventRunStarted                EventType = "run.started.v1"
	EventRunStatusChanged          EventType = "run.status.changed.v1"
	EventTaskCreated               EventType = "task.created.v1"
	EventTaskStatusChanged         EventType = "task.status.changed.v1"
	EventWorkspaceCommitted        EventType = "workspace.committed.v1"
	EventArtifactStored            EventType = "artifact.stored.v1"
	EventPolicyEvaluated           EventType = "policy.evaluated.v1"
	EventBudgetExhausted           EventType = "budget.exhausted.v1"
	EventOutboxDeadLetterRequeued  EventType = "outbox.dead_letter.requeued.v1"
	EventOutboxDeadLetterDiscarded EventType = "outbox.dead_letter.discarded.v1"
)

type Event struct {
	ID               EventID   `json:"id"`
	Type             EventType `json:"type"`
	OccurredAt       time.Time `json:"occurred_at"`
	TenantID         TenantID  `json:"tenant_id"`
	AggregateType    string    `json:"aggregate_type"`
	AggregateID      string    `json:"aggregate_id"`
	AggregateVersion int64     `json:"aggregate_version"`
	Actor            Actor     `json:"actor"`
	TraceID          string    `json:"trace_id,omitempty"`

	// Seq is the store-assigned, per-tenant monotonic cursor used for stable
	// event pagination. Clients pass it back as ?after=.
	Seq int64 `json:"seq"`

	// RunID routes task- and workspace-level events into their run's stream.
	// It is additive routing metadata on top of the plan envelope.
	RunID RunID `json:"run_id,omitempty"`

	Data map[string]any `json:"data"`
}
