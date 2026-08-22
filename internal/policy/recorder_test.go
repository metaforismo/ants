package policy

import (
	"context"
	"sync"

	"github.com/metaforismo/ants/internal/domain"
)

// recorder is a minimal in-memory implementation of the two persistence
// ports the engine depends on, for asserting that decisions and audit events
// are durably recorded.
type recorder struct {
	mu          sync.Mutex
	decisionLog []*domain.PolicyDecision
	auditLog    []*domain.AuditEvent
}

func newRecorder() *recorder { return &recorder{} }

func (r *recorder) Record(_ context.Context, d *domain.PolicyDecision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisionLog = append(r.decisionLog, d)
	return nil
}

func (r *recorder) ListByRun(_ context.Context, _ domain.TenantID, _ domain.RunID) ([]*domain.PolicyDecision, error) {
	return nil, nil
}

func (r *recorder) Append(_ context.Context, e *domain.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auditLog = append(r.auditLog, e)
	return nil
}

func (r *recorder) ListByTenant(_ context.Context, _ domain.TenantID, _ string, _ int) ([]*domain.AuditEvent, error) {
	return nil, nil
}

func (r *recorder) auditEvents() []*domain.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*domain.AuditEvent(nil), r.auditLog...)
}
