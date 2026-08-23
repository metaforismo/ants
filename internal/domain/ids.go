package domain

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// ID prefixes are part of the public API contract: they make entity types
// recognizable in logs, events, and support conversations without type context.
const (
	PrefixTenant         = "ten"
	PrefixProject        = "prj"
	PrefixThread         = "thr"
	PrefixMessage        = "msg"
	PrefixSpec           = "spc"
	PrefixTask           = "tsk"
	PrefixRun            = "run"
	PrefixWorkspace      = "wsp"
	PrefixArtifact       = "art"
	PrefixPolicyDecision = "pol"
	PrefixBudget         = "bgt"
	PrefixIntegration    = "icn"
	PrefixAuditEvent     = "aud"
	PrefixPrincipal      = "prn"
	PrefixSandbox        = "sbx"
	PrefixEvent          = "evt"
	// PrefixRequest names HTTP request correlation identifiers (ADR-0017):
	// the request-log counterpart of the trace_id slot events carry, sharing
	// the same prefixed shape so identifiers are recognizable across logs,
	// events, and operator tooling.
	PrefixRequest    = "req"
	idSeparator      = "_"
	minIDRandomChars = 20
	maxIDSuffixLen   = 64
)

// Typed identifiers. Each wraps a validated "<prefix>_<suffix>" string so
// cross-entity mixups fail to compile instead of failing at runtime.
type (
	TenantID         string
	ProjectID        string
	ThreadID         string
	MessageID        string
	SpecID           string
	TaskID           string
	RunID            string
	WorkspaceID      string
	ArtifactID       string
	PolicyDecisionID string
	BudgetID         string
	IntegrationID    string
	AuditEventID     string
	PrincipalID      string
	SandboxID        string
	EventID          string
)

func newPrefixed(prefix, suffix string) string {
	return prefix + idSeparator + suffix
}

func validatePrefixed(kindName, prefix, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s id is empty", kindName)
	}
	gotPrefix, suffix, ok := strings.Cut(value, idSeparator)
	if !ok || gotPrefix != prefix {
		return "", fmt.Errorf("%s id %q must have prefix %q", kindName, value, prefix)
	}
	if len(suffix) < minIDRandomChars || len(suffix) > maxIDSuffixLen {
		return "", fmt.Errorf("%s id %q suffix length must be between %d and %d", kindName, value, minIDRandomChars, maxIDSuffixLen)
	}
	for _, r := range suffix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return "", fmt.Errorf("%s id %q contains invalid character %q", kindName, value, r)
		}
	}
	return value, nil
}

// NewID returns a random identifier for the given prefix using the
// crypto/rand source. Suffixes are lowercase-insensitive alphanumeric.
func NewID(prefix string) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, 26)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id entropy: %w", err)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return newPrefixed(prefix, string(out)), nil
}

func NewTenantID() (TenantID, error)   { v, err := NewID(PrefixTenant); return TenantID(v), err }
func NewProjectID() (ProjectID, error) { v, err := NewID(PrefixProject); return ProjectID(v), err }
func NewThreadID() (ThreadID, error)   { v, err := NewID(PrefixThread); return ThreadID(v), err }
func NewMessageID() (MessageID, error) { v, err := NewID(PrefixMessage); return MessageID(v), err }
func NewSpecID() (SpecID, error)       { v, err := NewID(PrefixSpec); return SpecID(v), err }
func NewTaskID() (TaskID, error)       { v, err := NewID(PrefixTask); return TaskID(v), err }
func NewRunID() (RunID, error)         { v, err := NewID(PrefixRun); return RunID(v), err }
func NewWorkspaceID() (WorkspaceID, error) {
	v, err := NewID(PrefixWorkspace)
	return WorkspaceID(v), err
}
func NewArtifactID() (ArtifactID, error) { v, err := NewID(PrefixArtifact); return ArtifactID(v), err }
func NewPolicyDecisionID() (PolicyDecisionID, error) {
	v, err := NewID(PrefixPolicyDecision)
	return PolicyDecisionID(v), err
}
func NewBudgetID() (BudgetID, error) { v, err := NewID(PrefixBudget); return BudgetID(v), err }
func NewIntegrationID() (IntegrationID, error) {
	v, err := NewID(PrefixIntegration)
	return IntegrationID(v), err
}
func NewAuditEventID() (AuditEventID, error) {
	v, err := NewID(PrefixAuditEvent)
	return AuditEventID(v), err
}
func NewPrincipalID() (PrincipalID, error) {
	v, err := NewID(PrefixPrincipal)
	return PrincipalID(v), err
}
func NewSandboxID() (SandboxID, error) { v, err := NewID(PrefixSandbox); return SandboxID(v), err }
func NewEventID() (EventID, error)     { v, err := NewID(PrefixEvent); return EventID(v), err }

func ParseTenantID(v string) (TenantID, error) {
	parsed, err := validatePrefixed("tenant", PrefixTenant, v)
	return TenantID(parsed), err
}
func ParseProjectID(v string) (ProjectID, error) {
	parsed, err := validatePrefixed("project", PrefixProject, v)
	return ProjectID(parsed), err
}
func ParseThreadID(v string) (ThreadID, error) {
	parsed, err := validatePrefixed("thread", PrefixThread, v)
	return ThreadID(parsed), err
}
func ParseMessageID(v string) (MessageID, error) {
	parsed, err := validatePrefixed("message", PrefixMessage, v)
	return MessageID(parsed), err
}
func ParseSpecID(v string) (SpecID, error) {
	parsed, err := validatePrefixed("spec", PrefixSpec, v)
	return SpecID(parsed), err
}
func ParseTaskID(v string) (TaskID, error) {
	parsed, err := validatePrefixed("task", PrefixTask, v)
	return TaskID(parsed), err
}
func ParseRunID(v string) (RunID, error) {
	parsed, err := validatePrefixed("run", PrefixRun, v)
	return RunID(parsed), err
}
func ParseWorkspaceID(v string) (WorkspaceID, error) {
	parsed, err := validatePrefixed("workspace", PrefixWorkspace, v)
	return WorkspaceID(parsed), err
}
func ParseArtifactID(v string) (ArtifactID, error) {
	parsed, err := validatePrefixed("artifact", PrefixArtifact, v)
	return ArtifactID(parsed), err
}
func ParsePolicyDecisionID(v string) (PolicyDecisionID, error) {
	parsed, err := validatePrefixed("policy decision", PrefixPolicyDecision, v)
	return PolicyDecisionID(parsed), err
}
func ParseBudgetID(v string) (BudgetID, error) {
	parsed, err := validatePrefixed("budget", PrefixBudget, v)
	return BudgetID(parsed), err
}
func ParseIntegrationID(v string) (IntegrationID, error) {
	parsed, err := validatePrefixed("integration connection", PrefixIntegration, v)
	return IntegrationID(parsed), err
}
func ParseAuditEventID(v string) (AuditEventID, error) {
	parsed, err := validatePrefixed("audit event", PrefixAuditEvent, v)
	return AuditEventID(parsed), err
}
func ParsePrincipalID(v string) (PrincipalID, error) {
	parsed, err := validatePrefixed("principal", PrefixPrincipal, v)
	return PrincipalID(parsed), err
}
func ParseSandboxID(v string) (SandboxID, error) {
	parsed, err := validatePrefixed("sandbox", PrefixSandbox, v)
	return SandboxID(parsed), err
}
func ParseEventID(v string) (EventID, error) {
	parsed, err := validatePrefixed("event", PrefixEvent, v)
	return EventID(parsed), err
}
