// Package worker implements the process-level run executor (ADR-0012 part 2).
// It claims bounded batches of durable run claims through RunClaims.AcquireNext,
// hands each fenced claim to the orchestration engine, keeps live leases alive
// through heartbeats, and persists claim-terminal outcomes on a context that
// survives request and shutdown cancellation. Runs abandoned by crashed
// workers are converged to a classified terminal failure instead of being
// silently resumed.
//
// Fencing discipline: every disposition write carries the acquisition
// epoch's credential tuple, so a superseded epoch cannot complete, release,
// or clean up a claim it no longer owns — the store rejects the operation
// with a typed conflict and the worker treats that as the loss signal.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// Executor is the execution boundary of this worker. *orchestration.Engine
// satisfies it; the narrow interface keeps tests honest about what the worker
// may invoke.
type Executor interface {
	Execute(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error
	RecoverInterrupted(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, cause error) error
}

// Config bounds worker behavior. Every value validates at construction so a
// mistyped deployment fails at startup instead of misbehaving under load.
type Config struct {
	// BatchSize caps how many claims one round acquires.
	BatchSize int
	// Interval is the poll cadence between rounds.
	Interval time.Duration
	// Lease is the claim lifetime granted on acquisition and every heartbeat.
	Lease time.Duration
	// HeartbeatEvery is the renewal cadence for active claims.
	HeartbeatEvery time.Duration
	// CleanupTimeout bounds every terminal persistence after execution ends;
	// it runs detached from shutdown cancellation.
	CleanupTimeout time.Duration
	// Concurrency caps simultaneous engine executions.
	Concurrency int
}

func (c Config) Validate() error {
	switch {
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return fmt.Errorf("worker.batch_size must be within [1,1000], got %d", c.BatchSize)
	case c.Interval < 10*time.Millisecond:
		return fmt.Errorf("worker.interval must be at least 10ms, got %s", c.Interval)
	case c.Lease < time.Second:
		return fmt.Errorf("worker.lease must be at least 1s, got %s", c.Lease)
	case c.HeartbeatEvery < 10*time.Millisecond:
		return fmt.Errorf("worker.heartbeat_every must be at least 10ms, got %s", c.HeartbeatEvery)
	case c.Lease < 3*c.HeartbeatEvery:
		return fmt.Errorf("worker.lease %s must be at least three times worker.heartbeat_every %s so two missed beats never expire a live worker", c.Lease, c.HeartbeatEvery)
	case c.CleanupTimeout < 100*time.Millisecond:
		return fmt.Errorf("worker.cleanup_timeout must be at least 100ms, got %s", c.CleanupTimeout)
	case c.Concurrency < 1 || c.Concurrency > 64:
		return fmt.Errorf("worker.concurrency must be within [1,64], got %d", c.Concurrency)
	}
	return nil
}

type Worker struct {
	claims ports.RunClaimStore
	runs   ports.RunStore
	exec   Executor
	logger *slog.Logger
	cfg    Config
	owner  string
}

// New builds a run worker over one claim store. The owner identity must be
// stable for the process lifetime (it fences every mutation the worker makes)
// and unique among concurrently running workers; the composition root derives
// it from hostname and pid, tests inject their own.
func New(claims ports.RunClaimStore, runs ports.RunStore, exec Executor, logger *slog.Logger, cfg Config, owner string) (*Worker, error) {
	if claims == nil || runs == nil || exec == nil || logger == nil {
		return nil, fmt.Errorf("worker: claims, runs, executor and logger are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}
	if err := domain.ValidateClaimOwner(owner); err != nil {
		return nil, fmt.Errorf("worker: owner identity: %w", err)
	}
	return &Worker{claims: claims, runs: runs, exec: exec, logger: logger, cfg: cfg, owner: owner}, nil
}

// Run claims and executes runs until ctx is cancelled. Returning implies all
// in-flight executions have unwound and their terminal persistence has been
// attempted, which is what gives graceful shutdown its drain guarantee.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
				w.logger.Error("run worker round failed", "error", safeErr(err))
			}
		}
	}
}

// ProcessOnce claims and fully executes one batch, returning how many claims
// were handled. Exported so tests and operators drive rounds manually without
// waiting on the poll interval.
//
// The batch is capped at Concurrency: the worker never holds more leases than
// it actively executes and heartbeats, so a queued claim can never sit on an
// un-renewed lease waiting for a free slot.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	limit := min(w.cfg.BatchSize, w.cfg.Concurrency)
	acquired, err := w.claims.AcquireNext(ctx, ports.RunClaimBatchRequest{
		Owner:    w.owner,
		Limit:    limit,
		LeaseFor: w.cfg.Lease,
	})
	if err != nil {
		return 0, err
	}

	var wg sync.WaitGroup
	for _, claim := range acquired {
		wg.Add(1)
		go func(c *domain.RunClaim) {
			defer wg.Done()
			w.runClaim(ctx, c)
		}(claim)
	}
	wg.Wait()
	return len(acquired), nil
}

// runClaim dispatches one freshly acquired claim by the run's current status:
// pending runs execute, terminal runs need only their leftover claim removed,
// anything else was abandoned mid-flight by some earlier epoch and converges
// to a classified interrupted failure.
func (w *Worker) runClaim(ctx context.Context, c *domain.RunClaim) {
	ref := ports.RunClaimRef{
		TenantID:   c.TenantID,
		RunID:      c.RunID,
		Owner:      w.owner,
		Token:      c.Token,
		Generation: c.Generation,
	}
	run, err := w.runs.Get(ctx, ref.TenantID, ref.RunID)
	if err != nil {
		w.logger.Error("run worker could not load run; claim left to expire for retry",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID), "error", safeErr(err))
		return
	}
	switch {
	case run.Status == domain.RunPending:
		w.executeClaim(ctx, ref)
	case run.Status.Terminal():
		// A previous epoch reached a terminal state but died before deleting
		// its claim; cleanup is guarded and idempotent. It persists on the
		// detached bounded context like every other terminal write so a
		// shutdown racing this round cannot strand the leftover until expiry.
		cleanupCtx, cancel := w.cleanupContext(ctx)
		defer cancel()
		w.cleanupTerminal(cleanupCtx, ref, "leftover claim of finished run")
	default:
		w.recoverInterrupted(ctx, ref, run.Status)
	}
}

// fenceState records whether this epoch lost its credentials while executing.
// Once fenced, the epoch touches nothing: every further write would be
// rejected anyway, and skipping it keeps stale holders strictly read-only.
type fenceState struct {
	lost atomic.Bool
}

func (f *fenceState) lose() { f.lost.Store(true) }

func (f *fenceState) isLost() bool { return f.lost.Load() }

// executeClaim drives one pending run through the engine under an active
// lease: a heartbeat loop renews the claim while the engine works and cancels
// the execution path the moment the lease is lost, so a stale epoch stops
// before it can race the new owner.
func (w *Worker) executeClaim(ctx context.Context, ref ports.RunClaimRef) {
	fenced := &fenceState{}

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()

	hbStop := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		w.keepLease(execCtx, hbStop, ref, cancelExec, fenced)
	}()

	execErr := w.exec.Execute(execCtx, ref.TenantID, ref.RunID)

	close(hbStop)
	hbWG.Wait()

	if fenced.isLost() {
		w.logger.Warn("execution abandoned: claim epoch lost",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID),
			"generation", ref.Generation)
		return
	}
	w.disposeAfterExecution(ctx, ref, execErr)
}

// keepLease heartbeats the claim until execution ends. Losing the lease —
// expiry or any fencing conflict — cancels the execution immediately: an
// expired holder forfeited its epoch and must neither continue working nor
// revive the lease.
//
// Heartbeat calls run on a context detached from cancellation so an in-flight
// renewal always delivers its verdict — renewed or lost — instead of being cut
// off mid-call by shutdown; the loop itself still exits deterministically via
// hbStop and execCtx.Done the moment Execute unwinds, so it never outlives the
// epoch it serves.
func (w *Worker) keepLease(execCtx context.Context, stop <-chan struct{}, ref ports.RunClaimRef, cancelExec context.CancelFunc, fenced *fenceState) {
	hbCtx, cancelHb := context.WithCancel(context.WithoutCancel(execCtx))
	defer cancelHb()
	ticker := time.NewTicker(w.cfg.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-execCtx.Done():
			return
		case <-ticker.C:
			if _, err := w.claims.Heartbeat(hbCtx, ref, w.cfg.Lease); err != nil {
				if execCtx.Err() != nil {
					// Execution already unwinding (shutdown); not a fencing verdict.
					return
				}
				fenced.lose()
				w.logger.Warn("run claim lease lost; cancelling execution",
					"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID),
					"generation", ref.Generation, "error", safeErr(err))
				cancelExec()
				return
			}
		}
	}
}

// disposeAfterExecution persists the claim-terminal outcome on a detached,
// bounded context: shutdown cancellation must never abort the final write,
// and cleanup must never hang forever.
func (w *Worker) disposeAfterExecution(ctx context.Context, ref ports.RunClaimRef, execErr error) {
	cleanupCtx, cancel := w.cleanupContext(ctx)
	defer cancel()

	run, err := w.runs.Get(cleanupCtx, ref.TenantID, ref.RunID)
	if err != nil {
		w.logger.Error("post-execution run reload failed; claim left to expire for recovery",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID), "error", safeErr(err))
		return
	}

	switch {
	case run.Status.Terminal():
		// Complete proves we still own the epoch; if the lease lapsed in the
		// gap between the last heartbeat and completion, the guarded sweeper
		// removes the leftover instead.
		if err := w.claims.Complete(cleanupCtx, ref); err != nil {
			if cleanErr := w.claims.CleanupTerminal(cleanupCtx, ref.TenantID, ref.RunID); cleanErr != nil {
				w.logger.Error("claim completion failed",
					"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID),
					"complete_error", safeErr(err), "cleanup_error", safeErr(cleanErr))
			}
		}
	case ctx.Err() != nil || isCancellation(execErr):
		// Shutdown or teardown ended execution before the engine reached a
		// terminal state: hand the claim back so the next round (here or in
		// another process) retries without waiting out the full lease.
		if _, err := w.claims.Release(cleanupCtx, ref); err != nil {
			w.logger.Error("claim release failed",
				"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID), "error", safeErr(err))
		} else {
			w.logger.Info("execution stopped before completion; claim released for retry",
				"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID))
		}
	default:
		// The engine returned without reaching a terminal state and without
		// cancellation: an infrastructure failure before the first legal
		// failure transition (planning entry). Release so the next round
		// retries; the full lease the next holder receives paces repeats.
		w.logger.Error("run execution aborted before reaching a terminal state",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID),
			"error", safeErr(execErr))
		if _, err := w.claims.Release(cleanupCtx, ref); err != nil {
			w.logger.Error("claim release failed",
				"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID), "error", safeErr(err))
		}
	}
}

// recoverInterrupted converges a run abandoned mid-flight by an earlier epoch
// and then removes the claim this epoch holds. Both steps persist on the
// detached bounded context: a shutdown racing the recovery must not leave the
// convergence half-done.
func (w *Worker) recoverInterrupted(ctx context.Context, ref ports.RunClaimRef, stuckAt domain.RunStatus) {
	cleanupCtx, cancel := w.cleanupContext(ctx)
	defer cancel()

	cause := fmt.Errorf("claim lease lapsed while run was %s; abandoned by a previous epoch", stuckAt)
	if err := w.exec.RecoverInterrupted(cleanupCtx, ref.TenantID, ref.RunID, cause); err != nil {
		w.logger.Error("interrupted-run convergence failed; claim left to expire for retry",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID), "error", safeErr(err))
		return
	}
	w.cleanupTerminal(cleanupCtx, ref, "recovered interrupted run")
}

// cleanupTerminal deletes the claim behind a terminal run; guarded and
// idempotent by contract, so repeated calls and missing rows are fine.
func (w *Worker) cleanupTerminal(ctx context.Context, ref ports.RunClaimRef, why string) {
	if err := w.claims.CleanupTerminal(ctx, ref.TenantID, ref.RunID); err != nil {
		w.logger.Error("terminal claim cleanup failed",
			"tenant_id", string(ref.TenantID), "run_id", string(ref.RunID),
			"reason", why, "error", safeErr(err))
	}
}

func (w *Worker) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), w.cfg.CleanupTimeout)
}

func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	return domain.ErrKindOf(err) == domain.ErrKindCancelled || errors.Is(err, context.Canceled)
}

func safeErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	const max = 512
	msg := err.Error()
	if len(msg) > max {
		return msg[:max]
	}
	return msg
}
