// Package app is the composition root: it turns validated configuration into
// wired components. Every binary (CLI, API server) builds the world here so
// wiring rules live in exactly one place.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/metrics"
	"github.com/metaforismo/ants/internal/orchestration"
	"github.com/metaforismo/ants/internal/outbox"
	"github.com/metaforismo/ants/internal/outboxgc"
	"github.com/metaforismo/ants/internal/outboxops"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/policy"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/review"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
	"github.com/metaforismo/ants/internal/store/postgres"
	"github.com/metaforismo/ants/internal/worker"
)

// App holds the fully wired application for one configuration.
type App struct {
	Config  config.Config
	Logger  *slog.Logger
	Clock   ports.Clock
	Repos   ports.Repositories
	Uow     ports.Transactor
	Engine  *orchestration.Engine
	Sandbox sandbox.Driver
	SCM     scm.Driver
	Seeder  orchestration.Seeder
	Outbox  *outbox.Dispatcher
	Worker  *worker.Worker
	// OutboxOps is the dead-letter operator seam (ADR-0015); the CLI's
	// outbox commands run through it so every mutation commits its event,
	// delivery, and audit record atomically.
	OutboxOps *outboxops.Service
	// Retention is the bounded outbox GC seam (ADR-0016); serve starts its
	// scheduled loop only when a retention horizon is configured, and the
	// CLI drives preview/manual rounds through it.
	Retention *outboxgc.Service
	// Metrics is the Prometheus collector behind /metrics and the observer
	// of the outbox dispatcher and run worker; nil when metrics are disabled
	// by configuration (ADR-0014).
	Metrics *metrics.Metrics
	// Ready reports whether backing dependencies can serve traffic; the API
	// server exposes it behind /readyz. The memory store has no external
	// dependency, so its check is trivially satisfied.
	Ready func(ctx context.Context) error
}

// Build wires every component from cfg. Store mode postgres is rejected with
// an explicit error until that adapter ships (ADR-0009); nothing falls back
// silently to memory.
func Build(cfg config.Config, logOut io.Writer) (*App, error) {
	logger := newLogger(cfg.Log, logOut)

	var (
		repos      ports.Repositories
		transactor ports.Transactor
		ready      func(ctx context.Context) error
	)
	switch cfg.Store.Mode {
	case config.StoreModeMemory:
		mem := memorystore.NewRepos()
		repos = mem.AsPorts()
		transactor = mem.NewTransactor()
		ready = func(context.Context) error { return nil }
	case config.StoreModePostgres:
		poolCfg := cfg.Store.PostgresPool
		pgStore, pgErr := postgres.New(context.Background(), postgres.Options{
			DSN:               cfg.Store.PostgresDSN.Expose(),
			MaxOpenConns:      poolCfg.MaxOpenConns,
			MaxIdleConns:      poolCfg.MaxIdleConns,
			ConnMaxLifetime:   poolCfg.ConnMaxLifetime.Duration,
			OutboxMaxAttempts: cfg.Outbox.MaxAttempts,
		})
		if pgErr != nil {
			return nil, fmt.Errorf("app: connect to postgres: %w", pgErr)
		}
		repos = pgStore.Repositories()
		transactor = pgStore
		ready = pgStore.Ping
	default:
		return nil, fmt.Errorf("app: store.mode %q is not supported", cfg.Store.Mode)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}

	clock := ports.SystemClock{}
	ids := ports.RandomIDs{}
	sleeper := ports.WallSleeper{}

	// One collector instruments the HTTP edge, the outbox dispatcher, and the
	// run worker (ADR-0014); nil when metrics are disabled by configuration.
	var collector *metrics.Metrics
	if cfg.Metrics.Enabled {
		collector = metrics.New()
	}

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
		RunClaims:  repos.RunClaims,
		Uow:        transactor,
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

	// One stable identity per process for both durable subsystems: the outbox
	// lease holder and the run claim owner must not change while the process
	// runs, and must differ between processes. Tests build components
	// directly to inject their own identities.
	nodeID, nerr := os.Hostname()
	if nerr != nil {
		nodeID = "unknown-host"
	}
	nodeID = fmt.Sprintf("%s-%d", nodeID, os.Getpid())

	// The dispatcher drains the durable outbox both store modes fill on
	// event append (ADR-0011); long-running processes start it via Run.
	dispatcher, derr := outbox.New(repos.Outbox,
		outbox.LogSink{Logger: logger}, logger,
		outbox.Config{
			BatchSize:        cfg.Outbox.BatchSize,
			Interval:         cfg.Outbox.Interval.Duration,
			Lease:            cfg.Outbox.Lease.Duration,
			MaxAttempts:      cfg.Outbox.MaxAttempts,
			RetryBackoffBase: cfg.Outbox.RetryBackoffBase.Duration,
		},
		nodeID, collector)
	if derr != nil {
		return nil, fmt.Errorf("app: wire outbox dispatcher: %w", derr)
	}

	// The run worker owns execution of every started run (ADR-0012 part 2);
	// StartRun only enqueues the durable claim.
	runWorker, werr := worker.New(repos.RunClaims, repos.Runs, engine, logger,
		worker.Config{
			BatchSize:      cfg.Worker.BatchSize,
			Interval:       cfg.Worker.Interval.Duration,
			Lease:          cfg.Worker.Lease.Duration,
			HeartbeatEvery: cfg.Worker.HeartbeatEvery.Duration,
			CleanupTimeout: cfg.Worker.CleanupTimeout.Duration,
			Concurrency:    cfg.Worker.Concurrency,
			MaxAttempts:    cfg.Worker.MaxAttempts,
		},
		nodeID, collector)
	if werr != nil {
		return nil, fmt.Errorf("app: wire run worker: %w", werr)
	}

	// Dead-letter operator actions share the collector through the same
	// observer seam (ADR-0015); a nil observer when metrics are disabled
	// changes outcomes not at all.
	var opsObserver outboxops.Observer
	if collector != nil {
		opsObserver = collector
	}
	outboxOps, oerr := outboxops.New(outboxops.Deps{
		Outbox:   repos.Outbox,
		Events:   repos.Events,
		Audit:    repos.Audit,
		Tx:       transactor,
		IDs:      ids,
		Clock:    clock,
		Observer: opsObserver,
	})
	if oerr != nil {
		return nil, fmt.Errorf("app: wire outbox operator service: %w", oerr)
	}

	// Retention rounds share the collector through the same observer seam
	// (ADR-0014 pattern); a nil observer when metrics are disabled changes
	// outcomes not at all. The service is inert unless a retention horizon
	// is configured (ADR-0016).
	var gcObserver outboxgc.Observer
	if collector != nil {
		gcObserver = collector
	}
	retention, rerr := outboxgc.New(repos.Outbox, logger, outboxgc.Config{
		DeliveredAfter: cfg.Outbox.Retention.DeliveredAfter.Duration,
		DiscardedAfter: cfg.Outbox.Retention.DiscardedAfter.Duration,
		BatchSize:      cfg.Outbox.Retention.BatchSize,
		Interval:       cfg.Outbox.Retention.Interval.Duration,
	}, gcObserver)
	if rerr != nil {
		return nil, fmt.Errorf("app: wire outbox retention service: %w", rerr)
	}

	return &App{
		Config:    cfg,
		Logger:    logger,
		Clock:     clock,
		Repos:     repos,
		Uow:       transactor,
		Engine:    engine,
		Sandbox:   sandboxDriver,
		SCM:       scmDriver,
		Seeder:    seeder{},
		Outbox:    dispatcher,
		Worker:    runWorker,
		OutboxOps: outboxOps,
		Retention: retention,
		Metrics:   collector,
		Ready:     ready,
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
