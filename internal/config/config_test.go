package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookupFrom(mapEnv map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		v, ok := mapEnv[key]
		return v, ok
	}
}

func TestDefaultsValidate(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if cfg.Server.DevHeaderAuth {
		t.Fatalf("dev auth must default to off")
	}
	if cfg.Store.Mode != StoreModeMemory {
		t.Fatalf("memory store is the safe default")
	}
}

func TestLayeringFileThenEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ants.yaml")
	file := `
server:
  http_addr: "127.0.0.1:9999"
orchestrator:
  max_parallel_tasks: 8
  task_timeout: 90s
`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, lookupFrom(map[string]string{
		"ANTS_ORCHESTRATOR_MAX_PARALLEL_TASKS": "2",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:9999" {
		t.Fatalf("file layer not applied: %s", cfg.Server.HTTPAddr)
	}
	if cfg.Orchestrator.MaxParallelTasks != 2 {
		t.Fatalf("env must override file, got %d", cfg.Orchestrator.MaxParallelTasks)
	}
	if cfg.Orchestrator.TaskTimeout.Duration != 90*time.Second {
		t.Fatalf("file duration not applied: %s", cfg.Orchestrator.TaskTimeout)
	}
}

func TestUnknownYAMLKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ants.yaml")
	if err := os.WriteFile(path, []byte("server:\n  http_addr: \"x\"\n  typo_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, lookupFrom(nil)); err == nil || !strings.Contains(err.Error(), "typo_key") {
		t.Fatalf("unknown key must fail loudly, got %v", err)
	}
}

// TestMetricsYAMLSection covers the metrics block: the enabled flag parses
// from file and unknown keys inside the section fail under strict decoding.
func TestMetricsYAMLSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ants.yaml")
	if err := os.WriteFile(path, []byte("metrics:\n  enbled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, lookupFrom(nil)); err == nil || !strings.Contains(err.Error(), "enbled") {
		t.Fatalf("unknown key in metrics section must fail loudly, got %v", err)
	}
	path = filepath.Join(dir, "ants-off.yaml")
	if err := os.WriteFile(path, []byte("metrics:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, lookupFrom(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Metrics.Enabled {
		t.Fatal("metrics.enabled=false from file must be retained")
	}
}

func TestUnknownEnvVarRejected(t *testing.T) {
	t.Setenv("ANTS_ORCHESTRATOR_MAX_PARALEL_TASKS", "3") // deliberate typo
	if _, err := load("", os.LookupEnv); err == nil || !strings.Contains(err.Error(), "ANTS_ORCHESTRATOR_MAX_PARALEL_TASKS") {
		t.Fatalf("unknown ANTS_ var must fail loudly, got %v", err)
	}
}

func TestPostgresModeRequiresDSN(t *testing.T) {
	_, err := loadWithLookupConfig(StoreModePostgres, "")
	if err == nil || !strings.Contains(err.Error(), "ANTS_STORE_POSTGRES_DSN") {
		t.Fatalf("postgres mode without DSN must fail, got %v", err)
	}
	cfg, err := loadWithLookupConfig(StoreModePostgres, "postgres://localhost/ants")
	if err != nil {
		t.Fatalf("postgres mode with DSN must load: %v", err)
	}
	if cfg.Store.PostgresDSN.Expose() != "postgres://localhost/ants" {
		t.Fatalf("dsn not retained")
	}
}

func loadWithLookupConfig(mode StoreMode, dsn string) (Config, error) {
	return load("", lookupFrom(map[string]string{
		"ANTS_STORE_MODE":         string(mode),
		"ANTS_STORE_POSTGRES_DSN": dsn,
	}))
}

func TestSecretNeverLeaks(t *testing.T) {
	cfg := Config{}
	cfg.Store.PostgresDSN = Secret("postgres://user:hunter2@db/ants")
	rendered := cfg.Diagnostics()
	if strings.Contains(rendered, "hunter2") {
		t.Fatalf("diagnostics leaked secret: %s", rendered)
	}
	if !strings.Contains(rendered, RedactedPlaceholder) {
		t.Fatalf("diagnostics should show placeholder")
	}
	if cfg.Store.PostgresDSN.String() != RedactedPlaceholder {
		t.Fatalf("String() must redact")
	}
	if rendered == "{}" {
		t.Fatalf("diagnostics should render structure")
	}
}

func TestValidationFailures(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Server.HTTPAddr = "" },
		func(c *Config) { c.Server.HTTPAddr = "no-port" },
		func(c *Config) { c.Server.ReadTimeout = Duration{} },
		func(c *Config) { c.Server.IdleTimeout = Duration{} },
		func(c *Config) { c.Server.ReadinessTimeout = Duration{} },
		func(c *Config) { c.Store.Mode = "sqlite" },
		func(c *Config) { c.Orchestrator.MaxParallelTasks = 0 },
		func(c *Config) { c.Orchestrator.MaxParallelTasks = 65 },
		func(c *Config) { c.Orchestrator.MaxAttempts = 11 },
		func(c *Config) { c.Sandbox.Driver = "docker" },
		func(c *Config) { c.SCM.Driver = "github_live" },
		func(c *Config) { c.Worker.BatchSize = 0 },
		func(c *Config) { c.Worker.Concurrency = 65 },
		func(c *Config) { c.Worker.MaxAttempts = 11 },
		// A heartbeat interval without margin inside the lease would expire
		// live workers after two missed beats.
		func(c *Config) {
			c.Worker.Lease = Duration{2 * time.Second}
			c.Worker.HeartbeatEvery = Duration{time.Second}
		},
		func(c *Config) { c.Log.Level = "verbose" },
		func(c *Config) { c.Log.Format = "csv" },
	}
	for i, mutate := range cases {
		cfg := Defaults()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: expected validation failure", i)
		}
	}
}

// TestDevHeaderAuthConfinedToLoopback pins the ADR-0004 production gate: the
// development posture that trusts unauthenticated identity headers can never
// serve a bind reachable beyond this machine. Startup fails instead of
// exposing tenant switching to the network.
func TestDevHeaderAuthConfinedToLoopback(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8080",
		"127.9.9.9:8080",
		"[::1]:8080",
		"localhost:8080",
	}
	for _, addr := range loopback {
		cfg := Defaults()
		cfg.Server.DevHeaderAuth = true
		cfg.Server.HTTPAddr = addr
		if err := cfg.Validate(); err != nil {
			t.Fatalf("dev auth on loopback %s must validate: %v", addr, err)
		}
	}

	exposed := []string{
		"0.0.0.0:8080",
		"[::]:8080",
		"192.168.1.10:8080",
		"ants.example.com:8080",
	}
	for _, addr := range exposed {
		cfg := Defaults()
		cfg.Server.DevHeaderAuth = true
		cfg.Server.HTTPAddr = addr
		if err := cfg.Validate(); err == nil {
			t.Fatalf("dev auth on non-loopback %s must fail startup", addr)
		} else if !strings.Contains(err.Error(), "dev_header_auth") {
			t.Fatalf("refusal must name dev_header_auth: %v", err)
		}

		// The same binds are fine without the development posture.
		cfg.Server.DevHeaderAuth = false
		if err := cfg.Validate(); err != nil {
			t.Fatalf("non-loopback bind without dev auth must validate: %v", err)
		}
	}
}

func TestWorkerEnvLayering(t *testing.T) {
	cfg, err := load("", lookupFrom(map[string]string{
		"ANTS_WORKER_BATCH_SIZE":      "16",
		"ANTS_WORKER_INTERVAL":        "1s",
		"ANTS_WORKER_LEASE":           "45s",
		"ANTS_WORKER_HEARTBEAT_EVERY": "7s",
		"ANTS_WORKER_CLEANUP_TIMEOUT": "3s",
		"ANTS_WORKER_CONCURRENCY":     "9",
		"ANTS_WORKER_MAX_ATTEMPTS":    "5",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Worker.BatchSize != 16 || cfg.Worker.Concurrency != 9 || cfg.Worker.MaxAttempts != 5 {
		t.Fatalf("int env not applied: %+v", cfg.Worker)
	}
	if cfg.Worker.Interval.Duration != time.Second ||
		cfg.Worker.Lease.Duration != 45*time.Second ||
		cfg.Worker.HeartbeatEvery.Duration != 7*time.Second ||
		cfg.Worker.CleanupTimeout.Duration != 3*time.Second {
		t.Fatalf("duration env not applied: %+v", cfg.Worker)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("layered worker config must validate: %v", err)
	}
}

func TestServerEnvLayering(t *testing.T) {
	cfg, err := load("", lookupFrom(map[string]string{
		"ANTS_SERVER_IDLE_TIMEOUT":      "45s",
		"ANTS_SERVER_READINESS_TIMEOUT": "750ms",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.IdleTimeout.Duration != 45*time.Second {
		t.Fatalf("idle timeout env not applied: %s", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ReadinessTimeout.Duration != 750*time.Millisecond {
		t.Fatalf("readiness timeout env not applied: %s", cfg.Server.ReadinessTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("layered server config must validate: %v", err)
	}
}

func TestMetricsEnvLayering(t *testing.T) {
	cfg, err := load("", lookupFrom(map[string]string{"ANTS_METRICS_ENABLED": "false"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Metrics.Enabled {
		t.Fatalf("metrics must be disableable via environment: %+v", cfg.Metrics)
	}
	cfg, err = load("", lookupFrom(map[string]string{"ANTS_METRICS_ENABLED": "true"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Metrics.Enabled {
		t.Fatalf("metrics env override not applied: %+v", cfg.Metrics)
	}
}

func TestDurationParsing(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("1500ms")); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Duration != 1500*time.Millisecond {
		t.Fatalf("wrong value: %s", d)
	}
	if err := d.UnmarshalText([]byte("soon")); err == nil {
		t.Fatalf("invalid duration accepted")
	}
}
