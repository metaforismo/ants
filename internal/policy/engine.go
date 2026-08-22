// Package policy enforces the capability boundary between Ants components and
// the host/network/secrets. Decisions are explicit records: every evaluation
// produces an auditable outcome, and unknown actions are denied rather than
// defaulted.
package policy

import (
	"context"
	"fmt"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// Version identifies the compiled-in rule set so audit trails can explain why
// a decision was made even after rules change.
const Version = "policy.v1"

// Engine evaluates capability requests. It is immutable after construction
// and safe for concurrent use.
type Engine struct {
	allowLocalCommits bool

	clock     ports.Clock
	ids       ports.IDGenerator
	decisions ports.PolicyDecisionStore
	audit     ports.AuditStore
}

func NewEngine(allowLocalCommits bool, clock ports.Clock, ids ports.IDGenerator, decisions ports.PolicyDecisionStore, audit ports.AuditStore) *Engine {
	return &Engine{
		allowLocalCommits: allowLocalCommits,
		clock:             clock,
		ids:               ids,
		decisions:         decisions,
		audit:             audit,
	}
}

// Evaluate decides req, persists the decision, and appends an audit event.
// Persistence failures abort the operation: an unevaluated or unrecorded
// boundary crossing must never proceed.
func (e *Engine) Evaluate(ctx context.Context, req domain.PolicyRequest) (*domain.PolicyDecision, error) {
	outcome, reason := e.decide(req)
	idStr, err := e.ids.NewID(domain.PrefixPolicyDecision)
	if err != nil {
		return nil, fmt.Errorf("policy: generate decision id: %w", err)
	}
	decision := &domain.PolicyDecision{
		ID:            domain.PolicyDecisionID(idStr),
		TenantID:      req.TenantID,
		Request:       req,
		Outcome:       outcome,
		Reason:        reason,
		PolicyVersion: Version,
		CreatedAt:     e.clock.Now().UTC(),
	}
	if err := e.decisions.Record(ctx, decision); err != nil {
		return nil, fmt.Errorf("policy: persist decision: %w", err)
	}
	if err := e.appendAudit(ctx, decision); err != nil {
		return nil, fmt.Errorf("policy: persist audit event: %w", err)
	}
	return decision, nil
}

// Authorize is Evaluate plus enforcement: deny and require_approval return a
// typed domain error so callers cannot accidentally ignore the outcome.
func (e *Engine) Authorize(ctx context.Context, req domain.PolicyRequest) (*domain.PolicyDecision, error) {
	decision, err := e.Evaluate(ctx, req)
	if err != nil {
		return nil, err
	}
	switch decision.Outcome {
	case domain.PolicyAllow:
		return decision, nil
	case domain.PolicyDeny:
		return decision, &domain.Error{
			Kind:    domain.ErrKindPolicyDenied,
			Code:    "policy_deny",
			Message: fmt.Sprintf("action %s denied by policy: %s", req.Action, decision.Reason),
			Details: map[string]any{"action": string(req.Action), "decision_id": string(decision.ID)},
		}
	default:
		return decision, &domain.Error{
			Kind:    domain.ErrKindPolicyDenied,
			Code:    "policy_approval_required",
			Message: fmt.Sprintf("action %s requires human approval", req.Action),
			Details: map[string]any{"action": string(req.Action), "decision_id": string(decision.ID)},
		}
	}
}

// decide encodes the v1 rule table. Denials here are structural: no flag can
// enable push, merge-to-protected, network, secret reads, destructive host
// mutation, or global installs from within task execution.
func (e *Engine) decide(req domain.PolicyRequest) (domain.PolicyOutcome, string) {
	if _, err := domain.ParsePrincipalID(string(req.Principal)); err != nil {
		return domain.PolicyDeny, "actor identity is missing or invalid"
	}
	if _, err := domain.ParseTenantID(string(req.TenantID)); err != nil {
		return domain.PolicyDeny, "tenant identity is missing or invalid"
	}
	switch req.Action {
	case domain.ActionSCMLocalCommit:
		if e.allowLocalCommits {
			return domain.PolicyAllow, "local commits inside isolated task workspaces are allowed by configuration"
		}
		return domain.PolicyDeny, "local commits are disabled by configuration"
	case domain.ActionSandboxCreate, domain.ActionSandboxExec:
		return domain.PolicyAllow, "sandbox lifecycle inside the rooted workspace driver is allowed"
	case domain.ActionSCMPush:
		return domain.PolicyDeny, "task execution must never push to remotes; integration produces PRs through the control plane"
	case domain.ActionSCMMergeToProtected:
		return domain.PolicyDeny, "merge stays a human decision by default"
	case domain.ActionNetworkAccess:
		return domain.PolicyDeny, "task execution has no network capability in this release"
	case domain.ActionSecretRead:
		return domain.PolicyDeny, "task execution cannot read secrets; delivery is just-in-time via the control plane only"
	case domain.ActionHostMutation:
		return domain.PolicyDeny, "writes outside the task workspace root are forbidden"
	case domain.ActionGlobalInstall:
		return domain.PolicyDeny, "global package installation is forbidden inside tasks; dependencies come from environment snapshots"
	default:
		return domain.PolicyDeny, fmt.Sprintf("unknown action %q is denied by default", req.Action)
	}
}

func (e *Engine) appendAudit(ctx context.Context, d *domain.PolicyDecision) error {
	result := domain.AuditResultAllowed
	if d.Outcome == domain.PolicyDeny || d.Outcome == domain.PolicyRequireApproval {
		result = domain.AuditResultDenied
	}
	idStr, err := e.ids.NewID(domain.PrefixAuditEvent)
	if err != nil {
		return err
	}
	evt := &domain.AuditEvent{
		ID:           domain.AuditEventID(idStr),
		TenantID:     d.TenantID,
		Action:       d.Request.Action,
		ResourceType: "policy_decision",
		ResourceID:   string(d.ID),
		Result:       result,
		Metadata: map[string]any{
			"outcome":        string(d.Outcome),
			"policy_version": d.PolicyVersion,
			"principal":      string(d.Request.Principal),
		},
		At: d.CreatedAt,
	}
	return e.audit.Append(ctx, evt)
}
