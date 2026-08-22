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
