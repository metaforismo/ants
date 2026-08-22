package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/review"
)

// stageIntegrate composes verified task branches onto a dedicated integration
// branch. The default branch is never touched: merge to protected branches is
// a human decision (ADR-0007), so even local composition works on its own
// branch.
func (e *Engine) stageIntegrate(ctx context.Context, st *runState) error {
	for _, t := range st.tasks {
		if err := e.transitionTask(ctx, t, domain.TaskIntegrating); err != nil {
			return err
		}
	}

	branch := integrationBranchName(st.run.ID)
	if err := e.deps.SCM.CreateBranch(ctx, st.repo, branch, st.project.DefaultBranch); err != nil {
		return e.failRun(ctx, st, "integration_branch", err)
	}

	var conflicts []string
	for _, t := range sortedTasksByName(st.tasks) {
		result, err := e.deps.SCM.Merge(ctx, st.repo, branch, branchForTask(t),
			fmt.Sprintf("integrate %s (%s)", t.Name, t.ID))
		if err != nil {
			return e.failRun(ctx, st, "integration_failed", err)
		}
		conflicts = append(conflicts, result.Conflicts...)
	}
	if len(conflicts) > 0 {
		st.findings = append(st.findings, domain.Finding{
			Category:   "integration-conflict",
			Severity:   domain.SeverityBlocker,
			Confidence: domain.ConfidenceHigh,
			Location:   strings.Join(conflicts, ","),
			Scenario:   "tasks modified the same files differently; composing them would require a product decision no agent made",
		})
		return e.failRun(ctx, st, "integration_conflict",
			domain.Conflictf("integration_conflicts", "conflicting files: %s", strings.Join(conflicts, ", ")))
	}

	head, err := e.deps.SCM.Head(ctx, st.repo, branch)
	if err != nil {
		return e.failRun(ctx, st, "integration_head", err)
	}
	st.integrated = domain.ReportIntegration{Branch: branch, SHA: head}

	diff, err := e.deps.SCM.Diff(ctx, st.repo, st.baseSHA, head)
	if err != nil {
		return e.failRun(ctx, st, "integration_diff", err)
	}
	ref, err := e.storeDiffArtifact(ctx, st, diff)
	if err != nil {
		return err
	}
	st.addArtifactRef(ref)

	for _, t := range st.tasks {
		if err := e.transitionTask(ctx, t, domain.TaskDone); err != nil {
			return err
		}
	}
	return nil
}

// stageVerifyIntegrated executes the capability's integrated verification
// commands against the composed tree; their outputs become the spec's
// success-criteria evidence.
func (e *Engine) stageVerifyIntegrated(ctx context.Context, st *runState) error {
	sbx, err := e.newWorkspace(ctx, st)
	if err != nil {
		return e.failRun(ctx, st, "provision_verification_workspace", err)
	}
	treeFiles, err := e.deps.SCM.Files(ctx, st.repo, st.integrated.Branch)
	if err != nil {
		return e.failRun(ctx, st, "read_integration_tree", err)
	}
	if err := e.materialize(ctx, sbx, treeFiles); err != nil {
		return e.failRun(ctx, st, "materialize_integration_tree", err)
	}

	criteria := st.spec.Spec.SuccessCriteria
	cmds := st.spec.IntegratedVerification
	indexAligned := len(criteria) == len(cmds)

	var failed []string
	for i, cmd := range cmds {
		criterion := strings.Join(cmd, " ")
		if indexAligned {
			criterion = criteria[i]
		}
		ev, err := e.execWithRetries(ctx, st, sbx, cmd, criterion)
		if err != nil {
			return e.failRun(ctx, st, "verification_execution", err)
		}
		st.evidence = append(st.evidence, ev)
		if !ev.Passed {
			failed = append(failed, criterion)
		}
	}
	if len(failed) > 0 {
		return e.failRun(ctx, st, "verification_failed",
			domain.Invalidf("verification_failed", "criteria without passing evidence: %s", strings.Join(failed, "; ")))
	}
	return nil
}

// stageReviewAndReport runs the ready gate and produces the durable report.
func (e *Engine) stageReviewAndReport(ctx context.Context, st *runState) error {
	if err := e.transitionThread(ctx, st.thread, domain.ThreadReviewing); err != nil {
		return err
	}

	diff, err := e.deps.SCM.Diff(ctx, st.repo, st.baseSHA, st.integrated.SHA)
	if err != nil {
		return e.failRun(ctx, st, "review_diff", err)
	}
	verdict, err := e.deps.Reviewer.Review(ctx, review.Input{
		Spec:     st.spec.Spec,
		Diff:     diff,
		Evidence: st.evidence,
	})
	if err != nil {
		return e.failRun(ctx, st, "reviewer_error", err)
	}
	st.findings = append(st.findings, verdict.Findings...)

	reportRef, err := e.storeReportArtifact(ctx, st, st.buildReport(verdict.Passed))
	if err != nil {
		return err
	}
	st.addArtifactRef(reportRef)

	expected := st.run.Version
	st.run.Report = st.buildReport(verdict.Passed)
	if err := st.run.Finish(domain.RunCompleted, e.deps.Clock.Now(), nil); err != nil {
		return err
	}
	if err := e.deps.Runs.Update(ctx, st.run, expected); err != nil {
		return err
	}
	if err := e.emitEvent(ctx, evtFromRun(st.run, domain.EventRunStatusChanged, map[string]any{
		"to":               string(domain.RunCompleted),
		"ready_for_review": verdict.Passed,
	})); err != nil {
		return err
	}

	if verdict.Passed {
		if err := e.transitionThread(ctx, st.thread, domain.ThreadReadyForReview); err != nil {
			return err
		}
		return nil
	}
	// Blocked reviews surface as a failed thread carrying the findings in the
	// report; richer needs-attention routing arrives with the fix loop.
	reviewErr := domain.Invalidf("review_blocked", "%d blocking finding(s)", countBlockers(st.findings))
	return e.failThreadAfterCompletion(ctx, st, "review_blocked", reviewErr)
}

// ---- failure machinery ----

// finishUnsuccessfully terminates the run after task-stage failures.
func (e *Engine) finishUnsuccessfully(ctx context.Context, st *runState) error {
	first := firstFailedTask(st)
	code := "task_failed"
	message := "one or more tasks did not complete"
	if first != nil && first.Failure != nil {
		code = first.Failure.Code
		message = first.Failure.Message
	}
	return e.failRun(ctx, st, code, domain.Invalidf(code, "%s", message))
}

// failRun terminates run and thread with explicit failure info and stores a
// partial report so observers see how far the pipeline got and why.
func (e *Engine) failRun(ctx context.Context, st *runState, code string, cause error) error {
	failure := &domain.FailureInfo{Code: code, Message: cause.Error(), Transient: false}

	expected := st.run.Version
	if err := st.run.Finish(domain.RunFailed, e.deps.Clock.Now(), failure); err != nil {
		return err
	}
	st.run.Report = st.buildReport(false)
	if err := e.deps.Runs.Update(ctx, st.run, expected); err != nil {
		return err
	}
	if err := e.emitEvent(ctx, evtFromRun(st.run, domain.EventRunStatusChanged, map[string]any{
		"to":     string(domain.RunFailed),
		"code":   code,
		"detail": safeDetail(cause),
	})); err != nil {
		return err
	}

	switch st.thread.Status {
	case domain.ThreadPlanning, domain.ThreadExecuting, domain.ThreadReviewing:
		if err := e.transitionThreadWithData(ctx, st.thread, domain.ThreadFailed, map[string]any{"reason": code}); err != nil {
			if domain.ErrKindOf(err) == domain.ErrKindInvalidTransition {
				break
			}
			return err
		}
	}

	if ref, err := e.storeReportArtifact(ctx, st, st.run.Report); err == nil {
		st.addArtifactRef(ref)
	}
	return &domain.Error{
		Kind:    domain.ErrKindOf(cause),
		Code:    "run_" + code,
		Message: fmt.Sprintf("run failed at %s: %v", code, cause),
		Cause:   cause,
	}
}

func (e *Engine) failThreadAfterCompletion(ctx context.Context, st *runState, code string, cause error) error {
	if st.thread.Status == domain.ThreadReviewing {
		if err := e.transitionThreadWithData(ctx, st.thread, domain.ThreadFailed, map[string]any{"reason": code}); err != nil {
			return err
		}
	}
	return &domain.Error{
		Kind:    domain.ErrKindInvalid,
		Code:    "run_" + code,
		Message: cause.Error(),
		Cause:   cause,
	}
}

// checkCancelled converts parent-context cancellation into explicit run and
// task states before unwinding.
func (e *Engine) checkCancelled(ctx context.Context, st *runState) error {
	if ctx.Err() == nil {
		return nil
	}
	expected := st.run.Version
	if err := st.run.Finish(domain.RunCancelled, e.deps.Clock.Now(), nil); err == nil {
		if err := e.deps.Runs.Update(ctx, st.run, expected); err != nil {
			return err
		}
		_ = e.emitEvent(ctx, evtFromRun(st.run, domain.EventRunStatusChanged, map[string]any{"to": string(domain.RunCancelled)}))
	}
	switch st.thread.Status {
	case domain.ThreadExecuting, domain.ThreadReadyToExecute:
		_ = e.transitionThreadWithData(ctx, st.thread, domain.ThreadNeedsAttention, map[string]any{"reason": "run_cancelled"})
	case domain.ThreadPlanning:
		// Cancelled mid-planning: back to the human, who decides whether to
		// retry or archive.
		_ = e.transitionThreadWithData(ctx, st.thread, domain.ThreadAwaitingInput, map[string]any{"reason": "run_cancelled"})
	}
	return &domain.Error{
		Kind:    domain.ErrKindCancelled,
		Code:    "run_cancelled",
		Message: "run cancelled",
		Cause:   ctx.Err(),
	}
}

// ---- report construction ----

func (st *runState) buildReport(ready bool) *domain.RunReport {
	reportTasks := make([]domain.ReportTask, 0, len(st.tasks))
	for _, t := range st.tasks {
		rt := domain.ReportTask{
			ID:       t.ID,
			Name:     t.Name,
			Branch:   branchForTask(t),
			Status:   t.Status,
			Attempts: t.Attempts,
			Failure:  t.Failure,
		}
		if res := st.resultFor(t); res != nil {
			rt.CommitSHA = res.headSHA
		}
		reportTasks = append(reportTasks, rt)
	}
	verificationPassed := len(st.evidence) > 0
	for _, ev := range st.evidence {
		if !ev.Passed {
			verificationPassed = false
		}
	}
	return &domain.RunReport{
		RunID:       st.run.ID,
		ThreadID:    st.run.ThreadID,
		SpecID:      st.run.SpecID,
		Spec:        st.specContent(),
		Tasks:       reportTasks,
		Integration: st.integrated,
		Verification: domain.ReportVerification{
			Passed:   verificationPassed,
			Evidence: st.evidence,
		},
		Findings:  st.findings,
		Artifacts: st.artifactRefs,
		Budget: domain.BudgetSummary{
			TasksUsed:   st.ledger.TasksUsed,
			MaxTasks:    st.ledger.Limits.MaxTasks,
			ExecOpsUsed: st.ledger.ExecOpsUsed,
			MaxExecOps:  st.ledger.Limits.MaxExecOps,
		},
		ReadyForReview: ready,
		Summary:        summarize(st, ready),
		StartedAt:      st.startedAt,
		FinishedAt:     st.finishedAtOrNow(),
	}
}

func (st *runState) specContent() domain.SpecContent {
	if st.spec == nil {
		return domain.SpecContent{}
	}
	return domain.SpecContent{
		Outcome:         st.spec.Spec.Outcome,
		Assumptions:     st.spec.Spec.Assumptions,
		Requirements:    st.spec.Spec.Requirements,
		NonGoals:        st.spec.Spec.NonGoals,
		SuccessCriteria: st.spec.Spec.SuccessCriteria,
		Blockers:        st.spec.Spec.Blockers,
	}
}

func (st *runState) resultFor(t *domain.Task) *taskResult {
	for _, r := range st.results {
		if r.task != nil && r.task.ID == t.ID {
			return r
		}
	}
	return nil
}

func (st *runState) finishedAtOrNow() time.Time {
	if st.run.FinishedAt != nil {
		return *st.run.FinishedAt
	}
	return st.startedAt
}

func summarize(st *runState, ready bool) string {
	done := 0
	for _, t := range st.tasks {
		if t.Status == domain.TaskDone {
			done++
		}
	}
	base := fmt.Sprintf("%d/%d tasks integrated; %d evidence records; %d finding(s)",
		done, len(st.tasks), len(st.evidence), len(st.findings))
	if ready {
		return base + ": ready for human review"
	}
	return base + ": not ready"
}

func countBlockers(findings []domain.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Blocking() {
			n++
		}
	}
	return n
}

func jsonBytes(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":"report serialization failed: %v"}`, err))
	}
	return b
}

func sortedTasksByName(tasks []*domain.Task) []*domain.Task {
	out := append([]*domain.Task(nil), tasks...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func integrationBranchName(runID domain.RunID) string {
	suffix := strings.TrimPrefix(string(runID), domain.PrefixRun+"_")
	return "ants/integration-" + sanitizeBranch(suffix)
}

func sanitizeBranch(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "run"
	}
	return out
}
