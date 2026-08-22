package storetest

import (
	"testing"

	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

// TestMemoryStoreContract pins the deterministic adapter to the shared
// behavioral contract; TestPostgresStoreContract runs the same suite against
// a real database.
func TestMemoryStoreContract(t *testing.T) {
	Run(t, func() World {
		mem := memorystore.NewRepos()
		repos := mem.AsPorts()
		return World{Repos: repos, Tx: mem.NewTransactor()}
	})
}
