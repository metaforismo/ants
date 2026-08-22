package memory

import (
	"github.com/metaforismo/ants/internal/domain"
)

// stateBackup is a detached copy of every stored collection. Entity values
// themselves are treated as immutable once stored (repositories replace
// rather than mutate), so copying the containers is sufficient. Slices are
// copied with capacity == length so appends during fn allocate fresh backing
// arrays and cannot leak into the backup.
type stateBackup struct {
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
	eventSeq     int64
}

// backup clones the live state. Callers must hold unitMu (and should take
// mu briefly around the actual copy).
func (st *storeState) backup() *stateBackup {
	st.mu.RLock()
	defer st.mu.RUnlock()

	b := &stateBackup{
		tenants:      make(map[domain.TenantID]*domain.Tenant, len(st.tenants)),
		tenantSlugs:  make(map[string]domain.TenantID, len(st.tenantSlugs)),
		projects:     make(map[domain.ProjectID]*domain.Project, len(st.projects)),
		threads:      make(map[domain.ThreadID]*domain.Thread, len(st.threads)),
		messages:     make(map[domain.ThreadID][]*domain.Message, len(st.messages)),
		specs:        make(map[domain.SpecID]*domain.Spec, len(st.specs)),
		tasks:        make(map[domain.TaskID]*domain.Task, len(st.tasks)),
		runs:         make(map[domain.RunID]*domain.Run, len(st.runs)),
		runIdemKeys:  make(map[idemKey]domain.RunID, len(st.runIdemKeys)),
		workspaces:   make(map[domain.WorkspaceID]*domain.Workspace, len(st.workspaces)),
		artifacts:    make(map[domain.ArtifactID]*domain.Artifact, len(st.artifacts)),
		policyByRun:  make(map[domain.RunID][]*domain.PolicyDecision, len(st.policyByRun)),
		integrations: make(map[domain.IntegrationID]*domain.IntegrationConnection, len(st.integrations)),
		auditLog:     make([]*domain.AuditEvent, len(st.auditLog)),
		events:       make([]*domain.Event, len(st.events)),
		eventSeq:     st.eventSeq,
	}
	for k, v := range st.tenants {
		b.tenants[k] = cloneTenant(v)
	}
	for k, v := range st.tenantSlugs {
		b.tenantSlugs[k] = v
	}
	for k, v := range st.projects {
		b.projects[k] = cloneProject(v)
	}
	for k, v := range st.threads {
		b.threads[k] = cloneThread(v)
	}
	for k, v := range st.messages {
		b.messages[k] = cloneMessageSlice(v)
	}
	for k, v := range st.specs {
		b.specs[k] = cloneSpec(v)
	}
	for k, v := range st.tasks {
		b.tasks[k] = cloneTask(v)
	}
	for k, v := range st.runs {
		b.runs[k] = cloneRun(v)
	}
	for k, v := range st.runIdemKeys {
		b.runIdemKeys[k] = v
	}
	for k, v := range st.workspaces {
		b.workspaces[k] = cloneWorkspace(v)
	}
	for k, v := range st.artifacts {
		b.artifacts[k] = cloneArtifact(v)
	}
	copy(b.auditLog, st.auditLog)
	for k, v := range st.policyByRun {
		b.policyByRun[k] = clonePolicyDecisions(v)
	}
	for k, v := range st.integrations {
		b.integrations[k] = cloneIntegration(v)
	}
	copy(b.events, st.events)
	return b
}

// restore puts the backup back into the live state in place, so repository
// pointers captured at construction keep working.
func (st *storeState) restore(b *stateBackup) {
	st.mu.Lock()
	defer st.mu.Unlock()

	freshTenants := make(map[domain.TenantID]*domain.Tenant, len(b.tenants))
	for k, v := range b.tenants {
		freshTenants[k] = v
	}
	st.tenants = freshTenants

	freshSlugs := make(map[string]domain.TenantID, len(b.tenantSlugs))
	for k, v := range b.tenantSlugs {
		freshSlugs[k] = v
	}
	st.tenantSlugs = freshSlugs

	freshProjects := make(map[domain.ProjectID]*domain.Project, len(b.projects))
	for k, v := range b.projects {
		freshProjects[k] = v
	}
	st.projects = freshProjects

	freshThreads := make(map[domain.ThreadID]*domain.Thread, len(b.threads))
	for k, v := range b.threads {
		freshThreads[k] = v
	}
	st.threads = freshThreads

	freshMessages := make(map[domain.ThreadID][]*domain.Message, len(b.messages))
	for k, v := range b.messages {
		freshMessages[k] = cloneMessageSlice(v)
	}
	st.messages = freshMessages

	freshSpecs := make(map[domain.SpecID]*domain.Spec, len(b.specs))
	for k, v := range b.specs {
		freshSpecs[k] = v
	}
	st.specs = freshSpecs

	freshTasks := make(map[domain.TaskID]*domain.Task, len(b.tasks))
	for k, v := range b.tasks {
		freshTasks[k] = v
	}
	st.tasks = freshTasks

	freshRuns := make(map[domain.RunID]*domain.Run, len(b.runs))
	for k, v := range b.runs {
		freshRuns[k] = v
	}
	st.runs = freshRuns

	freshIdem := make(map[idemKey]domain.RunID, len(b.runIdemKeys))
	for k, v := range b.runIdemKeys {
		freshIdem[k] = v
	}
	st.runIdemKeys = freshIdem

	freshWorkspaces := make(map[domain.WorkspaceID]*domain.Workspace, len(b.workspaces))
	for k, v := range b.workspaces {
		freshWorkspaces[k] = v
	}
	st.workspaces = freshWorkspaces

	freshArtifacts := make(map[domain.ArtifactID]*domain.Artifact, len(b.artifacts))
	for k, v := range b.artifacts {
		freshArtifacts[k] = v
	}
	st.artifacts = freshArtifacts

	freshAudit := make([]*domain.AuditEvent, len(b.auditLog))
	copy(freshAudit, b.auditLog)
	st.auditLog = freshAudit

	freshPolicy := make(map[domain.RunID][]*domain.PolicyDecision, len(b.policyByRun))
	for k, v := range b.policyByRun {
		freshPolicy[k] = clonePolicyDecisions(v)
	}
	st.policyByRun = freshPolicy

	freshIntegrations := make(map[domain.IntegrationID]*domain.IntegrationConnection, len(b.integrations))
	for k, v := range b.integrations {
		freshIntegrations[k] = v
	}
	st.integrations = freshIntegrations

	freshEvents := make([]*domain.Event, len(b.events))
	copy(freshEvents, b.events)
	st.events = freshEvents

	st.eventSeq = b.eventSeq
}

func cloneMessageSlice(in []*domain.Message) []*domain.Message {
	out := make([]*domain.Message, len(in))
	copy(out, in)
	return out
}

func clonePolicyDecisions(in []*domain.PolicyDecision) []*domain.PolicyDecision {
	out := make([]*domain.PolicyDecision, len(in))
	copy(out, in)
	return out
}
