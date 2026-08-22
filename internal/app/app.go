// Package app is the composition root: it turns validated configuration into
// wired components. Every binary (CLI, API server) builds the world here so
// wiring rules live in exactly one place.
package app

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/orchestration"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/policy"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/review"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

// App holds the fully wired application for one configuration.
type App struct {
	Config  config.Config
	Logger  *slog.Logger
	Clock   ports.Clock
	Repos   ports.Repositories
	Engine  *orchestration.Engine
	Sandbox sandbox.Driver
	SCM     scm.Driver
	Seeder  orchestration.Seeder
}

// Build wires every component from cfg. Store mode postgres is rejected with
// an explicit error until that adapter ships (ADR-0009); nothing falls back
// silently to memory.
func Build(cfg config.Config, logOut io.Writer) (*App, error) {
	logger := newLogger(cfg.Log, logOut)

	if cfg.Store.Mode != config.StoreModeMemory {
		return nil, fmt.Errorf("app: store.mode %q requires the PostgreSQL adapter, which is not implemented yet; use store.mode memory", cfg.Store.Mode)
	}
	mem := memorystore.NewRepos()
	repos := ports.Repositories{
		Tenants:         mem.Tenants,
		Projects:        mem.Projects,
		Threads:         mem.Threads,
		Specs:           mem.Specs,
		Tasks:           mem.Tasks,
		Runs:            mem.Runs,
		Workspaces:      mem.Workspaces,
		Artifacts:       mem.Artifacts,
		Audit:           mem.Audit,
		PolicyDecisions: mem.PolicyDecisions,
		Integrations:    mem.Integrations,
		Events:          mem.Events,
	}

	clock := ports.SystemClock{}
	ids := ports.RandomIDs{}
	sleeper := ports.WallSleeper{}

	pol := policy.NewEngine(cfg.Policy.AllowLocalCommits, clock, ids, repos.PolicyDecisions, repos.Audit)

	sandboxDriver, err := buildSandbox(cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	scmDriver, err := buildSCM(cfg.SCM)
	if err != nil {
		return nil, err
	}

	engine, err := orchestration.New(orchestration.Deps{
		Threads:    repos.Threads,
		Projects:   repos.Projects,
		Specs:      repos.Specs,
		Tasks:      repos.Tasks,
		Runs:       repos.Runs,
		Workspaces: repos.Workspaces,
		Artifacts:  repos.Artifacts,
		Events:     repos.Events,
		Policy:     pol,
		Sandbox:    sandboxDriver,
		SCM:        scmDriver,
		Planner:    planner.NewDeterministic(),
		Reviewer:   review.NewDeterministic(2000),
		Seeder:     seeder{},
		Clock:      clock,
		IDs:        ids,
		Sleeper:    sleeper,
	}, orchestration.Config{
		MaxParallelTasks: cfg.Orchestrator.MaxParallelTasks,
		TaskTimeout:      cfg.Orchestrator.TaskTimeout.Duration,
		StageTimeout:     cfg.Orchestrator.StageTimeout.Duration,
		MaxAttempts:      cfg.Orchestrator.MaxAttempts,
		RetryBackoff:     cfg.Orchestrator.RetryBackoffBase.Duration,
		MaxTasksPerRun:   cfg.Orchestrator.MaxTasksPerRun,
		MaxExecOpsPerRun: cfg.Orchestrator.MaxExecOpsPerRun,
	})
	if err != nil {
		return nil, fmt.Errorf("app: wire orchestration: %w", err)
	}

	return &App{
		Config:  cfg,
		Logger:  logger,
		Clock:   clock,
		Repos:   repos,
		Engine:  engine,
		Sandbox: sandboxDriver,
		SCM:     scmDriver,
		Seeder:  seeder{},
	}, nil
}

func buildSandbox(cfg config.Sandbox) (sandbox.Driver, error) {
	switch cfg.Driver {
	case config.SandboxDriverProcess:
		return sandbox.NewProcessDriver(cfg.WorkRoot)
	case config.SandboxDriverFake:
		return sandbox.NewFakeDriver(), nil
	default:
		return nil, fmt.Errorf("app: unknown sandbox driver %q", cfg.Driver)
	}
}

func buildSCM(cfg config.SCM) (scm.Driver, error) {
	switch cfg.Driver {
	case config.SCMDriverMemory:
		return scm.NewMemory(), nil
	case config.SCMDriverLocalGit:
		return scm.NewLocalGit()
	default:
		return nil, fmt.Errorf("app: unknown scm driver %q", cfg.Driver)
	}
}

func newLogger(log config.Log, out io.Writer) *slog.Logger {
	level := slog.LevelInfo
	switch log.Level {
	case config.LogDebug:
		level = slog.LevelDebug
	case config.LogWarn:
		level = slog.LevelWarn
	case config.LogError:
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if log.Format == config.LogFormatJSON {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	return slog.New(handler)
}
