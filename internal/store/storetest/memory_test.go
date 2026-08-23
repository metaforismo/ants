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
		return World{Repos: mem.AsPorts(), Tx: mem.NewTransactor()}
	})
}

func TestMemoryOutboxContract(t *testing.T) {
	RunOutbox(t, func() World {
		clock := NewAdvancingClock()
		mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
		if err != nil {
			t.Fatalf("build memory store: %v", err)
		}
		return World{
			Repos:   mem.AsPorts(),
			Tx:      mem.NewTransactor(),
			Advance: clock.Advance,
		}
	})
}

func TestMemoryOutboxOperatorContract(t *testing.T) {
	RunOutboxOperator(t, func() World {
		clock := NewAdvancingClock()
		mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
		if err != nil {
			t.Fatalf("build memory store: %v", err)
		}
		return World{
			Repos:   mem.AsPorts(),
			Tx:      mem.NewTransactor(),
			Advance: clock.Advance,
			Clock:   clock,
		}
	})
}

func TestMemoryOutboxRetentionContract(t *testing.T) {
	RunOutboxRetention(t, func() World {
		clock := NewAdvancingClock()
		mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
		if err != nil {
			t.Fatalf("build memory store: %v", err)
		}
		return World{
			Repos:   mem.AsPorts(),
			Tx:      mem.NewTransactor(),
			Advance: clock.Advance,
			Clock:   clock,
		}
	})
}

func TestMemoryRunClaimContract(t *testing.T) {
	RunRunClaims(t, func() World {
		clock := NewAdvancingClock()
		mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
		if err != nil {
			t.Fatalf("build memory store: %v", err)
		}
		return World{
			Repos:   mem.AsPorts(),
			Tx:      mem.NewTransactor(),
			Advance: clock.Advance,
		}
	})
}
