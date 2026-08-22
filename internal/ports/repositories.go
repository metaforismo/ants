package ports

// Repositories bundles every persistence port for wiring convenience.
// Consumers still depend on the individual interfaces they need; this struct
// only exists so constructors can take one explicit dependency bag.
type Repositories struct {
	Tenants         TenantStore
	Projects        ProjectStore
	Threads         ThreadStore
	Specs           SpecStore
	Tasks           TaskStore
	Runs            RunStore
	Workspaces      WorkspaceStore
	Artifacts       ArtifactStore
	Audit           AuditStore
	PolicyDecisions PolicyDecisionStore
	Integrations    IntegrationStore
	Events          EventLog
}
