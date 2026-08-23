package cli

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/authn"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/server"
	"github.com/metaforismo/ants/internal/store/migrate"
)

// newServer wires the API server for one application. Authentication is
// selected here — the composition boundary (ADR-0019): a configured OIDC
// issuer installs the resource-server verifier and joins its IdP warm-up
// into the readiness chain; otherwise the server refuses every request
// explicitly. Nothing else may authenticate.
func newServer(application *app.App) (*server.Server, error) {
	var auth server.Authenticator = server.UnconfiguredAuthenticator{}
	ready := application.Ready
	if application.Config.Auth.OIDC.Configured() {
		verifier, err := authn.NewBearer(authn.Options{
			Config:   application.Config.Auth.OIDC,
			Tenants:  application.Repos.Tenants,
			Observer: application.Metrics,
		})
		if err != nil {
			return nil, fmt.Errorf("wire oidc authenticator: %w", err)
		}
		auth = verifier
		idpReady := verifier.Ready
		ready = func(ctx context.Context) error {
			if err := application.Ready(ctx); err != nil {
				return err
			}
			if err := idpReady(ctx); err != nil {
				// Typed so /readyz reports the IdP as the failing dependency
				// instead of the generic persistence classification.
				return &domain.Error{
					Kind:    domain.ErrKindTransient,
					Code:    "auth_provider_unavailable",
					Message: "identity provider metadata or keys are not reachable",
				}
			}
			return nil
		}
	}
	return server.New(server.Deps{
		Config:  application.Config,
		Repos:   application.Repos,
		Auth:    auth,
		Uow:     application.Uow,
		Engine:  application.Engine,
		Logger:  application.Logger,
		Ready:   ready,
		Metrics: application.Metrics,
	})
}

func applyMigrations(ctx context.Context, dsn string) (applied []string, pending []string, err error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	report, err := migrate.Up(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	applied, pending, err = migrate.Current(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	// Current() reports everything applied so far; surface only what this
	// invocation changed.
	if len(report.Applied) > 0 {
		applied = report.Applied
	} else {
		applied = nil
	}
	return applied, pending, nil
}
