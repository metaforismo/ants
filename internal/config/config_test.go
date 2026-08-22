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
		func(c *Config) { c.Store.Mode = "sqlite" },
		func(c *Config) { c.Orchestrator.MaxParallelTasks = 0 },
		func(c *Config) { c.Orchestrator.MaxParallelTasks = 65 },
		func(c *Config) { c.Orchestrator.MaxAttempts = 11 },
		func(c *Config) { c.Sandbox.Driver = "docker" },
		func(c *Config) { c.SCM.Driver = "github_live" },
		func(c *Config) { c.Worker.BatchSize = 0 },
		func(c *Config) { c.Worker.Concurrency = 65 },
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

func TestWorkerEnvLayering(t *testing.T) {
	cfg, err := load("", lookupFrom(map[string]string{
		"ANTS_WORKER_BATCH_SIZE":      "16",
		"ANTS_WORKER_INTERVAL":        "1s",
		"ANTS_WORKER_LEASE":           "45s",
		"ANTS_WORKER_HEARTBEAT_EVERY": "7s",
		"ANTS_WORKER_CLEANUP_TIMEOUT": "3s",
		"ANTS_WORKER_CONCURRENCY":     "9",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Worker.BatchSize != 16 || cfg.Worker.Concurrency != 9 {
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
