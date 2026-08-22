package memory

import (
	"context"
	"sort"

	"github.com/metaforismo/ants/internal/domain"
)

type TenantRepository struct{ st *storeState }

func (r *TenantRepository) Create(_ context.Context, tenant *domain.Tenant) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.tenants[tenant.ID]; exists {
		return domain.Conflictf("tenant_exists", "tenant %s already exists", tenant.ID)
	}
	if _, exists := r.st.tenantSlugs[tenant.Slug]; exists {
		return domain.Conflictf("tenant_slug_taken", "tenant slug %q already exists", tenant.Slug)
	}
	r.st.tenants[tenant.ID] = cloneTenant(tenant)
	r.st.tenantSlugs[tenant.Slug] = tenant.ID
	return nil
}

func (r *TenantRepository) Get(_ context.Context, id domain.TenantID) (*domain.Tenant, error) {
	unlock := lockRead(r.st)
	defer unlock()
	t, ok := r.st.tenants[id]
	if !ok {
		return nil, notFound("tenant", id)
	}
	return cloneTenant(t), nil
}

func (r *TenantRepository) GetBySlug(_ context.Context, slug string) (*domain.Tenant, error) {
	unlock := lockRead(r.st)
	defer unlock()
	id, ok := r.st.tenantSlugs[slug]
	if !ok {
		return nil, notFound("tenant", slug)
	}
	return cloneTenant(r.st.tenants[id]), nil
}

type ProjectRepository struct{ st *storeState }

func (r *ProjectRepository) Create(_ context.Context, project *domain.Project) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.projects[project.ID]; exists {
		return domain.Conflictf("project_exists", "project %s already exists", project.ID)
	}
	r.st.projects[project.ID] = cloneProject(project)
	return nil
}

func (r *ProjectRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.ProjectID) (*domain.Project, error) {
	unlock := lockRead(r.st)
	defer unlock()
	p, ok := r.st.projects[id]
	if !ok || p.TenantID != tenantID {
		// Uniform not-found: foreign-tenant ids are indistinguishable from
		// missing ones to prevent resource enumeration.
		return nil, notFound("project", id)
	}
	return cloneProject(p), nil
}

func (r *ProjectRepository) ListByTenant(_ context.Context, tenantID domain.TenantID) ([]*domain.Project, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Project{}
	for _, p := range r.st.projects {
		if p.TenantID == tenantID {
			out = append(out, cloneProject(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
