package policy

import (
	"context"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

type seqIDs struct{ n int }

func (s *seqIDs) NewID(prefix string) (string, error) {
	s.n++
	return domain.NewID(prefix)
}

func newTestEngine(t *testing.T, allowCommits bool) (*Engine, *recorder) {
	t.Helper()
	rec := newRecorder()
	return NewEngine(allowCommits, fixedClock{t: time.Now().UTC()}, &seqIDs{}, rec, rec), rec
}

func baseRequest(action domain.PolicyAction) domain.PolicyRequest {
	return domain.PolicyRequest{
		TenantID:  domain.TenantID("ten_testtenant00000000000"),
		RunID:     domain.RunID("run_testrun0000000000000"),
		TaskID:    domain.TaskID("tsk_testtask000000000000"),
		Principal: domain.PrincipalID("prn_testprincipal0000000"),
		Action:    action,
	}
}

func TestLocalCommitAllowedByConfig(t *testing.T) {
	engine, rec := newTestEngine(t, true)
	d, err := engine.Authorize(context.Background(), baseRequest(domain.ActionSCMLocalCommit))
	if err != nil {
		t.Fatalf("local commit should be allowed: %v", err)
	}
	if d.Outcome != domain.PolicyAllow {
		t.Fatalf("unexpected outcome %q", d.Outcome)
	}
	if len(rec.auditEvents()) != 1 {
		t.Fatalf("every decision must be audited")
	}
}

func TestLocalCommitDeniedWhenDisabled(t *testing.T) {
	engine, _ := newTestEngine(t, false)
	_, err := engine.Authorize(context.Background(), baseRequest(domain.ActionSCMLocalCommit))
	if domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
		t.Fatalf("expected policy denial, got %v", err)
	}
}

// Every action in this list must be denied regardless of configuration: these
// are the structural boundaries of the platform.
func TestForbiddenActionsAlwaysDenied(t *testing.T) {
	for _, tc := range []struct {
		action domain.PolicyAction
		reason string
	}{
		{domain.ActionSCMPush, "push"},
		{domain.ActionSCMMergeToProtected, "merge"},
		{domain.ActionNetworkAccess, "network"},
		{domain.ActionSecretRead, "secret"},
		{domain.ActionHostMutation, "host mutation"},
		{domain.ActionGlobalInstall, "global install"},
	} {
		for _, allowFlag := range []bool{true, false} {
			engine, _ := newTestEngine(t, allowFlag)
			decision, err := engine.Authorize(context.Background(), baseRequest(tc.action))
			if domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
				t.Errorf("%s must be denied (allowLocalCommits=%v), got %v", tc.action, allowFlag, err)
			}
			if decision == nil || decision.Outcome != domain.PolicyDeny {
				t.Errorf("%s must record an explicit deny decision", tc.action)
			}
		}
	}
}

func TestSandboxLifecycleAllowed(t *testing.T) {
	engine, _ := newTestEngine(t, true)
	for _, action := range []domain.PolicyAction{domain.ActionSandboxCreate, domain.ActionSandboxExec} {
		if _, err := engine.Authorize(context.Background(), baseRequest(action)); err != nil {
			t.Errorf("%s should be allowed inside the rooted driver: %v", action, err)
		}
	}
}

func TestUnknownActionDenied(t *testing.T) {
	engine, _ := newTestEngine(t, true)
	req := baseRequest("totally.unknown.action")
	decision, err := engine.Authorize(context.Background(), req)
	if domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
		t.Fatalf("unknown actions fail closed, got %v", err)
	}
	if decision.Reason == "" {
		t.Fatalf("denials carry a reason")
	}
}

func TestInvalidIdentityDenied(t *testing.T) {
	engine, _ := newTestEngine(t, true)
	req := baseRequest(domain.ActionSCMLocalCommit)
	req.Principal = "not-an-id"
	if _, err := engine.Authorize(context.Background(), req); domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
		t.Fatalf("missing principal identity must deny, got %v", err)
	}
	req2 := baseRequest(domain.ActionSandboxCreate)
	req2.TenantID = ""
	if _, err := engine.Authorize(context.Background(), req2); domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
		t.Fatalf("missing tenant identity must deny, got %v", err)
	}
}

func TestDecisionsAndAuditPersisted(t *testing.T) {
	engine, rec := newTestEngine(t, true)
	ctx := context.Background()
	_, _ = engine.Authorize(ctx, baseRequest(domain.ActionSCMLocalCommit))
	_, err := engine.Authorize(ctx, baseRequest(domain.ActionNetworkAccess))
	if err == nil {
		t.Fatal("second call should be denied")
	}
	if len(rec.decisionLog) != 2 {
		t.Fatalf("both evaluations recorded, got %d", len(rec.decisionLog))
	}
	if len(rec.auditLog) != 2 {
		t.Fatalf("denials are audited too, got %d", len(rec.auditLog))
	}
	if rec.decisionLog[1].Outcome != domain.PolicyDeny {
		t.Fatalf("network access recorded as %q", rec.decisionLog[1].Outcome)
	}
}
