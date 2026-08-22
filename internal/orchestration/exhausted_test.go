package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// seedPendingRun starts a run and leaves it exactly as StartRun commits it:
// pending with one runnable claim, nothing executing.
func seedPendingRun(t *testing.T, h *harness) (*domain.Thread, *domain.Run) {
	t.Helper()
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	result, err := h.engine.StartRun(context.Background(), startInput(thread, "exhausted-pending-key"))
	if err != nil {
		t.Fatal(err)
	}
	return thread, result.Run
}

func TestConvergeExhaustedMidFlightFailsRunWithClassifiedEvent(t *testing.T) {
	h := newHarness(t)
	thread, run := seedInterruptedRun(t, h)
	before := outboxTotals(t, h)

	err := h.engine.ConvergeExhausted(context.Background(), testTenantID, run.ID, 4,
		errors.New("claim acquired 4 times without reaching a terminal state"))
	if err != nil {
		t.Fatalf("convergence: %v", err)
	}

	finished, err := h.repos.Runs.Get(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.RunFailed || finished.Failure == nil {
		t.Fatalf("abandoned mid-flight run must converge to classified failure, got %+v", finished)
	}
	if finished.Failure.Code != ExhaustedFailureCode {
		t.Fatalf("failure code %s, want %s", finished.Failure.Code, ExhaustedFailureCode)
	}
	if finished.Failure.Transient {
		t.Fatal("an exhausted budget is terminal, not transient")
	}

	events, err := h.repos.Events.ListByRun(context.Background(), testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events {
		if evt.Type == domain.EventRunStatusChanged &&
			evt.Data["code"] == ExhaustedFailureCode && evt.Data["attempts"] == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("convergence must record code and attempt count on the status event, got %+v", events)
	}

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
		t.Fatalf("thread must route to failed, got %s", threadState.Status)
	}
}

func TestConvergeExhaustedPendingRunCancelsAndHandsThreadBack(t *testing.T) {
	h := newHarness(t)
	thread, run := seedPendingRun(t, h)
	before := outboxTotals(t, h)

	// A pending run never started: the closed state machine has no
	// pending->failed edge, so its budget exhaustion converges to cancelled,
	// the same disposition an interrupted-before-start run earns.
	if err := h.engine.ConvergeExhausted(context.Background(), testTenantID, run.ID, 3,
		errors.New("dispatch budget exhausted")); err != nil {
		t.Fatalf("convergence: %v", err)
	}

	finished, err := h.repos.Runs.Get(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.RunCancelled || finished.Failure != nil {
		t.Fatalf("never-started over-budget run must cancel without failure info, got %+v", finished)
	}

	events, err := h.repos.Events.ListByRun(context.Background(), testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events {
		if evt.Type == domain.EventRunStatusChanged &&
			evt.Data["to"] == string(domain.RunCancelled) && evt.Data["code"] == ExhaustedFailureCode {
			found = true
		}
	}
	if !found {
		t.Fatalf("cancellation must carry the exhausted classification, got %+v", events)
	}

	after := outboxTotals(t, h)
	before.Pending += 2
	if after != before {
		t.Fatalf("convergence must enqueue exactly its two status messages: before=%+v after=%+v", before, after)
	}

	threadState, err := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The thread sat in planning behind the never-started run; like a
	// cancellation mid-planning it returns to the human.
	if threadState.Status != domain.ThreadAwaitingInput {
		t.Fatalf("planning thread must return to awaiting_input, got %s", threadState.Status)
	}
}

func TestConvergeExhaustedIsIdempotent(t *testing.T) {
	h := newHarness(t)
	_, run := seedPendingRun(t, h)
	ctx := context.Background()

	if err := h.engine.ConvergeExhausted(ctx, testTenantID, run.ID, 3, errors.New("budget")); err != nil {
		t.Fatalf("first convergence: %v", err)
	}
	eventsBefore, err := h.repos.Events.ListByRun(ctx, testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	outboxBefore := outboxTotals(t, h)

	if err := h.engine.ConvergeExhausted(ctx, testTenantID, run.ID, 4, errors.New("again")); err != nil {
		t.Fatalf("repeat convergence of a terminal run must succeed idempotently, got %v", err)
	}
	eventsAfter, err := h.repos.Events.ListByRun(ctx, testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("repeat convergence must emit nothing: %d -> %d events", len(eventsBefore), len(eventsAfter))
	}
	if after := outboxTotals(t, h); after != outboxBefore {
		t.Fatalf("repeat convergence must enqueue nothing: %+v -> %+v", outboxBefore, after)
	}
}

func TestConvergeExhaustedUnitRollsBackAtomically(t *testing.T) {
	h := newHarness(t)
	thread, run := seedInterruptedRun(t, h)

	h.engine.deps.Events = &flakyEventLog{EventLog: h.repos.Events, failAfter: 0}
	before := outboxTotals(t, h)

	err := h.engine.ConvergeExhausted(context.Background(), testTenantID, run.ID, 3,
		errors.New("budget exhausted"))
	if err == nil {
		t.Fatal("injected failure must surface from convergence")
	}

	reloaded, getErr := h.repos.Runs.Get(context.Background(), testTenantID, run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if reloaded.Status != domain.RunExecuting || reloaded.Version != run.Version || reloaded.Failure != nil {
		t.Fatalf("rolled-back unit must not mutate the run: before=%+v after=%+v", run, reloaded)
	}
	if after := outboxTotals(t, h); after != before {
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

func TestConvergeExhaustedNeverTouchesTheClaim(t *testing.T) {
	h := newHarness(t)
	_, run := seedPendingRun(t, h)

	acquired, err := h.engine.deps.RunClaims.Acquire(context.Background(), ports.RunClaimLeaseRequest{
		TenantID: testTenantID, RunID: run.ID, Owner: "dead-worker", LeaseFor: 30 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.engine.ConvergeExhausted(context.Background(), testTenantID, run.ID, 3,
		errors.New("budget exhausted")); err != nil {
		t.Fatalf("convergence: %v", err)
	}

	claim, err := h.repos.RunClaims.Get(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatalf("convergence must not delete the claim it does not hold: %v", err)
	}
	if claim.Status != domain.ClaimClaimed || claim.Owner != acquired.Owner ||
		claim.Generation != acquired.Generation || claim.Attempts != acquired.Attempts {
		t.Fatalf("convergence must leave the holder's credentials untouched: before=%+v after=%+v",
			acquired, claim)
	}
}
