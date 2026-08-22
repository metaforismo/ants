// Package config implements layered configuration: code defaults, then an
// optional YAML file, then environment overrides, then validation. Secrets are
// held in a dedicated type so diagnostic output can never leak them by
// accident.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Secret wraps credential material. Its text representations (fmt, JSON,
// logging) always render as "[REDACTED]"; access to the raw value requires the
// explicit Expose accessor.
type Secret string

const RedactedPlaceholder = "[REDACTED]"

func (s Secret) MarshalText() ([]byte, error) { return []byte(RedactedPlaceholder), nil }

func (s Secret) String() string { return RedactedPlaceholder }

func (s Secret) GoString() string { return RedactedPlaceholder }

// Expose returns the raw secret value. Call sites must justify use: passing
// credentials to the system that owns them, never logging or serializing.
func (s Secret) Expose() string { return string(s) }

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type StoreMode string

const (
	StoreModeMemory   StoreMode = "memory"
	StoreModePostgres StoreMode = "postgres"
)

type SandboxDriverKind string

const (
	SandboxDriverProcess SandboxDriverKind = "process"
	SandboxDriverFake    SandboxDriverKind = "fake"
)

type SCMDriverKind string

const (
	SCMDriverMemory   SCMDriverKind = "memory"
	SCMDriverLocalGit SCMDriverKind = "local_git"
)

type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

type Server struct {
	HTTPAddr        string   `yaml:"http_addr"`
	DevHeaderAuth   bool     `yaml:"dev_header_auth"`
	ReadTimeout     Duration `yaml:"read_timeout"`
	WriteTimeout    Duration `yaml:"write_timeout"`
	IdleTimeout     Duration `yaml:"idle_timeout"`
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`
	// ReadinessTimeout bounds each readiness probe's dependency checks so a
	// slow database turns into a fast 503 instead of a hung health check.
	ReadinessTimeout Duration `yaml:"readiness_timeout"`
}

// PostgresPool bounds the connection pool. Defaults are sized for a
// single-node deployment; every value is validated at startup so a typo
// cannot produce an unbounded or dead pool.
type PostgresPool struct {
	MaxOpenConns    int      `yaml:"max_open_conns"`
	MaxIdleConns    int      `yaml:"max_idle_conns"`
	ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
}

func (p PostgresPool) Validate() error {
	if p.MaxOpenConns < 1 || p.MaxOpenConns > 200 {
		return fmt.Errorf("store.pool.max_open_conns must be within [1,200], got %d", p.MaxOpenConns)
	}
	if p.MaxIdleConns < 1 || p.MaxIdleConns > p.MaxOpenConns {
		return fmt.Errorf("store.pool.max_idle_conns must be within [1, max_open_conns], got %d", p.MaxIdleConns)
	}
	if p.ConnMaxLifetime.Duration < time.Second {
		return fmt.Errorf("store.pool.conn_max_lifetime must be at least 1s, got %s", p.ConnMaxLifetime)
	}
	return nil
}

type Store struct {
	Mode           StoreMode    `yaml:"mode"`
	PostgresPool   PostgresPool `yaml:"pool"`
	PostgresDSNEnv string       `yaml:"-"` // populated from ANTS_STORE_POSTGRES_DSN only
	PostgresDSN    Secret       `yaml:"-"`
}

type Orchestrator struct {
	MaxParallelTasks int      `yaml:"max_parallel_tasks"`
	MaxTasksPerRun   int      `yaml:"max_tasks_per_run"`
	MaxExecOpsPerRun int      `yaml:"max_exec_ops_per_run"`
	TaskTimeout      Duration `yaml:"task_timeout"`
	StageTimeout     Duration `yaml:"stage_timeout"`
	MaxAttempts      int      `yaml:"max_attempts"`
	RetryBackoffBase Duration `yaml:"retry_backoff_base"`
}

type Sandbox struct {
	Driver   SandboxDriverKind `yaml:"driver"`
	WorkRoot string            `yaml:"work_root"`
}

type SCM struct {
	Driver SCMDriverKind `yaml:"driver"`
}

type Policy struct {
	AllowLocalCommits bool `yaml:"allow_local_commits"`
}

// Outbox bounds the in-process dispatcher that drains durably queued
// events. Defaults suit a single node; every value validates at startup.
type Outbox struct {
	BatchSize        int      `yaml:"batch_size"`
	Interval         Duration `yaml:"interval"`
	Lease            Duration `yaml:"lease"`
	MaxAttempts      int      `yaml:"max_attempts"`
	RetryBackoffBase Duration `yaml:"retry_backoff_base"`
}

// Worker bounds the process-level run executor that claims durable run
// claims and drives them through the engine (ADR-0012 part 2). The lease
// must leave room for missed heartbeats: lease >= 3x heartbeat_every means
// two consecutive lost beats still renew inside the window. MaxAttempts caps
// dispatches per claim before its run converges as exhausted (ADR-0013).
type Worker struct {
	BatchSize      int      `yaml:"batch_size"`
	Interval       Duration `yaml:"interval"`
	Lease          Duration `yaml:"lease"`
	HeartbeatEvery Duration `yaml:"heartbeat_every"`
	CleanupTimeout Duration `yaml:"cleanup_timeout"`
	Concurrency    int      `yaml:"concurrency"`
	MaxAttempts    int      `yaml:"max_attempts"`
}

func (o Outbox) Validate() error {
	if o.BatchSize < 1 || o.BatchSize > 1000 {
		return fmt.Errorf("outbox.batch_size must be within [1,1000], got %d", o.BatchSize)
	}
	if o.Interval.Duration < 10*time.Millisecond {
		return fmt.Errorf("outbox.interval must be at least 10ms, got %s", o.Interval)
	}
	if o.Lease.Duration < time.Second {
		return fmt.Errorf("outbox.lease must be at least 1s, got %s", o.Lease)
	}
	if o.MaxAttempts < 1 || o.MaxAttempts > 100 {
		return fmt.Errorf("outbox.max_attempts must be within [1,100], got %d", o.MaxAttempts)
	}
	if o.RetryBackoffBase.Duration < 0 {
		return fmt.Errorf("outbox.retry_backoff_base must not be negative")
	}
	return nil
}

func (w Worker) Validate() error {
	if w.BatchSize < 1 || w.BatchSize > 1000 {
		return fmt.Errorf("worker.batch_size must be within [1,1000], got %d", w.BatchSize)
	}
	if w.Interval.Duration < 10*time.Millisecond {
		return fmt.Errorf("worker.interval must be at least 10ms, got %s", w.Interval)
	}
	if w.Lease.Duration < time.Second {
		return fmt.Errorf("worker.lease must be at least 1s, got %s", w.Lease)
	}
	if w.HeartbeatEvery.Duration < 10*time.Millisecond {
		return fmt.Errorf("worker.heartbeat_every must be at least 10ms, got %s", w.HeartbeatEvery)
	}
	if w.Lease.Duration < 3*w.HeartbeatEvery.Duration {
		return fmt.Errorf("worker.lease %s must be at least three times worker.heartbeat_every %s so two missed beats never expire a live worker", w.Lease, w.HeartbeatEvery)
	}
	if w.CleanupTimeout.Duration < 100*time.Millisecond {
		return fmt.Errorf("worker.cleanup_timeout must be at least 100ms, got %s", w.CleanupTimeout)
	}
	if w.Concurrency < 1 || w.Concurrency > 64 {
		return fmt.Errorf("worker.concurrency must be within [1,64], got %d", w.Concurrency)
	}
	if w.MaxAttempts < 1 || w.MaxAttempts > 10 {
		return fmt.Errorf("worker.max_attempts must be within [1,10], got %d", w.MaxAttempts)
	}
	return nil
}

type Log struct {
	Level  LogLevel  `yaml:"level"`
	Format LogFormat `yaml:"format"`
}

type Config struct {
	Server       Server       `yaml:"server"`
	Store        Store        `yaml:"store"`
	Orchestrator Orchestrator `yaml:"orchestrator"`
	Sandbox      Sandbox      `yaml:"sandbox"`
	SCM          SCM          `yaml:"scm"`
	Policy       Policy       `yaml:"policy"`
	Outbox       Outbox       `yaml:"outbox"`
	Worker       Worker       `yaml:"worker"`
	Log          Log          `yaml:"log"`
}

// Defaults are safe: local-only bind address, memory store, deny-by-default
// posture for anything crossing a trust boundary. Dev auth is off by default;
// enabling it is an explicit operator decision.
func Defaults() Config {
	return Config{
		Server: Server{
			HTTPAddr:         "127.0.0.1:8080",
			DevHeaderAuth:    false,
			ReadTimeout:      Duration{10 * time.Second},
			WriteTimeout:     Duration{30 * time.Second},
			IdleTimeout:      Duration{120 * time.Second},
			ShutdownTimeout:  Duration{10 * time.Second},
			ReadinessTimeout: Duration{2 * time.Second},
		},
		Store: Store{
			Mode: StoreModeMemory,
			PostgresPool: PostgresPool{
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: Duration{30 * time.Minute},
			},
		},
		Orchestrator: Orchestrator{
			MaxParallelTasks: 4,
			MaxTasksPerRun:   8,
			MaxExecOpsPerRun: 64,
			TaskTimeout:      Duration{2 * time.Minute},
			StageTimeout:     Duration{5 * time.Minute},
			MaxAttempts:      3,
			RetryBackoffBase: Duration{100 * time.Millisecond},
		},
		Sandbox: Sandbox{
			Driver: SandboxDriverProcess,
		},
		SCM: SCM{
			Driver: SCMDriverLocalGit,
		},
		Policy: Policy{
			AllowLocalCommits: true,
		},
		Outbox: Outbox{
			BatchSize:        100,
			Interval:         Duration{250 * time.Millisecond},
			Lease:            Duration{30 * time.Second},
			MaxAttempts:      5,
			RetryBackoffBase: Duration{500 * time.Millisecond},
		},
		Worker: Worker{
			BatchSize:      8,
			Interval:       Duration{250 * time.Millisecond},
			Lease:          Duration{30 * time.Second},
			HeartbeatEvery: Duration{5 * time.Second},
			CleanupTimeout: Duration{10 * time.Second},
			Concurrency:    4,
			MaxAttempts:    3,
		},
		Log: Log{
			Level:  LogInfo,
			Format: LogFormatText,
		},
	}
}

// Validate checks cross-field invariants after all layers are applied.
func (c Config) Validate() error {
	if c.Server.HTTPAddr == "" {
		return fmt.Errorf("server.http_addr must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.Server.HTTPAddr); err != nil {
		return fmt.Errorf("server.http_addr %q is not host:port", c.Server.HTTPAddr)
	}
	if c.Server.ReadTimeout.Duration <= 0 || c.Server.WriteTimeout.Duration <= 0 ||
		c.Server.IdleTimeout.Duration <= 0 || c.Server.ShutdownTimeout.Duration <= 0 ||
		c.Server.ReadinessTimeout.Duration <= 0 {
		return fmt.Errorf("server timeouts must be positive")
	}
	// Dev-header auth trusts unauthenticated identity headers (ADR-0004), so
	// it may only serve loopback binds: anything routable beyond this host
	// would expose tenant switching to the network. Enforced here so an
	// operator cannot ship the development posture to a real interface.
	if c.Server.DevHeaderAuth {
		host, _, err := net.SplitHostPort(c.Server.HTTPAddr)
		if err != nil {
			return fmt.Errorf("server.http_addr %q is not host:port", c.Server.HTTPAddr)
		}
		if !isLoopbackHost(host) {
			return fmt.Errorf("server.dev_header_auth must not listen on %q: bind a loopback address (127.0.0.1, ::1, localhost) or disable dev auth and deploy OIDC", c.Server.HTTPAddr)
		}
	}
	switch c.Store.Mode {
	case StoreModePostgres:
		if c.Store.PostgresDSN.Expose() == "" {
			return fmt.Errorf("store.postgres_dsn is required when store.mode is postgres (set ANTS_STORE_POSTGRES_DSN)")
		}
		if err := c.Store.PostgresPool.Validate(); err != nil {
			return err
		}
	case StoreModeMemory:
	default:
		return fmt.Errorf("store.mode %q is not supported", c.Store.Mode)
	}
	if c.Orchestrator.MaxParallelTasks < 1 || c.Orchestrator.MaxParallelTasks > 64 {
		return fmt.Errorf("orchestrator.max_parallel_tasks must be within [1,64], got %d", c.Orchestrator.MaxParallelTasks)
	}
	if c.Orchestrator.MaxTasksPerRun < 1 || c.Orchestrator.MaxTasksPerRun > 64 {
		return fmt.Errorf("orchestrator.max_tasks_per_run must be within [1,64], got %d", c.Orchestrator.MaxTasksPerRun)
	}
	if c.Orchestrator.MaxExecOpsPerRun < 1 || c.Orchestrator.MaxExecOpsPerRun > 4096 {
		return fmt.Errorf("orchestrator.max_exec_ops_per_run must be within [1,4096], got %d", c.Orchestrator.MaxExecOpsPerRun)
	}
	if c.Orchestrator.TaskTimeout.Duration <= 0 || c.Orchestrator.StageTimeout.Duration <= 0 {
		return fmt.Errorf("orchestrator timeouts must be positive")
	}
	if c.Orchestrator.MaxAttempts < 1 || c.Orchestrator.MaxAttempts > 10 {
		return fmt.Errorf("orchestrator.max_attempts must be within [1,10], got %d", c.Orchestrator.MaxAttempts)
	}
	if c.Orchestrator.RetryBackoffBase.Duration < 0 {
		return fmt.Errorf("orchestrator.retry_backoff_base must not be negative")
	}
	switch c.Sandbox.Driver {
	case SandboxDriverProcess, SandboxDriverFake:
	default:
		return fmt.Errorf("sandbox.driver %q is not supported", c.Sandbox.Driver)
	}
	switch c.SCM.Driver {
	case SCMDriverMemory, SCMDriverLocalGit:
	default:
		return fmt.Errorf("scm.driver %q is not supported", c.SCM.Driver)
	}
	if err := c.Outbox.Validate(); err != nil {
		return err
	}
	if err := c.Worker.Validate(); err != nil {
		return err
	}
	switch c.Log.Level {
	case LogDebug, LogInfo, LogWarn, LogError:
	default:
		return fmt.Errorf("log.level %q is not supported", c.Log.Level)
	}
	switch c.Log.Format {
	case LogFormatText, LogFormatJSON:
	default:
		return fmt.Errorf("log.format %q is not supported", c.Log.Format)
	}
	return nil
}

// isLoopbackHost reports whether a literal host binds only this machine.
// Only literal loopback IPs and the reserved name "localhost" qualify; other
// names would require DNS resolution inside validation, which must stay
// deterministic and offline.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Redacted returns a copy whose secrets render as placeholders under any
// serialization. It is the only form allowed in logs and error reports.
func (c Config) Redacted() Config {
	out := c
	out.Store.PostgresDSN = Secret(RedactedPlaceholder)
	return out
}

// Diagnostics renders the effective configuration as redacted JSON.
func (c Config) Diagnostics() string {
	b, err := json.MarshalIndent(c.Redacted(), "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Environment lookup seam so tests can inject variables without mutating the
// process environment.
type LookupFunc func(key string) (string, bool)

// ApplyEnv overlays ANTS_* variables onto cfg. Unknown ANTS_ variables are
// reported as errors: a typo'd override must fail loudly instead of silently
// running with defaults.
func (c *Config) ApplyEnv(lookup LookupFunc) error {
	str := func(key string, dst *string) error {
		if v, ok := lookup(key); ok && v != "" {
			*dst = v
		}
		return nil
	}
	boolVar := func(key string, dst *bool) error {
		v, ok := lookup(key)
		if !ok || v == "" {
			return nil
		}
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: expected boolean, got %q", key, v)
		}
		*dst = parsed
		return nil
	}
	intVar := func(key string, dst *int) error {
		v, ok := lookup(key)
		if !ok || v == "" {
			return nil
		}
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: expected integer, got %q", key, v)
		}
		*dst = parsed
		return nil
	}
	durVar := func(key string, dst *Duration) error {
		v, ok := lookup(key)
		if !ok || v == "" {
			return nil
		}
		if err := dst.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		return nil
	}

	if err := str(envServerAddr, &c.Server.HTTPAddr); err != nil {
		return err
	}
	if err := boolVar(envServerDevAuth, &c.Server.DevHeaderAuth); err != nil {
		return err
	}
	if err := durVar(envServerIdleTimeout, &c.Server.IdleTimeout); err != nil {
		return err
	}
	if err := durVar(envServerReadinessTimeout, &c.Server.ReadinessTimeout); err != nil {
		return err
	}
	mode := string(c.Store.Mode)
	if err := str(envStoreMode, &mode); err != nil {
		return err
	}
	c.Store.Mode = StoreMode(mode)
	if dsn, ok := lookup(envStorePostgresDSN); ok {
		c.Store.PostgresDSN = Secret(dsn)
	}
	if err := intVar(envStorePgMaxOpen, &c.Store.PostgresPool.MaxOpenConns); err != nil {
		return err
	}
	if err := intVar(envStorePgMaxIdle, &c.Store.PostgresPool.MaxIdleConns); err != nil {
		return err
	}
	if err := intVar(envOrchMaxParallel, &c.Orchestrator.MaxParallelTasks); err != nil {
		return err
	}
	if err := intVar(envOrchMaxTasksRun, &c.Orchestrator.MaxTasksPerRun); err != nil {
		return err
	}
	if err := intVar(envOrchMaxExecOpsRun, &c.Orchestrator.MaxExecOpsPerRun); err != nil {
		return err
	}
	if err := durVar(envOrchTaskTimeout, &c.Orchestrator.TaskTimeout); err != nil {
		return err
	}
	if err := durVar(envOrchStageTimeout, &c.Orchestrator.StageTimeout); err != nil {
		return err
	}
	if err := intVar(envOrchMaxAttempts, &c.Orchestrator.MaxAttempts); err != nil {
		return err
	}
	if err := durVar(envOrchRetryBackoff, &c.Orchestrator.RetryBackoffBase); err != nil {
		return err
	}
	driver := string(c.Sandbox.Driver)
	if err := str(envSandboxDriver, &driver); err != nil {
		return err
	}
	c.Sandbox.Driver = SandboxDriverKind(driver)
	if err := str(envSandboxWorkRoot, &c.Sandbox.WorkRoot); err != nil {
		return err
	}
	scmDriver := string(c.SCM.Driver)
	if err := str(envSCMDriver, &scmDriver); err != nil {
		return err
	}
	c.SCM.Driver = SCMDriverKind(scmDriver)
	if err := boolVar(envPolicyAllowCommits, &c.Policy.AllowLocalCommits); err != nil {
		return err
	}
	if err := intVar(envOutboxBatchSize, &c.Outbox.BatchSize); err != nil {
		return err
	}
	if err := durVar(envOutboxInterval, &c.Outbox.Interval); err != nil {
		return err
	}
	if err := durVar(envOutboxLease, &c.Outbox.Lease); err != nil {
		return err
	}
	if err := intVar(envOutboxMaxAttempts, &c.Outbox.MaxAttempts); err != nil {
		return err
	}
	if err := durVar(envOutboxBackoff, &c.Outbox.RetryBackoffBase); err != nil {
		return err
	}
	if err := intVar(envWorkerBatchSize, &c.Worker.BatchSize); err != nil {
		return err
	}
	if err := durVar(envWorkerInterval, &c.Worker.Interval); err != nil {
		return err
	}
	if err := durVar(envWorkerLease, &c.Worker.Lease); err != nil {
		return err
	}
	if err := durVar(envWorkerHeartbeat, &c.Worker.HeartbeatEvery); err != nil {
		return err
	}
	if err := durVar(envWorkerCleanup, &c.Worker.CleanupTimeout); err != nil {
		return err
	}
	if err := intVar(envWorkerConcurrency, &c.Worker.Concurrency); err != nil {
		return err
	}
	if err := intVar(envWorkerMaxAttempts, &c.Worker.MaxAttempts); err != nil {
		return err
	}
	level := string(c.Log.Level)
	if err := str(envLogLevel, &level); err != nil {
		return err
	}
	c.Log.Level = LogLevel(level)
	format := string(c.Log.Format)
	if err := str(envLogFormat, &format); err != nil {
		return err
	}
	c.Log.Format = LogFormat(format)

	if unknown := unknownAntsVars(); len(unknown) > 0 {
		return fmt.Errorf("unknown ANTS_ environment variables: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// Environment variable names. Kept in one place so docs and code cannot drift.
const (
	envServerAddr             = "ANTS_SERVER_HTTP_ADDR"
	envServerDevAuth          = "ANTS_SERVER_DEV_AUTH"
	envServerIdleTimeout      = "ANTS_SERVER_IDLE_TIMEOUT"
	envServerReadinessTimeout = "ANTS_SERVER_READINESS_TIMEOUT"
	envStoreMode              = "ANTS_STORE_MODE"
	envStorePostgresDSN       = "ANTS_STORE_POSTGRES_DSN"
	envStorePgMaxOpen         = "ANTS_STORE_POOL_MAX_OPEN_CONNS"
	envStorePgMaxIdle         = "ANTS_STORE_POOL_MAX_IDLE_CONNS"
	envOrchMaxParallel        = "ANTS_ORCHESTRATOR_MAX_PARALLEL_TASKS"
	envOrchMaxTasksRun        = "ANTS_ORCHESTRATOR_MAX_TASKS_PER_RUN"
	envOrchMaxExecOpsRun      = "ANTS_ORCHESTRATOR_MAX_EXEC_OPS_PER_RUN"
	envOrchTaskTimeout        = "ANTS_ORCHESTRATOR_TASK_TIMEOUT"
	envOrchStageTimeout       = "ANTS_ORCHESTRATOR_STAGE_TIMEOUT"
	envOrchMaxAttempts        = "ANTS_ORCHESTRATOR_MAX_ATTEMPTS"
	envOrchRetryBackoff       = "ANTS_ORCHESTRATOR_RETRY_BACKOFF_BASE"
	envSandboxDriver          = "ANTS_SANDBOX_DRIVER"
	envSandboxWorkRoot        = "ANTS_SANDBOX_WORK_ROOT"
	envSCMDriver              = "ANTS_SCM_DRIVER"
	envPolicyAllowCommits     = "ANTS_POLICY_ALLOW_LOCAL_COMMITS"
	envOutboxBatchSize        = "ANTS_OUTBOX_BATCH_SIZE"
	envOutboxInterval         = "ANTS_OUTBOX_INTERVAL"
	envOutboxLease            = "ANTS_OUTBOX_LEASE"
	envOutboxMaxAttempts      = "ANTS_OUTBOX_MAX_ATTEMPTS"
	envOutboxBackoff          = "ANTS_OUTBOX_RETRY_BACKOFF_BASE"
	envWorkerBatchSize        = "ANTS_WORKER_BATCH_SIZE"
	envWorkerInterval         = "ANTS_WORKER_INTERVAL"
	envWorkerLease            = "ANTS_WORKER_LEASE"
	envWorkerHeartbeat        = "ANTS_WORKER_HEARTBEAT_EVERY"
	envWorkerCleanup          = "ANTS_WORKER_CLEANUP_TIMEOUT"
	envWorkerConcurrency      = "ANTS_WORKER_CONCURRENCY"
	envWorkerMaxAttempts      = "ANTS_WORKER_MAX_ATTEMPTS"
	envLogLevel               = "ANTS_LOG_LEVEL"
	envLogFormat              = "ANTS_LOG_FORMAT"
)

var knownEnvVars = map[string]bool{
	envServerAddr: true, envServerDevAuth: true,
	envServerIdleTimeout: true, envServerReadinessTimeout: true,
	envStoreMode: true, envStorePostgresDSN: true,
	envStorePgMaxOpen: true, envStorePgMaxIdle: true,
	envOrchMaxParallel: true, envOrchMaxTasksRun: true, envOrchMaxExecOpsRun: true,
	envOrchTaskTimeout: true, envOrchStageTimeout: true,
	envOrchMaxAttempts: true, envOrchRetryBackoff: true,
	envSandboxDriver: true, envSandboxWorkRoot: true, envSCMDriver: true,
	envPolicyAllowCommits: true, envLogLevel: true, envLogFormat: true,
	envOutboxBatchSize: true, envOutboxInterval: true, envOutboxLease: true,
	envOutboxMaxAttempts: true, envOutboxBackoff: true,
	envWorkerBatchSize: true, envWorkerInterval: true, envWorkerLease: true,
	envWorkerHeartbeat: true, envWorkerCleanup: true, envWorkerConcurrency: true,
	envWorkerMaxAttempts: true,
}

// unknownAntsVars scans the process environment so a mistyped override fails
// loudly at startup rather than being ignored.
func unknownAntsVars() []string {
	var unknown []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "ANTS_") || knownEnvVars[name] {
			continue
		}
		unknown = append(unknown, name)
	}
	return unknown
}
