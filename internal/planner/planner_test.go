package planner

import (
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/domain"
)

const demoCatalogYAML = `version: 1
capabilities:
  - id: arithmetic-helpers
    request_keywords: ["add", "multiply"]
    spec:
      outcome: "calc.sh evaluates integer addition and multiplication"
      requirements:
        - "lib_add.sh defines add(a, b)"
        - "lib_mul.sh defines multiply(a, b)"
      success_criteria:
        - "bash tests/calc_test.sh exits 0 on the integrated tree"
    verify_all:
      - ["bash", "tests/calc_test.sh"]
    tasks:
      - name: implement-add
        commit_message: "feat(calc): implement integer addition"
        files:
          lib_add.sh: |
            add() {
              echo $(($1 + $2))
            }
        verify:
          - ["test", "-f", "lib_add.sh"]
          - ["sh", "calc.sh", "add", "20", "22"]
      - name: implement-multiply
        commit_message: "feat(calc): implement integer multiplication"
        files:
          lib_mul.sh: |
            multiply() {
              echo $(($1 * $2))
            }
        verify:
          - ["test", "-f", "lib_mul.sh"]
`

func demoCatalog() map[string][]byte {
	return map[string][]byte{catalogPath: []byte(demoCatalogYAML)}
}

func TestPlanMatchesFixtureCapability(t *testing.T) {
	p := NewDeterministic()
	out, err := p.Plan(t.Context(), PlanInput{
		Request:   "please implement add and multiply",
		RepoFiles: demoCatalog(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Spec.Outcome == "" || len(out.Spec.SuccessCriteria) == 0 {
		t.Fatalf("spec incomplete: %+v", out.Spec)
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("expected two parallel tasks, got %d", len(out.Tasks))
	}
	names := map[string]bool{}
	for _, task := range out.Tasks {
		names[task.Name] = true
		if len(task.Files) == 0 {
			t.Errorf("task %s writes no files", task.Name)
		}
		if !strings.HasPrefix(task.Branch, "ants/") {
			t.Errorf("task %s branch %q must live under ants/", task.Name, task.Branch)
		}
		if len(task.VerifyCommands) == 0 {
			t.Errorf("task %s has no verification commands", task.Name)
		}
	}
	if !names["implement-add"] || !names["implement-multiply"] {
		t.Fatalf("unexpected task set: %v", names)
	}
	if len(out.IntegratedVerification) == 0 {
		t.Fatalf("integrated verification missing")
	}
}

func TestPlanRejectsUnknownRequests(t *testing.T) {
	p := NewDeterministic()
	_, err := p.Plan(t.Context(), PlanInput{
		Request:   "rewrite the database engine",
		RepoFiles: demoCatalog(),
	})
	if domain.ErrKindOf(err) != domain.ErrKindInvalid {
		t.Fatalf("unmatched request must fail as invalid, got %v", err)
	}
}

func TestPlanRejectsAmbiguousRequests(t *testing.T) {
	files := demoCatalog()
	files[catalogPath] = []byte(`version: 1
capabilities:
  - id: first
    request_keywords: ["update"]
    spec:
      outcome: first
      requirements: ["r"]
      success_criteria: ["c"]
    verify_all: [["true"]]
    tasks:
      - name: t1
        commit_message: m
        files:
          a.txt: "a"
  - id: second
    request_keywords: ["update"]
    spec:
      outcome: second
      requirements: ["r"]
      success_criteria: ["c"]
    verify_all: [["true"]]
    tasks:
      - name: t2
        commit_message: m
        files:
          b.txt: "b"
`)
	p := NewDeterministic()
	_, err := p.Plan(t.Context(), PlanInput{Request: "update things", RepoFiles: files})
	if domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("ambiguous request must conflict, got %v", err)
	}
}

func TestParseCatalogRejectsMissingOrMalformed(t *testing.T) {
	if _, err := ParseCatalog(map[string][]byte{}); err == nil {
		t.Fatalf("missing catalog must fail")
	}
	bad := map[string][]byte{catalogPath: []byte("version: 99\ncapabilities: []")}
	if _, err := ParseCatalog(bad); err == nil {
		t.Fatalf("unsupported catalog version must fail")
	}
	bad2 := map[string][]byte{catalogPath: []byte("not: [valid")}
	if _, err := ParseCatalog(bad2); err == nil {
		t.Fatalf("malformed yaml must fail")
	}
	noVerify := map[string][]byte{catalogPath: []byte(`version: 1
capabilities:
  - id: c1
    request_keywords: ["x"]
    spec:
      outcome: o
      requirements: ["r"]
      success_criteria: ["c"]
    tasks:
      - name: t
        commit_message: m
        files:
          f: "1"
`)}
	if _, err := ParseCatalog(noVerify); err == nil {
		t.Fatalf("capability without integrated verification must fail validation")
	}
}

func TestBranchNamesAreDeterministicAndClean(t *testing.T) {
	cases := map[string]string{
		"implement-add": "ants/implement-add",
		"Fix_Bug #42!":  "ants/fix-bug-42",
		"UPPER":         "ants/upper",
	}
	for name, want := range cases {
		if got := branchFor(name); got != want {
			t.Errorf("branchFor(%q) = %q, want %q", name, got, want)
		}
	}
}
