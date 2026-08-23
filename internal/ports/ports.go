// Package ports declares the seams between the domain and the outside world:
// persistence, time, identifier generation, and event delivery. Adapters
// implement these interfaces; application code depends only on them.
package ports

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

type Clock interface {
	Now() time.Time
}

// Sleeper abstracts retry backoff. Production uses wall-clock sleeps; tests
// inject a recording sleeper so backoff behavior is verified without delay.
type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

type IDGenerator interface {
	NewID(prefix string) (string, error)
}

// EventSink receives domain events at the moment their state change commits.
// Implementations must not lose events; durable outbox semantics are layered
// on top per ADR-0005.
type EventSink interface {
	Emit(ctx context.Context, evt domain.Event) error
}

type TenantStore interface {
	Create(ctx context.Context, tenant *domain.Tenant) error
	Get(ctx context.Context, id domain.TenantID) (*domain.Tenant, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
}

type ProjectStore interface {
	Create(ctx context.Context, project *domain.Project) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.ProjectID) (*domain.Project, error)
	ListByTenant(ctx context.Context, tenantID domain.TenantID) ([]*domain.Project, error)
}

type ThreadStore interface {
	Create(ctx context.Context, thread *domain.Thread) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.ThreadID) (*domain.Thread, error)
	Update(ctx context.Context, thread *domain.Thread, expectedVersion int64) error
	AppendMessage(ctx context.Context, message *domain.Message) error
	Messages(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID, afterSeq int64, limit int) ([]*domain.Message, int64, error)
}

type SpecStore interface {
	Create(ctx context.Context, spec *domain.Spec) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.SpecID) (*domain.Spec, error)
	LatestForThread(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID) (*domain.Spec, error)
}

type TaskStore interface {
	Create(ctx context.Context, task *domain.Task) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.TaskID) (*domain.Task, error)
	Update(ctx context.Context, task *domain.Task, expectedVersion int64) error
	ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Task, error)
}

type RunStore interface {
	Create(ctx context.Context, run *domain.Run) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.RunID) (*domain.Run, error)
	Update(ctx context.Context, run *domain.Run, expectedVersion int64) error
	GetByIdempotencyKey(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID, key string) (*domain.Run, error)
}

type WorkspaceStore interface {
	Create(ctx context.Context, ws *domain.Workspace) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.WorkspaceID) (*domain.Workspace, error)
	Update(ctx context.Context, ws *domain.Workspace, expectedVersion int64) error
	ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Workspace, error)
}

type ArtifactStore interface {
	Create(ctx context.Context, artifact *domain.Artifact) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.ArtifactID) (*domain.Artifact, error)
	ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Artifact, error)
}

type AuditStore interface {
	Append(ctx context.Context, event *domain.AuditEvent) error
	ListByTenant(ctx context.Context, tenantID domain.TenantID, after string, limit int) ([]*domain.AuditEvent, error)
}

type PolicyDecisionStore interface {
	Record(ctx context.Context, decision *domain.PolicyDecision) error
	ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.PolicyDecision, error)
}

type IntegrationStore interface {
	Create(ctx context.Context, conn *domain.IntegrationConnection) error
	Get(ctx context.Context, tenantID domain.TenantID, id domain.IntegrationID) (*domain.IntegrationConnection, error)
	Update(ctx context.Context, conn *domain.IntegrationConnection, expectedVersion int64) error
	ListByTenant(ctx context.Context, tenantID domain.TenantID) ([]*domain.IntegrationConnection, error)
}

// EventLog is the append-only event stream with per-tenant cursor pagination.
type EventLog interface {
	Append(ctx context.Context, evt *domain.Event) error
	ListByTenant(ctx context.Context, tenantID domain.TenantID, afterSeq int64, limit int) ([]*domain.Event, error)
	ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, afterSeq int64, limit int) ([]*domain.Event, error)
}

// Transactor delimits a unit of work: every store operation performed by fn
// on the returned context commits together, or — on error or panic — rolls
// back together. Nesting joins the outer unit; no nested transaction is
// created. Isolation and retry posture are documented per implementation
// (ADR-0010).
type Transactor interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// OutboxMessage is one durably queued delivery. Envelope carries the
// serialized domain event; consumers deduplicate on ID because at-least-once
// delivery may repeat messages (ADR-0011). Publish visibility is decided by
// the store's own clock — callers never supply timestamps.
type OutboxMessage struct {
	ID          string
	DedupKey    string
	TenantID    domain.TenantID
	Envelope    []byte
	Attempts    int
	MaxAttempts int
}

// OutboxLeaseRequest bounds one claim round. Due-ness (publish visibility,
// lease expiry) is evaluated against the store's clock, not caller time.
type OutboxLeaseRequest struct {
	WorkerID string
	LeaseFor time.Duration
	Limit    int
}

type OutboxStats struct {
	Pending   int64
	Leased    int64
	Delivered int64
	Dead      int64
	Discarded int64
}

// OutboxStore is the durable work/event queue behind the transactional
// outbox pattern: publishes join the caller's transaction, claims are
// exclusive under concurrency, and failures advance bounded classified
// retries toward a dead-letter state. Every adapter owns ONE injected Clock
// (SystemClock by default) that decides publish visibility, due claims,
// lease expiry, and retry instants; callers pass only durations.
type OutboxStore interface {
	// Publish enqueues a message exactly once per dedup key. It participates
	// in the caller's transaction when one is active.
	Publish(ctx context.Context, msg OutboxMessage) error
	// Lease atomically claims due messages for one worker and increments
	// their attempt counters.
	Lease(ctx context.Context, req OutboxLeaseRequest) ([]OutboxMessage, error)
	// MarkDelivered acknowledges a message on behalf of its current lessee.
	MarkDelivered(ctx context.Context, id, leasedBy string) error
	// FailWithBackoff reschedules a failed delivery for its current lessee
	// retryIn from the store clock's present instant (the adapter computes
	// the absolute retry time on its own clock); exhausting max attempts
	// dead-letters the message.
	FailWithBackoff(ctx context.Context, id, leasedBy string, retryIn time.Duration, cause string) error
	Stats(ctx context.Context) (OutboxStats, error)

	// Operator surface for dead letters (ADR-0015). Mutations are valid only
	// from the dead state and require the expected generation as a
	// compare-and-swap credential: stale credentials conflict, wrong state is
	// an invalid transition, unknown and foreign-tenant messages are uniform
	// not-found. Callers composing these with event and audit writes must do
	// so inside one unit of work so an operator action can never land without
	// its trail.
	ListDeadLetters(ctx context.Context, req ListDeadLettersRequest) ([]DeadLetterSummary, error)
	GetDeadLetter(ctx context.Context, tenantID domain.TenantID, messageID string) (*DeadLetterSummary, error)
	RequeueDeadLetter(ctx context.Context, req OutboxMutationRequest) (OutboxMutationResult, error)
	DiscardDeadLetter(ctx context.Context, req OutboxMutationRequest) (OutboxMutationResult, error)
}
