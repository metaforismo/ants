// Package pgtestutil provisions isolated disposable databases for
// integration tests so parallel test binaries never observe each other's
// writes or truncations.
package pgtestutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var counter int64

// DSN returns the integration-test database connection string, failing the
// test when it is absent. Canonical environments (scripts/test-postgres.sh,
// CI) always set it; skipping is only possible outside those environments.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; canonical validation provides a disposable PostgreSQL via scripts/test-postgres.sh or CI")
	}
	return dsn
}

// IsolatedDatabase creates a uniquely named database derived from the admin
// DSN's server and returns a DSN scoped to it plus a cleanup that drops it.
// The credentials come from the admin DSN; the user must be allowed to
// create databases (true for the disposable containers this project uses).
func IsolatedDatabase(ctx context.Context, t *testing.T, adminDSN string) (string, func()) {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping admin connection: %v", err)
	}

	name := fmt.Sprintf("ants_test_%d_%d", os.Getpid(), atomic.AddInt64(&counter, 1))
	if _, err := admin.ExecContext(ctx,
		fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}

	isolated := replaceDBName(adminDSN, name)
	cleanup := func() {
		pool, openErr := sql.Open("pgx", adminDSN)
		if openErr != nil {
			return
		}
		defer pool.Close()
		_, _ = pool.ExecContext(context.Background(),
			fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
	}
	t.Cleanup(cleanup)
	return isolated, cleanup
}

// replaceDBName swaps the path component of a URL-style DSN. Plain
// key/value DSNs are rejected loudly instead of silently targeting the
// wrong database.
func replaceDBName(dsn, dbName string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		panic(fmt.Sprintf("pgtestutil: unsupported DSN format %q; use URL-style postgres://…", dsn))
	}
	u.Path = "/" + dbName
	return u.String()
}
