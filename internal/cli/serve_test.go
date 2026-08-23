package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the serve argument contract. Each one would fail against the
// pre-fix behavior where a leading "serve" positional stopped flag parsing, so
// `ants-api serve --config file` silently served with defaults instead of
// reading the named file (release finding repaired in tranche 3.7).

func TestServeRejectsUnknownPositional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunServe([]string{"serve", "extra"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Fatalf("stderr must name the rejected operand, got %q", stderr.String())
	}
}

func TestServeHonorsConfigFlagWithLeadingSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	code := RunServe([]string{"serve", "--config", missing}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr must name the unread config file, got %q", stderr.String())
	}
}

func TestServeHonorsConfigFlagWithoutLeadingSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	code := RunServe([]string{"--config", missing}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr must name the unread config file, got %q", stderr.String())
	}
}

func TestServeFallsBackToANTSConfigEnv(t *testing.T) {
	t.Setenv("ANTS_CONFIG", filepath.Join(t.TempDir(), "absent-env.yaml"))
	var stdout, stderr bytes.Buffer
	code := RunServe([]string{"serve"}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "absent-env.yaml") {
		t.Fatalf("stderr must show ANTS_CONFIG was consulted, got %q", stderr.String())
	}
}

func TestServeFlagConfigWinsOverANTSConfigEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTS_CONFIG", filepath.Join(dir, "from-env.yaml"))
	var stdout, stderr bytes.Buffer
	code := RunServe([]string{"serve", "--config", filepath.Join(dir, "from-flag.yaml")}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitFailure, stderr.String())
	}
	if strings.Contains(stderr.String(), "from-env.yaml") {
		t.Fatalf("flag path must take precedence over ANTS_CONFIG, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "from-flag.yaml") {
		t.Fatalf("stderr must name the flag-provided file, got %q", stderr.String())
	}
}
