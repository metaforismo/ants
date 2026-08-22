package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// outboxTotals snapshots the outbox so tests can pin exactly how many
// messages a convergence enqueues.
func outboxTotals(t *testing.T, h *harness) ports.OutboxStats {
	t.Helper()
	stats, err := h.repos.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// advanceRunMidFlight moves a run past pending the way an executing worker
// epoch would: legal transitions persisted directly, no engine round.
func advanceRunMidFlight(t *testing.T, h *harness, runID domain.RunID) *domain.Run {
	t.Helper()
	ctx := context.Background()
	for _, next := range []domain.RunStatus{domain.RunPlanning, domain.RunExecuting} {
		run, err := h.repos.Runs.Get(ctx, testTenantID, runID)
		if err != nil {
			t.Fatal(err)
		}
		expected := run.Version
		if err := run.TransitionTo(next); err != nil {
			t.Fatal(err)
		}
		if err := h.repos.Runs.Update(ctx, run, expected); err != nil {
			t.Fatal(err)
		}
	}
	run, err := h.repos.Runs.Get(ctx, testTenantID, runID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func seedInterruptedRun(t *testing.T, h *harness) (*domain.Thread, *domain.Run) {
	t.Helper()
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	result, err := h.engine.StartRun(context.Background(), startInput(thread, "interrupted-key"))
	if err != nil {
		t.Fatal(err)
	}
	return thread, advanceRunMidFlight(t, h, result.Run.ID)
}

func TestRecoverInterruptedConvergesRunWithEventAndOutbox(t *testing.T) {
	h := newHarness(t)
	thread, run := seedInterruptedRun(t, h)
	before := outboxTotals(t, h)

	cause := errors.New("claim lease lapsed while run was executing")
	if err := h.engine.RecoverInterrupted(context.Background(), testTenantID, run.ID, cause); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	finished, err := h.repos.Runs.Get(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.RunFailed || finished.Failure == nil {
		t.Fatalf("run must converge to classified failure, got %+v", finished)
	}
	if finished.Failure.Code != InterruptedFailureCode {
		t.Fatalf("failure code %s, want %s", finished.Failure.Code, InterruptedFailureCode)
	}
	if finished.Failure.Message != cause.Error() {
		t.Fatalf("failure must carry the caller's cause verbatim, got %q", finished.Failure.Message)
	}

	events, err := h.repos.Events.ListByRun(context.Background(), testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events {
		if evt.Type == domain.EventRunStatusChanged && evt.Data["code"] == InterruptedFailureCode && evt.Data["to"] == string(domain.RunFailed) {
			found = true
		}
	}
	if !found {
		t.Fatalf("convergence must emit a classified status event, got %+v", events)
	}

	// Both events the convergence commits — the run's classified status and
	// the routed thread status — enqueue their own outbox message (ADR-0011).
	after := outboxTotals(t, h)
	before.Pending += 2
	if after != before {
		t.Fatalf("convergence must enqueue exactly its two status messages: before=%+v after=%+v", before, after)
	}

	threadState, err := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if threadState.Status != domain.ThreadFailed {
		t.Fatalf("thread in %s must route to failed, got %s", domain.ThreadPlanning, threadState.Status)
	}
}

func TestRecoverInterruptedUnitRollsBackAtomically(t *testing.T) {
	h := newHarness(t)
	thread, run := seedInterruptedRun(t, h)

	// The recovery unit appends its event after the run update; injecting an
	// append failure there must roll the whole unit back — no half-converged
	// state, no orphan outbox message from the same commit.
	h.engine.deps.Events = &flakyEventLog{EventLog: h.repos.Events, failAfter: 0}
	before := outboxTotals(t, h)

	err := h.engine.RecoverInterrupted(context.Background(), testTenantID, run.ID,
		errors.New("claim lease lapsed while run was executing"))
	if err == nil {
		t.Fatal("injected failure must surface from recovery")
	}

	reloaded, getErr := h.repos.Runs.Get(context.Background(), testTenantID, run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if reloaded.Status != domain.RunExecuting || reloaded.Version != run.Version || reloaded.Failure != nil {
		t.Fatalf("rolled-back unit must not mutate the run: before=%+v after=%+v", run, reloaded)
	}
	after := outboxTotals(t, h)
	if after != before {
		t.Fatalf("rolled-back unit must enqueue nothing: before=%+v after=%+v", before, after)
	}
	threadState, err := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if threadState.Status != domain.ThreadPlanning {
		t.Fatalf("thread must stay untouched by a rolled-back unit, got %s", threadState.Status)
	}
}

func TestRecoverInterruptedConvergesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	_, run := seedInterruptedRun(t, h)
	ctx := context.Background()

	first := h.engine.RecoverInterrupted(ctx, testTenantID, run.ID, errors.New("lease lost"))
	if first != nil {
		t.Fatalf("first recovery: %v", first)
	}
	finished, err := h.repos.Runs.Get(ctx, testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}

	eventsBefore, err := h.repos.Events.ListByRun(ctx, testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	outboxBefore := outboxTotals(t, h)

	second := h.engine.RecoverInterrupted(ctx, testTenantID, run.ID, errors.New("second epoch"))
	if second != nil {
		t.Fatalf("recovery of an already-terminal run must succeed idempotently, got %v", second)
	}

	eventsAfter, err := h.repos.Events.ListByRun(ctx, testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("repeat recovery must emit nothing: %d -> %d events", len(eventsBefore), len(eventsAfter))
	}
	if after := outboxTotals(t, h); after != outboxBefore {
		t.Fatalf("repeat recovery must enqueue nothing: %+v -> %+v", outboxBefore, after)
	}
	unchanged, err := h.repos.Runs.Get(ctx, testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != finished.Version || unchanged.Failure.Message == "" {
		t.Fatalf("repeat recovery must leave the converged run untouched: %+v vs %+v", unchanged, finished)
	}
}

func TestRecoverInterruptedRefusesPendingRuns(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	result, err := h.engine.StartRun(context.Background(), startInput(thread, "pending-key"))
	if err != nil {
		t.Fatal(err)
	}
	before := outboxTotals(t, h)

	err = h.engine.RecoverInterrupted(context.Background(), testTenantID, result.Run.ID,
		errors.New("lease lost"))
	if domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("pending run must be refused as conflict, got %v", err)
	}

	reloaded, getErr := h.repos.Runs.Get(context.Background(), testTenantID, result.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if reloaded.Status != domain.RunPending || reloaded.Failure != nil {
		t.Fatalf("refused recovery must not touch a pending run, got %+v", reloaded)
	}
	if after := outboxTotals(t, h); after != before {
		t.Fatalf("refused recovery must enqueue nothing: %+v -> %+v", before, after)
	}
}

func TestRecoverInterruptedNeverTouchesTheClaim(t *testing.T) {
	h := newHarness(t)
	_, run := seedInterruptedRun(t, h)

	// A crashed worker still holds the claim row it died with. Fencing is the
	// claim holder's discipline (ADR-0012): recovery converges the run and
	// must leave the claim — including its fencing credentials — for guarded,
	// credential-checked cleanup instead of deleting another epoch's lease.
	acquired, err := h.repos.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: testTenantID, RunID: run.ID, Owner: "dead-worker", LeaseFor: 30 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.engine.RecoverInterrupted(context.Background(), testTenantID, run.ID,
		errors.New("claim lease lapsed while run was executing")); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	claim, err := h.repos.RunClaims.Get(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatalf("recovery must not delete the claim it does not hold: %v", err)
	}
	if claim.Status != domain.ClaimClaimed || claim.Owner != acquired.Owner ||
		claim.Generation != acquired.Generation || claim.Attempts != acquired.Attempts {
		t.Fatalf("recovery must leave the crashed epoch's credentials untouched: before=%+v after=%+v",
			acquired, claim)
	}
}
