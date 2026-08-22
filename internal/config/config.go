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
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`
}

type Store struct {
	Mode           StoreMode `yaml:"mode"`
	PostgresDSNEnv string    `yaml:"-"` // populated from ANTS_STORE_POSTGRES_DSN only
	PostgresDSN    Secret    `yaml:"-"`
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
	Log          Log          `yaml:"log"`
}

// Defaults are safe: local-only bind address, memory store, deny-by-default
// posture for anything crossing a trust boundary. Dev auth is off by default;
// enabling it is an explicit operator decision.
func Defaults() Config {
	return Config{
		Server: Server{
			HTTPAddr:        "127.0.0.1:8080",
			DevHeaderAuth:   false,
			ReadTimeout:     Duration{10 * time.Second},
			WriteTimeout:    Duration{30 * time.Second},
			ShutdownTimeout: Duration{10 * time.Second},
		},
		Store: Store{
			Mode: StoreModeMemory,
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
	if c.Server.ReadTimeout.Duration <= 0 || c.Server.WriteTimeout.Duration <= 0 || c.Server.ShutdownTimeout.Duration <= 0 {
		return fmt.Errorf("server timeouts must be positive")
	}
	switch c.Store.Mode {
	case StoreModePostgres:
		if c.Store.PostgresDSN.Expose() == "" {
			return fmt.Errorf("store.postgres_dsn is required when store.mode is postgres (set ANTS_STORE_POSTGRES_DSN)")
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
	mode := string(c.Store.Mode)
	if err := str(envStoreMode, &mode); err != nil {
		return err
	}
	c.Store.Mode = StoreMode(mode)
	if dsn, ok := lookup(envStorePostgresDSN); ok {
		c.Store.PostgresDSN = Secret(dsn)
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
	envServerAddr         = "ANTS_SERVER_HTTP_ADDR"
	envServerDevAuth      = "ANTS_SERVER_DEV_AUTH"
	envStoreMode          = "ANTS_STORE_MODE"
	envStorePostgresDSN   = "ANTS_STORE_POSTGRES_DSN"
	envOrchMaxParallel    = "ANTS_ORCHESTRATOR_MAX_PARALLEL_TASKS"
	envOrchMaxTasksRun    = "ANTS_ORCHESTRATOR_MAX_TASKS_PER_RUN"
	envOrchMaxExecOpsRun  = "ANTS_ORCHESTRATOR_MAX_EXEC_OPS_PER_RUN"
	envOrchTaskTimeout    = "ANTS_ORCHESTRATOR_TASK_TIMEOUT"
	envOrchStageTimeout   = "ANTS_ORCHESTRATOR_STAGE_TIMEOUT"
	envOrchMaxAttempts    = "ANTS_ORCHESTRATOR_MAX_ATTEMPTS"
	envOrchRetryBackoff   = "ANTS_ORCHESTRATOR_RETRY_BACKOFF_BASE"
	envSandboxDriver      = "ANTS_SANDBOX_DRIVER"
	envSandboxWorkRoot    = "ANTS_SANDBOX_WORK_ROOT"
	envSCMDriver          = "ANTS_SCM_DRIVER"
	envPolicyAllowCommits = "ANTS_POLICY_ALLOW_LOCAL_COMMITS"
	envLogLevel           = "ANTS_LOG_LEVEL"
	envLogFormat          = "ANTS_LOG_FORMAT"
)

var knownEnvVars = map[string]bool{
	envServerAddr: true, envServerDevAuth: true,
	envStoreMode: true, envStorePostgresDSN: true,
	envOrchMaxParallel: true, envOrchMaxTasksRun: true, envOrchMaxExecOpsRun: true,
	envOrchTaskTimeout: true, envOrchStageTimeout: true,
	envOrchMaxAttempts: true, envOrchRetryBackoff: true,
	envSandboxDriver: true, envSandboxWorkRoot: true, envSCMDriver: true,
	envPolicyAllowCommits: true, envLogLevel: true, envLogFormat: true,
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
