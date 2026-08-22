package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	migrations "github.com/metaforismo/ants/db"
	"github.com/metaforismo/ants/internal/store/migrate"
	"github.com/metaforismo/ants/internal/store/pgtestutil"
	pgrepos "github.com/metaforismo/ants/internal/store/postgres"
	"github.com/metaforismo/ants/internal/store/storetest"
)

// TestPostgresStoreContract runs the exact behavioral assertions the memory
// adapter satisfies against real PostgreSQL. It is gated on TEST_PG_DSN
// so plain unit runs need no services; canonical environments
// (scripts/test-postgres.sh and CI) always provide the database, where
// skipping is impossible.
func TestPostgresStoreContract(t *testing.T) {
	adminDSN := pgtestutil.DSN(t)
	ctx := context.Background()
	dsn, _ := pgtestutil.IsolatedDatabase(ctx, t, adminDSN)
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if err := pool.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := migrations.All(); err != nil {
		t.Fatalf("embedded migrations unreadable: %v", err)
	}
	if _, err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	store, err := pgrepos.New(ctx, dsn, 4, 2, time.Minute)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	repos := store.Repositories()
	// Each contract subtest starts from an emptied schema; truncation always
	// goes through the long-lived pool so it never races store teardown.
	storetest.Run(t, func() storetest.World {
		truncateAll(ctx, t, pool)
		return storetest.World{Repos: repos, Tx: store}
	})
}

func truncateAll(ctx context.Context, t *testing.T, pool *sql.DB) {
	t.Helper()
	rows, err := pool.QueryContext(ctx,
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
	_, err = pool.ExecContext(ctx,
		fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(quoteIdent(tables), ", ")))
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func quoteIdent(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
	}
	return out
}
