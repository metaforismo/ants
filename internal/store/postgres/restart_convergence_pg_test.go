package postgres_test

// Restart-convergence proof (ADR-0017): the durable outbox converges to
// fully-delivered across a real process death, with at-least-once redelivery
// but no duplicated logical effects at the consumer boundary.
//
// Production code versus test harness, explicitly:
//
//   - PRODUCTION components under test: the PostgreSQL store adapter (its
//     transactional event append + outbox enqueue) and the real
//     internal/outbox dispatcher with its lease, backoff, and fencing
//     machinery. Nothing in those packages is modified for this test.
//   - TEST HARNESS ONLY: restartSink — the first consumer of the documented
//     Sink seam (ADR-0011) — plus three scratch tables created in this
//     test's isolated database (attempt ledger, idempotent effects, hold
//     control). The production default LogSink has no observable effects,
//     so proving "no duplicated logical effects" requires a sink that HAS
//     effects; the Sink seam is exactly where external subscribers plug in.
//     No debug endpoints, no config surface, no production wiring exists
//     for any harness piece.
//
// Epoch one runs as a separate OS process (helper execution of this same
// test binary) and is SIGKILLed mid-dispatch while a harness "hold" forces
// every delivery to fail: memory, leases, and pooled sockets die exactly as
// in a crash. A fresh epoch-two process drains. Every wait polls database
// state under a deadline; timing never depends on fixed sleeps.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/outbox"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/store/migrate"
	"github.com/metaforismo/ants/internal/store/pgtestutil"
	pgrepos "github.com/metaforismo/ants/internal/store/postgres"
)

const (
	envRestartEpoch = "ANTS_TEST_RESTART_EPOCH"
	envRestartDSN   = "ANTS_TEST_RESTART_DSN"

	seededEvents = 6

	// Production-legal values (dispatcher/store validation bounds) chosen so
	// the scenario is fast while exercising real scheduling: held deliveries
	// retry with backoff instead of dead-lettering before the crash lands.
	restartInterval = 25 * time.Millisecond
	restartLease    = 2 * time.Second
	restartBackoff  = 10 * time.Millisecond
	restartAttempts = 100
)

var errDeliveryHeld = errors.New("delivery held by restart harness")

// TestMain branches into helper-process mode when the parent test re-execs
// this binary; otherwise it is the ordinary test entry point.
func TestMain(m *testing.M) {
	if epoch := os.Getenv(envRestartEpoch); epoch != "" {
		os.Exit(runRestartEpoch(epoch))
	}
	os.Exit(m.Run())
}

func runRestartEpoch(epoch string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store, err := pgrepos.New(ctx, pgrepos.Options{
		DSN:               os.Getenv(envRestartDSN),
		MaxOpenConns:      4,
		MaxIdleConns:      2,
		ConnMaxLifetime:   time.Minute,
		OutboxMaxAttempts: restartAttempts,
	})
	if err != nil {
		logger.Error("restart epoch: open store", "epoch", epoch, "error", err.Error())
		return 1
	}
	defer store.Close()

	harnessPool, err := sql.Open("pgx", os.Getenv(envRestartDSN))
	if err != nil {
		logger.Error("restart epoch: open harness pool", "epoch", epoch, "error", err.Error())
		return 1
	}
	defer harnessPool.Close()

	dispatcher, derr := outbox.New(
		store.Repositories().Outbox,
		restartSink{pool: harnessPool},
		logger,
		outbox.Config{
			BatchSize:        seededEvents * 2,
			Interval:         restartInterval,
			Lease:            restartLease,
			MaxAttempts:      restartAttempts,
			RetryBackoffBase: restartBackoff,
		},
		fmt.Sprintf("restart-epoch-%s-pid%d", epoch, os.Getpid()),
		nil,
	)
	if derr != nil {
		logger.Error("restart epoch: build dispatcher", "epoch", epoch, "error", derr.Error())
		return 1
	}
	if err := dispatcher.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("restart epoch: dispatcher failed", "epoch", epoch, "error", err.Error())
		return 1
	}
	return 0
}

// restartSink is the test-only consumer at the outbox boundary. Every
// Deliver invocation appends one attempt-ledger row (attempts are distinct
// from effects), honors the hold flag as a scripted transient outage, and
// otherwise applies the logical effect idempotently on event identity — the
// property every real subscriber must provide because delivery is
// at-least-once (ADR-0011). When die_on_effect is armed the sink crashes its
// own process with SIGKILL immediately after committing an effect and BEFORE
// the dispatcher can acknowledge, deterministically creating the
// effect-applied/unacknowledged state that forces already-effected work to
// be redelivered.
type restartSink struct {
	pool *sql.DB
}

var _ outbox.Sink = restartSink{}

func (s restartSink) Deliver(ctx context.Context, msg ports.OutboxMessage) error {
	eventID := strings.TrimPrefix(msg.DedupKey, "event:")
	if _, err := s.pool.ExecContext(ctx,
		`INSERT INTO restart_delivery_attempts (event_id) VALUES ($1)`, eventID); err != nil {
		return fmt.Errorf("record delivery attempt: %w", err)
	}
	var hold, die bool
	if err := s.pool.QueryRowContext(ctx,
		`SELECT hold, die_on_effect FROM restart_control WHERE id = 1`).Scan(&hold, &die); err != nil {
		return fmt.Errorf("read harness control: %w", err)
	}
	if hold {
		return errDeliveryHeld
	}
	if _, err := s.pool.ExecContext(ctx,
		`INSERT INTO restart_effects (event_id) VALUES ($1) ON CONFLICT (event_id) DO NOTHING`,
		eventID); err != nil {
		return fmt.Errorf("apply idempotent effect: %w", err)
	}
	if die {
		// Uncatchable and precisely placed: the logical effect is durable
		// while this delivery stays leased and unacknowledged.
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	return nil
}

// TestOutboxDeliveryConvergesAcrossProcessRestart seeds events through the
// transactional append, kills a dispatching process with SIGKILL, drains
// with a fresh process, then asserts state-based convergence with delivery
// attempts strictly exceeding uniquely-applied effects.
func TestOutboxDeliveryConvergesAcrossProcessRestart(t *testing.T) {
	ctx := context.Background()
	adminDSN := pgtestutil.DSN(t)
	isolatedDSN, _ := pgtestutil.IsolatedDatabase(ctx, t, adminDSN)

	pool, err := sql.Open("pgx", isolatedDSN)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	createRestartHarnessTables(t, pool)

	// Rows carry the attempt budget of the store that published them; both
	// epochs must agree or held deliveries would dead-letter before the
	// crash lands instead of surviving for epoch two to reclaim.
	seedStore, err := pgrepos.New(ctx, pgrepos.Options{
		DSN: isolatedDSN, MaxOpenConns: 2, MaxIdleConns: 1,
		ConnMaxLifetime: time.Minute, OutboxMaxAttempts: restartAttempts,
	})
	if err != nil {
		t.Fatalf("open seeding store: %v", err)
	}
	t.Cleanup(func() { _ = seedStore.Close() })

	tenantID := seedRestartTenant(t, ctx, seedStore)
	seeded := seedRestartEvents(t, ctx, seedStore, tenantID)

	// Phase A — scripted outage: hold is armed before launch, so epoch one
	// dispatches into a failing consumer and every message accumulates
	// failed attempts with backoff.
	epoch1 := startRestartEpoch(t, "1", isolatedDSN)
	waitForCountAtLeast(t, pool, "restart_delivery_attempts", seededEvents,
		20*time.Second, "every seeded message attempted under hold")

	// Phase B — deterministic crash inside the sink: release the hold and
	// arm die_on_effect. The next delivery that succeeds commits its effect
	// and is SIGKILLed before acknowledging, leaving durable effects on
	// leased, unacknowledged deliveries.
	if _, err := pool.ExecContext(ctx,
		`UPDATE restart_control SET hold = false, die_on_effect = true WHERE id = 1`); err != nil {
		t.Fatalf("arm sink crash: %v", err)
	}
	waitErr := waitRestartEpoch(epoch1, 20*time.Second)
	if waitErr == nil || !strings.Contains(fmt.Sprint(waitErr), "killed") {
		t.Fatalf("epoch 1 must die by its own in-sink SIGKILL, got %v (stderr:\n%s)", waitErr, epoch1.stderr.String())
	}
	epoch1.reaped.Store(true)
	t.Logf("epoch 1 crashed inside the sink as designed: %v", waitErr)

	// The crash must have left at least one applied-but-unacknowledged
	// effect; otherwise the redelivery phase proves nothing.
	waitForCountAtLeast(t, pool, "restart_effects", 1,
		10*time.Second, "an effect committed before the crash")

	// Phase C — fresh epoch over the wreckage: disarms nothing but time.
	// Effected-but-unacked messages are reclaimed after lease expiry and
	// delivered again; the idempotent sink must absorb them exactly once.
	if _, err := pool.ExecContext(ctx,
		`UPDATE restart_control SET die_on_effect = false WHERE id = 1`); err != nil {
		t.Fatalf("disarm sink crash: %v", err)
	}

	epoch2 := startRestartEpoch(t, "2", isolatedDSN)
	waitForOutboxConvergence(t, pool, seededEvents, 30*time.Second)
	stopRestartEpochGracefully(t, epoch2)

	assertRestartConvergence(t, pool, seeded)
}

// ---- epoch process management ----

type restartEpoch struct {
	cmd    *exec.Cmd
	stderr *strings.Builder
	reaped atomic.Bool
}

func startRestartEpoch(t *testing.T, epoch, dsn string) *restartEpoch {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command(exe) // helper mode is selected purely by environment
	cmd.Env = append(os.Environ(),
		envRestartEpoch+"="+epoch,
		envRestartDSN+"="+dsn,
	)
	stderr := &strings.Builder{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	e := &restartEpoch{cmd: cmd, stderr: stderr}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start epoch %s: %v", epoch, err)
	}
	t.Cleanup(func() { terminateRestartEpoch(e, 5*time.Second) })
	return e
}

// stopRestartEpochGracefully delivers SIGTERM — the same signal `ants serve`
// handles — and expects exit code 0, proving the fresh epoch also shuts down
// cleanly once converged.
func stopRestartEpochGracefully(t *testing.T, e *restartEpoch) {
	t.Helper()
	if err := e.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM epoch 2: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case werr := <-done:
		e.reaped.Store(true)
		if werr != nil {
			t.Errorf("epoch 2 must exit 0 after a graceful drain, got %v (stderr:\n%s)", werr, e.stderr.String())
		} else {
			t.Logf("epoch 2 drained and exited 0")
		}
	case <-time.After(10 * time.Second):
		t.Errorf("epoch 2 ignored SIGTERM; stderr:\n%s", e.stderr.String())
	}
}

// waitRestartEpoch reaps the epoch process with a bounded wait so a child
// that fails to die cannot hang the suite; the returned error distinguishes
// signal deaths from clean exits.
func waitRestartEpoch(e *restartEpoch, within time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case werr := <-done:
		e.reaped.Store(true)
		return werr
	case <-time.After(within):
		_ = e.cmd.Process.Kill()
		<-done
		e.reaped.Store(true)
		return fmt.Errorf("epoch did not die within %s; killed forcibly", within)
	}
}

func terminateRestartEpoch(e *restartEpoch, timeout time.Duration) {
	if !e.reaped.CompareAndSwap(false, true) {
		return
	}
	_ = e.cmd.Process.Kill()
	done := make(chan struct{})
	go func() { _ = e.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// ---- seeding ----

func seedRestartTenant(t *testing.T, ctx context.Context, store *pgrepos.Store) domain.TenantID {
	t.Helper()
	id, err := domain.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := domain.NewTenant(id, "restart-"+fmt.Sprint(time.Now().UnixNano()), "Restart Convergence", domain.PlanFree, "local", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Tenants.Create(ctx, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tenant.ID
}

func seedRestartEvents(t *testing.T, ctx context.Context, store *pgrepos.Store, tenantID domain.TenantID) []domain.EventID {
	t.Helper()
	ids := make([]domain.EventID, 0, seededEvents)
	for i := 0; i < seededEvents; i++ {
		evtID, err := domain.NewEventID()
		if err != nil {
			t.Fatal(err)
		}
		evt := &domain.Event{
			ID:               evtID,
			Type:             domain.EventTenantCreated,
			OccurredAt:       time.Now().UTC(),
			TenantID:         tenantID,
			AggregateType:    "tenant",
			AggregateID:      string(tenantID),
			AggregateVersion: int64(i + 1),
			Actor:            domain.Actor{Type: domain.PrincipalSystem},
			TraceID:          "tr_restart-convergence",
			Data:             map[string]any{"harness": true},
		}
		// The transactional append enqueues exactly one outbox delivery per
		// event (ADR-0011) — the work whose convergence this test proves.
		if err := store.Events.Append(ctx, evt); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
		ids = append(ids, evtID)
	}
	return ids
}

func createRestartHarnessTables(t *testing.T, pool *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE restart_delivery_attempts (
			seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			event_id text NOT NULL,
			at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE restart_effects (
			event_id text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE restart_control (
			id integer PRIMARY KEY,
			hold boolean NOT NULL,
			die_on_effect boolean NOT NULL)`,
		`INSERT INTO restart_control VALUES (1, TRUE, FALSE)`,
	}
	for _, stmt := range stmts {
		if _, err := pool.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("harness schema: %v\nstatement: %s", err, stmt)
		}
	}
}

// ---- deadline-bounded state polling ----

func waitForCountAtLeast(t *testing.T, pool *sql.DB, table string, minimum int, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRowContext(context.Background(),
			fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
			t.Fatalf("poll %s: %v", table, err)
		}
		if n >= minimum {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: %s never reached %d rows in time", table, what, minimum)
}

func waitForOutboxConvergence(t *testing.T, pool *sql.DB, wantDelivered int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var undelivered, rows, effects int
	for time.Now().Before(deadline) {
		if err := pool.QueryRowContext(context.Background(),
			`SELECT count(*) FILTER (WHERE status <> 'delivered'), count(*) FROM outbox`,
		).Scan(&undelivered, &rows); err != nil {
			t.Fatalf("poll outbox: %v", err)
		}
		if err := pool.QueryRowContext(context.Background(),
			`SELECT count(*) FROM restart_effects`).Scan(&effects); err != nil {
			t.Fatalf("poll effects: %v", err)
		}
		if undelivered == 0 && rows == wantDelivered && effects == wantDelivered {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("outbox did not converge in time: undelivered=%d rows=%d effects=%d (want %d delivered, %d effects)",
		undelivered, rows, effects, wantDelivered, wantDelivered)
}

// ---- final state-based assertions ----

func assertRestartConvergence(t *testing.T, pool *sql.DB, seeded []domain.EventID) {
	t.Helper()
	ctx := context.Background()

	var rows, delivered, attemptsSum, attemptsMin int
	err := pool.QueryRowContext(ctx,
		`SELECT count(*),
		       count(*) FILTER (WHERE status = 'delivered'),
		       coalesce(sum(attempts), 0),
		       coalesce(min(attempts), 0)
		 FROM outbox`).Scan(&rows, &delivered, &attemptsSum, &attemptsMin)
	if err != nil {
		t.Fatalf("outbox summary: %v", err)
	}
	if rows != seededEvents || delivered != seededEvents {
		t.Errorf("convergence requires all %d outbox rows delivered, got %d/%d", seededEvents, delivered, rows)
	}
	if attemptsMin < 2 {
		t.Errorf("every message must have been leased by BOTH epochs (held in epoch 1, drained in epoch 2); min attempts = %d", attemptsMin)
	}
	if attemptsSum <= seededEvents {
		t.Errorf("total leases %d must strictly exceed seeded events %d to prove redelivery", attemptsSum, seededEvents)
	}

	var attemptRows int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM restart_delivery_attempts`).Scan(&attemptRows); err != nil {
		t.Fatalf("attempt ledger: %v", err)
	}
	var effectRows int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM restart_effects`).Scan(&effectRows); err != nil {
		t.Fatalf("effects: %v", err)
	}
	if attemptRows < 2*seededEvents {
		t.Errorf("attempt ledger must record epoch-1 failures plus epoch-2 successes (%d), got %d", 2*seededEvents, attemptRows)
	}
	if effectRows != seededEvents {
		t.Errorf("logical effects must equal seeded events exactly (%d), got %d — duplication or loss", seededEvents, effectRows)
	}

	// Effect identity: exactly the seeded event ids, each applied once.
	gotEffects := map[string]int{}
	effectsIter, err := pool.QueryContext(ctx, `SELECT event_id FROM restart_effects`)
	if err != nil {
		t.Fatalf("read effects: %v", err)
	}
	defer effectsIter.Close()
	for effectsIter.Next() {
		var id string
		if err := effectsIter.Scan(&id); err != nil {
			t.Fatal(err)
		}
		gotEffects[id]++
	}
	if err := effectsIter.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, id := range seeded {
		want[string(id)] = true
	}
	for id := range gotEffects {
		if !want[id] {
			t.Errorf("effect recorded for unknown event %q", id)
		}
	}
	for id := range want {
		if gotEffects[id] != 1 {
			t.Errorf("event %q applied %d times, want exactly 1", id, gotEffects[id])
		}
	}

	var eventRows int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM events WHERE trace_id = 'tr_restart-convergence'`).Scan(&eventRows); err != nil {
		t.Fatalf("events survived: %v", err)
	}
	if eventRows != seededEvents {
		t.Errorf("durable events must remain untouched (%d), got %d", seededEvents, eventRows)
	}
}
