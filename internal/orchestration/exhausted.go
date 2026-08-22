package orchestration

import (
	"context"

	"github.com/metaforismo/ants/internal/domain"
)

// ExhaustedFailureCode classifies runs converged because their dispatch
// budget ran out: the claim was acquired more times than the worker's
// max_attempts allows without any epoch reaching a terminal state. It is the
// run-level counterpart of the outbox dead-letter (ADR-0011): bounded
// retries, then a visible terminal outcome — never an unbounded reclaim loop.
const ExhaustedFailureCode = "run_attempts_exhausted"

// ConvergeExhausted finishes a run whose claim budget is spent instead of
// letting workers reclaim it forever.
//
// The outcome follows the closed run machine's honesty about what happened.
// A run still pending never started, so it converges to cancelled — the same
// disposition an interrupted-before-start run gets — while a run abandoned
// mid-flight converges to failed with this code. Both commit their state
// change and status event in one unit of work (ADR-0010, ADR-0011). Like
// recovery, convergence is idempotent for already-terminal runs, routes the
// thread like its outcome does, and deliberately touches no claim row —
// deletion stays with the credential-holding epoch (ADR-0012).
func (e *Engine) ConvergeExhausted(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, attempts int, cause error) error {
	detail := "dispatch attempts exhausted without reaching a terminal state"
	if cause != nil {
		detail = safeDetail(cause)
	}
	run, err := e.deps.Runs.Get(ctx, tenantID, runID)
	if err != nil {
		return err
	}
	if run.Status == domain.RunPending {
		return e.convergePendingExhausted(ctx, run, detail, attempts)
	}
	return e.convergeRunToFailed(ctx, run, ExhaustedFailureCode, detail, map[string]any{"attempts": attempts})
}

// convergePendingExhausted cancels a never-started run whose dispatch budget
// is spent and hands its thread back the way cancellation does: planning goes
// to awaiting_input, executing/ready go to needs_attention.
func (e *Engine) convergePendingExhausted(ctx context.Context, run *domain.Run, detail string, attempts int) error {
	if run.Status.Terminal() {
		return nil
	}
	thread, err := e.deps.Threads.Get(ctx, run.TenantID, run.ThreadID)
	if err != nil {
		return err
	}

	expected := run.Version
	if err := run.Finish(domain.RunCancelled, e.deps.Clock.Now(), nil); err != nil {
		return err
	}
	if err := e.deps.Uow.Do(ctx, func(ctx context.Context) error {
		if err := e.deps.Runs.Update(ctx, run, expected); err != nil {
			return err
		}
		return e.emitEvent(ctx, evtFromRun(run, domain.EventRunStatusChanged, map[string]any{
			"to":       string(domain.RunCancelled),
			"code":     ExhaustedFailureCode,
			"detail":   detail,
			"attempts": attempts,
		}))
	}); err != nil {
		return err
	}

	switch thread.Status {
	case domain.ThreadPlanning:
		if terr := e.transitionThreadWithData(ctx, thread, domain.ThreadAwaitingInput, map[string]any{"reason": ExhaustedFailureCode}); terr != nil {
			if domain.ErrKindOf(terr) == domain.ErrKindInvalidTransition {
				break
			}
			return terr
		}
	case domain.ThreadExecuting, domain.ThreadReadyToExecute:
		if terr := e.transitionThreadWithData(ctx, thread, domain.ThreadNeedsAttention, map[string]any{"reason": ExhaustedFailureCode}); terr != nil {
			if domain.ErrKindOf(terr) == domain.ErrKindInvalidTransition {
				break
			}
			return terr
		}
	}
	return nil
}
