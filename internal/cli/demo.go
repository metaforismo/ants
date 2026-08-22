package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/sandbox"
)

// runDemo executes the complete vertical slice in one process: request ->
// plan/spec -> isolated parallel tasks -> integration -> tests -> report.
// It uses the memory store with real command execution and prints the event
// timeline plus the final evidence-based verdict.
func runDemo(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: ants demo run [--out <dir>] [--scm local_git|memory] [--sandbox process|fake]")
		return exitUsage
	}
	fs := flag.NewFlagSet("demo run", flag.ContinueOnError)
	outDir := fs.String("out", "", "directory for report.json and integrated.diff (default: a new temporary directory)")
	scmFlag := fs.String("scm", "local_git", "SCM driver: local_git or memory")
	sandboxFlag := fs.String("sandbox", "process", "sandbox driver: process or fake")
	fs.SetOutput(stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}

	cfg := config.Defaults()
	switch *scmFlag {
	case "local_git":
		cfg.SCM.Driver = config.SCMDriverLocalGit
	case "memory":
		cfg.SCM.Driver = config.SCMDriverMemory
	default:
		fmt.Fprintf(stderr, "unknown --scm %q\n", *scmFlag)
		return exitUsage
	}
	switch *sandboxFlag {
	case "process":
		cfg.Sandbox.Driver = config.SandboxDriverProcess
	case "fake":
		cfg.Sandbox.Driver = config.SandboxDriverFake
	default:
		fmt.Fprintf(stderr, "unknown --sandbox %q\n", *sandboxFlag)
		return exitUsage
	}
	// The engine refuses fake-sandbox + non-memory SCM pairing; keep the CLI
	// message clearer than the internal one.
	if cfg.Sandbox.Driver == config.SandboxDriverFake && cfg.SCM.Driver != config.SCMDriverMemory {
		fmt.Fprintln(stderr, "--sandbox fake requires --scm memory")
		return exitUsage
	}

	world, err := appBuildForDemo(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "demo setup failed:\n%v\n", err)
		return exitFailure
	}
	// In fake mode the fixture declares its own scripted outcomes; real
	// drivers execute actual commands instead.
	if cfg.Sandbox.Driver == config.SandboxDriverFake {
		fake, ok := world.application.Sandbox.(*sandbox.FakeDriver)
		if !ok {
			fmt.Fprintln(stderr, "demo setup failed: fake driver wiring broken")
			return exitFailure
		}
		if serr := fixtures.ScriptFake(fake); serr != nil {
			fmt.Fprintf(stderr, "demo setup failed:\n%v\n", serr)
			return exitFailure
		}
	}

	started := time.Now()
	runID, execErr := world.execute(context.Background(), func(line string) {
		fmt.Fprintln(stdout, line)
	})
	if execErr != nil {
		fmt.Fprintf(stderr, "\ndemo run failed:\n%v\n", execErr)
	}

	run, getErr := world.application.Repos.Runs.Get(context.Background(), world.tenant.ID, runID)
	if getErr != nil {
		fmt.Fprintf(stderr, "internal error reading run: %v\n", getErr)
		return exitFailure
	}
	fmt.Fprintf(stdout, "\nrun %s finished in %s with status %s\n",
		run.ID, time.Since(started).Round(time.Millisecond), run.Status)
	printReportSummary(stdout, run)

	dir := *outDir
	if dir == "" {
		tmp, tmpErr := os.MkdirTemp("", "ants-demo-*")
		if tmpErr != nil {
			fmt.Fprintf(stderr, "warning: no artifact directory: %v\n", tmpErr)
		} else {
			dir = tmp
		}
	}
	if dir != "" {
		if werr := writeArtifacts(world, run.ID, dir); werr != nil {
			fmt.Fprintf(stderr, "warning: could not persist artifacts: %v\n", werr)
		} else {
			fmt.Fprintf(stdout, "artifacts written to %s\n", dir)
		}
	}

	if execErr != nil || run.Status != domain.RunCompleted || run.Report == nil || !run.Report.ReadyForReview {
		return exitFailure
	}
	return exitOK
}

func printReportSummary(w io.Writer, run *domain.Run) {
	report := run.Report
	if report == nil {
		fmt.Fprintln(w, "no report produced")
		return
	}
	fmt.Fprintf(w, "\nreport\n------\n")
	fmt.Fprintf(w, "summary:          %s\n", report.Summary)
	fmt.Fprintf(w, "ready_for_review: %v\n", report.ReadyForReview)
	fmt.Fprintf(w, "integration:      branch=%s sha=%s\n", report.Integration.Branch, short(report.Integration.SHA))
	for _, t := range report.Tasks {
		line := fmt.Sprintf("task %-20s status=%-9s attempts=%d branch=%s",
			t.Name, t.Status, t.Attempts, t.Branch)
		if t.CommitSHA != "" {
			line += " commit=" + short(t.CommitSHA)
		}
		fmt.Fprintln(w, line)
	}
	for _, ev := range report.Verification.Evidence {
		status := "PASS"
		if !ev.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(w, "evidence %-4s criterion=%q cmd=%q exit=%d\n",
			status, ev.Criterion, strings.Join(ev.Command, " "), ev.ExitCode)
	}
	for _, f := range report.Findings {
		fmt.Fprintf(w, "finding %-8s category=%s location=%s scenario=%s\n",
			f.Severity, f.Category, f.Location, f.Scenario)
	}
	fmt.Fprintf(w, "budget:           tasks %d/%d exec-ops %d/%d\n",
		report.Budget.TasksUsed, report.Budget.MaxTasks,
		report.Budget.ExecOpsUsed, report.Budget.MaxExecOps)
}

func writeArtifacts(world *demoWorld, runID domain.RunID, dir string) error {
	ctx := context.Background()
	artifacts, err := world.application.Repos.Artifacts.ListByRun(ctx, world.tenant.ID, runID)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return mkErr
	}
	for _, a := range artifacts {
		name := ""
		switch a.Kind {
		case domain.ArtifactDiff:
			name = "integrated.diff"
		case domain.ArtifactReport:
			name = "report.json"
		default:
			continue
		}
		content := a.Content
		if a.Kind == domain.ArtifactReport {
			var pretty map[string]any
			if uerr := json.Unmarshal(content, &pretty); uerr == nil {
				if indented, ierr := json.MarshalIndent(pretty, "", "  "); ierr == nil {
					content = indented
				}
			}
		}
		if werr := os.WriteFile(filepath.Join(dir, name), content, 0o600); werr != nil {
			return werr
		}
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
