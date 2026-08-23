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
	if cfg.Auth.OIDC.Configured() {
		t.Fatalf("OIDC must default to unconfigured (refuse-all posture)")
	}
	if cfg.Store.Mode != StoreModeMemory {
		t.Fatalf("memory store is the safe default")
	}
	// Retention is inert by default (ADR-0016): no horizon is configured,
	// so an upgraded deployment never starts deleting outbox rows.
	if cfg.Outbox.Retention.Active() {
		t.Fatalf("retention horizons must default to zero/inert, got %+v", cfg.Outbox.Retention)
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
		func(c *Config) { c.Outbox.Retention.BatchSize = 0 },
		func(c *Config) { c.Outbox.Retention.Interval = Duration{} },
		func(c *Config) { c.Outbox.Retention.DeliveredAfter = Duration{-1} },
		// A heartbeat interval without margin inside the lease would expire
		// live workers after two missed beats.
		func(c *Config) {
			c.Worker.Lease = Duration{2 * time.Second}
			c.Worker.HeartbeatEvery = Duration{time.Second}
		},
		func(c *Config) { c.Log.Level = "verbose" },
		func(c *Config) { c.Log.Format = "csv" },
		// OIDC misconfigurations must fail startup, never authenticate nobody
		// silently (ADR-0019).
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("http://127.0.0.1:8081/r").Auth.OIDC
			c.Auth.OIDC.Audience = ""
		},
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("not-a-url").Auth.OIDC
		},
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("http://127.0.0.1:8081/r").Auth.OIDC
			c.Auth.OIDC.JWKSRefreshInterval = Duration{time.Millisecond}
		},
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("http://127.0.0.1:8081/r").Auth.OIDC
			c.Auth.OIDC.ClockSkew = Duration{-1}
		},
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("http://127.0.0.1:8081/r").Auth.OIDC
			c.Auth.OIDC.HTTPTimeout = Duration{time.Minute}
		},
		func(c *Config) {
			c.Auth.OIDC = oidcDefaults("http://127.0.0.1:8081/r").Auth.OIDC
			c.Auth.OIDC.TenantClaim = "tenant slug with spaces"
		},
	}
	for i, mutate := range cases {
		cfg := Defaults()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: expected validation failure", i)
		}
	}
}

// TestOIDCIssuerConfinedToLoopbackOverHTTP pins the ADR-0019 transport rule:
// a plaintext issuer may only be a literal loopback host (local Keycloak),
// while anything remote must be https. The same class of gate ADR-0004 and
// ADR-0013 applied to dev auth now guards the IdP trust root.
func TestOIDCIssuerConfinedToLoopbackOverHTTP(t *testing.T) {
	loopback := []string{
		"http://127.0.0.1:8081/realms/ants",
		"http://127.9.9.9:8081/realms/ants",
		"http://[::1]:8081/realms/ants",
		"http://localhost:8081/realms/ants",
	}
	for _, issuer := range loopback {
		cfg := oidcDefaults(issuer)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("plaintext loopback issuer %s must validate: %v", issuer, err)
		}
	}

	exposed := []string{
		"http://192.168.1.10:8081/realms/ants",
		"http://keycloak.internal:8081/realms/ants",
	}
	for _, issuer := range exposed {
		cfg := oidcDefaults(issuer)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("plaintext non-loopback issuer %s must fail startup", issuer)
		} else if !strings.Contains(err.Error(), "https") {
			t.Fatalf("refusal must name the https requirement: %v", err)
		}
	}
	cfg := oidcDefaults("https://sso.example.com/realms/ants")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("https issuer must validate over any host: %v", err)
	}
}

// TestOIDCPartialConfigurationRejected pins the all-or-nothing rule: an
// audience without an issuer is a typo, not a configuration.
func TestOIDCPartialConfigurationRejected(t *testing.T) {
	cfg := Defaults()
	cfg.Auth.OIDC.Audience = "ants-api"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "issuer_url") {
		t.Fatalf("audience without issuer must fail loudly, got %v", err)
	}
}

func TestOIDCEnvLayering(t *testing.T) {
	cfg, err := load("", lookupFrom(map[string]string{
		"ANTS_AUTH_OIDC_ISSUER_URL":            "http://127.0.0.1:8081/realms/ants",
		"ANTS_AUTH_OIDC_AUDIENCE":              "ants-api",
		"ANTS_AUTH_OIDC_TENANT_CLAIM":          "org_tenant",
		"ANTS_AUTH_OIDC_JWKS_REFRESH_INTERVAL": "5m",
		"ANTS_AUTH_OIDC_CLOCK_SKEW":            "10s",
		"ANTS_AUTH_OIDC_HTTP_TIMEOUT":          "2s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Auth.OIDC.Configured() {
		t.Fatal("issuer env not applied")
	}
	if cfg.Auth.OIDC.Audience != "ants-api" ||
		cfg.Auth.OIDC.TenantClaim != "org_tenant" ||
		cfg.Auth.OIDC.JWKSRefreshInterval.Duration != 5*time.Minute ||
		cfg.Auth.OIDC.ClockSkew.Duration != 10*time.Second ||
		cfg.Auth.OIDC.HTTPTimeout.Duration != 2*time.Second {
		t.Fatalf("oidc env overrides not applied: %+v", cfg.Auth.OIDC)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("layered oidc config must validate: %v", err)
	}
}

func oidcDefaults(issuer string) Config {
	cfg := Defaults()
	cfg.Auth.OIDC.IssuerURL = issuer
	cfg.Auth.OIDC.Audience = "ants-api"
	return cfg
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
