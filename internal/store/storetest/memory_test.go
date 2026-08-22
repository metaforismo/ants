package storetest

import (
	"testing"

	"github.com/metaforismo/ants/internal/ports"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

func newMemoryRepos() ports.Repositories {
	mem := memorystore.NewRepos()
	return ports.Repositories{
		Tenants:         mem.Tenants,
		Projects:        mem.Projects,
		Threads:         mem.Threads,
		Specs:           mem.Specs,
		Tasks:           mem.Tasks,
		Runs:            mem.Runs,
		Workspaces:      mem.Workspaces,
		Artifacts:       mem.Artifacts,
		Audit:           mem.Audit,
		PolicyDecisions: mem.PolicyDecisions,
		Integrations:    mem.Integrations,
		Events:          mem.Events,
	}
}

// TestMemoryStoreContract pins the deterministic adapter to the shared
// behavioral contract. The future PostgreSQL implementation runs this exact
// suite against a real database.
func TestMemoryStoreContract(t *testing.T) {
	Run(t, newMemoryRepos)
}
