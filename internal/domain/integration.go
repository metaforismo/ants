package domain

import "time"

// IntegrationProvider enumerates SCM/collaboration providers an adapter can
// exist for. Wave A is GitHub + generic webhook; the enum is closed so a
// typo'd provider string fails validation at the boundary.
type IntegrationProvider string

const (
	IntegrationGitHub  IntegrationProvider = "github"
	IntegrationGitLab  IntegrationProvider = "gitlab"
	IntegrationGeneric IntegrationProvider = "generic"
)

func (p IntegrationProvider) Valid() bool {
	switch p {
	case IntegrationGitHub, IntegrationGitLab, IntegrationGeneric:
		return true
	default:
		return false
	}
}

// ConnectionStatus is the lifecycle of one tenant integration connection.
type ConnectionStatus string

const (
	ConnectionPending   ConnectionStatus = "pending"
	ConnectionConnected ConnectionStatus = "connected"
	ConnectionErrored   ConnectionStatus = "errored"
	ConnectionRevoked   ConnectionStatus = "revoked"
)

var AllConnectionStatuses = []ConnectionStatus{
	ConnectionPending,
	ConnectionConnected,
	ConnectionErrored,
	ConnectionRevoked,
}

var connectionTransitions = transitionTable[ConnectionStatus]{
	ConnectionPending:   {ConnectionConnected, ConnectionErrored},
	ConnectionConnected: {ConnectionErrored, ConnectionRevoked},
	ConnectionErrored:   {ConnectionConnected, ConnectionRevoked},
	ConnectionRevoked:   {},
}

func init() {
	if err := checkTransitionTable(AllConnectionStatuses, connectionTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionConnection(from, to ConnectionStatus) bool {
	return connectionTransitions.allows(from, to)
}

// SecretRef points at a secret in the configured secret store. The reference
// itself is safe to persist and log; the material must never be.
type IntegrationConnection struct {
	ID        IntegrationID       `json:"id"`
	TenantID  TenantID            `json:"tenant_id"`
	Provider  IntegrationProvider `json:"provider"`
	Status    ConnectionStatus    `json:"status"`
	Scopes    []string            `json:"scopes"`
	SecretRef string              `json:"secret_ref"`
	Version   int64               `json:"version"`
	CreatedAt time.Time           `json:"created_at"`
}

func NewIntegration(id IntegrationID, tenantID TenantID, provider IntegrationProvider, scopes []string, secretRef string, now time.Time) (*IntegrationConnection, error) {
	if _, err := ParseIntegrationID(string(id)); err != nil {
		return nil, err
	}
	if !provider.Valid() {
		return nil, Invalidf("integration_provider", "provider %q is not supported", provider)
	}
	if secretRef == "" {
		return nil, Invalidf("integration_secret_ref", "secret ref must not be empty")
	}
	now = now.UTC()
	return &IntegrationConnection{
		ID:        id,
		TenantID:  tenantID,
		Provider:  provider,
		Status:    ConnectionPending,
		Scopes:    append([]string(nil), scopes...),
		SecretRef: secretRef,
		Version:   1,
		CreatedAt: now,
	}, nil
}

func (c *IntegrationConnection) TransitionTo(next ConnectionStatus) error {
	if c.Status == next {
		return NewInvalidTransitionError(c.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionConnection(c.Status, next) {
		return NewInvalidTransitionError(c.Status, next)
	}
	c.Status = next
	c.Version++
	return nil
}
