package orchestration

import (
	"context"

	"github.com/metaforismo/ants/internal/domain"
)

// InterruptedFailureCode classifies runs converged by recovery instead of by
// their own execution: a worker lost its claim lease mid-flight and another
// epoch finished the run on its behalf.
const InterruptedFailureCode = "run_interrupted"

// RecoverInterrupted converges a run abandoned mid-flight by a lost worker
// epoch to a visible terminal failure (ADR-0012 part 2). The engine cannot
// resume a half-executed pipeline, so the honest outcome is failed with
// InterruptedFailureCode rather than a silent retry from an unknown point.
//
// The run update and its status event commit in one unit of work — the event
// append enqueues its outbox message in the same commit (ADR-0011) — so the
// convergence is atomic and observable. It is idempotent: an already-terminal
// run returns nil without emitting anything, so repeated recovery epochs never
// duplicate terminal effects, and concurrent epochs are serialized by the
// version-guarded update inside the unit.
//
// A pending run is refused: pending means no worker ever started it; such
// runs are executed, not mourned.
func (e *Engine) RecoverInterrupted(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, cause error) error {
	run, err := e.deps.Runs.Get(ctx, tenantID, runID)
	if err != nil {
		return err
	}
	switch {
	case run.Status == domain.RunPending:
		return domain.Conflictf("run_not_interrupted", "run %s is pending; only runs abandoned mid-flight can be recovered as interrupted", runID)
	case run.Status.Terminal():
		return nil
	}
	thread, err := e.deps.Threads.Get(ctx, tenantID, run.ThreadID)
	if err != nil {
		return err
	}

	detail := "execution lease lost before completion"
	if cause != nil {
		detail = safeDetail(cause)
	}
	failure := &domain.FailureInfo{Code: InterruptedFailureCode, Message: detail, Transient: false}
	expected := run.Version
	if err := run.Finish(domain.RunFailed, e.deps.Clock.Now(), failure); err != nil {
		return err
	}
	err = e.deps.Uow.Do(ctx, func(ctx context.Context) error {
		if err := e.deps.Runs.Update(ctx, run, expected); err != nil {
			return err
		}
		return e.emitEvent(ctx, evtFromRun(run, domain.EventRunStatusChanged, map[string]any{
			"to":     string(domain.RunFailed),
			"code":   InterruptedFailureCode,
			"detail": detail,
		}))
	})
	if err != nil {
		return err
	}

	// Route the thread like failRun does; ready_to_execute has no failed edge
	// and goes to the human instead, mirroring cancellation routing.
	switch thread.Status {
	case domain.ThreadPlanning, domain.ThreadExecuting, domain.ThreadReviewing:
		if terr := e.transitionThreadWithData(ctx, thread, domain.ThreadFailed, map[string]any{"reason": InterruptedFailureCode}); terr != nil {
			if domain.ErrKindOf(terr) == domain.ErrKindInvalidTransition {
				break
			}
			return terr
		}
	case domain.ThreadReadyToExecute:
		if terr := e.transitionThreadWithData(ctx, thread, domain.ThreadNeedsAttention, map[string]any{"reason": InterruptedFailureCode}); terr != nil {
			if domain.ErrKindOf(terr) == domain.ErrKindInvalidTransition {
				break
			}
			return terr
		}
	}
	return nil
}
