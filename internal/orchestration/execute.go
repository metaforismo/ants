package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/scm"
)

// rootedSandbox is implemented by drivers exposing their workspace directory,
// letting SCM seeding and file materialization share one location.
type rootedSandbox interface {
	Root(ctx context.Context, id domain.SandboxID) (string, error)
}

type workspace struct {
	id   domain.SandboxID
	root string
}

type taskResult struct {
	task     *domain.Task
	headSHA  string
	evidence []domain.Evidence
}

type runState struct {
	mu sync.Mutex

	run       *domain.Run
	thread    *domain.Thread
	project   *domain.Project
	seed      scm.Seed
	repo      scm.Handle
	baseSHA   string
	ledger    *domain.BudgetLedger
	startedAt time.Time

	spec          *planner.Output
	tasks         []*domain.Task
	taskTemplates map[domain.TaskID]planner.TaskTemplate
	results       []*taskResult

	evidence     []domain.Evidence
	artifactRefs []domain.ReportArtifactRef
	findings     []domain.Finding
	integrated   domain.ReportIntegration

	sandboxes []domain.SandboxID
}

// track registers a sandbox for cleanup; called concurrently by task workers.
func (st *runState) track(id domain.SandboxID) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sandboxes = append(st.sandboxes, id)
}

func (st *runState) reserveTaskBudget() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ledger.ReserveTask()
}

func (st *runState) chargeExecOp() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ledger.RecordExecOp()
}

// addArtifactRef records an artifact reference; called concurrently by task
// workers storing verification logs.
func (st *runState) addArtifactRef(ref domain.ReportArtifactRef) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.artifactRefs = append(st.artifactRefs, ref)
}

func (st *runState) anyTaskFailed() bool {
	for _, task := range st.tasks {
		if task.Status == domain.TaskFailed || task.Status == domain.TaskBlocked || task.Status == domain.TaskCancelled {
			return true
		}
	}
	return false
}

// Execute drives one run synchronously through all stages. The API server
// invokes it on a background goroutine; the CLI demo calls it inline so the
// flow is observable step by step.
func (e *Engine) Execute(parent context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := e.registerCancel(runID, cancel); err != nil {
		return err
	}
	defer e.clearCancel(runID)

	run, err := e.deps.Runs.Get(ctx, tenantID, runID)
	if err != nil {
		return err
	}
	if run.Status != domain.RunPending {
		return domain.Conflictf("run_not_pending", "run %s is %s; only pending runs can execute", runID, run.Status)
	}
	thread, err := e.deps.Threads.Get(ctx, tenantID, run.ThreadID)
	if err != nil {
		return err
	}
	project, err := e.deps.Projects.Get(ctx, tenantID, thread.ProjectID)
	if err != nil {
		return err
	}
	seed, err := e.deps.Seeder.Seed(ctx, project.SeedName)
	if err != nil {
		return err
	}

	st := &runState{
		run:     run,
		thread:  thread,
		project: project,
		seed:    seed,
		ledger: domain.NewLedger(&domain.Budget{
			TenantID: tenantID,
			RunID:    runID,
			Scope:    domain.BudgetScopeRun,
			Limits: domain.BudgetLimits{
				MaxTasks:   e.cfg.MaxTasksPerRun,
				MaxExecOps: e.cfg.MaxExecOpsPerRun,
			},
		}),
		startedAt:     e.deps.Clock.Now().UTC(),
		taskTemplates: map[domain.TaskID]planner.TaskTemplate{},
		results:       []*taskResult{},
	}

	// Enter planning before any infrastructure work: from here on, every
	// failure has a legal transition to RunFailed.
	if err := e.transitionRun(ctx, st.run, domain.RunPlanning); err != nil {
		return err
	}

	runSbx, err := e.newWorkspace(ctx, st)
	if err != nil {
		return e.failRun(ctx, st, "provision_run_workspace", err)
	}
	st.repo = scm.Handle{
		Driver:        e.deps.SCM.Name(),
		SandboxID:     runSbx.id,
		Root:          runSbx.root,
		DefaultBranch: project.DefaultBranch,
	}
	if err := e.deps.SCM.Init(ctx, st.repo, seed); err != nil {
		return e.failRun(ctx, st, "init_repository", err)
	}
	baseSHA, err := e.deps.SCM.Head(ctx, st.repo, project.DefaultBranch)
	if err != nil {
		return e.failRun(ctx, st, "read_base_head", err)
	}
	st.baseSHA = baseSHA

	if err := e.stagePlanning(ctx, st); err != nil {
		return err
	}
	if err := e.checkCancelled(ctx, st); err != nil {
		return err
	}
	// The thread enters executing before the run does so a cancellation
	// landing in between always finds a state the cancel handler routes.
	if err := e.transitionThread(ctx, st.thread, domain.ThreadExecuting); err != nil {
		return err
	}
	if err := e.transitionRun(ctx, st.run, domain.RunExecuting); err != nil {
		return err
	}
	if err := e.stageTasks(ctx, st); err != nil {
		return e.failRun(ctx, st, "task_infrastructure", err)
	}
	if err := e.checkCancelled(ctx, st); err != nil {
		return err
	}
	if st.anyTaskFailed() {
		return e.finishUnsuccessfully(ctx, st)
	}
	if err := e.transitionRun(ctx, st.run, domain.RunIntegrating); err != nil {
		return err
	}
	if err := e.stageIntegrate(ctx, st); err != nil {
		return err
	}
	if err := e.checkCancelled(ctx, st); err != nil {
		return err
	}
	if err := e.transitionRun(ctx, st.run, domain.RunVerifying); err != nil {
		return err
	}
	if err := e.stageVerifyIntegrated(ctx, st); err != nil {
		return err
	}
	if err := e.checkCancelled(ctx, st); err != nil {
		return err
	}
	if err := e.transitionRun(ctx, st.run, domain.RunReporting); err != nil {
		return err
	}
	return e.stageReviewAndReport(ctx, st)
}

// ---- planning ----

func (e *Engine) stagePlanning(ctx context.Context, st *runState) error {
	request, err := e.lastUserRequest(ctx, st.thread.TenantID, st.thread.ID)
	if err != nil {
		return e.failRun(ctx, st, "no_request", err)
	}
	planned, err := e.deps.Planner.Plan(ctx, planner.PlanInput{
		TenantID:  st.run.TenantID,
		ThreadID:  st.run.ThreadID,
		Request:   request,
		RepoFiles: st.seed.Files,
	})
	if err != nil {
		return e.failRun(ctx, st, "plan_failed", err)
	}
	planned, err = planner.NormalizeOutput(planned)
	if err != nil {
		return e.failRun(ctx, st, "plan_failed", err)
	}
	st.spec = planned

	specID, err := e.newID(domain.PrefixSpec)
	if err != nil {
		return err
	}
	content := domain.SpecContent{
		Outcome:         planned.Spec.Outcome,
		Assumptions:     planned.Spec.Assumptions,
		Requirements:    planned.Spec.Requirements,
		NonGoals:        planned.Spec.NonGoals,
		SuccessCriteria: planned.Spec.SuccessCriteria,
		Blockers:        planned.Spec.Blockers,
	}
	spec, err := domain.NewSpec(domain.SpecID(specID), st.run.TenantID, st.run.ThreadID, 1, content, e.deps.Clock.Now())
	if err != nil {
		return e.failRun(ctx, st, "spec_invalid", err)
	}
	// Tranche 1 has no human approval surface: this gate approves only specs
	// that declare observable success criteria and carry no blockers. Human
	// approval arrives with the review UX (ADR-0007).
	if len(content.SuccessCriteria) == 0 || len(content.Blockers) > 0 {
		return e.failRun(ctx, st, "spec_not_executable", domain.Invalidf("spec_gate", "spec lacks verifiable success criteria or carries blockers"))
	}
	if err := spec.Approve(); err != nil {
		return e.failRun(ctx, st, "spec_gate", err)
	}
	if err := e.deps.Specs.Create(ctx, spec); err != nil {
		return e.failRun(ctx, st, "spec_persist", err)
	}
	st.run.SpecID = spec.ID
	if err := e.emitEvent(ctx, evtFromRun(st.run, domain.EventSpecRecorded, map[string]any{
		"spec_id": string(spec.ID),
		"version": spec.Version,
	})); err != nil {
		return err
	}

	if err := e.persistPlannedTasks(ctx, st, planned.Tasks); err != nil {
		return err
	}
	if err := e.transitionThread(ctx, st.thread, domain.ThreadReadyToExecute); err != nil {
		return err
	}
	return nil
}

// ---- task execution ----

func (e *Engine) stageTasks(ctx context.Context, st *runState) error {
	return e.executeTaskGraph(ctx, st)
}

// executeTask moves one task through queued -> provisioning -> working ->
// verifying with bounded retries for transient failures only.
func (e *Engine) executeTask(ctx context.Context, st *runState, task *domain.Task, tmpl planner.TaskTemplate) (*taskResult, error) {
	result := &taskResult{task: task}
	if err := e.transitionTask(ctx, task, domain.TaskQueued); err != nil {
		return result, err
	}
	for {
		if ctx.Err() != nil {
			return result, e.cancelTask(ctx, st, task)
		}
		if err := task.BeginAttempt(); err != nil {
			return result, e.failTaskTerminal(ctx, st, task, "attempts_exhausted", err)
		}
		expected := task.Version
		if err := e.deps.Tasks.Update(ctx, task, expected); err != nil {
			return result, err
		}

		attemptCtx, stop := context.WithTimeout(ctx, e.cfg.TaskTimeout)
		outcomeErr := e.attemptTask(attemptCtx, st, task, tmpl, result)
		stop()

		switch {
		case outcomeErr == nil:
			return result, nil
		case isCancellation(outcomeErr):
			return result, e.cancelTask(ctx, st, task)
		case domain.IsRetryable(outcomeErr):
			failure := &domain.FailureInfo{Code: "transient_failure", Message: outcomeErr.Error(), Transient: true}
			if err := e.markTaskFailed(ctx, task, failure); err != nil {
				return result, err
			}
			if err := e.transitionTask(ctx, task, domain.TaskQueued); err != nil {
				return result, err
			}
			backoff := e.cfg.RetryBackoff << uint(task.Attempts-1)
			if sleepErr := e.deps.Sleeper.Sleep(ctx, backoff); sleepErr != nil || ctx.Err() != nil {
				return result, e.cancelTask(ctx, st, task)
			}
		default:
			code := classifyFailure(outcomeErr)
			return result, e.failTaskTerminal(ctx, st, task, code, outcomeErr)
		}
	}
}

func (e *Engine) attemptTask(ctx context.Context, st *runState, task *domain.Task, tmpl planner.TaskTemplate, result *taskResult) error {
	sbx, err := e.newWorkspace(ctx, st)
	if err != nil {
		return fmt.Errorf("provision task workspace: %w", err)
	}
	if err := e.transitionTask(ctx, task, domain.TaskProvisioning); err != nil {
		return err
	}
	branch, err := e.prepareTaskBranch(ctx, st, task, tmpl)
	if err != nil {
		return err
	}

	if err := e.authorize(ctx, st, task, domain.ActionSCMLocalCommit, branch); err != nil {
		return err
	}
	if err := e.transitionTask(ctx, task, domain.TaskWorking); err != nil {
		return err
	}
	files := make(map[string][]byte, len(tmpl.Files))
	for filePath, content := range tmpl.Files {
		files[filePath] = []byte(content)
	}
	commit, err := e.deps.SCM.CommitFiles(ctx, st.repo, branch, tmpl.CommitMessage, files)
	if err != nil {
		return err
	}
	result.headSHA = commit.SHA
	if err := e.emitEvent(ctx, evtFromTask(task, domain.EventWorkspaceCommitted, map[string]any{
		"branch": branch,
		"sha":    commit.SHA,
	})); err != nil {
		return err
	}

	if err := e.transitionTask(ctx, task, domain.TaskVerifying); err != nil {
		return err
	}
	treeFiles, err := e.deps.SCM.Files(ctx, st.repo, branch)
	if err != nil {
		return err
	}
	if err := e.materialize(ctx, sbx, treeFiles); err != nil {
		return err
	}
	for _, command := range tmpl.VerifyCommands {
		evidence, err := e.execVerified(ctx, st, task, sbx, command, "")
		if err != nil {
			return err
		}
		result.evidence = append(result.evidence, evidence)
		if !evidence.Passed {
			return domain.Invalidf("task_verification_failed",
				"verification command %q exited %d", strings.Join(command, " "), evidence.ExitCode)
		}
	}
	return nil
}
