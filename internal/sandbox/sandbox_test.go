package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

func testCreateRequest() CreateRequest {
	return CreateRequest{
		TenantID:  domain.TenantID("ten_testtenant00000000000"),
		RunID:     domain.RunID("run_testrun0000000000000"),
		TaskID:    domain.TaskID("tsk_testtask000000000000"),
		Principal: domain.PrincipalID("prn_testprincipal0000000"),
	}
}

func TestProcessDriverLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("process driver exercises real subprocesses")
	}
	d, err := NewProcessDriver("")
	if err != nil {
		t.Fatalf("create driver: %v", err)
	}
	ctx := context.Background()
	caps, err := d.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.Network {
		t.Fatalf("no driver may advertise network capability in this release")
	}
	id, err := d.Create(ctx, testCreateRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	root, err := d.Root(ctx, id)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	res, err := d.Exec(ctx, id, ExecRequest{Command: []string{"sh", "-c", "pwd && echo hello-from-sandbox"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(string(res.Stdout), "hello-from-sandbox") {
		t.Fatalf("unexpected stdout %q", res.Stdout)
	}
	if !strings.Contains(string(res.Stdout), root) {
		t.Fatalf("command must run inside its own workspace root, got %q (want %q)", res.Stdout, root)
	}
	if err := d.Destroy(ctx, id); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := d.Exec(ctx, id, ExecRequest{Command: []string{"true"}, Timeout: time.Second}); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("exec on destroyed sandbox must be not found, got %v", err)
	}
}

func TestProcessDriverRejectsDisallowedCommands(t *testing.T) {
	d, err := NewProcessDriver("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := d.Create(ctx, testCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{
		{"curl", "https://example.com"},
		{"wget", "https://example.com"},
		{"npm", "install", "-g", "something"},
		{"pip", "install", "requests"},
		{"go", "get", "evil.example.com/x"},
		{"/bin/sh", "-c", "echo hi"},
	} {
		_, err := d.Exec(ctx, id, ExecRequest{Command: cmd, Timeout: 5 * time.Second})
		if domain.ErrKindOf(err) != domain.ErrKindPolicyDenied {
			t.Errorf("command %v must fail admission as policy-denied, got %v", cmd, err)
		}
	}
	_ = d.Destroy(ctx, id)
}

func TestProcessDriverExecTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("uses real sleep")
	}
	d, _ := NewProcessDriver("")
	ctx := context.Background()
	id, _ := d.Create(ctx, testCreateRequest())
	defer d.Destroy(ctx, id)
	_, err := d.Exec(ctx, id, ExecRequest{Command: []string{"sh", "-c", "sleep 5"}, Timeout: 300 * time.Millisecond})
	if domain.ErrKindOf(err) != domain.ErrKindTimeout {
		t.Fatalf("expected timeout classification, got %v", err)
	}
}

func TestFakeDriverIsDeterministicAndScripted(t *testing.T) {
	fake := NewFakeDriver().Script("bash tests/run.sh exit0", ExecResult{ExitCode: 0, Stdout: []byte("ok")})
	ctx := context.Background()
	id, _ := fake.Create(ctx, testCreateRequest())
	res, err := fake.Exec(ctx, id, ExecRequest{Command: []string{"bash", "tests/run.sh", "exit0"}, Timeout: time.Second})
	if err != nil {
		t.Fatalf("scripted exec failed: %v", err)
	}
	if string(res.Stdout) != "ok" {
		t.Fatalf("unexpected output")
	}
	if _, err := fake.Exec(ctx, id, ExecRequest{Command: []string{"unscripted"}, Timeout: time.Second}); domain.ErrKindOf(err) != domain.ErrKindInvalid {
		t.Fatalf("unscripted commands must fail loudly, got %v", err)
	}
	if fake.CreatedCount() != 1 || len(fake.ExecCalls()) != 2 {
		t.Fatalf("driver must record calls for assertions")
	}
}
