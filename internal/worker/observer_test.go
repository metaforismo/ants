package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// recordingObserver captures the instrumentation callbacks so tests can pin
// which dispatch outcomes surface as metrics without pulling in Prometheus.
type recordingObserver struct {
	mu        sync.Mutex
	acquired  []int
	finished  []string
	converged []string
}

func (o *recordingObserver) ClaimsAcquired(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acquired = append(o.acquired, n)
}

func (o *recordingObserver) RunFinished(outcome string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, outcome)
}

func (o *recordingObserver) RunConverged(kind string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.converged = append(o.converged, kind)
}

func (o *recordingObserver) totals() (acquired int, finished, converged []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, n := range o.acquired {
		acquired += n
	}
	return acquired, append([]string(nil), o.finished...), append([]string(nil), o.converged...)
}

// TestObserverRecordsAcquisitionAndTerminalOutcome pins that a normal
// execution surfaces exactly one acquisition and one terminal-outcome
// observation, with no convergence events.
func TestObserverRecordsAcquisitionAndTerminalOutcome(t *testing.T) {
	obs := &recordingObserver{}
	w := newTestWorldFull(t, nil, obs)
	run := w.seedRun("observed")
	w.exec.script(run.ID, w.completeBehavior(run.ID, domain.RunCompleted))

	awaitRound(t, w.runInRound(context.Background()))

	acquired, finished, converged := obs.totals()
	if acquired != 1 {
		t.Fatalf("one claim acquisition must be observed, got %d", acquired)
	}
	if len(finished) != 1 || finished[0] != string(domain.RunCompleted) {
		t.Fatalf("exactly one completed outcome must be observed, got %v", finished)
	}
	if len(converged) != 0 {
		t.Fatalf("a completed run must not record convergence, got %v", converged)
	}
}

// TestNilObserverKeepsExecutionBehavior pins the observer contract: nil only
// disables instrumentation, construction and execution behave exactly as
// instrumented rounds do.
func TestNilObserverKeepsExecutionBehavior(t *testing.T) {
	w := newTestWorld(t, nil)
	run := w.seedRun("unobserved")
	w.exec.script(run.ID, w.completeBehavior(run.ID, domain.RunCompleted))

	awaitRound(t, w.runInRound(context.Background()))

	if got := w.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run must complete with no observer attached, got %s", got)
	}
}

// TestObserverRecordsExhaustedConvergence pins that an over-budget
// acquisition converges instead of executing and is observed as exhausted.
func TestObserverRecordsExhaustedConvergence(t *testing.T) {
	obs := &recordingObserver{}
	w := newTestWorldFull(t, func(c *Config) { c.MaxAttempts = 2 }, obs)
	run := w.seedRun("observed-poison")
	for i := 0; i < 3; i++ {
		w.burnAttempt(run.ID)
	}

	awaitRound(t, w.runInRound(context.Background()))
	w.exec.neverStarted(t, run.ID)

	acquired, finished, converged := obs.totals()
	if acquired != 1 {
		t.Fatalf("the converging round's acquisition must be observed, got %d", acquired)
	}
	if len(finished) != 0 {
		t.Fatalf("an unexecuted run must not record a terminal outcome, got %v", finished)
	}
	if len(converged) != 1 || converged[0] != "exhausted" {
		t.Fatalf("exactly one exhausted convergence must be observed, got %v", converged)
	}
}

// TestObserverRecordsInterruptedConvergence pins that a run abandoned
// mid-flight by a dead epoch is observed as interrupted convergence.
func TestObserverRecordsInterruptedConvergence(t *testing.T) {
	obs := &recordingObserver{}
	w := newTestWorldFull(t, nil, obs)
	run := w.seedRun("observed-interrupted")
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

	acquired, finished, converged := obs.totals()
	if acquired != 1 {
		t.Fatalf("the recovery round's acquisition must be observed, got %d", acquired)
	}
	if len(finished) != 0 {
		t.Fatalf("recovery converges without executing; outcomes must stay empty, got %v", finished)
	}
	if len(converged) != 1 || converged[0] != "interrupted" {
		t.Fatalf("exactly one interrupted convergence must be observed, got %v", converged)
	}
}
