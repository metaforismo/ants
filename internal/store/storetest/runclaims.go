package storetest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

const (
	testOwner1 = "worker-one"
	testOwner2 = "worker-two"
	testLease  = time.Minute
)

// RunRunClaims executes the run-claim behavioral contract (ADR-0012) against
// a fresh store per subtest. Memory and PostgreSQL adapters must satisfy the
// identical assertions; all time-based behavior is driven through the
// world's Advance hook, so expiry and heartbeat windows are deterministic
// without sleeping.
func RunRunClaims(t *testing.T, f Factory) {
	t.Run("ClaimCreationIsAtomicWithRunInUnitOfWork", func(t *testing.T) {
		repos, tx, _ := world(f)
		testClaimCreationAtomic(t, repos, tx)
	})
	t.Run("CreateRejectsUnknownRunAndDuplicates", func(t *testing.T) {
		repos, _, _ := world(f)
		testClaimCreateGuards(t, repos)
	})
	t.Run("AcquireClaimsOnceThenHoldsAndRedactsToken", func(t *testing.T) {
		repos, _, _ := world(f)
		testAcquireHolds(t, repos)
	})
	t.Run("ConcurrentAcquireNextNeverOverlaps", func(t *testing.T) {
		repos, _, _ := world(f)
		testConcurrentAcquireNext(t, repos)
	})
	t.Run("ExpiredLeaseReclaimsBumpGenerationAndAttempts", func(t *testing.T) {
		repos, _, advance := world(f)
		testExpiryReclaim(t, repos, advance)
	})
	t.Run("StaleAndForeignCredentialsAreRejected", func(t *testing.T) {
		repos, _, advance := world(f)
		testStaleCredentials(t, repos, advance)
	})
	t.Run("HeartbeatExtendsLiveLeaseOnly", func(t *testing.T) {
		repos, _, advance := world(f)
		testHeartbeatExtension(t, repos, advance)
	})
	t.Run("ReleaseReturnsToRunnableKeepingHistory", func(t *testing.T) {
		repos, _, _ := world(f)
		testRelease(t, repos)
	})
	t.Run("CompleteIsFencedAndRemovesTheClaim", func(t *testing.T) {
		repos, _, _ := world(f)
		testComplete(t, repos)
	})
	t.Run("TerminalCleanupIsGuardedAndIdempotent", func(t *testing.T) {
		repos, _, _ := world(f)
		testTerminalCleanup(t, repos)
	})
	t.Run("RolledBackUnitLeavesNoClaimEventOrOutboxRow", func(t *testing.T) {
		repos, tx, _ := world(f)
		testClaimRollbackDualWrite(t, repos, tx)
	})
	t.Run("NestedUnitsJoinOuterForClaims", func(t *testing.T) {
		repos, tx, _ := world(f)
		testClaimNestedUnits(t, repos, tx)
	})
	t.Run("AcquireNextHonorsLimitAndCreationOrder", func(t *testing.T) {
		repos, _, advance := world(f)
		testAcquireNextOrdering(t, repos, advance)
	})
	t.Run("MalformedRequestsAreTypedInvalid", func(t *testing.T) {
		repos, _, _ := world(f)
		testClaimValidation(t, repos)
	})
}

// claimWorld seeds tenant/project/thread and returns the thread runs attach to.
func claimWorld(ctx context.Context, t *testing.T, repos ports.Repositories) *domain.Thread {
	t.Helper()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	return seedThread(ctx, t, repos, tenantID, project.ID)
}

func seedRunForClaims(t *testing.T, repos ports.Repositories, thread *domain.Thread, key string) *domain.Run {
	t.Helper()
	ctx := context.Background()
	idStr, err := domain.NewID(domain.PrefixRun)
	if err != nil {
		t.Fatalf("generate run id: %v", err)
	}
	run, err := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID, tid("key", key), fixedTime(30))
	if err != nil {
		t.Fatalf("construct run: %v", err)
	}
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := repos.RunClaims.Create(ctx, tenantID, run.ID); err != nil {
		t.Fatalf("create claim: %v", err)
	}
	return run
}

func refOf(c *domain.RunClaim) ports.RunClaimRef {
	return ports.RunClaimRef{
		TenantID:   c.TenantID,
		RunID:      c.RunID,
		Owner:      c.Owner,
		Token:      c.Token,
		Generation: c.Generation,
	}
}

func foreignToken() string { return strings.Repeat("Z", domain.ClaimTokenLength) }

func acquireCtx(t *testing.T, repos ports.Repositories, req ports.RunClaimLeaseRequest) *domain.RunClaim {
	t.Helper()
	c, err := repos.RunClaims.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("acquire %s: %v", req.RunID, err)
	}
	return c
}

// requireErrKind fails unless err carries exactly the expected taxonomy kind.
func requireErrKind(t *testing.T, err error, want domain.ErrorKind) {
	t.Helper()
	if ErrKind(err) != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}

func testClaimCreationAtomic(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)

	var committed *domain.Run
	err := tx.Do(ctx, func(ctx context.Context) error {
		idStr, idErr := domain.NewID(domain.PrefixRun)
		if idErr != nil {
			return idErr
		}
		run, rErr := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID, "atomic-commit", fixedTime(31))
		if rErr != nil {
			return rErr
		}
		if cErr := repos.Runs.Create(ctx, run); cErr != nil {
			return cErr
		}
		committed = run
		return repos.RunClaims.Create(ctx, tenantID, run.ID)
	})
	if err != nil {
		t.Fatalf("unit with run+claim must commit: %v", err)
	}
	got, err := repos.RunClaims.Get(ctx, tenantID, committed.ID)
	if err != nil {
		t.Fatalf("claim must exist after commit: %v", err)
	}
	if got.Status != domain.ClaimRunnable || got.Generation != 0 || got.Attempts != 0 ||
		got.Owner != "" || got.ExpiresAt != nil {
		t.Fatalf("fresh claim shape wrong: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("store clock must stamp creation: %+v", got)
	}

	boom := errors.New("rollback run+claim unit")
	var doomed *domain.Run
	err = tx.Do(ctx, func(ctx context.Context) error {
		idStr, idErr := domain.NewID(domain.PrefixRun)
		if idErr != nil {
			return idErr
		}
		run, rErr := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID, "atomic-rollback", fixedTime(32))
		if rErr != nil {
			return rErr
		}
		doomed = run
		if cErr := repos.Runs.Create(ctx, run); cErr != nil {
			return cErr
		}
		if cErr := repos.RunClaims.Create(ctx, tenantID, run.ID); cErr != nil {
			return cErr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unit must fail as scripted, got %v", err)
	}
	if _, err := repos.Runs.Get(ctx, tenantID, doomed.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back run must not exist, got %v", err)
	}
	if _, err := repos.RunClaims.Get(ctx, tenantID, doomed.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back claim must not exist, got %v", err)
	}
}

func testClaimCreateGuards(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)

	fakeID := domain.RunID(tid("run", "ghost"))
	requireErrKind(t, repos.RunClaims.Create(ctx, tenantID, fakeID), domain.ErrKindInvalid)

	run := seedRunForClaims(t, repos, thread, "guards")
	requireErrKind(t, repos.RunClaims.Create(ctx, tenantID, run.ID), domain.ErrKindConflict)
	requireErrKind(t, repos.RunClaims.Create(ctx, otherTenID, run.ID), domain.ErrKindInvalid)
}

func testAcquireHolds(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "acquire")

	c := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: testLease})
	if c.Status != domain.ClaimClaimed || c.Owner != testOwner1 {
		t.Fatalf("acquired claim state wrong: %+v", c)
	}
	if len(c.Token) != domain.ClaimTokenLength {
		t.Fatalf("token must be an opaque credential of length %d, got %d", domain.ClaimTokenLength, len(c.Token))
	}
	if c.Generation != 1 || c.Attempts != 1 {
		t.Fatalf("first acquisition must set generation=attempts=1, got %d/%d", c.Generation, c.Attempts)
	}
	if c.AcquiredAt == nil || c.HeartbeatAt == nil || c.ExpiresAt == nil {
		t.Fatalf("store clock must stamp the acquisition: %+v", c)
	}

	got, err := repos.RunClaims.Get(ctx, tenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ClaimClaimed || got.Owner != testOwner1 {
		t.Fatalf("get after acquire wrong: %+v", got)
	}
	if got.Token != "" {
		t.Fatalf("read paths must redact the bearer token")
	}

	for _, owner := range []string{testOwner2, testOwner1} {
		_, err := repos.RunClaims.Acquire(ctx, ports.RunClaimLeaseRequest{
			TenantID: tenantID, RunID: run.ID, Owner: owner, LeaseFor: testLease,
		})
		requireErrKind(t, err, domain.ErrKindConflict)
	}
}

func testConcurrentAcquireNext(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)

	const total = 16
	runIDs := make([]domain.RunID, 0, total)
	for i := range total {
		run := seedRunForClaims(t, repos, thread, "conc-"+string(rune('a'+i)))
		runIDs = append(runIDs, run.ID)
	}

	const workers = 8
	var mu sync.Mutex
	claimsBy := map[domain.RunID]string{} // run -> claiming worker
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			owner := "worker-" + string(rune('A'+n))
			for round := 0; round < total; round++ {
				batch, err := repos.RunClaims.AcquireNext(ctx, ports.RunClaimBatchRequest{
					Owner: owner, Limit: 2, LeaseFor: time.Hour,
				})
				if err != nil {
					t.Errorf("worker %s batch: %v", owner, err)
					return
				}
				mu.Lock()
				for _, c := range batch {
					if prev, dup := claimsBy[c.RunID]; dup {
						t.Errorf("run %s claimed by %s and %s", c.RunID, prev, owner)
					} else {
						claimsBy[c.RunID] = owner
					}
				}
				mu.Unlock()
				if len(batch) == 0 {
					break
				}
			}
		}(w)
	}
	wg.Wait()

	if len(claimsBy) != total {
		t.Fatalf("every run must be claimed exactly once: %d/%d", len(claimsBy), total)
	}
	for _, id := range runIDs {
		c, err := repos.RunClaims.Get(ctx, tenantID, id)
		if err != nil {
			t.Fatalf("claimed run %s readable: %v", id, err)
		}
		if c.Attempts != 1 || c.Generation != 1 {
			t.Fatalf("run %s must record exactly one acquisition epoch: %+v", id, c)
		}
	}
}

func testExpiryReclaim(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "expiry")

	first := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: testLease})

	advance(2 * testLease)

	reclaimed, err := repos.RunClaims.AcquireNext(ctx, ports.RunClaimBatchRequest{Owner: testOwner2, Limit: 5, LeaseFor: testLease})
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("expired lease must be reclaimable exactly once: %d %v", len(reclaimed), err)
	}
	got := reclaimed[0]
	if got.RunID != run.ID || got.Owner != testOwner2 {
		t.Fatalf("wrong reclaim result: %+v", got)
	}
	if got.Generation != first.Generation+1 || got.Attempts != first.Attempts+1 {
		t.Fatalf("reclaim must bump generation and attempts: %+v", got)
	}
	if got.Token == first.Token {
		t.Fatalf("reclaim must mint a fresh bearer token")
	}

	// The superseded holder is fenced out of every mutating operation.
	oldRef := refOf(first)
	_, err = repos.RunClaims.Heartbeat(ctx, oldRef, testLease)
	requireErrKind(t, err, domain.ErrKindConflict)
	_, err = repos.RunClaims.Release(ctx, oldRef)
	requireErrKind(t, err, domain.ErrKindConflict)
	requireErrKind(t, repos.RunClaims.Complete(ctx, oldRef), domain.ErrKindConflict)
}

func testStaleCredentials(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "stale")

	current := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: testLease})

	staleVariants := map[string]ports.RunClaimRef{
		"wrong-token":      {TenantID: tenantID, RunID: run.ID, Owner: current.Owner, Token: foreignToken(), Generation: current.Generation},
		"wrong-generation": {TenantID: tenantID, RunID: run.ID, Owner: current.Owner, Token: current.Token, Generation: current.Generation + 7},
	}
	for name, ref := range staleVariants {
		_, err := repos.RunClaims.Heartbeat(ctx, ref, testLease)
		if ErrKind(err) != domain.ErrKindConflict {
			t.Errorf("%s heartbeat must conflict, got %v", name, err)
		}
		if ErrKind(repos.RunClaims.Complete(ctx, ref)) != domain.ErrKindConflict {
			t.Errorf("%s complete must not succeed silently", name)
		}
	}

	unknown := ports.RunClaimRef{TenantID: tenantID, RunID: domain.RunID(tid("run", "unknown")), Owner: testOwner1, Token: foreignToken(), Generation: 1}
	for name, op := range map[string]func() error{
		"heartbeat": func() error { _, e := repos.RunClaims.Heartbeat(ctx, unknown, testLease); return e },
		"release":   func() error { _, e := repos.RunClaims.Release(ctx, unknown); return e },
		"complete":  func() error { return repos.RunClaims.Complete(ctx, unknown) },
		"cleanup":   func() error { return repos.RunClaims.CleanupTerminal(ctx, tenantID, unknown.RunID) },
		"foreign-heartbeat": func() error {
			f := unknown
			f.TenantID = otherTenID
			_, e := repos.RunClaims.Heartbeat(ctx, f, testLease)
			return e
		},
		"foreign-complete": func() error { f := unknown; f.TenantID = otherTenID; return repos.RunClaims.Complete(ctx, f) },
	} {
		if ErrKind(op()) != domain.ErrKindNotFound {
			t.Errorf("%s on unknown/foreign claim must be uniform not-found", name)
		}
	}

	// A live lease held by someone else conflicts for a second acquirer even
	// when the run exists and credentials are well-formed.
	if _, err := repos.RunClaims.Acquire(ctx, ports.RunClaimLeaseRequest{
		TenantID: tenantID, RunID: run.ID, Owner: testOwner2, LeaseFor: testLease,
	}); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("live lease must conflict for second acquirer, got %v", err)
	}

	// Let the lease lapse without reclaim; the expired holder's heartbeat is
	// rejected as expired rather than revived.
	advance(2 * testLease)
	if _, err := repos.RunClaims.Heartbeat(ctx, refOf(current), testLease); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("expired heartbeat must conflict, got %v", err)
	}
}

func testHeartbeatExtension(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "heartbeat")

	const firstLease = 60 * time.Second
	c := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: firstLease})
	ref := refOf(c)

	advance(40 * time.Second)
	beaten, err := repos.RunClaims.Heartbeat(ctx, ref, firstLease)
	if err != nil {
		t.Fatalf("live heartbeat: %v", err)
	}
	if !beaten.ExpiresAt.After(*c.ExpiresAt) {
		t.Fatalf("heartbeat must extend the deadline: %v <= %v", beaten.ExpiresAt, c.ExpiresAt)
	}
	if !beaten.HeartbeatAt.After(*c.HeartbeatAt) {
		t.Fatalf("heartbeat must refresh the heartbeat instant")
	}

	// Still inside the extended window: nobody else can take it.
	batch, err := repos.RunClaims.AcquireNext(ctx, ports.RunClaimBatchRequest{Owner: testOwner2, Limit: 5, LeaseFor: testLease})
	if err != nil || len(batch) != 0 {
		t.Fatalf("extended lease must keep holding: %d %v", len(batch), err)
	}

	// Near the end of the window the holder can still renew...
	advance(50 * time.Second)
	if _, err := repos.RunClaims.Heartbeat(ctx, ref, firstLease); err != nil {
		t.Fatalf("renewal before expiry: %v", err)
	}
	// ...but once past it, the epoch is forfeited.
	advance(firstLease + time.Second)
	if _, err := repos.RunClaims.Heartbeat(ctx, ref, firstLease); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("expired heartbeat must conflict, got %v", err)
	}
	reclaimed, err := repos.RunClaims.Acquire(ctx, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner2, LeaseFor: testLease})
	if err != nil || reclaimed.Generation != 2 || reclaimed.Attempts != 2 {
		t.Fatalf("expired lease must reclaim with bumped counters: %+v %v", reclaimed, err)
	}
}

func testRelease(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "release")

	c := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: testLease})

	released, err := repos.RunClaims.Release(ctx, refOf(c))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != domain.ClaimRunnable || released.Owner != "" || released.Token != "" || released.ExpiresAt != nil {
		t.Fatalf("released claim must be runnable and unowned: %+v", released)
	}
	if released.Generation != c.Generation || released.Attempts != c.Attempts {
		t.Fatalf("release preserves history: %+v", released)
	}

	next := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner2, LeaseFor: testLease})
	if next.Generation != c.Generation+1 || next.Attempts != c.Attempts+1 {
		t.Fatalf("post-release acquisition bumps counters: %+v", next)
	}
	// The previous holder cannot act on the new epoch.
	if _, err := repos.RunClaims.Release(ctx, refOf(c)); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("stale release must conflict, got %v", err)
	}
}

func testComplete(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "complete")

	c := acquireCtx(t, repos, ports.RunClaimLeaseRequest{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: testLease})
	wrongToken := refOf(c)
	wrongToken.Token = foreignToken()
	requireErrKind(t, repos.RunClaims.Complete(ctx, wrongToken), domain.ErrKindConflict)

	if err := repos.RunClaims.Complete(ctx, refOf(c)); err != nil {
		t.Fatalf("fenced complete: %v", err)
	}
	if _, err := repos.RunClaims.Get(ctx, tenantID, run.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("completed claim must be gone, got %v", err)
	}
	// Repeating a completion finds nothing to act on: typed not-found, never
	// a silent success.
	requireErrKind(t, repos.RunClaims.Complete(ctx, refOf(c)), domain.ErrKindNotFound)
}

func testTerminalCleanup(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "cleanup")

	// A non-terminal run refuses cleanup.
	requireErrKind(t, repos.RunClaims.CleanupTerminal(ctx, tenantID, run.ID), domain.ErrKindInvalid)

	expectedVersion := int64(1)
	if err := run.TransitionTo(domain.RunCancelled); err != nil {
		t.Fatal(err)
	}
	if err := repos.Runs.Update(ctx, run, expectedVersion); err != nil {
		t.Fatalf("cancel run: %v", err)
	}

	if err := repos.RunClaims.CleanupTerminal(ctx, tenantID, run.ID); err != nil {
		t.Fatalf("terminal cleanup: %v", err)
	}
	if _, err := repos.RunClaims.Get(ctx, tenantID, run.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("cleaned claim must be gone, got %v", err)
	}
	// Idempotent: repeating terminal cleanup succeeds.
	if err := repos.RunClaims.CleanupTerminal(ctx, tenantID, run.ID); err != nil {
		t.Fatalf("repeat cleanup must stay a no-op: %v", err)
	}

	requireErrKind(t, repos.RunClaims.CleanupTerminal(ctx, tenantID, domain.RunID(tid("run", "missing"))), domain.ErrKindNotFound)
	seedTenant(ctx, t, repos, otherTenID, "other")
	requireErrKind(t, repos.RunClaims.CleanupTerminal(ctx, otherTenID, run.ID), domain.ErrKindNotFound)
}

func testClaimRollbackDualWrite(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)

	evtID, _ := domain.NewID(domain.PrefixEvent)
	boom := errors.New("rollback claim+event unit")
	var doomed *domain.Run
	err := tx.Do(ctx, func(ctx context.Context) error {
		idStr, idErr := domain.NewID(domain.PrefixRun)
		if idErr != nil {
			return idErr
		}
		run, rErr := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID, "rollback-unit", fixedTime(34))
		if rErr != nil {
			return rErr
		}
		doomed = run
		if cErr := repos.Runs.Create(ctx, run); cErr != nil {
			return cErr
		}
		if cErr := repos.RunClaims.Create(ctx, tenantID, run.ID); cErr != nil {
			return cErr
		}
		if aErr := repos.Events.Append(ctx, &domain.Event{
			ID:            domain.EventID(evtID),
			Type:          domain.EventRunStarted,
			TenantID:      tenantID,
			AggregateType: "run",
			AggregateID:   string(run.ID),
			RunID:         run.ID,
			Data:          map[string]any{},
		}); aErr != nil {
			return aErr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unit must fail as scripted, got %v", err)
	}
	if _, err := repos.Runs.Get(ctx, tenantID, doomed.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back run must not exist, got %v", err)
	}
	if _, err := repos.RunClaims.Get(ctx, tenantID, doomed.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back claim must not exist, got %v", err)
	}
	events, _ := repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if len(events) != 0 {
		t.Fatalf("rolled-back unit must leave no event row: %d", len(events))
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 0 {
		t.Fatalf("rolled-back unit must leave no outbox row: %+v", stats)
	}
}

func testClaimNestedUnits(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	seedRunForClaims(t, repos, thread, "nested")

	boom := errors.New("outer failure")
	var innerRun *domain.Run
	err := tx.Do(ctx, func(ctx context.Context) error {
		if innerErr := tx.Do(ctx, func(ctx context.Context) error {
			idStr, idErr := domain.NewID(domain.PrefixRun)
			if idErr != nil {
				return idErr
			}
			run, rErr := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID, "inner-run", fixedTime(33))
			if rErr != nil {
				return rErr
			}
			innerRun = run
			if cErr := repos.Runs.Create(ctx, run); cErr != nil {
				return cErr
			}
			return repos.RunClaims.Create(ctx, tenantID, run.ID)
		}); innerErr != nil {
			return innerErr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("outer unit must fail as scripted, got %v", err)
	}
	// The nested unit joined the outer one: its committed-looking run and
	// claim roll back together with the outer failure.
	if _, err := repos.Runs.Get(ctx, tenantID, innerRun.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back nested run must not exist, got %v", err)
	}
	if _, err := repos.RunClaims.Get(ctx, tenantID, innerRun.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back nested claim must not exist, got %v", err)
	}
}

func testAcquireNextOrdering(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)

	var ordered []domain.RunID
	for i := range 4 {
		run := seedRunForClaims(t, repos, thread, "order-"+string(rune('a'+i)))
		ordered = append(ordered, run.ID)
		advance(time.Second) // strictly increasing claim creation instants
	}

	all, err := repos.RunClaims.AcquireNext(ctx, ports.RunClaimBatchRequest{Owner: testOwner1, Limit: 6, LeaseFor: testLease})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("limit above pool size takes everything: %d", len(all))
	}
	for i, c := range all {
		if c.RunID != ordered[i] {
			t.Fatalf("claims must follow deterministic creation order at %d", i)
		}
	}

	// Held leases are invisible until they expire.
	none, err := repos.RunClaims.AcquireNext(ctx, ports.RunClaimBatchRequest{Owner: testOwner2, Limit: 6, LeaseFor: testLease})
	if err != nil || len(none) != 0 {
		t.Fatalf("held claims must not reappear: %d %v", len(none), err)
	}
}

func testClaimValidation(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	thread := claimWorld(ctx, t, repos)
	run := seedRunForClaims(t, repos, thread, "validation")

	invalidAcquires := []ports.RunClaimLeaseRequest{
		{TenantID: tenantID, RunID: run.ID, Owner: "", LeaseFor: testLease},
		{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: 0},
		{TenantID: tenantID, RunID: run.ID, Owner: testOwner1, LeaseFor: -time.Minute},
	}
	for _, req := range invalidAcquires {
		requireErrKind(t, func() error { _, e := repos.RunClaims.Acquire(ctx, req); return e }(), domain.ErrKindInvalid)
	}
	invalidBatches := []ports.RunClaimBatchRequest{
		{Owner: "", Limit: 1, LeaseFor: testLease},
		{Owner: testOwner1, Limit: 0, LeaseFor: testLease},
		{Owner: testOwner1, Limit: 1, LeaseFor: 0},
	}
	for _, req := range invalidBatches {
		requireErrKind(t, func() error { _, e := repos.RunClaims.AcquireNext(ctx, req); return e }(), domain.ErrKindInvalid)
	}
	requireErrKind(t, repos.RunClaims.CleanupTerminal(ctx, tenantID, run.ID), domain.ErrKindInvalid)
}
