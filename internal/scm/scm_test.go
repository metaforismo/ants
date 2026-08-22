package scm

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/domain"
)

// driversUnderTest returns every driver available on this machine so both
// implementations satisfy the same behavioral contract.
func driversUnderTest(t *testing.T) []Driver {
	t.Helper()
	out := []Driver{NewMemory()}
	if _, err := NewLocalGit(); err == nil {
		lg, err := NewLocalGit()
		if err != nil {
			t.Fatalf("local git: %v", err)
		}
		out = append(out, lg)
	} else {
		t.Logf("git binary unavailable; local_git contract cases skipped")
	}
	return out
}

func newHandle(driver Driver, root string) Handle {
	return Handle{
		Driver:        driver.Name(),
		SandboxID:     domain.SandboxID("sbx_testsbx000000000000000"),
		Root:          root,
		DefaultBranch: "main",
	}
}

func TestDriverContract(t *testing.T) {
	ctx := context.Background()
	for _, drv := range driversUnderTest(t) {
		t.Run(drv.Name(), func(t *testing.T) {
			root := t.TempDir()
			if drv.Name() == "memory" {
				root = "mem:" + root
			}
			h := newHandle(drv, root)
			if err := drv.Init(ctx, h, Seed{DefaultBranch: "main", Files: map[string][]byte{
				"README.md": []byte("demo\n"),
				"calc.sh":   []byte("#!/bin/sh\n. ./lib_add.sh 2>/dev/null || true\n. ./lib_mul.sh 2>/dev/null || true\n"),
			}}); err != nil {
				t.Fatalf("init: %v", err)
			}

			baseSHA, err := drv.Head(ctx, h, "main")
			if err != nil || len(baseSHA) < 7 {
				t.Fatalf("head after init: %q %v", baseSHA, err)
			}

			if err := drv.CreateBranch(ctx, h, "ants/task-a", "main"); err != nil {
				t.Fatalf("branch: %v", err)
			}
			c1, err := drv.CommitFiles(ctx, h, "ants/task-a", "add add()", map[string][]byte{
				"lib_add.sh": []byte("add() { echo $(($1 + $2)); }\n"),
			})
			if err != nil {
				t.Fatalf("commit: %v", err)
			}
			if c1.SHA == "" || c1.SHA == baseSHA {
				t.Fatalf("commit must produce a new SHA: %+v", c1)
			}

			if err := drv.CreateBranch(ctx, h, "ants/task-b", "main"); err != nil {
				t.Fatalf("branch b: %v", err)
			}
			_, err = drv.CommitFiles(ctx, h, "ants/task-b", "add multiply()", map[string][]byte{
				"lib_mul.sh": []byte("multiply() { echo $(($1 * $2)); }\n"),
			})
			if err != nil {
				t.Fatalf("commit b: %v", err)
			}

			merged, err := drv.Merge(ctx, h, "main", "ants/task-a", "integrate task a")
			if err != nil || merged.HasConflicts() {
				t.Fatalf("merge a: %+v %v", merged, err)
			}
			merged2, err := drv.Merge(ctx, h, "main", "ants/task-b", "integrate task b")
			if err != nil || merged2.HasConflicts() {
				t.Fatalf("merge b: %+v %v", merged2, err)
			}

			files, err := drv.Files(ctx, h, "main")
			if err != nil {
				t.Fatalf("files: %v", err)
			}
			for _, want := range []string{"lib_add.sh", "lib_mul.sh", "calc.sh"} {
				if _, ok := files[want]; !ok {
					t.Errorf("merged main missing %s; have %v", want, keys(files))
				}
			}

			diff, err := drv.Diff(ctx, h, baseSHA, merged2.SHA)
			if err != nil {
				t.Fatalf("diff: %v", err)
			}
			if len(diff) == 0 {
				t.Fatalf("diff must be non-empty")
			}
			if drv.Name() == "local_git" && !strings.Contains(string(diff), "lib_add") {
				t.Fatalf("real git diff should mention added file")
			}

			head, err := drv.Head(ctx, h, "ants/task-a")
			if err != nil || head != c1.SHA {
				t.Fatalf("task branch head drifted: %s vs %s (%v)", head, c1.SHA, err)
			}
		})
	}
}

func TestMergeConflictIsReportedNotResolved(t *testing.T) {
	ctx := context.Background()
	for _, drv := range driversUnderTest(t) {
		t.Run(drv.Name(), func(t *testing.T) {
			root := t.TempDir()
			if drv.Name() == "memory" {
				root = "mem:" + root
			}
			h := newHandle(drv, root)
			if err := drv.Init(ctx, h, Seed{DefaultBranch: "main", Files: map[string][]byte{
				"config.txt": []byte("mode=off\n"),
			}}); err != nil {
				t.Fatal(err)
			}
			_ = drv.CreateBranch(ctx, h, "a", "main")
			_, _ = drv.CommitFiles(ctx, h, "a", "a sets mode", map[string][]byte{"config.txt": []byte("mode=alpha\n")})
			_ = drv.CreateBranch(ctx, h, "b", "main")
			_, _ = drv.CommitFiles(ctx, h, "b", "b sets mode", map[string][]byte{"config.txt": []byte("mode=beta\n")})

			if _, err := drv.Merge(ctx, h, "main", "a", "merge a"); err != nil {
				t.Fatalf("first merge must succeed: %v", err)
			}
			res, err := drv.Merge(ctx, h, "main", "b", "merge b")
			if err != nil {
				t.Fatalf("conflict detection failed with error: %v", err)
			}
			if !res.HasConflicts() || len(res.Conflicts) != 1 || res.Conflicts[0] != "config.txt" {
				t.Fatalf("expected explicit conflict on config.txt, got %+v", res)
			}
			files, err := drv.Files(ctx, h, "main")
			if err != nil {
				t.Fatal(err)
			}
			if string(files["config.txt"]) != "mode=alpha\n" {
				t.Fatalf("target branch must stay untouched on conflict, got %q", files["config.txt"])
			}
		})
	}
}

func TestPathEscapeRejectedInMemoryCommits(t *testing.T) {
	drv := NewMemory()
	h := newHandle(drv, "mem:"+t.TempDir())
	ctx := context.Background()
	_ = drv.Init(ctx, h, Seed{DefaultBranch: "main"})
	for _, bad := range []string{"/etc/passwd", "../escape.txt", "ok/../../up"} {
		_, err := drv.CommitFiles(ctx, h, "main", "bad", map[string][]byte{bad: []byte("x")})
		if domain.ErrKindOf(err) != domain.ErrKindInvalid {
			t.Errorf("path %q must be rejected, got %v", bad, err)
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
