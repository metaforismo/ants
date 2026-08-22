package memory

import (
	"context"
	"sort"

	"github.com/metaforismo/ants/internal/domain"
)

type AuditRepository struct{ st *storeState }

func (r *AuditRepository) Append(_ context.Context, event *domain.AuditEvent) error {
	unlock := lockWrite(r.st)
	defer unlock()
	r.st.auditLog = append(r.st.auditLog, cloneAuditEvent(event))
	return nil
}

// ListByTenant returns audit events in append order; after is the last seen
// event ID for cursor pagination.
func (r *AuditRepository) ListByTenant(_ context.Context, tenantID domain.TenantID, after string, limit int) ([]*domain.AuditEvent, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.AuditEvent{}
	start := 0
	if after != "" {
		for i, e := range r.st.auditLog {
			if string(e.ID) == after {
				start = i + 1
				break
			}
		}
	}
	for _, e := range r.st.auditLog[start:] {
		if e.TenantID != tenantID {
			continue
		}
		out = append(out, cloneAuditEvent(e))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

type PolicyDecisionRepository struct{ st *storeState }

func (r *PolicyDecisionRepository) Record(_ context.Context, decision *domain.PolicyDecision) error {
	unlock := lockWrite(r.st)
	defer unlock()
	r.st.policyByRun[decision.Request.RunID] = append(r.st.policyByRun[decision.Request.RunID], clonePolicyDecision(decision))
	return nil
}

func (r *PolicyDecisionRepository) ListByRun(_ context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.PolicyDecision, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.PolicyDecision{}
	for _, d := range r.st.policyByRun[runID] {
		if d.TenantID == tenantID {
			out = append(out, clonePolicyDecision(d))
		}
	}
	return out, nil
}

type IntegrationRepository struct{ st *storeState }

func (r *IntegrationRepository) Create(_ context.Context, conn *domain.IntegrationConnection) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.integrations[conn.ID]; exists {
		return domain.Conflictf("integration_exists", "integration connection %s already exists", conn.ID)
	}
	r.st.integrations[conn.ID] = cloneIntegration(conn)
	return nil
}

func (r *IntegrationRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.IntegrationID) (*domain.IntegrationConnection, error) {
	unlock := lockRead(r.st)
	defer unlock()
	c, ok := r.st.integrations[id]
	if !ok || c.TenantID != tenantID {
		return nil, notFound("integration connection", id)
	}
	return cloneIntegration(c), nil
}

func (r *IntegrationRepository) Update(_ context.Context, conn *domain.IntegrationConnection, expectedVersion int64) error {
	unlock := lockWrite(r.st)
	defer unlock()
	cur, ok := r.st.integrations[conn.ID]
	if !ok || cur.TenantID != conn.TenantID {
		return notFound("integration connection", conn.ID)
	}
	if cur.Version != expectedVersion {
		return domain.NewStaleVersionError("integration connection", conn.ID, expectedVersion, cur.Version)
	}
	stored := cloneIntegration(conn)
	stored.Version = cur.Version + 1
	r.st.integrations[conn.ID] = stored
	conn.Version = stored.Version
	return nil
}

func (r *IntegrationRepository) ListByTenant(_ context.Context, tenantID domain.TenantID) ([]*domain.IntegrationConnection, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.IntegrationConnection{}
	for _, c := range r.st.integrations {
		if c.TenantID == tenantID {
			out = append(out, cloneIntegration(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
