// Package postgres implements every persistence port on PostgreSQL using
// database/sql over the pgx stdlib driver. SQL is explicit and reviewed per
// query; there is no ORM.
//
// Transaction participation: methods execute on the transaction carried in
// the context (see WithinTx) or on the pool when no transaction is active,
// so multi-record transitions commit atomically without leaking sql/pgx
// types to callers.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// executor is the surface both *sql.DB and *sql.Tx expose; store methods
// target whichever one the context carries.
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txKey struct{}

func withTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

func txFrom(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(txKey{}).(*sql.Tx)
	return tx
}

// Options configures one PostgreSQL store instance.
type Options struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	// OutboxMaxAttempts bounds retries for messages enqueued automatically
	// when events are persisted (ADR-0011).
	OutboxMaxAttempts int
	// Clock is the single time authority for outbox scheduling (publish
	// visibility, claim eligibility, lease expiry, retry instants). Nil
	// selects the system clock.
	Clock ports.Clock
}

// Store owns the connection pool and exposes every repository. Field order
// mirrors ports.Repositories so wiring is a mechanical copy.
type Store struct {
	pool  *sql.DB
	clock ports.Clock

	outboxMaxAttempts int

	Tenants         TenantRepository
	Projects        ProjectRepository
	Threads         ThreadRepository
	Specs           SpecRepository
	Tasks           TaskRepository
	Runs            RunRepository
	Workspaces      WorkspaceRepository
	Artifacts       ArtifactRepository
	Audit           AuditRepository
	PolicyDecisions PolicyDecisionRepository
	Integrations    IntegrationRepository
	Events          EventRepository
	Outbox          OutboxRepository
}

// Repositories bundles the store into the aggregate the application wires.
func (s *Store) Repositories() ports.Repositories {
	return ports.Repositories{
		Tenants:         &s.Tenants,
		Projects:        &s.Projects,
		Threads:         &s.Threads,
		Specs:           &s.Specs,
		Tasks:           &s.Tasks,
		Runs:            &s.Runs,
		Workspaces:      &s.Workspaces,
		Artifacts:       &s.Artifacts,
		Audit:           &s.Audit,
		PolicyDecisions: &s.PolicyDecisions,
		Integrations:    &s.Integrations,
		Events:          &s.Events,
		Outbox:          &s.Outbox,
	}
}

var (
	_ ports.TenantStore         = (*TenantRepository)(nil)
	_ ports.ProjectStore        = (*ProjectRepository)(nil)
	_ ports.ThreadStore         = (*ThreadRepository)(nil)
	_ ports.SpecStore           = (*SpecRepository)(nil)
	_ ports.TaskStore           = (*TaskRepository)(nil)
	_ ports.RunStore            = (*RunRepository)(nil)
	_ ports.WorkspaceStore      = (*WorkspaceRepository)(nil)
	_ ports.ArtifactStore       = (*ArtifactRepository)(nil)
	_ ports.AuditStore          = (*AuditRepository)(nil)
	_ ports.PolicyDecisionStore = (*PolicyDecisionRepository)(nil)
	_ ports.IntegrationStore    = (*IntegrationRepository)(nil)
	_ ports.EventLog            = (*EventRepository)(nil)
)

// New opens the pool with validated options.
func New(ctx context.Context, opts Options) (*Store, error) {
	pool, err := sql.Open("pgx", opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	pool.SetMaxOpenConns(opts.MaxOpenConns)
	pool.SetMaxIdleConns(opts.MaxIdleConns)
	pool.SetConnMaxLifetime(opts.ConnMaxLifetime)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if opts.OutboxMaxAttempts < 1 || opts.OutboxMaxAttempts > 100 {
		_ = pool.Close()
		return nil, fmt.Errorf("postgres: outbox max attempts must be within [1,100], got %d", opts.OutboxMaxAttempts)
	}
	clock := opts.Clock
	if clock == nil {
		clock = ports.SystemClock{}
	}
	s := &Store{pool: pool, clock: clock, outboxMaxAttempts: opts.OutboxMaxAttempts}
	s.Tenants = TenantRepository{st: s}
	s.Projects = ProjectRepository{st: s}
	s.Threads = ThreadRepository{st: s}
	s.Specs = SpecRepository{st: s}
	s.Tasks = TaskRepository{st: s}
	s.Runs = RunRepository{st: s}
	s.Workspaces = WorkspaceRepository{st: s}
	s.Artifacts = ArtifactRepository{st: s}
	s.Audit = AuditRepository{st: s}
	s.PolicyDecisions = PolicyDecisionRepository{st: s}
	s.Integrations = IntegrationRepository{st: s}
	s.Events = EventRepository{st: s}
	s.Outbox = OutboxRepository{st: s}
	return s, nil
}

func (s *Store) Close() error { return s.pool.Close() }

// now is the store's single time authority for outbox scheduling.
func (s *Store) now() time.Time { return s.clock.Now().UTC() }

// q returns the caller's transaction when present, else the pool. All store
// methods route through here so a single unit of work observes one snapshot.
func (s *Store) q(ctx context.Context) executor {
	if tx := txFrom(ctx); tx != nil {
		return tx
	}
	return s.pool
}

// mapUniqueViolation converts known constraint violations into typed domain
// conflicts; anything else is returned unchanged for upstream classification.
func mapUniqueViolation(err error, code string, format string, args ...any) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.Conflictf(code, format, args...)
	}
	return err
}

func marshalJSONColumn(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, domain.Internalf(err, "json_column", "serialize jsonb column")
	}
	return b, nil
}

func unmarshalJSONColumn(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return domain.Internalf(err, "json_column", "deserialize jsonb column")
	}
	return nil
}

// Do runs fn as one unit of work on this store. Nesting joins the caller's
// transaction; an error or panic rolls the unit back (panic is re-raised
// after rollback). Isolation is PostgreSQL's default READ COMMITTED, which
// is sufficient because multi-writer correctness relies on unique
// constraints and compare-and-swap version guards rather than repeatable
// reads (ADR-0010).
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if txFrom(ctx) != nil {
		return fn(ctx)
	}
	tx, beginErr := s.pool.BeginTx(ctx, nil)
	if beginErr != nil {
		return domain.Internalf(beginErr, "db_tx", "begin unit of work")
	}
	committed := false
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(withTx(ctx, tx)); err != nil {
		return err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return domain.Internalf(commitErr, "db_tx", "commit unit of work")
	}
	committed = true
	return nil
}

var _ ports.Transactor = (*Store)(nil)
