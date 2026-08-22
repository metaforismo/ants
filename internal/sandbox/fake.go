package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/metaforismo/ants/internal/domain"
)

// FakeDriver records every call and returns scripted exec results. It never
// executes anything: tests register exact-match responses per command.
type FakeDriver struct {
	mu        sync.Mutex
	scripts   map[string]ExecResult
	created   []CreateRequest
	execCalls []FakeExecCall
	destroyed []domain.SandboxID
	nextID    int
}

var _ Driver = (*FakeDriver)(nil)

type FakeExecCall struct {
	Sandbox domain.SandboxID
	Command []string
}

func NewFakeDriver() *FakeDriver {
	return &FakeDriver{scripts: map[string]ExecResult{}}
}

// Script registers a deterministic response for a command whose argv joined
// by spaces matches key exactly.
func (f *FakeDriver) Script(commandKey string, result ExecResult) *FakeDriver {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[commandKey] = result
	return f
}

func (f *FakeDriver) Name() string { return "fake" }

func (f *FakeDriver) Capabilities(_ context.Context) (Capabilities, error) {
	return Capabilities{
		Isolation:    "in-memory-recording",
		Network:      false,
		Capabilities: []Capability{CapRootedFilesystem, CapProcessExec},
	}, nil
}

func (f *FakeDriver) Create(_ context.Context, req CreateRequest) (domain.SandboxID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := domain.SandboxID(fmt.Sprintf("sbx_fakesandbox%016d", f.nextID))
	f.created = append(f.created, req)
	return id, nil
}

func (f *FakeDriver) Exec(_ context.Context, id domain.SandboxID, req ExecRequest) (ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, FakeExecCall{Sandbox: id, Command: append([]string(nil), req.Command...)})
	key := JoinCommand(req.Command)
	result, ok := f.scripts[key]
	if !ok {
		return ExecResult{}, &domain.Error{
			Kind:    domain.ErrKindInvalid,
			Code:    "sandbox_unscripted_command",
			Message: fmt.Sprintf("fake driver has no scripted result for %q", key),
		}
	}
	out := result
	out.Stdout = append([]byte(nil), result.Stdout...)
	out.Stderr = append([]byte(nil), result.Stderr...)
	return out, nil
}

func (f *FakeDriver) Destroy(_ context.Context, id domain.SandboxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *FakeDriver) CreatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *FakeDriver) ExecCalls() []FakeExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeExecCall(nil), f.execCalls...)
}

// JoinCommand renders argv with single spaces; it is the scripting key for
// FakeDriver and a stable rendering in evidence records.
func JoinCommand(cmd []string) string {
	return strings.Join(cmd, " ")
}
