package memory

import (
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
}

func NewRepos() *Repos {
	st := &storeState{
		tenants:      map[domain.TenantID]*domain.Tenant{},
		tenantSlugs:  map[string]domain.TenantID{},
		projects:     map[domain.ProjectID]*domain.Project{},
		threads:      map[domain.ThreadID]*domain.Thread{},
		messages:     map[domain.ThreadID][]*domain.Message{},
		specs:        map[domain.SpecID]*domain.Spec{},
		tasks:        map[domain.TaskID]*domain.Task{},
		runs:         map[domain.RunID]*domain.Run{},
		runIdemKeys:  map[idemKey]domain.RunID{},
		workspaces:   map[domain.WorkspaceID]*domain.Workspace{},
		artifacts:    map[domain.ArtifactID]*domain.Artifact{},
		policyByRun:  map[domain.RunID][]*domain.PolicyDecision{},
		integrations: map[domain.IntegrationID]*domain.IntegrationConnection{},
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
	}
}

// NewTransactor returns the unit-of-work seam over this store's state.
func (r *Repos) NewTransactor() ports.Transactor { return &transactor{r.st} }

type storeState struct {
	// mu guards every collection for individual operations; unitMu
	// serializes whole units of work (see transactor.go).
	mu     sync.RWMutex
	unitMu sync.Mutex

	tenants      map[domain.TenantID]*domain.Tenant
	tenantSlugs  map[string]domain.TenantID
	projects     map[domain.ProjectID]*domain.Project
	threads      map[domain.ThreadID]*domain.Thread
	messages     map[domain.ThreadID][]*domain.Message
	specs        map[domain.SpecID]*domain.Spec
	tasks        map[domain.TaskID]*domain.Task
	runs         map[domain.RunID]*domain.Run
	runIdemKeys  map[idemKey]domain.RunID
	workspaces   map[domain.WorkspaceID]*domain.Workspace
	artifacts    map[domain.ArtifactID]*domain.Artifact
	auditLog     []*domain.AuditEvent
	policyByRun  map[domain.RunID][]*domain.PolicyDecision
	integrations map[domain.IntegrationID]*domain.IntegrationConnection
	events       []*domain.Event

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
	}
}
