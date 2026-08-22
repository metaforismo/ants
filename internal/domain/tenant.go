package domain

import (
	"time"
)

type TenantStatus string

const (
	TenantActive    TenantStatus = "active"
	TenantSuspended TenantStatus = "suspended"
	TenantDeleted   TenantStatus = "deleted"
)

var AllTenantStatuses = []TenantStatus{TenantActive, TenantSuspended, TenantDeleted}

type PlanTier string

const (
	PlanFree       PlanTier = "free"
	PlanTeam       PlanTier = "team"
	PlanEnterprise PlanTier = "enterprise"
)

func (p PlanTier) Valid() bool {
	switch p {
	case PlanFree, PlanTeam, PlanEnterprise:
		return true
	default:
		return false
	}
}

const MaxSlugLen = 64

// Slug is the tenant's stable external identifier. Restricted to lowercase
// DNS-safe characters so it can anchor hostnames and URLs later.
func ValidateSlug(slug string) error {
	if slug == "" {
		return Invalidf("slug", "slug must not be empty")
	}
	if len(slug) > MaxSlugLen {
		return Invalidf("slug", "slug longer than %d characters", MaxSlugLen)
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return Invalidf("slug", "slug %q may only contain lowercase letters, digits and hyphens", slug)
		}
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return Invalidf("slug", "slug %q must not start or end with a hyphen", slug)
	}
	return nil
}

type Tenant struct {
	ID        TenantID     `json:"id"`
	Slug      string       `json:"slug"`
	Name      string       `json:"name"`
	Plan      PlanTier     `json:"plan"`
	Region    string       `json:"region"`
	Status    TenantStatus `json:"status"`
	Version   int64        `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func NewTenant(id TenantID, slug, name string, plan PlanTier, region string, now time.Time) (*Tenant, error) {
	if _, err := ParseTenantID(string(id)); err != nil {
		return nil, err
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, Invalidf("tenant_name", "tenant name must not be empty")
	}
	if !plan.Valid() {
		return nil, Invalidf("tenant_plan", "plan tier %q is not supported", plan)
	}
	now = now.UTC()
	return &Tenant{
		ID:        id,
		Slug:      slug,
		Name:      name,
		Plan:      plan,
		Region:    region,
		Status:    TenantActive,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

type Project struct {
	ID            ProjectID `json:"id"`
	TenantID      TenantID  `json:"tenant_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	DefaultBranch string    `json:"default_branch"`
	// SeedName references the repository seed source used by the local
	// pipeline (tranche 1: a registered fixture). Remote SCM connections
	// replace this field's role in later waves.
	SeedName  string    `json:"seed_name"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

func NewProject(id ProjectID, tenantID TenantID, slug, name, defaultBranch, seedName string, now time.Time) (*Project, error) {
	if _, err := ParseProjectID(string(id)); err != nil {
		return nil, err
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, Invalidf("project_name", "project name must not be empty")
	}
	if defaultBranch == "" {
		return nil, Invalidf("project_default_branch", "default branch must not be empty")
	}
	return &Project{
		ID:            id,
		TenantID:      tenantID,
		Slug:          slug,
		Name:          name,
		DefaultBranch: defaultBranch,
		SeedName:      seedName,
		Version:       1,
		CreatedAt:     now.UTC(),
	}, nil
}
