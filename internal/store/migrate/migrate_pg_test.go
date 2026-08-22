package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/metaforismo/ants/internal/store/migrate"
	"github.com/metaforismo/ants/internal/store/pgtestutil"
)

// TestMigrationsAgainstPostgres is the real-database evidence for the schema.
// It is skipped unless ANTS_TEST_PG_DSN points at a disposable local
// database; scripts/test-postgres.sh provisions one via Docker.
func TestMigrationsAgainstPostgres(t *testing.T) {
	adminDSN := pgtestutil.DSN(t)
	ctx := context.Background()
	dsn, cleanup := pgtestutil.IsolatedDatabase(ctx, t, adminDSN)
	_ = cleanup
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	report, err := migrate.Up(ctx, conn)
	if err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if len(report.Applied) == 0 {
		t.Fatalf("first Up must apply at least one migration")
	}

	second, err := migrate.Up(ctx, conn)
	if err != nil {
		t.Fatalf("re-apply must be a no-op: %v", err)
	}
	if len(second.Applied) != 0 {
		t.Fatalf("migrations are not idempotent: %v", second.Applied)
	}

	for _, table := range []string{
		"tenants", "projects", "threads", "thread_messages",
		"specs", "runs", "tasks", "workspaces",
		"artifacts", "policy_decisions", "budgets",
		"integration_connections", "audit_events", "events",
	} {
		var exists bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s missing after migrations", table)
		}
	}

	applied, pending, err := migrate.Current(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || len(applied) < len(report.Applied) {
		t.Fatalf("status inconsistent: applied=%v pending=%v", applied, pending)
	}
}
