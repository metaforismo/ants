package domain

import "time"

// AuditEvent is an immutable, append-only record of a security-relevant
// action. Metadata is redacted by the emitter; the audit layer never stores
// secrets, prompt bodies, or file contents.
type AuditResult string

const (
	AuditResultAllowed AuditResult = "allowed"
	AuditResultDenied  AuditResult = "denied"
	AuditResultError   AuditResult = "error"
)

type AuditEvent struct {
	ID           AuditEventID   `json:"id"`
	TenantID     TenantID       `json:"tenant_id"`
	Actor        Actor          `json:"actor"`
	Action       PolicyAction   `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Result       AuditResult    `json:"result"`
	TraceID      string         `json:"trace_id"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	At           time.Time      `json:"at"`
}

type PrincipalKind string

const (
	PrincipalHuman   PrincipalKind = "human"
	PrincipalService PrincipalKind = "service_principal"
	PrincipalSystem  PrincipalKind = "system"
)

func (p PrincipalKind) Valid() bool {
	switch p {
	case PrincipalHuman, PrincipalService, PrincipalSystem:
		return true
	default:
		return false
	}
}

// Actor identifies who caused an event. It mirrors the plan's event envelope.
type Actor struct {
	Type PrincipalKind `json:"type"`
	ID   string        `json:"id"`
}

func (a Actor) Validate() error {
	if !a.Type.Valid() {
		return Invalidf("actor_type", "actor type %q is not supported", a.Type)
	}
	if a.ID == "" {
		return Invalidf("actor_id", "actor id must not be empty")
	}
	return nil
}
