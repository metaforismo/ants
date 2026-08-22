package app

import (
	"context"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/orchestration"
	"github.com/metaforismo/ants/internal/scm"
)

// seeder resolves registered repository fixtures. Remote SCM connections
// replace this source in later waves; the orchestration.Seeder seam stays
// identical.
type seeder struct{}

func (seeder) Seed(_ context.Context, name string) (scm.Seed, error) {
	if name != fixtures.DemoName {
		return scm.Seed{}, domain.NotFoundf("repository fixture", name)
	}
	return fixtures.DemoSeed(), nil
}

var _ orchestration.Seeder = seeder{}
