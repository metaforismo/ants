// Package scm defines the Git/SCM seam used by task execution. The v1
// interface deliberately has no push, fetch, or remote operations: local
// commits inside task workspaces are expressible, remote effects are not.
// SCM-host integrations (GitHub etc.) arrive as separate adapters in a later
// wave behind their own capability manifests.
package scm

import (
	"context"

	"github.com/metaforismo/ants/internal/domain"
)

type Seed struct {
	DefaultBranch string
	Files         map[string][]byte
}

// Handle identifies one repository instance owned by a single workspace.
type Handle struct {
	Driver        string
	SandboxID     domain.SandboxID
	Root          string // filesystem root (local_git); empty for memory
	DefaultBranch string
}

type CommitResult struct {
	SHA  string
	Head string
}

type Driver interface {
	Name() string
	// Init seeds a fresh repository in the sandbox workspace.
	Init(ctx context.Context, h Handle, seed Seed) error
	Head(ctx context.Context, h Handle, branch string) (string, error)
	CreateBranch(ctx context.Context, h Handle, name, fromBranch string) error
	CommitFiles(ctx context.Context, h Handle, branch, message string, files map[string][]byte) (CommitResult, error)
	// Merge integrates source into target with a fast-forward-or-merge
	// commit. Conflicting files are reported without being auto-resolved:
	// conflict resolution is an explicit planner decision, never a blind pick.
	Merge(ctx context.Context, h Handle, targetBranch, sourceBranch, message string) (MergeResult, error)
	Diff(ctx context.Context, h Handle, baseSHA, headSHA string) ([]byte, error)
	Files(ctx context.Context, h Handle, branch string) (map[string][]byte, error)
}

type MergeResult struct {
	SHA       string   `json:"sha"`
	Conflicts []string `json:"conflicts,omitempty"`
}

func (m MergeResult) HasConflicts() bool { return len(m.Conflicts) > 0 }
