package cli

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/server"
	"github.com/metaforismo/ants/internal/store/migrate"
)

func newServer(application *app.App) (*server.Server, error) {
	return server.New(server.Deps{
		Config:  application.Config,
		Repos:   application.Repos,
		Uow:     application.Uow,
		Engine:  application.Engine,
		Logger:  application.Logger,
		Ready:   application.Ready,
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
