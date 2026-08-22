// Package migrate applies the embedded PostgreSQL migrations. It is
// deliberately small: ordered SQL files, one advisory-locked applier, and a
// bookkeeping table. Forward-only by design; rollback is a deployment
// decision, not an automated rewrite of history.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	migrations "github.com/metaforismo/ants/db"
)

const advisoryLockKey = 44741197

// Report summarizes one Up pass.
type Report struct {
	Applied []string
}

// Current lists applied and pending migration versions.
func Current(ctx context.Context, conn *sql.DB) (applied []string, pending []string, err error) {
	if err := ensureBookkeeping(ctx, conn); err != nil {
		return nil, nil, err
	}
	all, err := migrations.All()
	if err != nil {
		return nil, nil, fmt.Errorf("migrate: load embedded migrations: %w", err)
	}
	appliedSet := map[string]bool{}
	applied, err = appliedVersions(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range applied {
		appliedSet[v] = true
	}
	for _, m := range all {
		if !appliedSet[m.Version] {
			pending = append(pending, m.Version+"_"+m.Name)
		}
	}
	sort.Strings(applied)
	return applied, pending, nil
}

// Up applies every pending migration inside per-migration transactions,
// serialized by a global advisory lock so concurrent appliers are safe.
func Up(ctx context.Context, conn *sql.DB) (*Report, error) {
	if err := ensureBookkeeping(ctx, conn); err != nil {
		return nil, err
	}
	lockConn, err := conn.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: acquire lock connection: %w", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() {
		_, _ = lockConn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	all, err := migrations.All()
	if err != nil {
		return nil, fmt.Errorf("migrate: load embedded migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	appliedSet := map[string]bool{}
	for _, v := range applied {
		appliedSet[v] = true
	}

	report := &Report{}
	for _, m := range all {
		if appliedSet[m.Version] {
			continue
		}
		tx, txErr := conn.BeginTx(ctx, nil)
		if txErr != nil {
			return report, fmt.Errorf("migrate: begin %s: %w", m.Version, txErr)
		}
		if _, txErr = tx.ExecContext(ctx, m.SQL); txErr != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("migrate: apply %s_%s: %w", m.Version, m.Name, txErr)
		}
		if _, txErr = tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
			m.Version, m.Name); txErr != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("migrate: record %s: %w", m.Version, txErr)
		}
		if txErr = tx.Commit(); txErr != nil {
			return report, fmt.Errorf("migrate: commit %s: %w", m.Version, txErr)
		}
		report.Applied = append(report.Applied, m.Version+"_"+m.Name)
	}
	return report, nil
}

func ensureBookkeeping(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	if err != nil {
		return fmt.Errorf("migrate: ensure bookkeeping table: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *sql.DB) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied versions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
