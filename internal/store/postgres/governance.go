package postgres

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/metaforismo/ants/internal/domain"
)

type AuditRepository struct{ st *Store }

var _ interface {
	Append(context.Context, *domain.AuditEvent) error
	ListByTenant(context.Context, domain.TenantID, string, int) ([]*domain.AuditEvent, error)
} = (*AuditRepository)(nil)

const auditColumns = `id, tenant_id, actor_type, actor_id, action, resource_type,
	resource_id, result, trace_id, metadata, at`

func scanAudit(rows *sql.Rows) (*domain.AuditEvent, error) {
	var (
		e        domain.AuditEvent
		metadata []byte
	)
	err := rows.Scan(&e.ID, &e.TenantID, &e.Actor.Type, &e.Actor.ID, &e.Action,
		&e.ResourceType, &e.ResourceID, &e.Result, &e.TraceID, &metadata, &e.At)
	if err != nil {
		return nil, wrapScan(err)
	}
	if err := unmarshalJSONColumn(metadata, &e.Metadata); err != nil {
		return nil, err
	}
	return &e, nil
}

// Append is insert-only: the audit trail has no update or delete path in this
// adapter, matching the immutability contract.
func (r *AuditRepository) Append(ctx context.Context, event *domain.AuditEvent) error {
	metadata, err := marshalJSONColumn(nonNilMap(event.Metadata))
	if err != nil {
		return err
	}
	_, werr := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO audit_events (id, tenant_id, actor_type, actor_id, action,
		   resource_type, resource_id, result, trace_id, metadata, at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(event.ID), string(event.TenantID), string(event.Actor.Type), event.Actor.ID,
		string(event.Action), event.ResourceType, event.ResourceID, string(event.Result),
		event.TraceID, metadata, event.At)
	if werr != nil {
		return wrapWrite(werr)
	}
	return nil
}

func (r *AuditRepository) ListByTenant(ctx context.Context, tenantID domain.TenantID, after string, limit int) ([]*domain.AuditEvent, error) {
	query := `SELECT ` + auditColumns + ` FROM audit_events WHERE tenant_id = $1`
	args := []any{string(tenantID)}
	if after != "" {
		query += ` AND at > (SELECT at FROM audit_events WHERE id = $2)`
		args = append(args, after)
	}
	query += ` ORDER BY at, id`
	if limit > 0 {
		query += ` LIMIT $` + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := r.st.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.AuditEvent{}
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type PolicyDecisionRepository struct{ st *Store }

var _ interface {
	Record(context.Context, *domain.PolicyDecision) error
	ListByRun(context.Context, domain.TenantID, domain.RunID) ([]*domain.PolicyDecision, error)
} = (*PolicyDecisionRepository)(nil)

const policyColumns = `id, tenant_id, run_id, task_id, principal, action,
	resource, outcome, reason, policy_version, created_at`

func scanPolicyDecision(rows *sql.Rows) (*domain.PolicyDecision, error) {
	var (
		d      domain.PolicyDecision
		taskID sql.NullString
	)
	err := rows.Scan(&d.ID, &d.TenantID, &d.Request.RunID, &taskID,
		&d.Request.Principal, &d.Request.Action, &d.Request.Resource,
		&d.Outcome, &d.Reason, &d.PolicyVersion, &d.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	if taskID.Valid {
		d.Request.TaskID = domain.TaskID(taskID.String)
	}
	d.Request.TenantID = d.TenantID
	return &d, nil
}

func (r *PolicyDecisionRepository) Record(ctx context.Context, decision *domain.PolicyDecision) error {
	var task any
	if decision.Request.TaskID != "" {
		task = string(decision.Request.TaskID)
	}
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO policy_decisions (id, tenant_id, run_id, task_id, principal,
		   action, resource, outcome, reason, policy_version, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(decision.ID), string(decision.TenantID), string(decision.Request.RunID),
		task, string(decision.Request.Principal), string(decision.Request.Action),
		decision.Request.Resource, string(decision.Outcome), decision.Reason,
		decision.PolicyVersion, decision.CreatedAt)
	if err != nil {
		return wrapWrite(err)
	}
	return nil
}

func (r *PolicyDecisionRepository) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.PolicyDecision, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+policyColumns+` FROM policy_decisions
		 WHERE tenant_id = $1 AND run_id = $2 ORDER BY created_at, id`,
		string(tenantID), string(runID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.PolicyDecision{}
	for rows.Next() {
		d, err := scanPolicyDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type IntegrationRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.IntegrationConnection) error
	Get(context.Context, domain.TenantID, domain.IntegrationID) (*domain.IntegrationConnection, error)
	Update(context.Context, *domain.IntegrationConnection, int64) error
	ListByTenant(context.Context, domain.TenantID) ([]*domain.IntegrationConnection, error)
} = (*IntegrationRepository)(nil)

const integrationColumns = `id, tenant_id, provider, status, scopes, secret_ref, version, created_at`

func scanIntegration(rows *sql.Rows) (*domain.IntegrationConnection, error) {
	var (
		c      domain.IntegrationConnection
		scopes []byte
	)
	err := rows.Scan(&c.ID, &c.TenantID, &c.Provider, &c.Status, &scopes,
		&c.SecretRef, &c.Version, &c.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	if err := unmarshalJSONColumn(scopes, &c.Scopes); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *IntegrationRepository) Create(ctx context.Context, conn *domain.IntegrationConnection) error {
	scopes, err := marshalJSONColumn(nonNilStrings(conn.Scopes))
	if err != nil {
		return err
	}
	_, werr := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO integration_connections (id, tenant_id, provider, status, scopes, secret_ref, version, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(conn.ID), string(conn.TenantID), string(conn.Provider), string(conn.Status),
		scopes, conn.SecretRef, conn.Version, conn.CreatedAt)
	if werr != nil {
		if mapped := mapUniqueViolation(werr, "integration_exists", "integration connection %s already exists", conn.ID); mapped != werr {
			return mapped
		}
		return wrapWrite(werr)
	}
	return nil
}

func (r *IntegrationRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.IntegrationID) (*domain.IntegrationConnection, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+integrationColumns+` FROM integration_connections WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("integration connection", id)
	}
	return scanIntegration(rows)
}

func (r *IntegrationRepository) Update(ctx context.Context, conn *domain.IntegrationConnection, expectedVersion int64) error {
	scopes, err := marshalJSONColumn(nonNilStrings(conn.Scopes))
	if err != nil {
		return err
	}
	res, uerr := r.st.q(ctx).ExecContext(ctx,
		`UPDATE integration_connections
		 SET provider = $3, status = $4, scopes = $5, secret_ref = $6, version = version + 1
		 WHERE id = $1 AND tenant_id = $2 AND version = $7`,
		string(conn.ID), string(conn.TenantID), string(conn.Provider), string(conn.Status),
		scopes, conn.SecretRef, expectedVersion)
	if uerr != nil {
		return wrapWrite(uerr)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return classifyStaleOrMissing(ctx, "integration connection", string(conn.ID), func() error {
			_, gerr := r.Get(ctx, conn.TenantID, conn.ID)
			return gerr
		})
	}
	conn.Version = expectedVersion + 1
	return nil
}

func (r *IntegrationRepository) ListByTenant(ctx context.Context, tenantID domain.TenantID) ([]*domain.IntegrationConnection, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+integrationColumns+` FROM integration_connections WHERE tenant_id = $1 ORDER BY created_at, id`,
		string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.IntegrationConnection{}
	for rows.Next() {
		c, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nonNilMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	return in
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
