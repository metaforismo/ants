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
