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
        writes: ["lib_add.sh"]
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
        writes: ["lib_mul.sh"]
        commit_message: "feat(calc): implement integer multiplication"
        files:
          lib_mul.sh: |
            multiply() {
              echo $(($1 * $2))
            }
        verify:
          - ["test", "-f", "lib_mul.sh"]
      - name: document-arithmetic
        depends_on: ["implement-add", "implement-multiply"]
        writes: ["OPERATIONS.md"]
        commit_message: "docs(calc): document arithmetic operations"
        files:
          OPERATIONS.md: "# Arithmetic operations\n"
        verify:
          - ["test", "-f", "OPERATIONS.md"]
          - ["sh", "calc.sh", "add", "20", "22"]
          - ["sh", "calc.sh", "multiply", "6", "7"]
`

func demoCatalog() map[string][]byte {
	return map[string][]byte{catalogPath: []byte(demoCatalogYAML)}
}

func TestPlanBuildsValidatedTaskGraph(t *testing.T) {
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
	if len(out.Tasks) != 3 {
		t.Fatalf("expected three graph tasks, got %d", len(out.Tasks))
	}

	byName := make(map[string]TaskTemplate, len(out.Tasks))
	for _, task := range out.Tasks {
		byName[task.Name] = task
		if len(task.Files) == 0 || len(task.VerifyCommands) == 0 {
			t.Errorf("task %s is not executable: %+v", task.Name, task)
		}
		if !strings.HasPrefix(task.Branch, "ants/task-") {
			t.Errorf("task %s branch %q must live under ants/task-", task.Name, task.Branch)
		}
	}
	for _, root := range []string{"implement-add", "implement-multiply"} {
		if task := byName[root]; task.Depth != 0 || len(task.DependsOn) != 0 {
			t.Fatalf("root task %s has wrong graph metadata: %+v", root, task)
		}
	}
	doc := byName["document-arithmetic"]
	if doc.Depth != 1 || strings.Join(doc.DependsOn, ",") != "implement-add,implement-multiply" {
		t.Fatalf("dependent task has wrong graph metadata: %+v", doc)
	}
	got := []string{out.Tasks[0].Name, out.Tasks[1].Name, out.Tasks[2].Name}
	if strings.Join(got, ",") != "implement-add,implement-multiply,document-arithmetic" {
		t.Fatalf("unexpected topological order: %v", got)
	}
	if len(out.IntegratedVerification) == 0 {
		t.Fatal("integrated verification missing")
	}
}

func TestNormalizeOutputRejectsInvalidGraphs(t *testing.T) {
	base := func() *Output {
		return &Output{
			Spec: domain.SpecContent{Outcome: "outcome", SuccessCriteria: []string{"criterion"}},
			Tasks: []TaskTemplate{
				{Name: "first", CommitMessage: "first", Writes: []string{"first.txt"}, Files: map[string]string{"first.txt": "1"}, VerifyCommands: [][]string{{"true"}}},
				{Name: "second", CommitMessage: "second", Writes: []string{"second.txt"}, Files: map[string]string{"second.txt": "2"}, VerifyCommands: [][]string{{"true"}}},
			},
			IntegratedVerification: [][]string{{"true"}},
		}
	}

	cases := []struct {
		name string
		code string
		edit func(*Output)
	}{
		{name: "duplicate task", code: "plan_duplicate_task", edit: func(out *Output) { out.Tasks[1].Name = "first" }},
		{name: "branch collision", code: "plan_branch_collision", edit: func(out *Output) { out.Tasks[0].Name = "Fix Bug"; out.Tasks[1].Name = "fix_bug" }},
		{name: "missing dependency", code: "plan_missing_dependency", edit: func(out *Output) { out.Tasks[1].DependsOn = []string{"missing"} }},
		{name: "self dependency", code: "plan_self_dependency", edit: func(out *Output) { out.Tasks[1].DependsOn = []string{"second"} }},
		{name: "duplicate dependency", code: "plan_duplicate_dependency", edit: func(out *Output) { out.Tasks[1].DependsOn = []string{"first", "first"} }},
		{name: "cycle", code: "plan_task_cycle", edit: func(out *Output) { out.Tasks[0].DependsOn = []string{"second"}; out.Tasks[1].DependsOn = []string{"first"} }},
		{name: "unsafe write", code: "plan_write_path", edit: func(out *Output) { out.Tasks[0].Writes = []string{"../first.txt"} }},
		{name: "undeclared file", code: "plan_write_ownership", edit: func(out *Output) { out.Tasks[0].Writes = []string{"other.txt"} }},
		{name: "parallel write collision", code: "plan_parallel_write_collision", edit: func(out *Output) { out.Tasks[1].Writes = []string{"first.txt"}; out.Tasks[1].Files = map[string]string{"first.txt": "2"} }},
		{name: "empty task verification", code: "plan_task_verification", edit: func(out *Output) { out.Tasks[0].VerifyCommands = nil }},
		{name: "empty integrated verification", code: "plan_integrated_verification", edit: func(out *Output) { out.IntegratedVerification = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := base()
			tc.edit(out)
			_, err := NormalizeOutput(out)
			if err == nil {
				t.Fatalf("expected %s", tc.code)
			}
			typed, ok := err.(*domain.Error)
			if !ok || typed.Code != tc.code {
				t.Fatalf("error = %#v, want code %s", err, tc.code)
			}
		})
	}
}

func TestNormalizeOutputAllowsOrderedWriteReuseAndDeepCopies(t *testing.T) {
	input := &Output{
		Spec: domain.SpecContent{Outcome: "outcome", Requirements: []string{"original"}, SuccessCriteria: []string{"criterion"}},
		Tasks: []TaskTemplate{
			{Name: "base", CommitMessage: "base", Writes: []string{"shared.txt"}, Files: map[string]string{"shared.txt": "base"}, VerifyCommands: [][]string{{"true"}}},
			{Name: "refine", DependsOn: []string{"base"}, CommitMessage: "refine", Writes: []string{"shared.txt"}, Files: map[string]string{"shared.txt": "refined"}, VerifyCommands: [][]string{{"true"}}},
		},
		IntegratedVerification: [][]string{{"true"}},
	}

	out, err := NormalizeOutput(input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Tasks[1].Depth != 1 {
		t.Fatalf("dependent write must have depth 1: %+v", out.Tasks[1])
	}

	input.Spec.Requirements[0] = "mutated"
	input.Tasks[0].Files["shared.txt"] = "mutated"
	input.Tasks[1].DependsOn[0] = "mutated"
	input.IntegratedVerification[0][0] = "false"
	if out.Spec.Requirements[0] != "original" || out.Tasks[0].Files["shared.txt"] != "base" ||
		out.Tasks[1].DependsOn[0] != "base" || out.IntegratedVerification[0][0] != "true" {
		t.Fatalf("normalized output aliases planner-owned memory: %+v", out)
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
        verify: [["true"]]
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
        verify: [["true"]]
`)
	p := NewDeterministic()
	_, err := p.Plan(t.Context(), PlanInput{Request: "update things", RepoFiles: files})
	if domain.ErrKindOf(err) != domain.ErrKindConflict {
		t.Fatalf("ambiguous request must conflict, got %v", err)
	}
}

func TestParseCatalogRejectsMissingOrMalformed(t *testing.T) {
	if _, err := ParseCatalog(map[string][]byte{}); err == nil {
		t.Fatal("missing catalog must fail")
	}
	bad := map[string][]byte{catalogPath: []byte("version: 99\ncapabilities: []")}
	if _, err := ParseCatalog(bad); err == nil {
		t.Fatal("unsupported catalog version must fail")
	}
	bad2 := map[string][]byte{catalogPath: []byte("not: [valid")}
	if _, err := ParseCatalog(bad2); err == nil {
		t.Fatal("malformed yaml must fail")
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
        verify: [["true"]]
`)}
	if _, err := ParseCatalog(noVerify); err == nil {
		t.Fatal("capability without integrated verification must fail validation")
	}
}

func TestBranchNamesAreDeterministicAndClean(t *testing.T) {
	cases := map[string]string{
		"implement-add": "ants/task-implement-add",
		"Fix_Bug #42!":  "ants/task-fix-bug-42",
		"UPPER":         "ants/task-upper",
	}
	for name, want := range cases {
		if got := branchFor(name); got != want {
			t.Errorf("branchFor(%q) = %q, want %q", name, got, want)
		}
	}
}
