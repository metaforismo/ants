package postgres

import (
	"context"
	"database/sql"

	"github.com/metaforismo/ants/internal/domain"
)

type SpecRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Spec) error
	Get(context.Context, domain.TenantID, domain.SpecID) (*domain.Spec, error)
	LatestForThread(context.Context, domain.TenantID, domain.ThreadID) (*domain.Spec, error)
} = (*SpecRepository)(nil)

const specColumns = `id, tenant_id, thread_id, version, status, outcome,
	assumptions, requirements, non_goals, success_criteria, blockers, created_at`

func scanSpec(rows *sql.Rows) (*domain.Spec, error) {
	var (
		s                            domain.Spec
		assumptions, requirements    []byte
		nonGoals, criteria, blockers []byte
	)
	err := rows.Scan(&s.ID, &s.TenantID, &s.ThreadID, &s.Version, &s.Status,
		&s.Content.Outcome, &assumptions, &requirements, &nonGoals, &criteria, &blockers,
		&s.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	for _, pair := range []struct {
		raw []byte
		dst *[]string
	}{
		{assumptions, &s.Content.Assumptions},
		{requirements, &s.Content.Requirements},
		{nonGoals, &s.Content.NonGoals},
		{criteria, &s.Content.SuccessCriteria},
		{blockers, &s.Content.Blockers},
	} {
		if err := unmarshalJSONColumn(pair.raw, pair.dst); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func (r *SpecRepository) Create(ctx context.Context, spec *domain.Spec) error {
	assumptions, err := marshalJSONColumn(spec.Content.Assumptions)
	if err != nil {
		return err
	}
	requirements, err := marshalJSONColumn(spec.Content.Requirements)
	if err != nil {
		return err
	}
	nonGoals, err := marshalJSONColumn(spec.Content.NonGoals)
	if err != nil {
		return err
	}
	criteria, err := marshalJSONColumn(spec.Content.SuccessCriteria)
	if err != nil {
		return err
	}
	blockers, err := marshalJSONColumn(spec.Content.Blockers)
	if err != nil {
		return err
	}
	_, werr := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO specs (id, tenant_id, thread_id, version, status, outcome,
		   assumptions, requirements, non_goals, success_criteria, blockers, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(spec.ID), string(spec.TenantID), string(spec.ThreadID), spec.Version,
		string(spec.Status), spec.Content.Outcome,
		assumptions, requirements, nonGoals, criteria, blockers, spec.CreatedAt)
	if werr != nil {
		if mapped := mapUniqueViolation(werr, "spec_exists", "spec %s already exists", spec.ID); mapped != werr {
			return mapped
		}
		return wrapWrite(werr)
	}
	return nil
}

func (r *SpecRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.SpecID) (*domain.Spec, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+specColumns+` FROM specs WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("spec", id)
	}
	return scanSpec(rows)
}

func (r *SpecRepository) LatestForThread(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID) (*domain.Spec, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+specColumns+` FROM specs
		 WHERE thread_id = $1 AND tenant_id = $2
		 ORDER BY version DESC LIMIT 1`,
		string(threadID), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("spec", threadID)
	}
	return scanSpec(rows)
}
