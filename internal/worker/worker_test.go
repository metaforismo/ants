package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
	"github.com/metaforismo/ants/internal/store/storetest"
)

// Every timing decision in these tests is driven by the store's advancing
// clock or by channel hand-offs. The only wall-clock waits are guard
// timeouts that fail the test when an expected event never arrives; no test
// orders semantics through sleeps.

const (
	testOwner = "worker-under-test"
	// beatEvery is the real-time heartbeat cadence inside executeClaim: fast
	// enough that a live loop beats within the guard window, independent of
	// every store-clock scheduling decision.
	beatEvery = 10 * time.Millisecond
	// guardTimeout bounds any single channel wait in these tests.
	guardTimeout = 5 * time.Second
)

var completedPath = []domain.RunStatus{
	domain.RunPlanning, domain.RunExecuting, domain.RunIntegrating,
	domain.RunVerifying, domain.RunReporting, domain.RunCompleted,
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() Config {
	return Config{
		BatchSize:      8,
		Interval:       time.Hour, // rounds are driven manually via ProcessOnce
		Lease:          time.Hour, // store-clock lease: never expires unless advanced
		HeartbeatEvery: beatEvery,
		CleanupTimeout: time.Second,
		Concurrency:    4,
		MaxAttempts:    3,
	}
}

// ---- executor stand-in ----

// stubExecutor stands in for the orchestration engine at the Executor
// boundary. Each run's behavior is scripted; per-run outcomes and concurrency
// are recorded so tests can pin cancellation delivery and the parallelism
// cap without sleeping.
type stubExecutor struct {
	runStore ports.RunStore

	mu              sync.Mutex
	behavior        map[domain.RunID]func(ctx context.Context) error
	outcomes        map[domain.RunID]error
	recovered       []recoveryRecord
	converged       []convergeRecord
	convergeFailure map[domain.RunID]error

	started   chan domain.RunID
	active    atomic.Int64
	maxActive atomic.Int64
}

type recoveryRecord struct {
	runID domain.RunID
	cause string
}

type convergeRecord struct {
	runID    domain.RunID
	attempts int
	cause    string
}

func newStubExecutor(runs ports.RunStore) *stubExecutor {
	return &stubExecutor{
		runStore:        runs,
		behavior:        map[domain.RunID]func(ctx context.Context) error{},
		outcomes:        map[domain.RunID]error{},
		convergeFailure: map[domain.RunID]error{},
		started:         make(chan domain.RunID, 64),
	}
}

func (e *stubExecutor) script(id domain.RunID, fn func(ctx context.Context) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.behavior[id] = fn
}

// unscripted runs block until their context is done — the honest default for
// a long-running execution that nothing ever completes.
func (e *stubExecutor) Execute(ctx context.Context, _ domain.TenantID, id domain.RunID) error {
	cur := e.active.Add(1)
	for {
		max := e.maxActive.Load()
		if cur <= max || e.maxActive.CompareAndSwap(max, cur) {
			break
		}
	}
	defer e.active.Add(-1)

	select {
	case e.started <- id:
	default:
	}
	e.mu.Lock()
	fn := e.behavior[id]
	e.mu.Unlock()

	var err error
	if fn != nil {
		err = fn(ctx)
	} else {
		<-ctx.Done()
		err = ctx.Err()
	}
	e.mu.Lock()
	e.outcomes[id] = err
	e.mu.Unlock()
	return err
}

// RecoverInterrupted converges like the engine does: one guarded terminal
// failed write for a non-terminal run, nil for one already converged.
func (e *stubExecutor) RecoverInterrupted(ctx context.Context, tenantID domain.TenantID, id domain.RunID, cause error) error {
	e.mu.Lock()
	e.recovered = append(e.recovered, recoveryRecord{runID: id, cause: cause.Error()})
	e.mu.Unlock()

	run, err := e.runStore.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return nil
	}
	expected := run.Version
	failure := &domain.FailureInfo{Code: "interrupted", Message: cause.Error(), Transient: false}
	if err := run.Finish(domain.RunFailed, time.Now().UTC(), failure); err != nil {
		return err
	}
	return e.runStore.Update(ctx, run, expected)
}

// ConvergeExhausted mirrors the engine's exhausted-run path: a guarded
// terminal write — cancelled for a never-started run, failed with the
// exhausted code for one abandoned mid-flight — unless a failure is injected
// for that run.
func (e *stubExecutor) ConvergeExhausted(ctx context.Context, tenantID domain.TenantID, id domain.RunID, attempts int, cause error) error {
	e.mu.Lock()
	e.converged = append(e.converged, convergeRecord{runID: id, attempts: attempts, cause: cause.Error()})
	injected := e.convergeFailure[id]
	e.mu.Unlock()
	if injected != nil {
		return injected
	}

	run, err := e.runStore.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return nil
	}
	expected := run.Version
	target := domain.RunFailed
	failure := &domain.FailureInfo{Code: "run_attempts_exhausted", Message: cause.Error(), Transient: false}
	if run.Status == domain.RunPending {
		target = domain.RunCancelled
		failure = nil
	}
	if err := run.Finish(target, time.Now().UTC(), failure); err != nil {
		return err
	}
	return e.runStore.Update(ctx, run, expected)
}

func (e *stubExecutor) failConvergence(id domain.RunID, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.convergeFailure[id] = err
}

// clearConvergenceFailure removes a previously injected convergence failure.
func (e *stubExecutor) clearConvergenceFailure(id domain.RunID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.convergeFailure, id)
}

func (e *stubExecutor) convergenceCount(t *testing.T) int {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.converged)
}

func (e *stubExecutor) waitStarted(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-e.started:
		case <-time.After(guardTimeout):
			t.Fatalf("timed out waiting for execution %d to start", i+1)
		}
	}
}

func (e *stubExecutor) outcome(t *testing.T, id domain.RunID) error {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	err, ok := e.outcomes[id]
	if !ok {
		t.Fatalf("run %s has no recorded outcome", id)
	}
	return err
}

func (e *stubExecutor) recoveryCount(t *testing.T) int {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.recovered)
}

// neverStarted fails the test if the given run was ever handed to Execute.
func (e *stubExecutor) neverStarted(t *testing.T, id domain.RunID) {
	t.Helper()
	select {
	case started := <-e.started:
		t.Fatalf("run %s must never execute, but execution of %s started", id, started)
	default:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.outcomes[id]; ok {
		t.Fatalf("run %s must never execute, but an outcome was recorded", id)
	}
}

// ---- claim-store decorator ----

// beatWatcher wraps the claim store so tests observe acquisition credentials
// and await heartbeat attempts deterministically.
type beatWatcher struct {
	ports.RunClaimStore
	acquired []*domain.RunClaim
	beats    chan struct{}
	mu       sync.Mutex
}

func (b *beatWatcher) AcquireNext(ctx context.Context, req ports.RunClaimBatchRequest) ([]*domain.RunClaim, error) {
	claims, err := b.RunClaimStore.AcquireNext(ctx, req)
	if err == nil {
		b.mu.Lock()
		b.acquired = append(b.acquired, claims...)
		b.mu.Unlock()
	}
	return claims, err
}

func (b *beatWatcher) Heartbeat(ctx context.Context, ref ports.RunClaimRef, extendFor time.Duration) (*domain.RunClaim, error) {
	c, err := b.RunClaimStore.Heartbeat(ctx, ref, extendFor)
	select {
	case b.beats <- struct{}{}:
	default:
	}
	return c, err
}

func (b *beatWatcher) waitBeats(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-b.beats:
		case <-time.After(guardTimeout):
			t.Fatalf("timed out waiting for heartbeat %d", i+1)
		}
	}
}

// acquiredRef returns the credential tuple of the nth acquisition epoch in
// order, letting tests replay stale credentials exactly as the worker saw
// them at acquire time.
func (b *beatWatcher) acquiredRef(t *testing.T, n int) ports.RunClaimRef {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if n >= len(b.acquired) {
		t.Fatalf("acquisition %d never happened (%d recorded)", n+1, len(b.acquired))
	}
	c := b.acquired[n]
	return ports.RunClaimRef{TenantID: c.TenantID, RunID: c.RunID, Owner: c.Owner, Token: c.Token, Generation: c.Generation}
}

// ---- world ----

type testWorld struct {
	t        *testing.T
	repos    ports.Repositories
	clock    *storetest.AdvancingClock
	exec     *stubExecutor
	watch    *beatWatcher
	worker   *Worker
	tenantID domain.TenantID
	threadID domain.ThreadID
}

func newTestWorld(t *testing.T, mutate func(*Config)) *testWorld {
	return newTestWorldFull(t, mutate, nil)
}

// newTestWorldFull builds the standard world with an explicit observer
// (possibly nil), so observer tests drive the exact production dispatch
// paths instead of a separate instrumented variant.
func newTestWorldFull(t *testing.T, mutate func(*Config), obs Observer) *testWorld {
	t.Helper()
	cfg := testConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	clock := storetest.NewAdvancingClock()
	mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	if err != nil {
		t.Fatalf("memory repos: %v", err)
	}
	repos := mem.AsPorts()
	ctx := context.Background()

	tenantStr, _ := domain.NewID(domain.PrefixTenant)
	tenant, err := domain.NewTenant(domain.TenantID(tenantStr), "acme", "Acme", domain.PlanFree, "local", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	projectStr, _ := domain.NewID(domain.PrefixProject)
	project, err := domain.NewProject(domain.ProjectID(projectStr), tenant.ID, "calc", "Calculator", "main", "", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	threadStr, _ := domain.NewID(domain.PrefixThread)
	thread, err := domain.NewThread(domain.ThreadID(threadStr), tenant.ID, project.ID, "worker contract thread",
		domain.PrincipalID("prn_workertestprincipal00"), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatal(err)
	}

	exec := newStubExecutor(repos.Runs)
	watch := &beatWatcher{RunClaimStore: repos.RunClaims, beats: make(chan struct{}, 256)}
	w := &testWorld{
		t: t, repos: repos, clock: clock, exec: exec, watch: watch,
		tenantID: tenant.ID, threadID: thread.ID,
	}
	w.worker, err = New(watch, repos.Runs, exec, testLogger(), cfg, testOwner, obs)
	if err != nil {
		t.Fatalf("build worker: %v", err)
	}
	return w
}

// seedRun creates a pending run with its initial runnable claim, mirroring
// what StartRun commits in production. Each call moves the store clock so
// seeded runs get distinct creation instants — dispatch order is
// (created_at, run_id), so equal timestamps would make batch selection
// ambiguous.
func (w *testWorld) seedRun(key string) *domain.Run {
	w.t.Helper()
	w.clock.Advance(time.Millisecond)
	ctx := context.Background()
	idStr, err := domain.NewID(domain.PrefixRun)
	if err != nil {
		w.t.Fatal(err)
	}
	run, err := domain.NewRun(domain.RunID(idStr), w.tenantID, w.threadID, "idem-"+key, w.clock.Now())
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.repos.Runs.Create(ctx, run); err != nil {
		w.t.Fatal(err)
	}
	if err := w.repos.RunClaims.Create(ctx, run.TenantID, run.ID); err != nil {
		w.t.Fatal(err)
	}
	return run
}

// completeBehavior walks the run through legal transitions to target before
// returning, exactly the precondition the real engine guarantees when Execute
// returns nil after finishing a run.
func (w *testWorld) completeBehavior(id domain.RunID, target domain.RunStatus) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return w.advanceRun(ctx, id, target)
	}
}

func (w *testWorld) advanceRun(ctx context.Context, id domain.RunID, target domain.RunStatus) error {
	for _, next := range pathTo(target) {
		run, err := w.repos.Runs.Get(ctx, w.tenant(), id)
		if err != nil {
			return err
		}
		expected := run.Version
		if err := run.TransitionTo(next); err != nil {
			return err
		}
		if err := w.repos.Runs.Update(ctx, run, expected); err != nil {
			return err
		}
	}
	return nil
}

// pathTo returns the legal transition chain from RunPending to a target
// status per the closed run machine.
func pathTo(target domain.RunStatus) []domain.RunStatus {
	switch target {
	case domain.RunPlanning:
		return []domain.RunStatus{domain.RunPlanning}
	case domain.RunExecuting:
		return []domain.RunStatus{domain.RunPlanning, domain.RunExecuting}
	case domain.RunCancelled:
		return []domain.RunStatus{domain.RunCancelled}
	case domain.RunFailed:
		return []domain.RunStatus{domain.RunPlanning, domain.RunFailed}
	case domain.RunCompleted:
		return append([]domain.RunStatus(nil), completedPath...)
	default:
		return nil
	}
}

func (w *testWorld) run(id domain.RunID) *domain.Run {
	w.t.Helper()
	run, err := w.repos.Runs.Get(context.Background(), w.tenant(), id)
	if err != nil {
		w.t.Fatalf("reload run %s: %v", id, err)
	}
	return run
}

func (w *testWorld) claim(id domain.RunID) *domain.RunClaim {
	w.t.Helper()
	claim, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), id)
	if err != nil {
		w.t.Fatalf("reload claim %s: %v", id, err)
	}
	return claim
}

func (w *testWorld) tenant() domain.TenantID { return w.tenantID }

// burnAttempt acquires and immediately releases the claim once, exactly like
// an epoch that took the lease and gave it back without executing anything.
// Each call bumps the claim's attempts counter by one.
func (w *testWorld) burnAttempt(id domain.RunID) {
	w.t.Helper()
	acquired, err := w.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: w.tenant(), RunID: id, Owner: "burner-worker", LeaseFor: time.Minute,
	})
	if err != nil {
		w.t.Fatalf("burn attempt acquire: %v", err)
	}
	ref := ports.RunClaimRef{
		TenantID: acquired.TenantID, RunID: acquired.RunID,
		Owner: acquired.Owner, Token: acquired.Token, Generation: acquired.Generation,
	}
	if _, err := w.repos.RunClaims.Release(context.Background(), ref); err != nil {
		w.t.Fatalf("burn attempt release: %v", err)
	}
}

// runInRound executes ProcessOnce on its own goroutine and fails the test if
// the round does not return within the guard window.
func (w *testWorld) runInRound(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := w.worker.ProcessOnce(ctx)
		done <- err
	}()
	return done
}

func awaitRound(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round failed: %v", err)
		}
	case <-time.After(guardTimeout):
		t.Fatalf("round did not finish within the guard window")
	}
}

// ---- construction guards ----

func TestNewRejectsMissingDependenciesAndBadIdentity(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	if _, err := New(nil, nil, nil, logger, cfg, testOwner, nil); err == nil {
		t.Fatal("missing dependencies must be rejected")
	}
	// Both fields are individually legal, but one heartbeat margin leaves a
	// live worker expiring after two missed beats.
	badCfg := cfg
	badCfg.Lease = time.Second
	badCfg.HeartbeatEvery = 500 * time.Millisecond
	if _, err := New(&beatWatcher{}, nil, nil, logger, badCfg, testOwner, nil); err == nil {
		t.Fatal("config without heartbeat margin must be rejected")
	}
	if _, err := New(&beatWatcher{}, nil, nil, logger, cfg, "", nil); err == nil {
		t.Fatal("empty owner identity must be rejected")
	}
}

func TestConfigValidateBounds(t *testing.T) {
	base := testConfig()
	if err := base.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	// The margin boundary itself is legal: lease == 3x heartbeat, with both
	// fields at production-valid magnitudes.
	equal := base
	equal.Lease = time.Second
	equal.HeartbeatEvery = equal.Lease / 3
	if err := equal.Validate(); err != nil {
		t.Fatalf("lease == 3x heartbeat must validate: %v", err)
	}
	violations := []Config{
		func() Config { c := base; c.BatchSize = 1001; return c }(),
		func() Config { c := base; c.Interval = 5 * time.Millisecond; return c }(),
		func() Config { c := base; c.Lease = 500 * time.Millisecond; return c }(),
		func() Config { c := base; c.HeartbeatEvery = 5 * time.Millisecond; return c }(),
		func() Config { c := base; c.CleanupTimeout = 50 * time.Millisecond; return c }(),
		func() Config { c := base; c.Concurrency = 0; return c }(),
		func() Config { c := base; c.MaxAttempts = 0; return c }(),
		func() Config { c := base; c.MaxAttempts = 11; return c }(),
	}
	for i, cfg := range violations {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("violation %d must be rejected: %+v", i, cfg)
		}
	}
}

// ---- behavior ----

func TestProcessOnceClaimsExecutesAndCompletes(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("happy")
	w.exec.script(run.ID, w.completeBehavior(run.ID, domain.RunCompleted))

	done := w.runInRound(context.Background())
	w.exec.waitStarted(t, 1)
	awaitRound(t, done)

	if got := w.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run status %s, want completed", got)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("completed run's claim must be gone, got %v", err)
	}
	if err := w.exec.outcome(t, run.ID); err != nil {
		t.Fatalf("execution must end cleanly, got %v", err)
	}
	if w.exec.recoveryCount(t) != 0 {
		t.Fatal("happy path must not invoke recovery")
	}
}

func TestHeartbeatRenewsLiveLeaseWhileExecuting(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("renew")
	gate := make(chan struct{})
	w.exec.script(run.ID, func(ctx context.Context) error {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		return w.advanceRun(ctx, run.ID, domain.RunCompleted)
	})

	done := w.runInRound(context.Background())
	w.exec.waitStarted(t, 1)
	w.watch.waitBeats(t, 1)

	before := w.claim(run.ID).ExpiresAt
	w.clock.Advance(10 * time.Minute)
	w.watch.waitBeats(t, 1)

	after := w.claim(run.ID).ExpiresAt
	if after == nil || before == nil || !after.After(*before) {
		t.Fatalf("heartbeat must extend the deadline from the current store instant: %v -> %v", before, after)
	}

	close(gate)
	awaitRound(t, done)
	if got := w.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run status %s, want completed", got)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("completed run's claim must be gone, got %v", err)
	}
}

func TestLostLeaseCancelsExecutionAndFencesStaleEpoch(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.Lease = time.Second })
	run := w.seedRun("stolen")
	// The execution only ever ends through cancellation, so a finished round
	// proves the lost lease — not the engine — ended this epoch.
	w.exec.script(run.ID, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	done := w.runInRound(context.Background())
	w.exec.waitStarted(t, 1)
	w.watch.waitBeats(t, 1)

	// The lease lapses with the holder still alive: two missed beats expire
	// it even though this process is running, which is exactly the crash
	// signature another instance must be able to take over from.
	w.clock.Advance(3 * w.worker.cfg.Lease)
	awaitRound(t, done)

	if err := w.exec.outcome(t, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost lease must cancel the execution, got %v", err)
	}
	if got := w.run(run.ID).Status; got != domain.RunPending {
		t.Fatalf("fenced epoch must not have written terminal state, got %s", got)
	}

	// The forfeited epoch cannot revive its lapsed lease, and until some
	// other instance reclaims, the claim stays exactly as it lapsed.
	staleRef := w.watch.acquiredRef(t, 0)
	if _, err := w.repos.RunClaims.Heartbeat(context.Background(), staleRef, w.worker.cfg.Lease); domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("expired epoch must not revive its lease, got %v", err)
	}
	held := w.claim(run.ID)
	if held.Status != domain.ClaimClaimed || held.Generation != 1 || held.Owner != testOwner {
		t.Fatalf("claim must stay untouched until reclaim: %+v", held)
	}

	// Once a successor takes over with a fresh epoch, every mutation by the
	// dead credentials is fenced out of release and completion.
	if _, err := w.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: w.tenant(), RunID: run.ID, Owner: "successor-worker", LeaseFor: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.repos.RunClaims.Release(context.Background(), staleRef); domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("superseded epoch must not release the claim, got %v", err)
	}
	if err := w.repos.RunClaims.Complete(context.Background(), staleRef); domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("superseded epoch must not complete the claim, got %v", err)
	}
	held = w.claim(run.ID)
	if held.Status != domain.ClaimClaimed || held.Generation != 2 || held.Owner != "successor-worker" {
		t.Fatalf("takeover must hold a fresh untouched epoch: %+v", held)
	}
}

func TestCrashBeforeCompletionExpiresAndIsReclaimedOnRestart(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("crashed")

	// A previous process claimed the run and died before completing it: the
	// claim exists with a lease that silently lapses.
	dead, err := w.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: w.tenant(), RunID: run.ID, Owner: "dead-worker", LeaseFor: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(31 * time.Second)

	w.exec.script(run.ID, w.completeBehavior(run.ID, domain.RunCompleted))
	awaitRound(t, w.runInRound(context.Background()))

	if got := w.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("restarted worker must finish the abandoned run, got %s", got)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("reclaimed claim must be gone after completion, got %v", err)
	}
	ref := w.watch.acquiredRef(t, 0)
	if ref.Generation != dead.Generation+1 || ref.Owner != testOwner {
		t.Fatalf("reclaim must mint a fresh epoch: %+v", ref)
	}
}

func TestExpiredNonPendingRunConvergesOnceToInterruptedFailure(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("interrupted")
	// Simulate the crash mid-flight: execution had already moved the run past
	// pending before the lease lapsed.
	if err := w.advanceRun(context.Background(), run.ID, domain.RunExecuting); err != nil {
		t.Fatal(err)
	}
	if _, err := w.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: w.tenant(), RunID: run.ID, Owner: "dead-worker", LeaseFor: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(31 * time.Second)

	awaitRound(t, w.runInRound(context.Background()))
	finished := w.run(run.ID)
	if finished.Status != domain.RunFailed || finished.Failure == nil {
		t.Fatalf("abandoned mid-flight run must converge to classified failure, got %+v", finished)
	}

	// A second round finds nothing to recover: convergence happens once.
	awaitRound(t, w.runInRound(context.Background()))
	if w.exec.recoveryCount(t) != 1 {
		t.Fatalf("recovery must converge exactly once, ran %d times", w.exec.recoveryCount(t))
	}
}

// TestAttemptsAtBudgetStillExecute pins the boundary of the dispatch budget:
// an acquisition that lands exactly on MaxAttempts is the final legitimate
// chance and must run, not converge.
func TestAttemptsAtBudgetStillExecute(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.MaxAttempts = 2 })
	run := w.seedRun("last-chance")
	w.burnAttempt(run.ID)
	w.exec.script(run.ID, w.completeBehavior(run.ID, domain.RunCompleted))

	awaitRound(t, w.runInRound(context.Background()))

	// The round's own acquisition was dispatch number two: at the budget.
	if got := w.watch.acquiredRef(t, 0).Generation; got != 2 {
		t.Fatalf("precondition: this round must be acquisition 2, got %d", got)
	}
	if got := w.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("claim at the budget must still execute, run status %s", got)
	}
	if w.exec.convergenceCount(t) != 0 {
		t.Fatal("executing at the budget must not invoke convergence")
	}
}

func TestExhaustedPendingRunConvergesInsteadOfExecuting(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.MaxAttempts = 2 })
	run := w.seedRun("poison")
	for i := 0; i < 3; i++ {
		w.burnAttempt(run.ID)
	}

	awaitRound(t, w.runInRound(context.Background()))

	w.exec.neverStarted(t, run.ID)
	finished := w.run(run.ID)
	// A never-started run converges to cancelled — the honest terminal state
	// the run machine offers work that never began — with the exhausted code
	// recorded on its status event.
	if finished.Status != domain.RunCancelled {
		t.Fatalf("over-budget pending run must converge to cancelled, got %+v", finished)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("converged exhausted run's claim must be gone, got %v", err)
	}

	// Convergence is terminal: the next round has nothing to reclaim.
	awaitRound(t, w.runInRound(context.Background()))
	if w.exec.convergenceCount(t) != 1 {
		t.Fatalf("exhausted convergence must happen exactly once, ran %d times", w.exec.convergenceCount(t))
	}
}

func TestExhaustedMidFlightRunDoesNotRecoverAsInterrupted(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.MaxAttempts = 1 })
	run := w.seedRun("burnt-midflight")
	// The previous epoch moved the run mid-flight and died; enough reclaims
	// followed that this acquisition is already over budget.
	w.advanceRun(context.Background(), run.ID, domain.RunExecuting)
	w.burnAttempt(run.ID)

	awaitRound(t, w.runInRound(context.Background()))

	w.exec.neverStarted(t, run.ID)
	if w.exec.recoveryCount(t) != 0 {
		t.Fatal("an over-budget run must converge as exhausted, not as interrupted")
	}
	finished := w.run(run.ID)
	if finished.Status != domain.RunFailed || finished.Failure == nil ||
		finished.Failure.Code != "run_attempts_exhausted" {
		t.Fatalf("mid-flight over-budget run must converge exhausted, got %+v", finished)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("converged exhausted run's claim must be gone, got %v", err)
	}
}

func TestConvergeExhaustedFailureLeavesClaimForRetry(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.MaxAttempts = 1 })
	run := w.seedRun("flaky-converge")
	w.burnAttempt(run.ID)
	w.exec.failConvergence(run.ID, fmt.Errorf("persistence outage"))

	awaitRound(t, w.runInRound(context.Background()))

	// A failed convergence must leave both the run and its held claim for
	// expiry-based retry — no half-converged state, no lost work.
	held := w.claim(run.ID)
	if held.Status != domain.ClaimClaimed {
		t.Fatalf("failed convergence must keep the claim until expiry, got %+v", held)
	}
	if got := w.run(run.ID).Status; got != domain.RunPending {
		t.Fatalf("failed convergence must not mutate the run, got %s", got)
	}
	if w.exec.convergenceCount(t) != 1 {
		t.Fatalf("convergence attempted once, ran %d times", w.exec.convergenceCount(t))
	}

	// Once the outage clears and the lease lapses, the reclaimed claim
	// converges on the next round: the reclaim bumps attempts past the
	// budget again.
	w.exec.clearConvergenceFailure(run.ID)
	w.clock.Advance(2 * w.worker.cfg.Lease)
	awaitRound(t, w.runInRound(context.Background()))
	finished := w.run(run.ID)
	if finished.Status != domain.RunCancelled {
		t.Fatalf("retry after cleared outage must converge the run, got %+v", finished)
	}
	if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("converged exhausted run's claim must be gone, got %v", err)
	}
}

func TestShutdownCancellationStillReleasesOnDetachedBoundedContext(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("shutdown")

	ctx, cancel := context.WithCancel(context.Background())
	done := w.runInRound(ctx)
	w.exec.waitStarted(t, 1)
	cancel()
	awaitRound(t, done)

	// The round context is cancelled, yet the claim was handed back: disposal
	// persists on a context detached from shutdown cancellation.
	released := w.claim(run.ID)
	if released.Status != domain.ClaimRunnable || released.Owner != "" {
		t.Fatalf("cancelled execution must release its claim for retry, got %+v", released)
	}
	if got := w.run(run.ID).Status; got != domain.RunPending {
		t.Fatalf("released run stays pending for the next epoch, got %s", got)
	}
	if err := w.exec.outcome(t, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("execution must observe shutdown cancellation, got %v", err)
	}
}

func TestConcurrencyCapNeverHoldsMoreLeasesThanSlots(t *testing.T) {
	const total = 4
	w := newTestWorld(t, func(c *Config) { c.BatchSize = 8; c.Concurrency = 2 })

	gates := make([]chan struct{}, total)
	runs := make([]*domain.Run, total)
	for i := range total {
		gates[i] = make(chan struct{})
		run := w.seedRun(fmt.Sprintf("cap-%d", i))
		runs[i] = run
		capRunID, gate := run.ID, gates[i]
		w.exec.script(capRunID, func(ctx context.Context) error {
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
			return w.advanceRun(ctx, capRunID, domain.RunCompleted)
		})
	}

	done := w.runInRound(context.Background())
	w.exec.waitStarted(t, 2) // capped at Concurrency despite BatchSize=8
	for i := 2; i < total; i++ {
		if got := w.claim(runs[i].ID).Status; got != domain.ClaimRunnable {
			t.Fatalf("queued run %d must keep its runnable claim, got %s", i, got)
		}
	}
	if max := w.exec.maxActive.Load(); max > 2 {
		t.Fatalf("simultaneous executions exceeded the cap: %d", max)
	}
	for i := range total {
		close(gates[i])
	}
	awaitRound(t, done)

	// One round never queues work behind its slots: the leftover runnable
	// claims are picked up by the next round once the previous leases are gone.
	awaitRound(t, w.runInRound(context.Background()))
	for i, run := range runs {
		if got := w.run(run.ID).Status; got != domain.RunCompleted {
			t.Fatalf("run %d status %s, want completed", i, got)
		}
	}
	if max := w.exec.maxActive.Load(); max > 2 {
		t.Fatalf("simultaneous executions exceeded the cap across rounds: %d", max)
	}
}

func TestTerminalLeftoverClaimsCleanUpIdempotently(t *testing.T) {
	arrangements := map[string]func(w *testWorld, run *domain.Run){
		"runnable-leftover": func(w *testWorld, run *domain.Run) {
			if err := w.advanceRun(context.Background(), run.ID, domain.RunCompleted); err != nil {
				t.Fatal(err)
			}
			// The claim stays runnable: the previous epoch died between the
			// terminal write and its fenced Complete.
		},
		"expired-held-leftover": func(w *testWorld, run *domain.Run) {
			if _, err := w.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
				TenantID: w.tenant(), RunID: run.ID, Owner: "dead-worker", LeaseFor: 10 * time.Second,
			}); err != nil {
				t.Fatal(err)
			}
			if err := w.advanceRun(context.Background(), run.ID, domain.RunCancelled); err != nil {
				t.Fatal(err)
			}
			w.clock.Advance(11 * time.Second)
		},
	}
	for name, arrange := range arrangements {
		t.Run(name, func(t *testing.T) {
			w := newTestWorld(t, nil)
			run := w.seedRun("leftover")
			arrange(w, run)

			awaitRound(t, w.runInRound(context.Background()))
			if _, err := w.repos.RunClaims.Get(context.Background(), w.tenant(), run.ID); domain.ErrKindOf(err) != domain.ErrKindNotFound {
				t.Fatalf("leftover claim of a terminal run must be removed, got %v", err)
			}
			// Idempotent: the next round is a clean no-op.
			n, err := w.worker.ProcessOnce(context.Background())
			if err != nil || n != 0 {
				t.Fatalf("follow-up round must find nothing: %d %v", n, err)
			}
			if w.exec.recoveryCount(t) != 0 {
				t.Fatal("terminal leftovers must not route through recovery")
			}
		})
	}
}

func TestRunStopsCleanlyAndDrainsExecutionsOnCancel(t *testing.T) {
	w := newTestWorld(t, func(c *Config) { c.Interval = 10 * time.Millisecond })
	completed := make(chan struct{}, 8)
	first := w.seedRun("run-loop-1")
	second := w.seedRun("run-loop-2")
	for _, run := range []*domain.Run{first, second} {
		runID := run.ID
		w.exec.script(runID, func(ctx context.Context) error {
			if err := w.advanceRun(ctx, runID, domain.RunCompleted); err != nil {
				return err
			}
			completed <- struct{}{}
			return nil
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	serving := make(chan struct{})
	go func() {
		defer close(serving)
		_ = w.worker.Run(ctx)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-completed:
		case <-time.After(guardTimeout):
			t.Fatalf("timed out waiting for run %d to complete", i+1)
		}
	}
	cancel()
	select {
	case <-serving:
	case <-time.After(guardTimeout):
		t.Fatalf("Run must return after cancellation")
	}
	if active := w.exec.active.Load(); active != 0 {
		t.Fatalf("executions must be fully drained before Run returns, %d still active", active)
	}
}

// flakyRunStore makes one run unreadable, simulating a persistence outage
// behind the run store while the claim store stays healthy.
type flakyRunStore struct {
	ports.RunStore
	failFor domain.RunID
}

func (f *flakyRunStore) Get(ctx context.Context, tenantID domain.TenantID, id domain.RunID) (*domain.Run, error) {
	if id == f.failFor {
		return nil, domain.Internalf(errors.New("scripted outage"), "run_get", "run reload failed")
	}
	return f.RunStore.Get(ctx, tenantID, id)
}

func TestUnreadableRunLeavesClaimToExpireForRetry(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("unreadable")
	wrk, err := New(w.watch, &flakyRunStore{RunStore: w.repos.Runs, failFor: run.ID},
		w.exec, testLogger(), testConfig(), "second-owner", nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, perr := wrk.ProcessOnce(context.Background())
		done <- perr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round must survive the outage, got %v", err)
		}
	case <-time.After(guardTimeout):
		t.Fatalf("round did not finish within the guard window")
	}

	held := w.claim(run.ID)
	if held.Status != domain.ClaimClaimed || held.Owner != "second-owner" {
		t.Fatalf("claim must be left to expire for retry, got %+v", held)
	}
	select {
	case id := <-w.exec.started:
		t.Fatalf("no execution may start for an unreadable run, saw %s", id)
	default:
	}
}
