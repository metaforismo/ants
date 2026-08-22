package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/metaforismo/ants/internal/domain"
)

type TenantRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Tenant) error
	Get(context.Context, domain.TenantID) (*domain.Tenant, error)
	GetBySlug(context.Context, string) (*domain.Tenant, error)
} = (*TenantRepository)(nil)

const tenantColumns = `id, slug, name, plan, region, status, version, created_at, updated_at`

func scanTenant(row *sql.Row) (*domain.Tenant, error) {
	var t domain.Tenant
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Plan, &t.Region, &t.Status, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NotFoundf("tenant", "row")
	}
	if err != nil {
		return nil, wrapScan(err)
	}
	return &t, nil
}

func (r *TenantRepository) Create(ctx context.Context, tenant *domain.Tenant) error {
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO tenants (id, slug, name, plan, region, status, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		string(tenant.ID), tenant.Slug, tenant.Name, string(tenant.Plan), tenant.Region,
		string(tenant.Status), tenant.Version, tenant.CreatedAt, tenant.UpdatedAt)
	if err != nil {
		if mapped := mapUniqueViolation(err, "tenant_slug_taken", "tenant slug %q already exists", tenant.Slug); mapped != err {
			return mapped
		}
		return wrapWrite(err)
	}
	return nil
}

func (r *TenantRepository) Get(ctx context.Context, id domain.TenantID) (*domain.Tenant, error) {
	return scanTenant(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, string(id)))
}

func (r *TenantRepository) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	return scanTenant(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE slug = $1`, slug))
}
