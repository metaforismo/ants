// Package orchestration implements the deterministic run pipeline:
// request -> plan/spec -> isolated parallel tasks -> integration -> tests ->
// report. Domain logic is expressed as plain functions over the persistence
// ports, so a durable engine (Temporal per ADR-0002) can drive the same
// stages without rewriting them.
package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/policy"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/review"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
)

type Config struct {
	MaxParallelTasks int
	TaskTimeout      time.Duration
	StageTimeout     time.Duration
	MaxAttempts      int
	RetryBackoff     time.Duration
	MaxTasksPerRun   int
	MaxExecOpsPerRun int
}

// Seeder supplies initial repository content for a project. Tranche 1
// resolves registered fixtures; SCM connections replace this in later waves.
type Seeder interface {
	Seed(ctx context.Context, name string) (scm.Seed, error)
}

type Deps struct {
	Threads    ports.ThreadStore
	Projects   ports.ProjectStore
	Specs      ports.SpecStore
	Tasks      ports.TaskStore
	Runs       ports.RunStore
	Workspaces ports.WorkspaceStore
	Artifacts  ports.ArtifactStore
	Events     ports.EventLog

	Policy   *policy.Engine
	Sandbox  sandbox.Driver
	SCM      scm.Driver
	Planner  planner.Planner
	Reviewer review.Reviewer
	Seeder   Seeder

	Clock   ports.Clock
	IDs     ports.IDGenerator
	Sleeper ports.Sleeper
}

type Engine struct {
	deps Deps
	cfg  Config

	mu          sync.Mutex
	cancelFuncs map[domain.RunID]context.CancelFunc
}

func New(deps Deps, cfg Config) (*Engine, error) {
	if deps.Threads == nil || deps.Projects == nil || deps.Specs == nil || deps.Tasks == nil ||
		deps.Runs == nil || deps.Workspaces == nil || deps.Artifacts == nil || deps.Events == nil {
		return nil, fmt.Errorf("orchestration: all persistence stores are required")
	}
	if deps.Policy == nil || deps.Planner == nil || deps.Reviewer == nil || deps.Seeder == nil ||
		deps.Clock == nil || deps.IDs == nil || deps.Sleeper == nil {
		return nil, fmt.Errorf("orchestration: policy, planner, reviewer, seeder and time sources are required")
	}
	if deps.Sandbox == nil || deps.SCM == nil {
		return nil, fmt.Errorf("orchestration: sandbox and scm drivers are required")
	}
	if cfg.MaxParallelTasks < 1 || cfg.TaskTimeout <= 0 || cfg.StageTimeout <= 0 || cfg.MaxAttempts < 1 || cfg.RetryBackoff < 0 {
		return nil, fmt.Errorf("orchestration: config out of range")
	}
	if cfg.MaxTasksPerRun < 1 || cfg.MaxExecOpsPerRun < 1 {
		return nil, fmt.Errorf("orchestration: budget caps must be positive")
	}
	// The fake sandbox executes nothing; pairing it with real git would make
	// verification silently operate on stale trees.
	if deps.Sandbox.Name() == "fake" && deps.SCM.Name() != "memory" {
		return nil, fmt.Errorf("orchestration: fake sandbox requires the memory SCM driver")
	}
	if deps.Sandbox.Name() != "fake" {
		if _, ok := any(deps.Sandbox).(rootedSandbox); !ok {
			return nil, fmt.Errorf("orchestration: driver %q must expose its workspace root", deps.Sandbox.Name())
		}
	}
	return &Engine{deps: deps, cfg: cfg, cancelFuncs: map[domain.RunID]context.CancelFunc{}}, nil
}

// SystemPrincipal is the control-plane identity used for policy evaluation of
// infrastructure actions (sandbox creation for a run's canonical workspace).
const SystemPrincipal = systemPrincipalID

type StartInput struct {
	TenantID       domain.TenantID
	ThreadID       domain.ThreadID
	Principal      domain.PrincipalID
	Actor          domain.Actor
	IdempotencyKey string
}

type StartResult struct {
	Run      *domain.Run
	Replayed bool
}

// StartRun creates (or replays) a run for a thread and moves it to pending.
// Execution starts when Execute is called; callers own that decision so a
// durable engine can schedule it instead of this process.
func (e *Engine) StartRun(ctx context.Context, in StartInput) (*StartResult, error) {
	if err := in.Actor.Validate(); err != nil {
		return nil, err
	}
	thread, err := e.deps.Threads.Get(ctx, in.TenantID, in.ThreadID)
	if err != nil {
		return nil, err
	}

	existing, err := e.deps.Runs.GetByIdempotencyKey(ctx, in.TenantID, in.ThreadID, in.IdempotencyKey)
	if err == nil && existing != nil {
		return &StartResult{Run: existing, Replayed: true}, nil
	} else if err != nil && domain.ErrKindOf(err) != domain.ErrKindNotFound {
		return nil, err
	}

	if _, err := e.deps.Projects.Get(ctx, in.TenantID, thread.ProjectID); err != nil {
		return nil, domain.Invalidf("run_project_unknown", "thread references project %s which cannot be loaded", thread.ProjectID)
	}
	if _, _, lastErr := e.deps.Threads.Messages(ctx, in.TenantID, in.ThreadID, 0, 1); lastErr != nil {
		return nil, domain.Invalidf("run_no_request", "thread has no request message to plan from")
	}

	runID, err := e.newID(domain.PrefixRun)
	if err != nil {
		return nil, err
	}
	run, err := domain.NewRun(domain.RunID(runID), in.TenantID, in.ThreadID, in.IdempotencyKey, e.deps.Clock.Now())
	if err != nil {
		return nil, err
	}
	if err := e.deps.Runs.Create(ctx, run); err != nil {
		if domain.ErrKindOf(err) == domain.ErrKindConflict {
			// A concurrent request won the idempotency race; return its run.
			replayed, getErr := e.deps.Runs.GetByIdempotencyKey(ctx, in.TenantID, in.ThreadID, in.IdempotencyKey)
			if getErr == nil {
				return &StartResult{Run: replayed, Replayed: true}, nil
			}
		}
		return nil, err
	}

	if err := e.transitionThread(ctx, thread, domain.ThreadPlanning); err != nil {
		return nil, err
	}
	return &StartResult{Run: run}, nil
}

// Cancel requests cooperative cancellation of an active run.
func (e *Engine) Cancel(_ context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	run, err := e.deps.Runs.Get(context.Background(), tenantID, runID)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return domain.Conflictf("run_already_finished", "run %s is already %s", runID, run.Status)
	}
	e.mu.Lock()
	cancel := e.cancelFuncs[runID]
	e.mu.Unlock()
	if cancel == nil {
		return domain.Conflictf("run_not_running", "run %s is not executing in this process", runID)
	}
	cancel()
	return nil
}

func (e *Engine) newID(prefix string) (string, error) {
	id, err := e.deps.IDs.NewID(prefix)
	if err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return id, nil
}

// transitionRun persists a run state transition under optimistic concurrency.
func (e *Engine) transitionRun(ctx context.Context, run *domain.Run, next domain.RunStatus) error {
	expected := run.Version
	if err := run.TransitionTo(next); err != nil {
		return err
	}
	if err := e.deps.Runs.Update(ctx, run, expected); err != nil {
		return err
	}
	return e.emitEvent(ctx, evtFromRun(run, domain.EventRunStatusChanged, map[string]any{"to": string(next)}))
}

func (e *Engine) transitionThread(ctx context.Context, thread *domain.Thread, next domain.ThreadStatus) error {
	return e.transitionThreadWithData(ctx, thread, next, nil)
}

// transitionThreadWithData persists a thread transition and emits its status
// event; extra fields merge into the event data.
func (e *Engine) transitionThreadWithData(ctx context.Context, thread *domain.Thread, next domain.ThreadStatus, extra map[string]any) error {
	expected := thread.Version
	if err := thread.TransitionTo(next); err != nil {
		return err
	}
	if err := e.deps.Threads.Update(ctx, thread, expected); err != nil {
		return err
	}
	data := map[string]any{"to": string(next)}
	for k, v := range extra {
		data[k] = v
	}
	return e.emitEvent(ctx, evtFromThread(thread, domain.EventThreadStatusChanged, data))
}

func (e *Engine) transitionTask(ctx context.Context, task *domain.Task, next domain.TaskStatus) error {
	expected := task.Version
	if err := task.TransitionTo(next); err != nil {
		return err
	}
	if err := e.deps.Tasks.Update(ctx, task, expected); err != nil {
		return err
	}
	return e.emitEvent(ctx, evtFromTask(task, domain.EventTaskStatusChanged, map[string]any{
		"to":       string(next),
		"attempts": task.Attempts,
	}))
}

func (e *Engine) emitEvent(ctx context.Context, event *domain.Event) error {
	if event == nil {
		return nil
	}
	id, err := e.newID(domain.PrefixEvent)
	if err != nil {
		return err
	}
	event.ID = domain.EventID(id)
	event.OccurredAt = e.deps.Clock.Now().UTC()
	return e.deps.Events.Append(ctx, event)
}

func evtFromThread(thread *domain.Thread, eventType domain.EventType, data map[string]any) *domain.Event {
	return &domain.Event{
		Type:             eventType,
		TenantID:         thread.TenantID,
		AggregateType:    "thread",
		AggregateID:      string(thread.ID),
		AggregateVersion: thread.Version,
		Data:             data,
	}
}

func evtFromRun(run *domain.Run, eventType domain.EventType, data map[string]any) *domain.Event {
	return &domain.Event{
		Type:             eventType,
		TenantID:         run.TenantID,
		AggregateType:    "run",
		AggregateID:      string(run.ID),
		AggregateVersion: run.Version,
		RunID:            run.ID,
		Data:             data,
	}
}

func evtFromTask(task *domain.Task, eventType domain.EventType, data map[string]any) *domain.Event {
	return &domain.Event{
		Type:             eventType,
		TenantID:         task.TenantID,
		AggregateType:    "task",
		AggregateID:      string(task.ID),
		AggregateVersion: task.Version,
		RunID:            task.RunID,
		Data:             data,
	}
}
