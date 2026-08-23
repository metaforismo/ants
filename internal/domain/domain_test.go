package domain

import (
	"errors"
	"testing"
	"time"
)

// tid builds a deterministic, structurally valid identifier for tests.
func tid(prefix, seed string) string {
	base := prefix + "_" + seed
	for len(base)-len(prefix)-1 < 20 {
		base += "0"
	}
	return base
}

func TestTransitionTablesAreInternallyConsistent(t *testing.T) {
	for _, tt := range []struct {
		name  string
		check func() error
	}{
		{"thread", func() error { return checkTransitionTable(AllThreadStatuses, threadTransitions) }},
		{"task", func() error { return checkTransitionTable(AllTaskStatuses, taskTransitions) }},
		{"run", func() error { return checkTransitionTable(AllRunStatuses, runTransitions) }},
		{"spec", func() error { return checkTransitionTable(AllSpecStatuses, specTransitions) }},
		{"workspace", func() error { return checkTransitionTable(AllWorkspaceStatuses, workspaceTransitions) }},
		{"connection", func() error { return checkTransitionTable(AllConnectionStatuses, connectionTransitions) }},
		{"run_claim", func() error { return checkTransitionTable(AllRunClaimStatuses, runClaimTransitions) }},
		{"outbox_delivery", func() error { return checkTransitionTable(AllOutboxDeliveryStatuses, outboxDeliveryTransitions) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.check(); err != nil {
				t.Fatalf("transition table inconsistent: %v", err)
			}
		})
	}
}

func TestOutboxDeliveryStateMachine(t *testing.T) {
	happy := []struct{ from, to OutboxDeliveryStatus }{
		{OutboxPending, OutboxLeased},
		{OutboxLeased, OutboxDelivered},
		{OutboxLeased, OutboxPending},
		{OutboxLeased, OutboxDead},
		{OutboxDead, OutboxPending},   // operator requeue
		{OutboxDead, OutboxDiscarded}, // operator discard
	}
	for _, c := range happy {
		if !CanTransitionOutboxDelivery(c.from, c.to) {
			t.Errorf("expected %s -> %s to be allowed", c.from, c.to)
		}
	}

	illegal := []struct{ from, to OutboxDeliveryStatus }{
		{OutboxPending, OutboxDelivered},
		{OutboxPending, OutboxDead},
		{OutboxDelivered, OutboxPending},
		{OutboxDelivered, OutboxLeased},
		{OutboxDiscarded, OutboxPending},
		{OutboxDiscarded, OutboxDead},
		{OutboxDead, OutboxDelivered},
		{OutboxDead, OutboxLeased},
	}
	for _, c := range illegal {
		if CanTransitionOutboxDelivery(c.from, c.to) {
			t.Errorf("expected %s -> %s to be rejected", c.from, c.to)
		}
	}
}

func TestThreadStateMachine(t *testing.T) {
	happy := []struct{ from, to ThreadStatus }{
		{ThreadIdle, ThreadPlanning},
		{ThreadPlanning, ThreadAwaitingInput},
		{ThreadAwaitingInput, ThreadReadyToExecute},
		{ThreadReadyToExecute, ThreadExecuting},
		{ThreadExecuting, ThreadReviewing},
		{ThreadReviewing, ThreadFixing},
		{ThreadFixing, ThreadReviewing},
		{ThreadReviewing, ThreadReadyForReview},
		{ThreadReadyForReview, ThreadMerged},
	}
	for _, c := range happy {
		if !CanTransitionThread(c.from, c.to) {
			t.Errorf("expected %s -> %s to be allowed", c.from, c.to)
		}
	}

	illegal := []struct{ from, to ThreadStatus }{
		{ThreadIdle, ThreadExecuting},
		{ThreadIdle, ThreadMerged},
		{ThreadPlanning, ThreadExecuting},
		{ThreadReadyForReview, ThreadPlanning},
		{ThreadArchived, ThreadIdle},
		{ThreadMerged, ThreadIdle},
		{ThreadFailed, ThreadExecuting},
	}
	for _, c := range illegal {
		if CanTransitionThread(c.from, c.to) {
			t.Errorf("expected %s -> %s to be rejected", c.from, c.to)
		}
	}

	now := time.Now()
	thread, err := NewThread(ThreadID(tid("thr", "testthread")), TenantID(tid("ten", "testtenant")), ProjectID(tid("prj", "testproject")), "demo", PrincipalID(tid("prn", "testprincipal")), now)
	if err != nil {
		t.Fatalf("construct thread: %v", err)
	}
	versionBefore := thread.Version
	err = thread.TransitionTo(ThreadExecuting)
	if ErrKindOf(err) != ErrKindInvalidTransition {
		t.Fatalf("expected invalid_transition error, got %v", err)
	}
	if thread.Version != versionBefore {
		t.Errorf("failed transition must not bump version")
	}
	if err := thread.TransitionTo(ThreadPlanning); err != nil {
		t.Fatalf("legal transition failed: %v", err)
	}
	if thread.Status != ThreadPlanning || thread.Version != versionBefore+1 {
		t.Fatalf("transition not applied: %+v", thread)
	}
	if err := thread.TransitionTo(ThreadPlanning); ErrKindOf(err) != ErrKindInvalidTransition {
		t.Fatalf("self-transition must be rejected, got %v", err)
	}
}

func TestEveryThreadStateCanArchive(t *testing.T) {
	for _, s := range AllThreadStatuses {
		if s == ThreadArchived {
			continue
		}
		if !CanTransitionThread(s, ThreadArchived) {
			t.Errorf("state %s must be able to archive", s)
		}
	}
}

func TestTaskRetryCycle(t *testing.T) {
	if !CanTransitionTask(TaskQueued, TaskProvisioning) ||
		!CanTransitionTask(TaskProvisioning, TaskWorking) ||
		!CanTransitionTask(TaskWorking, TaskVerifying) ||
		!CanTransitionTask(TaskVerifying, TaskIntegrating) ||
		!CanTransitionTask(TaskIntegrating, TaskDone) {
		t.Fatalf("happy path must be connected")
	}
	if !CanTransitionTask(TaskWorking, TaskFailed) {
		t.Fatalf("working must be able to fail")
	}
	if !CanTransitionTask(TaskFailed, TaskQueued) {
		t.Fatalf("failed tasks must be retryable via queued")
	}
	if CanTransitionTask(TaskDone, TaskQueued) || CanTransitionTask(TaskCancelled, TaskQueued) {
		t.Fatalf("terminal task states must not restart")
	}
	if CanTransitionTask(TaskDraft, TaskWorking) {
		t.Fatalf("draft must not skip provisioning")
	}
}

func TestBeginAttemptEnforcesMaxAttempts(t *testing.T) {
	now := time.Now()
	task, err := NewTask(TaskID(tid("tsk", "testtask")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ThreadID(tid("thr", "testthread")), "t", TaskKindCodeChange, 0, nil, 2, now)
	if err != nil {
		t.Fatalf("construct task: %v", err)
	}
	if err := task.BeginAttempt(); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if err := task.BeginAttempt(); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	err = task.BeginAttempt()
	if ErrKindOf(err) != ErrKindBudgetExhausted {
		t.Fatalf("expected budget_exhausted at max attempts, got %v", err)
	}
}

func TestBudgetLedgerEnforcesCaps(t *testing.T) {
	now := time.Now()
	budget, err := NewBudget(BudgetID(tid("bgt", "testbudget")), TenantID(tid("ten", "testtenant")), "", BudgetScopeRun, BudgetLimits{MaxTasks: 2, MaxExecOps: 3}, now)
	if err != nil {
		t.Fatalf("construct budget: %v", err)
	}
	l := NewLedger(budget)
	for range 2 {
		if err := l.ReserveTask(); err != nil {
			t.Fatalf("reserve task %d: %v", l.TasksUsed+1, err)
		}
	}
	if ErrKindOf(l.ReserveTask()) != ErrKindBudgetExhausted {
		t.Fatalf("task cap not enforced")
	}
	for range 3 {
		if err := l.RecordExecOp(); err != nil {
			t.Fatalf("exec op %d: %v", l.ExecOpsUsed+1, err)
		}
	}
	if ErrKindOf(l.RecordExecOp()) != ErrKindBudgetExhausted {
		t.Fatalf("exec cap not enforced")
	}

	unlimited, _ := NewBudget(BudgetID(tid("bgt", "testbudget2")), TenantID(tid("ten", "testtenant")), "", BudgetScopeRun, BudgetLimits{Unlimited: true}, now)
	ul := NewLedger(unlimited)
	for range 100 {
		if err := ul.ReserveTask(); err != nil {
			t.Fatalf("unlimited ledger refused: %v", err)
		}
	}
}

func TestUnlimitedBudgetRejectsMixedDeclaration(t *testing.T) {
	err := BudgetLimits{Unlimited: true, MaxTasks: 5}.Validate()
	if ErrKindOf(err) != ErrKindInvalid {
		t.Fatalf("unlimited plus caps must be invalid, got %v", err)
	}
	negative := BudgetLimits{MaxTasks: -1}.Validate()
	if ErrKindOf(negative) != ErrKindInvalid {
		t.Fatalf("negative caps must be invalid, got %v", err)
	}
}

func TestIDValidation(t *testing.T) {
	if _, err := ParseTenantID("ten_abcdefghijklmnopqrst"); err != nil {
		t.Fatalf("valid tenant id rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"ten_",                       // no suffix
		"prj_abcdefghijklmnopqrst",   // wrong prefix
		"ten_short",                  // suffix too short
		"ten_has!special_chars12345", // invalid characters
		"ten-abcdefghijklmnopqrst",   // missing separator
	} {
		if _, err := ParseTenantID(bad); err == nil {
			t.Errorf("expected rejection of %q", bad)
		}
	}
	generated, err := NewTenantID()
	if err != nil {
		t.Fatalf("generate tenant id: %v", err)
	}
	if _, err := ParseTenantID(string(generated)); err != nil {
		t.Fatalf("generated id must validate: %v", err)
	}
}

// TestIDParseFailuresAreTypedInvalid pins the trust-boundary contract every
// /v1 handler leans on: a malformed identifier parses into a taxonomy-typed
// invalid error with the stable invalid_id code — never a plain error that
// would wrap into an internal 500 (asDomainError only passes *Error through).
func TestIDParseFailuresAreTypedInvalid(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string) error
	}{
		{"tenant", func(v string) error { _, err := ParseTenantID(v); return err }},
		{"project", func(v string) error { _, err := ParseProjectID(v); return err }},
		{"thread", func(v string) error { _, err := ParseThreadID(v); return err }},
		{"message", func(v string) error { _, err := ParseMessageID(v); return err }},
		{"spec", func(v string) error { _, err := ParseSpecID(v); return err }},
		{"task", func(v string) error { _, err := ParseTaskID(v); return err }},
		{"run", func(v string) error { _, err := ParseRunID(v); return err }},
		{"workspace", func(v string) error { _, err := ParseWorkspaceID(v); return err }},
		{"artifact", func(v string) error { _, err := ParseArtifactID(v); return err }},
		{"policy decision", func(v string) error { _, err := ParsePolicyDecisionID(v); return err }},
		{"budget", func(v string) error { _, err := ParseBudgetID(v); return err }},
		{"integration", func(v string) error { _, err := ParseIntegrationID(v); return err }},
		{"audit event", func(v string) error { _, err := ParseAuditEventID(v); return err }},
		{"principal", func(v string) error { _, err := ParsePrincipalID(v); return err }},
		{"sandbox", func(v string) error { _, err := ParseSandboxID(v); return err }},
		{"event", func(v string) error { _, err := ParseEventID(v); return err }},
	}
	bad := map[string]string{
		"empty":          "",
		"wrong prefix":   "zzz_abcdefghijklmnopqrst",
		"suffix short":   "thr_short",
		"bad char":       "thr_abcdefghijklmnopqrs!",
		"no separator":   "thr-abcdefghijklmnopqrst",
		"path traversal": "../etc/passwd",
		"url encoded":    "thr_%41bcdefghijklmnopqrst",
	}
	for _, tc := range cases {
		for badName, value := range bad {
			err := tc.parse(value)
			if err == nil {
				t.Errorf("%s: %s %q must be rejected", tc.name, badName, value)
				continue
			}
			var dom *Error
			if !errors.As(err, &dom) {
				t.Errorf("%s: %s must fail typed, got %T (%v)", tc.name, badName, err, err)
				continue
			}
			if dom.Kind != ErrKindInvalid || dom.Code != CodeInvalidID {
				t.Errorf("%s: %s must be invalid/%s, got %s/%s (%v)", tc.name, badName, CodeInvalidID, dom.Kind, dom.Code, err)
			}
		}
	}
}

func TestSlugValidation(t *testing.T) {
	valid := []string{"acme", "acme-corp", "team42"}
	for _, s := range valid {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("%q should be valid: %v", s, err)
		}
	}
	invalid := []string{"", "-lead", "trail-", "UPPER", "has space", "under_score", string(make([]byte, MaxSlugLen+1))}
	for _, s := range invalid {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestErrorTaxonomyClassification(t *testing.T) {
	wrapped := Wrap(ErrKindTransient, "db_unavailable", "store unavailable", errors.New("boom"))
	if !IsRetryable(wrapped) {
		t.Fatalf("transient errors are retryable")
	}
	// Outermost classification wins: an internal wrapper marks the whole
	// failure non-retryable even when a transient error sits underneath.
	if IsRetryable(Internalf(wrapped, "internal", "wrapped")) {
		t.Fatalf("outer classification must win over wrapped causes")
	}
	if IsRetryable(NotFoundf("run", "run_x")) {
		t.Fatalf("not found is not retryable")
	}
	foreign := errors.New("stdlib failure")
	if ErrKindOf(foreign) != ErrKindInternal {
		t.Fatalf("foreign errors classify as internal")
	}
}

func TestArtifactInvariants(t *testing.T) {
	now := time.Now()
	if _, err := NewArtifact(ArtifactID(tid("art", "testartifact")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ArtifactDiff, RetentionEphemeral, []byte("hello"), now); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	if _, err := NewArtifact(ArtifactID(tid("art", "testartifact")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ArtifactDiff, RetentionEphemeral, nil, now); ErrKindOf(err) != ErrKindInvalid {
		t.Fatalf("empty content must be rejected, got %v", err)
	}
	big := make([]byte, MaxArtifactBytes+1)
	if _, err := NewArtifact(ArtifactID(tid("art", "testartifact")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ArtifactLog, RetentionEphemeral, big, now); ErrKindOf(err) != ErrKindInvalid {
		t.Fatalf("oversized artifact must be rejected, got %v", err)
	}
	content := []byte("deterministic")
	a1, _ := NewArtifact(ArtifactID(tid("art", "testartifact1")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ArtifactDiff, RetentionEphemeral, content, now)
	a2, _ := NewArtifact(ArtifactID(tid("art", "testartifact2")), TenantID(tid("ten", "testtenant")), RunID(tid("run", "testrun")), ArtifactDiff, RetentionEphemeral, content, now)
	if a1.Digest != a2.Digest {
		t.Fatalf("same content must produce same digest")
	}
}

func TestRunFinishRequiresFailureInfoOnFailure(t *testing.T) {
	now := time.Now()
	run, err := NewRun(RunID(tid("run", "testrun")), TenantID(tid("ten", "testtenant")), ThreadID(tid("thr", "testthread")), "idem-key-1", now)
	if err != nil {
		t.Fatalf("construct run: %v", err)
	}
	if err := run.Finish(RunCompleted, now, nil); err == nil {
		t.Fatalf("cannot finish before starting")
	}
	if err := run.TransitionTo(RunPlanning); err != nil {
		t.Fatalf("start planning: %v", err)
	}
	if err := run.Finish(RunFailed, now, nil); ErrKindOf(err) != ErrKindInvalid {
		t.Fatalf("failed finish requires failure info, got %v", err)
	}
	if err := run.Finish(RunCancelled, now, nil); err != nil {
		t.Fatalf("cancel without failure info is fine: %v", err)
	}
	if !run.Status.Terminal() || run.FinishedAt == nil {
		t.Fatalf("cancel must stamp terminal state and time")
	}
}

func TestSpecLifecycleAndValidation(t *testing.T) {
	now := time.Now()
	_, err := NewSpec(SpecID(tid("spc", "testspec")), TenantID(tid("ten", "testtenant")), ThreadID(tid("thr", "testthread")), 1, SpecContent{}, now)
	if ErrKindOf(err) != ErrKindInvalid {
		t.Fatalf("spec without requirements must be invalid, got %v", err)
	}
	spec, err := NewSpec(SpecID(tid("spc", "testspec")), TenantID(tid("ten", "testtenant")), ThreadID(tid("thr", "testthread")), 1, SpecContent{
		Outcome:      "feature",
		Requirements: []string{"r1"},
	}, now)
	if err != nil {
		t.Fatalf("construct spec: %v", err)
	}
	if spec.Status != SpecDraft {
		t.Fatalf("specs start as draft")
	}
	if !CanTransitionSpec(SpecApproved, SpecSuperseded) {
		t.Fatalf("approved specs can supersede")
	}
	if CanTransitionSpec(SpecSuperseded, SpecDraft) {
		t.Fatalf("superseded specs are terminal")
	}
	if err := spec.Approve(); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := spec.Reject(); ErrKindOf(err) != ErrKindInvalidTransition {
		t.Fatalf("approved cannot reject directly, got %v", err)
	}
}
