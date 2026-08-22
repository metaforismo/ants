package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// ProcessDriver executes allow-listed commands inside a per-sandbox directory
// rooted under WorkRoot. It provides workspace confinement and command
// allow-listing, NOT a security boundary: a determined process can still
// reach the host. Untrusted-code isolation requires the microVM drivers from
// Horizon 2; this driver exists so the full pipeline is real on day one.
type ProcessDriver struct {
	workRoot string

	mu        sync.Mutex
	sandboxes map[domain.SandboxID]string
}

var _ Driver = (*ProcessDriver)(nil)

// allowedCommands is the fixed v1 execution surface for tasks. It covers the
// deterministic demo pipeline (sh, git, coreutils) and nothing else. Adding a
// binary here widens the trust surface and must be an explicit, reviewed
// change.
var allowedCommands = map[string]bool{
	"sh": true, "bash": true, "git": true,
	"cat": true, "ls": true, "mkdir": true, "cp": true, "mv": true,
	"rm": true, "touch": true, "grep": true, "sed": true, "diff": true,
	"wc": true, "head": true, "tail": true, "sort": true, "uniq": true,
	"test": true, "true": true, "false": true, "echo": true, "pwd": true,
}

func NewProcessDriver(workRoot string) (*ProcessDriver, error) {
	if workRoot == "" {
		base := filepath.Join(os.TempDir(), "ants-sandboxes")
		if err := os.MkdirAll(base, 0o700); err != nil {
			return nil, fmt.Errorf("sandbox: create work root: %w", err)
		}
		workRoot = base
	} else if !filepath.IsAbs(workRoot) {
		return nil, domain.Invalidf("sandbox_work_root", "sandbox work root must be an absolute path")
	}
	return &ProcessDriver{workRoot: workRoot, sandboxes: map[domain.SandboxID]string{}}, nil
}

func (d *ProcessDriver) Name() string { return "process" }

func (d *ProcessDriver) Capabilities(_ context.Context) (Capabilities, error) {
	return Capabilities{
		Isolation:    "process-rooted-directory",
		Network:      false,
		Capabilities: []Capability{CapRootedFilesystem, CapProcessExec},
	}, nil
}

func (d *ProcessDriver) Create(_ context.Context, req CreateRequest) (domain.SandboxID, error) {
	idStr, err := domain.NewID(domain.PrefixSandbox)
	if err != nil {
		return "", fmt.Errorf("sandbox: generate id: %w", err)
	}
	id := domain.SandboxID(idStr)
	root := d.rootFor(id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("sandbox: create workspace %s: %w", id, err)
	}
	d.mu.Lock()
	d.sandboxes[id] = root
	d.mu.Unlock()
	return id, nil
}

// Root exposes the filesystem location of a sandbox to the SCM layer so the
// git worktree and the sandbox are the same directory. Only drivers with
// CapRootedFilesystem can meaningfully answer.
func (d *ProcessDriver) Root(ctx context.Context, id domain.SandboxID) (string, error) {
	caps, err := d.Capabilities(ctx)
	if err != nil {
		return "", err
	}
	if !caps.Has(CapRootedFilesystem) {
		return "", domain.NewError(domain.ErrKindInvalid, "sandbox_no_root", "driver has no rooted filesystem")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	root, ok := d.sandboxes[id]
	if !ok {
		return "", notFound(id)
	}
	return root, nil
}

func (d *ProcessDriver) Exec(ctx context.Context, id domain.SandboxID, req ExecRequest) (ExecResult, error) {
	root, ok := func() (string, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		r, found := d.sandboxes[id]
		return r, found
	}()
	if !ok {
		return ExecResult{}, notFound(id)
	}
	if len(req.Command) == 0 {
		return ExecResult{}, domain.Invalidf("sandbox_exec_command", "command must not be empty")
	}
	if strings.ContainsRune(req.Command[0], '/') {
		// Only PATH-resolved names are allowed; absolute or relative paths
		// could point anywhere on the host.
		return ExecResult{}, &domain.Error{
			Kind:    domain.ErrKindPolicyDenied,
			Code:    "sandbox_command_not_allowed",
			Message: fmt.Sprintf("command %q must be a plain executable name from the allow list", req.Command[0]),
		}
	}
	bin := req.Command[0]
	if !allowedCommands[bin] {
		return ExecResult{}, &domain.Error{
			Kind:    domain.ErrKindPolicyDenied,
			Code:    "sandbox_command_not_allowed",
			Message: fmt.Sprintf("command %q is not in the sandbox allow list", bin),
		}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		return ExecResult{}, domain.Invalidf("sandbox_exec_timeout", "exec requires a positive timeout")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, req.Command[0], req.Command[1:]...)
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + root,
		"TMPDIR=" + filepath.Join(root, ".tmp"),
		"LC_ALL=C",
		"LANG=C",
	}
	_ = os.MkdirAll(filepath.Join(root, ".tmp"), 0o700)

	started := time.Now()
	var stdout, stderr strings.Builder
	cmd.Stdout = &cappedWriter{sb: &stdout, max: MaxExecOutputBytes}
	cmd.Stderr = &cappedWriter{sb: &stderr, max: MaxExecOutputBytes}
	err := cmd.Run()
	result := ExecResult{
		Stdout:   []byte(stdout.String()),
		Stderr:   []byte(stderr.String()),
		Duration: time.Since(started),
	}
	switch {
	case runCtx.Err() != nil && ctx.Err() == nil:
		return result, &domain.Error{
			Kind:    domain.ErrKindTimeout,
			Code:    "sandbox_exec_timeout",
			Message: fmt.Sprintf("command timed out after %s", timeout),
			Cause:   runCtx.Err(),
		}
	case ctx.Err() != nil:
		return result, &domain.Error{
			Kind:    domain.ErrKindCancelled,
			Code:    "sandbox_exec_cancelled",
			Message: "execution cancelled",
			Cause:   ctx.Err(),
		}
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, domain.Internalf(err, "sandbox_exec_failed", "start command %q", bin)
	default:
		result.ExitCode = 0
		return result, nil
	}
}

func (d *ProcessDriver) Destroy(_ context.Context, id domain.SandboxID) error {
	d.mu.Lock()
	root, ok := d.sandboxes[id]
	if ok {
		delete(d.sandboxes, id)
	}
	d.mu.Unlock()
	if !ok {
		return notFound(id)
	}
	if err := os.RemoveAll(root); err != nil {
		return domain.Internalf(err, "sandbox_destroy_failed", "remove workspace %s", id)
	}
	return nil
}

func (d *ProcessDriver) rootFor(id domain.SandboxID) string {
	return filepath.Join(d.workRoot, string(id))
}

func notFound(id domain.SandboxID) error {
	return domain.NotFoundf("sandbox", id)
}

// cappedWriter captures output up to max bytes; anything beyond is dropped so
// a runaway command cannot exhaust memory.
type cappedWriter struct {
	sb      *strings.Builder
	max     int
	written int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.written < w.max {
		remaining := w.max - w.written
		chunk := p
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		w.sb.Write(chunk)
	}
	w.written += len(p)
	return len(p), nil
}
