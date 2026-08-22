package postgres

import (
	"context"
	"database/sql"

	"github.com/metaforismo/ants/internal/domain"
)

// wrapScan classifies driver failures so callers never match on strings.
func wrapScan(err error) error {
	return domain.Internalf(err, "db_scan", "read from postgres")
}

func wrapWrite(err error) error {
	return domain.Internalf(err, "db_write", "write to postgres")
}

type ProjectRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Project) error
	Get(context.Context, domain.TenantID, domain.ProjectID) (*domain.Project, error)
	ListByTenant(context.Context, domain.TenantID) ([]*domain.Project, error)
} = (*ProjectRepository)(nil)

const projectColumns = `id, tenant_id, slug, name, default_branch, seed_name, version, created_at`

func scanProject(rows *sql.Rows) (*domain.Project, error) {
	var p domain.Project
	err := rows.Scan(&p.ID, &p.TenantID, &p.Slug, &p.Name, &p.DefaultBranch, &p.SeedName, &p.Version, &p.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	return &p, nil
}

func (r *ProjectRepository) Create(ctx context.Context, project *domain.Project) error {
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO projects (id, tenant_id, slug, name, default_branch, seed_name, version, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(project.ID), string(project.TenantID), project.Slug, project.Name,
		project.DefaultBranch, project.SeedName, project.Version, project.CreatedAt)
	if err != nil {
		if mapped := mapUniqueViolation(err, "project_exists", "project %s already exists", project.ID); mapped != err {
			return mapped
		}
		// The (tenant_id, slug) unique index guards duplicate slugs.
		if mapped := mapUniqueViolation(err, "project_slug_taken", "project slug %q already exists for this tenant", project.Slug); mapped != err {
			return mapped
		}
		return wrapWrite(err)
	}
	return nil
}

func (r *ProjectRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.ProjectID) (*domain.Project, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		// Uniform not-found: foreign-tenant ids are indistinguishable from
		// missing ones to prevent resource enumeration.
		return nil, domain.NotFoundf("project", id)
	}
	project, err := scanProject(rows)
	if err != nil {
		return nil, err
	}
	return project, nil
}

func (r *ProjectRepository) ListByTenant(ctx context.Context, tenantID domain.TenantID) ([]*domain.Project, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE tenant_id = $1 ORDER BY created_at, id`,
		string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
