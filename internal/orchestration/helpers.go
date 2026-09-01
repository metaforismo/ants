package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/sandbox"
)

const systemPrincipalID = domain.PrincipalID("prn_antssystemorchestrator0")

func (e *Engine) registerCancel(runID domain.RunID, cancel context.CancelFunc) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, running := e.cancelFuncs[runID]; running {
		return domain.Conflictf("run_already_running", "run %s is already executing in this process", runID)
	}
	e.cancelFuncs[runID] = cancel
	return nil
}

func (e *Engine) clearCancel(runID domain.RunID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.cancelFuncs, runID)
}

// authorize records a policy decision for a boundary crossing and enforces it.
// task may be nil for control-plane actions performed before tasks exist.
func (e *Engine) authorize(ctx context.Context, st *runState, task *domain.Task, action domain.PolicyAction, resource string) error {
	req := domain.PolicyRequest{
		TenantID:  st.run.TenantID,
		RunID:     st.run.ID,
		Principal: systemPrincipalID,
		Action:    action,
		Resource:  resource,
	}
	if task != nil {
		req.TaskID = task.ID
	}
	decision, err := e.deps.Policy.Authorize(ctx, req)
	if err != nil {
		return err
	}
	return e.emitEvent(ctx, &domain.Event{
		Type:          domain.EventPolicyEvaluated,
		TenantID:      st.run.TenantID,
		AggregateType: "policy_decision",
		AggregateID:   string(decision.ID),
		RunID:         st.run.ID,
		Data: map[string]any{
			"action":  string(action),
			"outcome": string(decision.Outcome),
			"reason":  decision.Reason,
		},
	})
}

// newWorkspace creates an isolated sandbox for execution or verification and
// registers it for cleanup. Authorization of sandbox.create is the caller's
// responsibility and happens before this call.
func (e *Engine) newWorkspace(ctx context.Context, st *runState) (*workspace, error) {
	if _, err := e.authorizeCreate(ctx, st); err != nil {
		return nil, err
	}
	id, err := e.deps.Sandbox.Create(ctx, sandbox.CreateRequest{
		TenantID:  st.run.TenantID,
		RunID:     st.run.ID,
		Principal: systemPrincipalID,
	})
	if err != nil {
		return nil, err
	}
	st.track(id)
	ws := &workspace{id: id}
	if rooted, ok := e.deps.Sandbox.(rootedSandbox); ok {
		root, err := rooted.Root(ctx, id)
		if err != nil {
			return nil, err
		}
		ws.root = root
	}
	return ws, nil
}

// authorizeCreate records the policy decision for sandbox creation; the
// decision precedes the effect so a denial never provisions anything.
func (e *Engine) authorizeCreate(ctx context.Context, st *runState) (struct{}, error) {
	decision, err := e.deps.Policy.Authorize(ctx, domain.PolicyRequest{
		TenantID:  st.run.TenantID,
		RunID:     st.run.ID,
		Principal: systemPrincipalID,
		Action:    domain.ActionSandboxCreate,
		Resource:  "run-workspace",
	})
	if err != nil {
		return struct{}{}, err
	}
	err = e.emitEvent(ctx, &domain.Event{
		Type:          domain.EventPolicyEvaluated,
		TenantID:      st.run.TenantID,
		AggregateType: "policy_decision",
		AggregateID:   string(decision.ID),
		RunID:         st.run.ID,
		Data: map[string]any{
			"action":  string(domain.ActionSandboxCreate),
			"outcome": string(decision.Outcome),
		},
	})
	return struct{}{}, err
}

// materialize writes a branch tree into a rooted workspace so verification
// commands observe exactly the committed content. Drivers without a root
// (the fake driver) skip materialization; their exec is scripted anyway.
func (e *Engine) materialize(_ context.Context, ws *workspace, files map[string][]byte) error {
	if ws.root == "" {
		return nil
	}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	for _, filePath := range paths {
		if !containedRelPath(filePath) {
			return domain.Invalidf("workspace_path", "file path %q is not contained", filePath)
		}
		target := filepath.Join(ws.root, filepath.FromSlash(filePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("materialize %s: %w", filePath, err)
		}
		if err := os.WriteFile(target, files[filePath], 0o600); err != nil {
			return fmt.Errorf("materialize %s: %w", filePath, err)
		}
	}
	return nil
}

func containedRelPath(filePath string) bool {
	if filePath == "" || strings.HasPrefix(filePath, "/") || strings.Contains(filePath, "..") || strings.Contains(filePath, "\\") {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filePath))
	return cleaned == "." || (!strings.HasPrefix(cleaned, "../") && cleaned != "..")
}

// execWithRetries runs one verification command with the same bounded retry
// discipline as tasks: only transient classifications are retried, and each
// attempt records its own evidence.
func (e *Engine) execWithRetries(ctx context.Context, st *runState, ws *workspace, cmd []string, criterion string) (domain.Evidence, error) {
	for attempt := 1; ; attempt++ {
		evidence, err := e.execVerified(ctx, st, nil, ws, cmd, criterion)
		switch {
		case err == nil:
			return evidence, nil
		case isCancellation(err):
			return evidence, err
		case domain.IsRetryable(err) && attempt < e.cfg.MaxAttempts:
			if sleepErr := e.deps.Sleeper.Sleep(ctx, e.cfg.RetryBackoff<<uint(attempt-1)); sleepErr != nil || ctx.Err() != nil {
				return evidence, &domain.Error{Kind: domain.ErrKindCancelled, Code: "run_cancelled", Message: "cancelled during verification backoff", Cause: ctx.Err()}
			}
		default:
			if err != nil && domain.ErrKindOf(err) != domain.ErrKindCancelled {
				// Terminal execution error: surface as failed evidence.
				evidence.Passed = false
				return evidence, nil
			}
			return evidence, err
		}
	}
}

// execVerified runs one command in a workspace under policy + budget control
// and records its output as evidence with a log artifact.
func (e *Engine) execVerified(ctx context.Context, st *runState, task *domain.Task, ws *workspace, cmd []string, criterion string) (domain.Evidence, error) {
	evidence := domain.Evidence{Criterion: criterion, Command: append([]string(nil), cmd...)}
	if criterion == "" {
		evidence.Criterion = strings.Join(cmd, " ")
	}

	if err := e.authorize(ctx, st, task, domain.ActionSandboxExec, evidence.Criterion); err != nil {
		return evidence, err
	}
	if err := st.chargeExecOp(); err != nil {
		return evidence, err
	}
	result, err := e.deps.Sandbox.Exec(ctx, ws.id, sandbox.ExecRequest{
		Command: cmd,
		Timeout: e.cfg.TaskTimeout,
	})
	if err != nil {
		// Cancellation and timeouts propagate so callers can distinguish
		// "command ran and failed" from "command never completed".
		return evidence, err
	}
	evidence.ExitCode = result.ExitCode
	evidence.Passed = result.ExitCode == 0

	logRef, artifactErr := e.storeLogArtifact(ctx, st, task, evidence.Criterion, result)
	if artifactErr == nil {
		evidence.LogArtifactID = logRef.ID
		st.addArtifactRef(logRef)
	}
	evidence.At = e.deps.Clock.Now().UTC()
	return evidence, nil
}

// ---- small helpers ----

func (e *Engine) lastUserRequest(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID) (string, error) {
	messages, _, err := e.deps.Threads.Messages(ctx, tenantID, threadID, 0, 0)
	if err != nil {
		return "", err
	}
	request := ""
	for _, message := range messages {
		if message.Role == domain.RoleUser {
			request = message.Content
		}
	}
	if request == "" {
		return "", domain.Invalidf("thread_without_request", "thread has no user message to plan from")
	}
	return request, nil
}

func (e *Engine) markTaskFailed(ctx context.Context, task *domain.Task, failure *domain.FailureInfo) error {
	expected := task.Version
	task.Failure = failure
	if err := task.TransitionTo(domain.TaskFailed); err != nil {
		return err
	}
	if err := e.deps.Tasks.Update(ctx, task, expected); err != nil {
		return err
	}
	return e.emitEvent(ctx, evtFromTask(task, domain.EventTaskStatusChanged, map[string]any{
		"to":   string(domain.TaskFailed),
		"code": failure.Code,
	}))
}

// failTaskTerminal records a non-retryable task failure on the task entity.
// Run-level termination is decided afterwards from the set of task states.
func (e *Engine) failTaskTerminal(ctx context.Context, st *runState, task *domain.Task, code string, cause error) error {
	return e.markTaskFailed(ctx, task, &domain.FailureInfo{Code: code, Message: cause.Error(), Transient: false})
}

func (e *Engine) failRunBudgetExhausted(ctx context.Context, st *runState, cause error) error {
	st.findings = append(st.findings, domain.Finding{
		Category:   "budget",
		Severity:   domain.SeverityBlocker,
		Confidence: domain.ConfidenceHigh,
		Location:   "run budget",
		Scenario:   "the run hit its declared task cap; continuing would spend beyond the approved envelope",
	})
	return e.failRun(ctx, st, "budget_exhausted", cause)
}

func (e *Engine) cancelTask(ctx context.Context, st *runState, task *domain.Task) error {
	expected := task.Version
	if err := task.TransitionTo(domain.TaskCancelled); err != nil {
		if domain.ErrKindOf(err) == domain.ErrKindInvalidTransition {
			// Terminal already (done/failed): leave as-is.
			return nil
		}
		return err
	}
	if err := e.deps.Tasks.Update(ctx, task, expected); err != nil {
		return err
	}
	return e.emitEvent(ctx, evtFromTask(task, domain.EventTaskStatusChanged, map[string]any{"to": string(domain.TaskCancelled)}))
}

func branchForTask(task *domain.Task) string {
	return planner.BranchForTask(task.Name)
}

// classifyFailure prefers the error's own stable code so precise failure
// identities survive into task records and reports.
func classifyFailure(err error) string {
	var typed *domain.Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	switch domain.ErrKindOf(err) {
	case domain.ErrKindPolicyDenied:
		return "policy_denied"
	case domain.ErrKindBudgetExhausted:
		return "budget_exhausted"
	case domain.ErrKindConflict:
		return "conflict"
	default:
		return "operation_failed"
	}
}

func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	return domain.ErrKindOf(err) == domain.ErrKindCancelled || errors.Is(err, context.Canceled)
}

func indexIn(tasks []*domain.Task, task *domain.Task) int {
	for i, candidate := range tasks {
		if candidate.ID == task.ID {
			return i
		}
	}
	return len(tasks)
}

func firstFailedTask(st *runState) *domain.Task {
	for _, task := range st.tasks {
		if task.Status == domain.TaskFailed {
			return task
		}
	}
	for _, task := range st.tasks {
		if task.Status == domain.TaskBlocked {
			return task
		}
	}
	for _, task := range st.tasks {
		if task.Status == domain.TaskCancelled {
			return task
		}
	}
	return nil
}

func safeDetail(err error) string {
	message := err.Error()
	const maxLen = 512
	if len(message) > maxLen {
		return message[:maxLen]
	}
	return message
}
