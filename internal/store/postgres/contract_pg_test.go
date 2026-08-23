package postgres_test

import (
	"testing"

	"github.com/metaforismo/ants/internal/store/storetest"
)

// TestPostgresStoreContract runs the exact behavioral assertions the memory
// adapter satisfies against real PostgreSQL. It is gated on TEST_PG_DSN so
// plain unit runs need no services; canonical environments
// (scripts/test-postgres.sh and CI) always provide the database, where
// skipping is impossible.
func TestPostgresStoreContract(t *testing.T) {
	w := newPGWorld(t)

	storetest.Run(t, func() storetest.World {
		w.truncateAll(t)
		return storetest.World{Repos: w.Repos, Tx: w.Store}
	})

	// The outbox contract drives scheduling through the world's advancing
	// clock so publish visibility, lease expiry, and backoff windows are
	// exercised deterministically.
	storetest.RunOutbox(t, func() storetest.World {
		w.truncateAll(t)
		return storetest.World{Repos: w.Repos, Tx: w.Store, Advance: w.Advance}
	})

	// The dead-letter operator contract pins fencing, uniform not-found,
	// pagination boundaries, and discard retention on the same clock.
	storetest.RunOutboxOperator(t, func() storetest.World {
		w.truncateAll(t)
		return storetest.World{Repos: w.Repos, Tx: w.Store, Advance: w.Advance}
	})

	// The run-claim contract exercises fencing, expiry reclaim, SKIP LOCKED
	// dispatch, and unit-of-work atomicity on the same advancing clock.
	storetest.RunRunClaims(t, func() storetest.World {
		w.truncateAll(t)
		return storetest.World{Repos: w.Repos, Tx: w.Store, Advance: w.Advance}
	})
}
