package domain

import "time"

// PolicyOutcome is the closed set of policy decisions at the tool/capability
// boundary. There is no implicit allow: callers must handle all three values.
type PolicyOutcome string

const (
	PolicyAllow           PolicyOutcome = "allow"
	PolicyDeny            PolicyOutcome = "deny"
	PolicyRequireApproval PolicyOutcome = "require_approval"
)

func (o PolicyOutcome) Valid() bool {
	switch o {
	case PolicyAllow, PolicyDeny, PolicyRequireApproval:
		return true
	default:
		return false
	}
}

// PolicyAction enumerates every capability the platform can gate. Adding an
// action without adding it to the policy table is a compile-adjacent failure:
// the engine rejects unknown actions instead of defaulting.
type PolicyAction string

const (
	ActionSandboxCreate       PolicyAction = "sandbox.create"
	ActionSandboxExec         PolicyAction = "sandbox.exec"
	ActionSCMLocalCommit      PolicyAction = "scm.local_commit"
	ActionSCMPush             PolicyAction = "scm.push"
	ActionSCMMergeToProtected PolicyAction = "scm.merge_to_protected_branch"
	ActionNetworkAccess       PolicyAction = "network.access"
	ActionSecretRead          PolicyAction = "secret.read"
	ActionHostMutation        PolicyAction = "host.mutate_outside_workspace"
	ActionGlobalInstall       PolicyAction = "package.install_global"
	ActionOutboxRequeue       PolicyAction = "operator.outbox.requeue_dead_letter"
	ActionOutboxDiscard       PolicyAction = "operator.outbox.discard_dead_letter"
)

var AllPolicyActions = []PolicyAction{
	ActionSandboxCreate,
	ActionSandboxExec,
	ActionSCMLocalCommit,
	ActionSCMPush,
	ActionSCMMergeToProtected,
	ActionNetworkAccess,
	ActionSecretRead,
	ActionHostMutation,
	ActionGlobalInstall,
	ActionOutboxRequeue,
	ActionOutboxDiscard,
}

// PolicyRequest is the normalized input to the decision function.
type PolicyRequest struct {
	TenantID  TenantID     `json:"tenant_id"`
	RunID     RunID        `json:"run_id,omitempty"`
	TaskID    TaskID       `json:"task_id,omitempty"`
	Principal PrincipalID  `json:"principal"`
	Action    PolicyAction `json:"action"`
	Resource  string       `json:"resource,omitempty"`
}

// PolicyDecision is an immutable record of one boundary evaluation.
type PolicyDecision struct {
	ID            PolicyDecisionID `json:"id"`
	TenantID      TenantID         `json:"tenant_id"`
	Request       PolicyRequest    `json:"request"`
	Outcome       PolicyOutcome    `json:"outcome"`
	Reason        string           `json:"reason"`
	PolicyVersion string           `json:"policy_version"`
	CreatedAt     time.Time        `json:"created_at"`
}
