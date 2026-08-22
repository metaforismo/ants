package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// failingClaimStore injects claim-create failures so tests can prove the run
// and its initial claim commit or roll back together inside StartRun's unit.
type failingClaimStore struct {
	ports.RunClaimStore
	err error
}

func (f *failingClaimStore) Create(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	if f.err != nil {
		return f.err
	}
	return f.RunClaimStore.Create(ctx, tenantID, runID)
}

// failingThreadStore injects thread-transition failures after run + claim
// creation, the latest point a StartRun unit can still abort.
type failingThreadStore struct {
	ports.ThreadStore
	failUpdate bool
}

func (f *failingThreadStore) Update(ctx context.Context, thread *domain.Thread, expectedVersion int64) error {
	if f.failUpdate {
		return &domain.Error{Kind: domain.ErrKindTransient, Code: "injected", Message: "thread store unavailable"}
	}
	return f.ThreadStore.Update(ctx, thread, expectedVersion)
}

// drainClaims is the only port-level way to count claims; it acquires what it
// finds, so tests must finish their state observations before calling it.
func drainClaims(t *testing.T, h *harness) []*domain.RunClaim {
	t.Helper()
	claims, err := h.repos.RunClaims.AcquireNext(context.Background(), ports.RunClaimBatchRequest{
		Owner:    "probe-worker",
		Limit:    100,
		LeaseFor: time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire next: %v", err)
	}
	return claims
}

func TestStartRunCreatesExactlyOneRunnableClaim(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	result, err := h.engine.StartRun(ctx, startInput(thread, "claim-key"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatalf("first start must not be a replay")
	}

	claim, err := h.repos.RunClaims.Get(ctx, testTenantID, result.Run.ID)
	if err != nil {
		t.Fatalf("successful StartRun must persist its initial claim: %v", err)
	}
	if claim.Status != domain.ClaimRunnable || claim.Owner != "" || claim.Token != "" {
		t.Fatalf("initial claim must be unowned runnable with redacted token: %+v", claim)
	}
	if claim.Generation != 0 || claim.Attempts != 0 {
		t.Fatalf("initial claim has no acquisition epoch yet: generation=%d attempts=%d", claim.Generation, claim.Attempts)
	}

	reloaded, err := h.repos.Runs.Get(ctx, testTenantID, result.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadState, err := h.repos.Threads.Get(ctx, testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != domain.RunPending || threadState.Status != domain.ThreadPlanning {
		t.Fatalf("run/thread must reach pending/planning, got %s/%s", reloaded.Status, threadState.Status)
	}

	claims := drainClaims(t, h)
	if len(claims) != 1 || claims[0].RunID != result.Run.ID {
		t.Fatalf("exactly one claim must exist for the started run, got %d: %+v", len(claims), claims)
	}
}

func TestIdempotentReplayDoesNotDuplicateClaim(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	ctx := context.Background()
	in := startInput(thread, "replay-claim-key")

	first, err := h.engine.StartRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.StartRun(ctx, in)
	if err != nil {
		t.Fatalf("duplicate start must replay: %v", err)
	}
	if !second.Replayed || second.Run.ID != first.Run.ID {
		t.Fatalf("expected replay of run %s, got %+v", first.Run.ID, second)
	}

	claim, err := h.repos.RunClaims.Get(ctx, testTenantID, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status != domain.ClaimRunnable || claim.Generation != 0 || claim.Attempts != 0 {
		t.Fatalf("replay must leave the original claim untouched: %+v", claim)
	}
	claims := drainClaims(t, h)
	if len(claims) != 1 || claims[0].RunID != first.Run.ID {
		t.Fatalf("replay must not create a duplicate claim, got %d: %+v", len(claims), claims)
	}
}

// TestStartRunRollsBackRunAndClaimAtomically drives failure injection at the
// real composition boundary: every store the StartRun unit touches can fail,
// and each variant must leave neither run, nor claim, nor idempotency mapping,
// nor thread transition, nor event/outbox rows behind.
func TestStartRunRollsBackRunAndClaimAtomically(t *testing.T) {
	t.Run("ClaimCreateFails", func(t *testing.T) {
		h := newHarness(t)
		_, thread := h.seedWorld(t)
		h.engine.deps.RunClaims = &failingClaimStore{
			RunClaimStore: h.repos.RunClaims,
			err:           &domain.Error{Kind: domain.ErrKindTransient, Code: "injected", Message: "claim store unavailable"},
		}
		assertStartRunRollback(t, h, thread)
	})

	t.Run("ThreadTransitionFailsAfterClaimCreate", func(t *testing.T) {
		h := newHarness(t)
		_, thread := h.seedWorld(t)
		threads := &failingThreadStore{ThreadStore: h.repos.Threads, failUpdate: true}
		h.engine.deps.Threads = threads
		assertStartRunRollback(t, h, thread)
	})

	t.Run("EventAppendFailsAfterClaimCreate", func(t *testing.T) {
		h := newHarness(t)
		_, thread := h.seedWorld(t)
		h.engine.deps.Events = &flakyEventLog{EventLog: h.repos.Events, failAfter: 0}
		assertStartRunRollback(t, h, thread)
	})
}

func assertStartRunRollback(t *testing.T, h *harness, thread *domain.Thread) {
	t.Helper()
	before, err := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, startErr := h.engine.StartRun(context.Background(), startInput(thread, "rollback-key"))
	if startErr == nil {
		t.Fatalf("injected failure must surface from StartRun")
	}
	if domain.ErrKindOf(startErr) == domain.ErrKindConflict {
		t.Fatalf("injected transient failure must not be masked as idempotency conflict: %v", startErr)
	}

	if _, getErr := h.repos.Runs.GetByIdempotencyKey(context.Background(), testTenantID, thread.ID, "rollback-key"); domain.ErrKindOf(getErr) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back unit must leave no run or idempotency mapping, got %v", getErr)
	}
	claims := drainClaims(t, h)
	if len(claims) != 0 {
		t.Fatalf("rolled-back unit must leave no claim behind, got %d: %+v", len(claims), claims)
	}
	after, err := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Version != before.Version {
		t.Fatalf("failed start must not mutate the thread: before=%s@%d after=%s@%d",
			before.Status, before.Version, after.Status, after.Version)
	}
	events, err := h.repos.Events.ListByTenant(context.Background(), testTenantID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("rolled-back unit must emit no events, got %d", len(events))
	}
	stats, err := h.repos.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 0 {
		t.Fatalf("rolled-back unit must enqueue no outbox messages: %+v", stats)
	}
}
