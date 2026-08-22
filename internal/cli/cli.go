// Package cli implements the ants command line: version, config validation,
// migrations, the API server, and the deterministic demo pipeline.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
)

// Version is overridable at build time via -ldflags.
var Version = "dev"

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Main runs one CLI invocation and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "ants %s\n", Version)
		return exitOK
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "migrate":
		return runMigrate(args[1:], stdout, stderr)
	case "serve":
		return RunServe(args[1:], stdout, stderr)
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `ants - durable software-engineering agent platform

Usage:
  ants <command> [flags]

Commands:
  version          print the build version
  config validate  load and validate a configuration file
  migrate up       apply PostgreSQL migrations (requires store.mode postgres)
  serve            start the /v1 API server
  demo run         execute the deterministic vertical slice locally

Use "ants <command> -h" for command-specific flags.
`)
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: ants config validate [--config <path>]")
		return exitUsage
	}
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML configuration file (optional)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration invalid:\n%v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, "configuration valid")
	fmt.Fprintln(stdout, cfg.Diagnostics())
	return exitOK
}

func runMigrate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "up" {
		fmt.Fprintln(stderr, "usage: ants migrate up --config <path>")
		return exitUsage
	}
	fs := flag.NewFlagSet("migrate up", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML configuration file")
	dsn := fs.String("dsn", "", "PostgreSQL DSN (overrides ANTS_STORE_POSTGRES_DSN)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration invalid:\n%v\n", err)
		return exitFailure
	}
	effectiveDSN := *dsn
	if effectiveDSN == "" {
		effectiveDSN = cfg.Store.PostgresDSN.Expose()
	}
	if effectiveDSN == "" {
		fmt.Fprintln(stderr, "migrate requires a PostgreSQL DSN via --dsn or ANTS_STORE_POSTGRES_DSN")
		return exitFailure
	}
	applied, pending, err := applyMigrations(context.Background(), effectiveDSN)
	if err != nil {
		fmt.Fprintf(stderr, "migration failed:\n%v\n", err)
		return exitFailure
	}
	for _, a := range applied {
		fmt.Fprintf(stdout, "applied %s\n", a)
	}
	for _, p := range pending {
		fmt.Fprintf(stdout, "pending %s\n", p)
	}
	if len(applied) == 0 && len(pending) == 0 {
		fmt.Fprintln(stdout, "schema up to date")
	}
	return exitOK
}

// RunServe starts the API server and blocks until SIGINT/SIGTERM.
func RunServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML configuration file")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration invalid:\n%v\n", err)
		return exitFailure
	}
	application, err := app.Build(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "failed to start:\n%v\n", err)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := newServer(application)
	if err != nil {
		fmt.Fprintf(stderr, "failed to start server:\n%v\n", err)
		return exitFailure
	}

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.Start() }()

	// Drain the durable outbox until shutdown; delivery is at-least-once
	// (ADR-0011), so stopping mid-batch only defers work, never loses it.
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		if err := application.Outbox.Run(dispatchCtx); err != nil && dispatchCtx.Err() == nil {
			application.Logger.Error("outbox dispatcher stopped unexpectedly", "error", err.Error())
		}
	}()
	defer func() {
		stopDispatch()
		<-dispatchDone
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			fmt.Fprintf(stderr, "server error: %v\n", err)
			return exitFailure
		}
		return exitOK
	case <-ctx.Done():
		application.Logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), application.Config.Server.ShutdownTimeout.Duration)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "graceful shutdown failed: %v\n", err)
		return exitFailure
	}
	return exitOK
}
