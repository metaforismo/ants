package scm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// LocalGit drives real git inside the sandbox workspace root. Remotes are not
// part of this driver's vocabulary: it never fetches, pushes, or reads host
// git config, so a task cannot reach a remote even by constructing arguments
// through other code paths.
type LocalGit struct {
	binary string
	// mu serializes every repository mutation on this driver instance:
	// tasks share one working tree, and git index operations are not safe
	// concurrently. Verification exec happens in per-task sandboxes, so the
	// critical sections are short.
	mu sync.Mutex
}

var _ Driver = (*LocalGit)(nil)

func NewLocalGit() (*LocalGit, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, &domain.Error{
			Kind:    domain.ErrKindInvalid,
			Code:    "scm_git_unavailable",
			Message: "the local_git SCM driver requires the git binary on PATH",
			Cause:   err,
		}
	}
	return &LocalGit{binary: path}, nil
}

func (g *LocalGit) Name() string { return "local_git" }

const gitTimeout = 30 * time.Second

// gitEnv isolates the child git process from host configuration: no system or
// global config, fixed identity, fixed dates for reproducible commits.
func gitEnv(root string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(root, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=Ants Task Runner",
		"GIT_AUTHOR_EMAIL=ants@invalid.local",
		"GIT_COMMITTER_NAME=Ants Task Runner",
		"GIT_COMMITTER_EMAIL=ants@invalid.local",
		"GIT_AUTHOR_DATE=@0 +0000",
		"GIT_COMMITTER_DATE=@0 +0000",
		"LC_ALL=C",
		"HOME=" + root,
	}
}

func (g *LocalGit) run(ctx context.Context, root string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", "-C", root}, args...)
	cmd := exec.CommandContext(runCtx, g.binary, full...)
	cmd.Env = gitEnv(root)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return nil, &domain.Error{Kind: domain.ErrKindCancelled, Code: "scm_cancelled", Message: "git operation cancelled", Cause: ctx.Err()}
	case runCtx.Err() != nil:
		return nil, &domain.Error{Kind: domain.ErrKindTimeout, Code: "scm_timeout", Message: fmt.Sprintf("git %s timed out", args[0]), Cause: runCtx.Err()}
	case err != nil:
		return out.Bytes(), &domain.Error{
			Kind:    domain.ErrKindTransient,
			Code:    "scm_git_error",
			Message: fmt.Sprintf("git %s failed: %s", args[0], firstLine(errOut.String())),
			Cause:   err,
		}
	default:
		return out.Bytes(), nil
	}
}

// runExpectingFailure distinguishes expected git failures (conflicts) from
// real errors.
func (g *LocalGit) runAllowExit(ctx context.Context, root string, args ...string) ([]byte, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-c", "commit.gpgsign=false", "-C", root}, args...)
	cmd := exec.CommandContext(runCtx, g.binary, full...)
	cmd.Env = gitEnv(root)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	code := 0
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
		err = nil
	}
	return out.Bytes(), code, err
}

func (g *LocalGit) Init(ctx context.Context, h Handle, seed Seed) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if h.Root == "" {
		return domain.Invalidf("scm_root_required", "local_git requires a filesystem root")
	}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		return domain.Internalf(err, "scm_root_create", "create repo root")
	}
	if _, err := g.run(ctx, h.Root, "init", "--initial-branch="+seed.DefaultBranch, "."); err != nil {
		return err
	}
	for path, content := range seed.Files {
		target := filepath.Join(h.Root, filepath.FromSlash(path))
		if err := writeContained(h.Root, target, content); err != nil {
			return err
		}
	}
	if len(seed.Files) > 0 {
		args := append([]string{"add", "--"}, sortedPaths(seed.Files)...)
		if _, err := g.run(ctx, h.Root, args...); err != nil {
			return err
		}
		if _, err := g.run(ctx, h.Root, "commit", "-m", "seed repository"); err != nil {
			return err
		}
	}
	return nil
}

func (g *LocalGit) Head(ctx context.Context, h Handle, branch string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.headLocked(ctx, h, branch)
}

func (g *LocalGit) headLocked(ctx context.Context, h Handle, branch string) (string, error) {
	out, err := g.run(ctx, h.Root, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil {
		return "", domain.NotFoundf("branch", branch)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *LocalGit) CreateBranch(ctx context.Context, h Handle, name, fromBranch string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, err := g.run(ctx, h.Root, "branch", name, fromBranch)
	if err != nil {
		return domain.Conflictf("scm_branch_exists", "cannot create branch %q from %q", name, fromBranch)
	}
	return nil
}

func (g *LocalGit) CommitFiles(ctx context.Context, h Handle, branch, message string, files map[string][]byte) (CommitResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	checkout := func(target string) error {
		_, err := g.run(ctx, h.Root, "checkout", "--quiet", target)
		return err
	}
	if err := checkout(branch); err != nil {
		return CommitResult{}, err
	}
	baseSHA, err := g.headLocked(ctx, h, branch)
	if err != nil {
		return CommitResult{}, err
	}
	for path, content := range files {
		target := filepath.Join(h.Root, filepath.FromSlash(path))
		if content == nil {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return CommitResult{}, domain.Internalf(err, "scm_delete_file", "remove %s", path)
			}
			continue
		}
		if err := writeContained(h.Root, target, content); err != nil {
			return CommitResult{}, err
		}
	}
	args := append([]string{"add", "--all", "--"}, sortedPaths(files)...)
	if _, err := g.run(ctx, h.Root, args...); err != nil {
		return CommitResult{}, err
	}
	status, err := g.run(ctx, h.Root, "status", "--porcelain")
	if err != nil {
		return CommitResult{}, err
	}
	if len(strings.TrimSpace(string(status))) == 0 {
		return CommitResult{SHA: baseSHA, Head: baseSHA}, nil
	}
	if _, err := g.run(ctx, h.Root, "commit", "--no-verify", "-m", message); err != nil {
		return CommitResult{}, err
	}
	head, err := g.headLocked(ctx, h, branch)
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{SHA: head, Head: head}, nil
}

func (g *LocalGit) Merge(ctx context.Context, h Handle, targetBranch, sourceBranch, message string) (MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := checkoutQuiet(g, ctx, h.Root, targetBranch); err != nil {
		return MergeResult{}, err
	}
	out, code, err := g.runAllowExit(ctx, h.Root, "merge", "--no-ff", "--no-commit", "-m", message, sourceBranch)
	if err != nil {
		return MergeResult{}, err
	}
	if code != 0 {
		conflicted, listErr := g.run(ctx, h.Root, "diff", "--name-only", "--diff-filter=U")
		if listErr != nil {
			conflicted = out
		}
		conflicts := nonEmptyLines(string(conflicted))
		// Abort so the target branch stays exactly as before: conflict
		// resolution is a planner decision, never a silent pick.
		_, _ = g.run(ctx, h.Root, "merge", "--abort")
		return MergeResult{Conflicts: conflicts}, nil
	}
	if _, err := g.run(ctx, h.Root, "commit", "--no-verify", "-m", message); err != nil {
		return MergeResult{}, err
	}
	head, err := g.headLocked(ctx, h, targetBranch)
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{SHA: head}, nil
}

func (g *LocalGit) Diff(ctx context.Context, h Handle, baseSHA, headSHA string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.run(ctx, h.Root, "diff", baseSHA, headSHA)
}

func (g *LocalGit) Files(ctx context.Context, h Handle, branch string) (map[string][]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.run(ctx, h.Root, "ls-tree", "-r", "--name-only", "--full-tree", branch)
	if err != nil {
		return nil, domain.NotFoundf("branch", branch)
	}
	files := map[string][]byte{}
	for _, path := range nonEmptyLines(string(out)) {
		blob, err := g.run(ctx, h.Root, "show", branch+":"+path)
		if err != nil {
			return nil, err
		}
		files[path] = blob
	}
	return files, nil
}

func checkoutQuiet(g *LocalGit, ctx context.Context, root, branch string) error {
	_, err := g.run(ctx, root, "checkout", "--quiet", branch)
	return err
}

func writeContained(root, target string, content []byte) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return domain.Internalf(err, "scm_path", "resolve root")
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return domain.Internalf(err, "scm_path", "resolve target")
	}
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return domain.Invalidf("scm_path_escape", "path escapes the workspace root")
	}
	if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o700); err != nil {
		return domain.Internalf(err, "scm_write", "mkdir for %s", rel)
	}
	if err := os.WriteFile(cleanTarget, content, 0o600); err != nil {
		return domain.Internalf(err, "scm_write", "write %s", rel)
	}
	return nil
}

func sortedPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "unknown git failure"
	}
	return s
}
