package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/policy"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/review"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
)

const principalID = domain.PrincipalID("prn_testprincipal0000000")

func startInput(thread *domain.Thread, key string) StartInput {
	return StartInput{
		TenantID:       testTenantID,
		ThreadID:       thread.ID,
		Principal:      principalID,
		Actor:          domain.Actor{Type: domain.PrincipalHuman, ID: string(principalID)},
		IdempotencyKey: key,
	}
}

func TestHappyPathCompletesWithEvidence(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	run, err := startAndExecute(t, h, thread)
	if err != nil {
		t.Fatalf("execute: %v\nreport=%+v", err, run.Report)
	}
	if run.Report == nil || !run.Report.ReadyForReview {
		t.Fatalf("completed run must carry a ready report: %+v", run.Report)
	}
	if len(run.Report.Tasks) != 2 {
		t.Fatalf("expected 2 parallel tasks, got %d", len(run.Report.Tasks))
	}
	for _, rt := range run.Report.Tasks {
		if rt.Status != domain.TaskDone || rt.CommitSHA == "" || rt.Attempts != 1 {
			t.Fatalf("task not cleanly done: %+v", rt)
		}
		if !strings.HasPrefix(rt.Branch, "ants/task-") {
			t.Fatalf("task must work on its own branch: %q", rt.Branch)
		}
	}
	if !run.Report.Verification.Passed || len(run.Report.Verification.Evidence) == 0 {
		t.Fatalf("verification evidence missing")
	}
	for _, ev := range run.Report.Verification.Evidence {
		if !ev.Passed || ev.ExitCode != 0 || ev.LogArtifactID == "" {
			t.Fatalf("evidence incomplete: %+v", ev)
		}
	}
	if run.Report.Integration.SHA == "" || strings.Contains(run.Report.Integration.Branch, "main") {
		t.Fatalf("integration must happen on a dedicated branch, got %+v", run.Report.Integration)
	}
	if run.Report.Budget.TasksUsed != 2 {
		t.Fatalf("budget accounting wrong: %+v", run.Report.Budget)
	}
	threadState, err := h.repos.Threads.Get(ctx, testTenantID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if threadState.Status != domain.ThreadReadyForReview {
		t.Fatalf("thread should be ready for review, got %s", threadState.Status)
	}
}

// recordingSCM remembers the handle of the most recently initialized repo so
// tests can inspect branch state through the memory driver.
type recordingSCM struct {
	*scm.Memory
	mu         sync.Mutex
	lastHandle scm.Handle
}

func (r *recordingSCM) Init(ctx context.Context, h scm.Handle, seed scm.Seed) error {
	r.mu.Lock()
	r.lastHandle = h
	r.mu.Unlock()
	return r.Memory.Init(ctx, h, seed)
}

func TestDefaultBranchUntouchedByPipeline(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	rec := &recordingSCM{Memory: h.memSCM}
	h.engine.deps.SCM = rec
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	run, err := startAndExecute(t, h, thread)
	if err != nil {
		t.Fatal(err)
	}
	handle := rec.lastHandle
	mainFiles, err := rec.Files(ctx, handle, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"lib_add.sh", "lib_mul.sh"} {
		if _, ok := mainFiles[forbidden]; ok {
			t.Fatalf("default branch must not receive task file %s before human merge", forbidden)
		}
	}
	integratedFiles, err := rec.Files(ctx, handle, run.Report.Integration.Branch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lib_add.sh", "lib_mul.sh"} {
		if _, ok := integratedFiles[want]; !ok {
			t.Fatalf("integration branch missing %s", want)
		}
	}
}

func TestEventsEmittedForWholePipeline(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if err != nil {
		t.Fatal(err)
	}
	events, err := h.repos.Events.ListByRun(context.Background(), testTenantID, run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	types := map[domain.EventType]int{}
	for _, e := range events {
		types[e.Type]++
		if e.TenantID != testTenantID {
			t.Fatalf("event tenant mismatch: %s", e.TenantID)
		}
		if e.Seq == 0 {
			t.Fatalf("events must carry a monotonic cursor")
		}
	}
	for _, want := range []domain.EventType{
		domain.EventSpecRecorded,
		domain.EventTaskCreated,
		domain.EventTaskStatusChanged,
		domain.EventWorkspaceCommitted,
		domain.EventArtifactStored,
		domain.EventPolicyEvaluated,
		domain.EventRunStatusChanged,
	} {
		if types[want] == 0 {
			t.Errorf("missing event %s in stream of %d", want, len(events))
		}
	}
}

func TestIdempotentStartReturnsSameRun(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	in := startInput(thread, "same-key")

	first, err := h.engine.StartRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.StartRun(context.Background(), in)
	if err != nil {
		t.Fatalf("duplicate start must replay, not error: %v", err)
	}
	if !second.Replayed || first.Run.ID != second.Run.ID {
		t.Fatalf("expected replay of same run")
	}
}

func TestUnknownRequestMessageFailsPlanning(t *testing.T) {
	h := newHarness(t)
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	msg := &domain.Message{
		TenantID:     testTenantID,
		ThreadID:     thread.ID,
		Role:         domain.RoleUser,
		DeliveryMode: domain.DeliveryImmediate,
		Content:      "please refactor the billing subsystem",
	}
	if err := h.repos.Threads.AppendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}

	start, err := h.engine.StartRun(ctx, startInput(thread, "unknown-cap"))
	if err != nil {
		t.Fatal(err)
	}
	execErr := h.engine.Execute(ctx, testTenantID, start.Run.ID)
	if execErr == nil {
		t.Fatalf("unmatched request must fail planning")
	}
	run, _ := h.repos.Runs.Get(ctx, testTenantID, start.Run.ID)
	if run.Status != domain.RunFailed || run.Failure.Code != "plan_failed" {
		t.Fatalf("expected plan_failed, got %s %+v", run.Status, run.Failure)
	}
}

func TestVerificationFailureIsTerminalAndVisible(t *testing.T) {
	h := newHarness(t)
	h.fake.Script("test -f lib_add.sh", sandbox.ExecResult{ExitCode: 1})
	h.fake.Script("sh calc.sh add 20 22", sandbox.ExecResult{ExitCode: 127})
	h.fake.Script("test -f lib_mul.sh", exit0())
	h.fake.Script("sh calc.sh multiply 6 7", stdout("42"))
	h.fake.Script("bash tests/calc_test.sh", stdout("all tests passed"))
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if err != nil && domain.ErrKindOf(err) == domain.ErrKindCancelled {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	if run.Status != domain.RunFailed {
		t.Fatalf("run must fail when verification fails, got %s (%+v)", run.Status, run.Failure)
	}
	if run.Failure == nil || run.Failure.Code != "task_verification_failed" {
		t.Fatalf("failure code must identify verification, got %+v", run.Failure)
	}
	failed := false
	for _, rt := range run.Report.Tasks {
		if rt.Status == domain.TaskFailed && rt.Failure != nil && rt.Failure.Code == "task_verification_failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("report must show the failed task with its code: %+v", run.Report.Tasks)
	}
	state, _ := h.repos.Threads.Get(context.Background(), testTenantID, thread.ID)
	if state.Status != domain.ThreadFailed {
		t.Fatalf("thread must surface the failure, got %s", state.Status)
	}
}

func TestTransientFailuresRetryThenSucceed(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	flaky := &flakyDriver{FakeDriver: h.fake, failFirstN: map[string]int{
		"bash tests/calc_test.sh": 1,
	}}
	h.engine.deps.Sandbox = flaky
	_, thread := h.seedWorld(t)

	start, err := h.engine.StartRun(context.Background(), startInput(thread, "retry-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Execute(context.Background(), testTenantID, start.Run.ID); err != nil {
		t.Fatalf("execute after retries: %v", err)
	}
	run, _ := h.repos.Runs.Get(context.Background(), testTenantID, start.Run.ID)
	if run.Status != domain.RunCompleted || !run.Report.ReadyForReview {
		t.Fatalf("transient blip must recover to ready: %s %+v", run.Status, run.Failure)
	}
	if len(h.sleeper.durations) == 0 {
		t.Fatalf("backoff must have been applied between attempts")
	}
}

// flakyDriver fails the first N invocations of matching commands with a
// transient classification, then delegates to the scripted fake.
type flakyDriver struct {
	*sandbox.FakeDriver
	failFirstN map[string]int
	mu         sync.Mutex
}

func (f *flakyDriver) Exec(ctx context.Context, id domain.SandboxID, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	key := sandbox.JoinCommand(req.Command)
	f.mu.Lock()
	if f.failFirstN[key] > 0 {
		f.failFirstN[key]--
		f.mu.Unlock()
		return sandbox.ExecResult{}, &domain.Error{
			Kind:    domain.ErrKindTimeout,
			Code:    "sandbox_exec_timeout",
			Message: "injected transient failure",
		}
	}
	f.mu.Unlock()
	return f.FakeDriver.Exec(ctx, id, req)
}

func TestBudgetCapStopsRunBeforeOverspend(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	h.engine.cfg.MaxTasksPerRun = 1
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if run == nil {
		t.Fatalf("run not persisted; err=%v", err)
	}
	if run.Status != domain.RunFailed || run.Failure == nil || run.Failure.Code != "budget_exhausted" {
		t.Fatalf("expected explicit budget failure, got %s %+v", run.Status, run.Failure)
	}
	blockers := false
	for _, f := range run.Report.Findings {
		if f.Category == "budget" && f.Blocking() {
			blockers = true
		}
	}
	if !blockers {
		t.Fatalf("budget failure must carry a blocker finding")
	}
}

// gateDriver parks the first matching command until the test releases it or
// the context is cancelled, making cancellation tests deterministic.
type gateDriver struct {
	sandbox.Driver
	gateKey string
	release chan struct{}
}

func (g *gateDriver) Exec(ctx context.Context, id domain.SandboxID, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	if sandbox.JoinCommand(req.Command) != g.gateKey {
		return g.Driver.Exec(ctx, id, req)
	}
	select {
	case <-ctx.Done():
		return sandbox.ExecResult{}, &domain.Error{
			Kind:    domain.ErrKindCancelled,
			Code:    "sandbox_exec_cancelled",
			Message: "execution cancelled while gated",
			Cause:   ctx.Err(),
		}
	case <-g.release:
		return g.Driver.Exec(ctx, id, req)
	}
}

func TestCancellationMarksRunAndThreadCooperatively(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	gated := &gateDriver{
		Driver:  h.fake,
		gateKey: "bash tests/calc_test.sh",
		release: make(chan struct{}),
	}
	h.engine.deps.Sandbox = gated
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	start, err := h.engine.StartRun(ctx, startInput(thread, "cancel-key"))
	if err != nil {
		t.Fatal(err)
	}
	execDone := make(chan error, 1)
	go func() {
		execDone <- h.engine.Execute(ctx, testTenantID, start.Run.ID)
	}()

	// Cancellation is only possible once the worker registered; the gated
	// verification command guarantees execution is still in flight.
	cancelled := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := h.engine.Cancel(ctx, testTenantID, start.Run.ID); err == nil {
			cancelled = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	execErr := <-execDone
	if !cancelled {
		close(gated.release)
		t.Skipf("execution finished before cancellation could register")
	}
	if domain.ErrKindOf(execErr) != domain.ErrKindCancelled {
		t.Fatalf("expected cancellation error, got %v", execErr)
	}
	run, _ := h.repos.Runs.Get(ctx, testTenantID, start.Run.ID)
	if run.Status != domain.RunCancelled {
		t.Fatalf("run must be cancelled, got %s", run.Status)
	}
	state, _ := h.repos.Threads.Get(ctx, testTenantID, thread.ID)
	switch state.Status {
	case domain.ThreadNeedsAttention, domain.ThreadAwaitingInput, domain.ThreadPlanning:
	default:
		t.Fatalf("thread must land in an inspectable state, got %s", state.Status)
	}
}

func TestDoubleExecuteRejected(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	result, err := h.engine.StartRun(context.Background(), startInput(thread, "double"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Execute(context.Background(), testTenantID, result.Run.ID); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	err = h.engine.Execute(context.Background(), testTenantID, result.Run.ID)
	if domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("second execute must conflict, got %v", err)
	}
}

func TestCrossTenantRunAccessDenied(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	_, thread := h.seedWorld(t)
	result, err := h.engine.StartRun(context.Background(), startInput(thread, "tenant-check"))
	if err != nil {
		t.Fatal(err)
	}
	otherTenant := domain.TenantID("ten_othertenant000000000")
	_, err = h.engine.deps.Runs.Get(context.Background(), otherTenant, result.Run.ID)
	if domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("cross-tenant read must be uniform not-found, got %v", err)
	}
	_, err = h.engine.deps.Runs.GetByIdempotencyKey(context.Background(), otherTenant, thread.ID, "tenant-check")
	if domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("cross-tenant idempotency lookup must not leak existence")
	}
}

func TestEngineRejectsInvalidDriverPairing(t *testing.T) {
	h := newHarness(t)
	pol := policy.NewEngine(true, h.clock, ports.RandomIDs{}, h.repos.PolicyDecisions, h.repos.Audit)
	_, err := New(Deps{
		Threads: h.repos.Threads, Projects: h.repos.Projects, Specs: h.repos.Specs,
		Tasks: h.repos.Tasks, Runs: h.repos.Runs, Workspaces: h.repos.Workspaces,
		Artifacts: h.repos.Artifacts, Events: h.repos.Events,
		Uow:      h.repos.NewTransactor(),
		Policy:   pol,
		Sandbox:  sandbox.NewFakeDriver(),
		SCM:      scm.NewMemory(),
		Planner:  planner.NewDeterministic(),
		Reviewer: review.NewDeterministic(2000),
		Seeder:   seederFunc(func(context.Context, string) (scm.Seed, error) { return scm.Seed{}, nil }),
		Clock:    h.clock, IDs: ports.RandomIDs{}, Sleeper: h.sleeper,
	}, validConfig())
	if err != nil {
		t.Fatalf("valid pairing rejected unexpectedly: %v", err)
	}

	missing := Deps{
		Threads: h.repos.Threads, Projects: h.repos.Projects, Specs: h.repos.Specs,
		Tasks: h.repos.Tasks, Runs: h.repos.Runs, Workspaces: h.repos.Workspaces,
		Artifacts: h.repos.Artifacts, Events: h.repos.Events,
		Uow:    h.repos.NewTransactor(),
		Policy: pol, Sandbox: sandbox.NewFakeDriver(), SCM: scm.NewMemory(),
		Seeder: seederFunc(func(context.Context, string) (scm.Seed, error) { return scm.Seed{}, nil }),
		Clock:  h.clock, IDs: ports.RandomIDs{}, Sleeper: h.sleeper,
		// Planner deliberately omitted.
	}
	if _, err := New(missing, validConfig()); err == nil {
		t.Fatalf("engine without planner must fail construction")
	}
}

func validConfig() Config {
	return Config{
		MaxParallelTasks: 2,
		TaskTimeout:      time.Second,
		StageTimeout:     5 * time.Second,
		MaxAttempts:      3,
		RetryBackoff:     time.Millisecond,
		MaxTasksPerRun:   8,
		MaxExecOpsPerRun: 64,
	}
}

// flakyEventLog fails appends after N successes so tests can prove that a
// state change and its event commit or roll back together.
type flakyEventLog struct {
	ports.EventLog
	failAfter int
	calls     int
}

func (f *flakyEventLog) Append(ctx context.Context, evt *domain.Event) error {
	f.calls++
	if f.calls > f.failAfter {
		return &domain.Error{Kind: domain.ErrKindTransient, Code: "injected", Message: "event log unavailable"}
	}
	return f.EventLog.Append(ctx, evt)
}

func TestTransitionAtomicityRollsBackStateWithoutEvent(t *testing.T) {
	h := newHarness(t)
	_, thread := h.seedWorld(t)
	ctx := context.Background()

	start, err := h.engine.StartRun(ctx, startInput(thread, "atomic-key"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := h.repos.Runs.Get(ctx, testTenantID, start.Run.ID)
	if err != nil {
		t.Fatal(err)
	}

	events := &flakyEventLog{EventLog: h.repos.Events, failAfter: 1 << 30}
	h.engine.deps.Events = events

	// First transition succeeds (append allowed), second hits the injected
	// failure and must leave neither state nor event behind.
	if err := h.engine.transitionRun(ctx, fresh, domain.RunPlanning); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	events.failAfter = 0 // every append now fails
	versionBefore := fresh.Version
	err = h.engine.transitionRun(ctx, fresh, domain.RunExecuting)
	if err == nil {
		t.Fatalf("injected failure must surface")
	}
	reloaded, _ := h.repos.Runs.Get(ctx, testTenantID, start.Run.ID)
	if reloaded.Status != domain.RunPlanning || reloaded.Version != versionBefore {
		t.Fatalf("failed unit must not mutate persisted state: %+v", reloaded)
	}
}
