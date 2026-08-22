// Package sandbox defines the SandboxDriver seam from the master plan plus
// the drivers available in tranche 1: a rooted-process driver for real local
// execution and a scripted fake for deterministic tests. MicroVM drivers
// (vfkit/Firecracker) implement the same interface in Horizon 2.
package sandbox

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// Capability names are negotiated at admission time. A driver that cannot
// honor a requirement must cause the run to fail before any work starts,
// never mid-task.
type Capability string

const (
	CapRootedFilesystem Capability = "rooted_filesystem"
	CapProcessExec      Capability = "process_exec"
)

type Capabilities struct {
	Isolation    string       `json:"isolation"`
	Network      bool         `json:"network"`
	Capabilities []Capability `json:"capabilities"`
}

func (c Capabilities) Has(cap Capability) bool {
	for _, existing := range c.Capabilities {
		if existing == cap {
			return true
		}
	}
	return false
}

type CreateRequest struct {
	TenantID  domain.TenantID
	RunID     domain.RunID
	TaskID    domain.TaskID
	Principal domain.PrincipalID
}

type ExecRequest struct {
	Command []string
	Timeout time.Duration
}

type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
}

const MaxExecOutputBytes = 1 << 20

type Driver interface {
	Name() string
	Capabilities(ctx context.Context) (Capabilities, error)
	Create(ctx context.Context, req CreateRequest) (domain.SandboxID, error)
	Exec(ctx context.Context, id domain.SandboxID, req ExecRequest) (ExecResult, error)
	Destroy(ctx context.Context, id domain.SandboxID) error
}
