package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/store/migrate"
	"github.com/metaforismo/ants/internal/store/pgtestutil"
	pgrepos "github.com/metaforismo/ants/internal/store/postgres"
	"github.com/metaforismo/ants/internal/store/storetest"
)

// pgWorld bundles everything one integration test needs against a fresh,
// isolated PostgreSQL database. The store runs on an AdvancingClock so
// outbox scheduling (publish visibility, lease expiry, backoff) is driven
// deterministically instead of by wall time.
type pgWorld struct {
	Store   *pgrepos.Store
	Repos   ports.Repositories
	Pool    *sql.DB // schema-level operations (truncation between subtests)
	Clock   *storetest.AdvancingClock
	Advance func(time.Duration)
}

func newPGWorld(t *testing.T) *pgWorld {
	t.Helper()
	ctx := context.Background()
	adminDSN := pgtestutil.DSN(t)
	dsn, _ := pgtestutil.IsolatedDatabase(ctx, t, adminDSN)

	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open migration pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	clock := storetest.NewAdvancingClock()
	store, err := pgrepos.New(ctx, pgrepos.Options{
		DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2,
		ConnMaxLifetime: time.Minute, OutboxMaxAttempts: 3,
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &pgWorld{
		Store:   store,
		Repos:   store.Repositories(),
		Pool:    pool,
		Clock:   clock,
		Advance: clock.Advance,
	}
}

// truncateAll empties every application table so each subtest starts from a
// pristine schema without reconnecting.
func (w *pgWorld) truncateAll(t *testing.T) {
	t.Helper()
	rows, err := w.Pool.QueryContext(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		return
	}
	quoted := make([]string, len(tables))
	for i, n := range tables {
		quoted[i] = `"` + n + `"`
	}
	if _, err := w.Pool.ExecContext(context.Background(),
		"TRUNCATE TABLE "+strings.Join(quoted, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func leaseAllNow() ports.OutboxLeaseRequest {
	return ports.OutboxLeaseRequest{
		WorkerID: "test-worker", LeaseFor: time.Minute, Limit: 100,
	}
}

func unmarshalTestJSON(raw []byte, dst *map[string]any) error {
	return json.Unmarshal(raw, dst)
}
