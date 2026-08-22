package memory

import (
	"fmt"
	"sync"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

var (
	_ ports.TenantStore         = (*TenantRepository)(nil)
	_ ports.ProjectStore        = (*ProjectRepository)(nil)
	_ ports.ThreadStore         = (*ThreadRepository)(nil)
	_ ports.SpecStore           = (*SpecRepository)(nil)
	_ ports.TaskStore           = (*TaskRepository)(nil)
	_ ports.RunStore            = (*RunRepository)(nil)
	_ ports.WorkspaceStore      = (*WorkspaceRepository)(nil)
	_ ports.ArtifactStore       = (*ArtifactRepository)(nil)
	_ ports.AuditStore          = (*AuditRepository)(nil)
	_ ports.PolicyDecisionStore = (*PolicyDecisionRepository)(nil)
	_ ports.IntegrationStore    = (*IntegrationRepository)(nil)
	_ ports.EventLog            = (*EventRepository)(nil)
	_ ports.OutboxStore         = (*OutboxRepository)(nil)
	_ ports.RunClaimStore       = (*RunClaimRepository)(nil)
)

// Repos is the collection of deterministic store implementations. Every
// repository shares one mutex-guarded state so cross-aggregate reads observe
// consistent snapshots.
type Repos struct {
	st *storeState

	Tenants         *TenantRepository
	Projects        *ProjectRepository
	Threads         *ThreadRepository
	Specs           *SpecRepository
	Tasks           *TaskRepository
	Runs            *RunRepository
	Workspaces      *WorkspaceRepository
	Artifacts       *ArtifactRepository
	Audit           *AuditRepository
	PolicyDecisions *PolicyDecisionRepository
	Integrations    *IntegrationRepository
	Events          *EventRepository
	Outbox          *OutboxRepository
	RunClaims       *RunClaimRepository
}

// defaultOutboxMaxAttempts mirrors config.Defaults().Outbox.MaxAttempts so
// the zero-value constructor stays valid; explicit wiring passes the value
// through Options.
const defaultOutboxMaxAttempts = 5

// Options configures one deterministic store instance. The zero value is
// valid: system clock, DefaultOutboxMaxAttempts retries.
type Options struct {
	// Clock is the single time authority for outbox scheduling (publish
	// visibility, claim eligibility, lease expiry, retry instants). Nil
	// selects the system clock; tests inject a manual clock instead of
	// sleeping.
	Clock ports.Clock
	// OutboxMaxAttempts bounds retries for messages enqueued automatically
	// when events are persisted (ADR-0011). Must be within [1,100].
	OutboxMaxAttempts int
}

// NewRepos builds the deterministic stores on the system clock.
func NewRepos() *Repos {
	repos, err := NewReposWithOptions(Options{})
	if err != nil {
		panic(fmt.Sprintf("memory: zero-value options must be valid: %v", err))
	}
	return repos
}

// NewReposWithOptions builds a world whose outbox scheduling follows the
// given options — the deterministic twin of postgres.Options.
func NewReposWithOptions(opts Options) (*Repos, error) {
	clock := opts.Clock
	if clock == nil {
		clock = ports.SystemClock{}
	}
	maxAttempts := opts.OutboxMaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultOutboxMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > 100 {
		return nil, fmt.Errorf("memory: outbox max attempts must be within [1,100], got %d", maxAttempts)
	}
	st := &storeState{
		clock:             clock,
		outboxMaxAttempts: maxAttempts,
		tenants:           map[domain.TenantID]*domain.Tenant{},
		tenantSlugs:       map[string]domain.TenantID{},
		projects:          map[domain.ProjectID]*domain.Project{},
		threads:           map[domain.ThreadID]*domain.Thread{},
		messages:          map[domain.ThreadID][]*domain.Message{},
		specs:             map[domain.SpecID]*domain.Spec{},
		tasks:             map[domain.TaskID]*domain.Task{},
		runs:              map[domain.RunID]*domain.Run{},
		runIdemKeys:       map[idemKey]domain.RunID{},
		workspaces:        map[domain.WorkspaceID]*domain.Workspace{},
		artifacts:         map[domain.ArtifactID]*domain.Artifact{},
		policyByRun:       map[domain.RunID][]*domain.PolicyDecision{},
		integrations:      map[domain.IntegrationID]*domain.IntegrationConnection{},
		outboxByID:        map[string]*outboxMessage{},
		outboxByDedup:     map[string]*outboxMessage{},
		runClaims:         map[domain.RunID]*domain.RunClaim{},
	}
	return &Repos{
		st:              st,
		Tenants:         &TenantRepository{st: st},
		Projects:        &ProjectRepository{st: st},
		Threads:         &ThreadRepository{st: st},
		Specs:           &SpecRepository{st: st},
		Tasks:           &TaskRepository{st: st},
		Runs:            &RunRepository{st: st},
		Workspaces:      &WorkspaceRepository{st: st},
		Artifacts:       &ArtifactRepository{st: st},
		Audit:           &AuditRepository{st: st},
		PolicyDecisions: &PolicyDecisionRepository{st: st},
		Integrations:    &IntegrationRepository{st: st},
		Events:          &EventRepository{st: st},
		Outbox:          &OutboxRepository{st: st},
		RunClaims:       &RunClaimRepository{st: st},
	}, nil
}

// NewTransactor returns the unit-of-work seam over this store's state.
func (r *Repos) NewTransactor() ports.Transactor { return &transactor{r.st} }

type storeState struct {
	// mu guards every collection for individual operations; unitMu
	// serializes whole units of work (see transactor.go).
	mu     sync.RWMutex
	unitMu sync.Mutex

	// clock is the single time authority for outbox scheduling, mirroring
	// the PostgreSQL adapter so contracts are deterministic.
	clock ports.Clock

	// outboxMaxAttempts bounds retries for messages enqueued automatically
	// when events are persisted (ADR-0011).
	outboxMaxAttempts int

	tenants       map[domain.TenantID]*domain.Tenant
	tenantSlugs   map[string]domain.TenantID
	projects      map[domain.ProjectID]*domain.Project
	threads       map[domain.ThreadID]*domain.Thread
	messages      map[domain.ThreadID][]*domain.Message
	specs         map[domain.SpecID]*domain.Spec
	tasks         map[domain.TaskID]*domain.Task
	runs          map[domain.RunID]*domain.Run
	runIdemKeys   map[idemKey]domain.RunID
	workspaces    map[domain.WorkspaceID]*domain.Workspace
	artifacts     map[domain.ArtifactID]*domain.Artifact
	auditLog      []*domain.AuditEvent
	policyByRun   map[domain.RunID][]*domain.PolicyDecision
	integrations  map[domain.IntegrationID]*domain.IntegrationConnection
	events        []*domain.Event
	outbox        []*outboxMessage
	outboxByID    map[string]*outboxMessage
	outboxByDedup map[string]*outboxMessage
	runClaims     map[domain.RunID]*domain.RunClaim

	eventSeq int64
}

type idemKey struct {
	tenant domain.TenantID
	thread domain.ThreadID
	key    string
}

func lockRead(st *storeState) func() {
	st.mu.RLock()
	return st.mu.RUnlock
}

func lockWrite(st *storeState) func() {
	st.mu.Lock()
	return st.mu.Unlock
}

func notFound(entity string, id any) error {
	return domain.NotFoundf(entity, id)
}

// AsPorts exposes the repositories through the aggregate the application
// wires, keeping call sites free of field-by-field copies.
func (r *Repos) AsPorts() ports.Repositories {
	return ports.Repositories{
		Tenants:         r.Tenants,
		Projects:        r.Projects,
		Threads:         r.Threads,
		Specs:           r.Specs,
		Tasks:           r.Tasks,
		Runs:            r.Runs,
		Workspaces:      r.Workspaces,
		Artifacts:       r.Artifacts,
		Audit:           r.Audit,
		PolicyDecisions: r.PolicyDecisions,
		Integrations:    r.Integrations,
		Events:          r.Events,
		Outbox:          r.Outbox,
		RunClaims:       r.RunClaims,
	}
}
