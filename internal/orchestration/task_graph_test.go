package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
)

type plannerFunc func(context.Context, planner.PlanInput) (*planner.Output, error)

func (f plannerFunc) Plan(ctx context.Context, input planner.PlanInput) (*planner.Output, error) {
	return f(ctx, input)
}

type graphRecordingSCM struct {
	*scm.Memory
	mu         sync.Mutex
	lastHandle scm.Handle
}

func (r *graphRecordingSCM) Init(ctx context.Context, handle scm.Handle, seed scm.Seed) error {
	r.mu.Lock()
	r.lastHandle = handle
	r.mu.Unlock()
	return r.Memory.Init(ctx, handle, seed)
}

func arithmeticGraphPlan() *planner.Output {
	return &planner.Output{
		Spec: domain.SpecContent{
			Outcome:         "arithmetic helpers and their documentation are implemented",
			Requirements:    []string{"addition, multiplication, and documentation land on isolated task branches"},
			SuccessCriteria: []string{"bash tests/calc_test.sh exits 0 on the integrated graph"},
		},
		Tasks: []planner.TaskTemplate{
			{
				Name:          "implement-add",
				CommitMessage: "feat(calc): implement integer addition",
				Writes:        []string{"lib_add.sh"},
				Files: map[string]string{
					"lib_add.sh": "add() {\n  echo $(($1 + $2))\n}\n",
				},
				VerifyCommands: [][]string{{"test", "-f", "lib_add.sh"}, {"sh", "calc.sh", "add", "20", "22"}},
			},
			{
				Name:          "implement-multiply",
				CommitMessage: "feat(calc): implement integer multiplication",
				Writes:        []string{"lib_mul.sh"},
				Files: map[string]string{
					"lib_mul.sh": "multiply() {\n  echo $(($1 * $2))\n}\n",
				},
				VerifyCommands: [][]string{{"test", "-f", "lib_mul.sh"}, {"sh", "calc.sh", "multiply", "6", "7"}},
			},
			{
				Name:          "document-arithmetic",
				DependsOn:     []string{"implement-add", "implement-multiply"},
				CommitMessage: "docs(calc): document arithmetic operations",
				Writes:        []string{"OPERATIONS.md"},
				Files: map[string]string{
					"OPERATIONS.md": "# Arithmetic operations\n\nThe calculator supports addition and multiplication.\n",
				},
				VerifyCommands: [][]string{
					{"test", "-f", "OPERATIONS.md"},
					{"sh", "calc.sh", "add", "20", "22"},
					{"sh", "calc.sh", "multiply", "6", "7"},
				},
			},
		},
		IntegratedVerification: [][]string{{"bash", "tests/calc_test.sh"}},
	}
}

func TestTaskGraphPersistsDependenciesAndComposesParentBranches(t *testing.T) {
	h := newHarness(t)
	scriptHappyPath(h)
	h.fake.Script("test -f OPERATIONS.md", exit0())
	h.engine.deps.Planner = plannerFunc(func(context.Context, planner.PlanInput) (*planner.Output, error) {
		return arithmeticGraphPlan(), nil
	})
	recording := &graphRecordingSCM{Memory: h.memSCM}
	h.engine.deps.SCM = recording
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if err != nil {
		t.Fatalf("execute graph: %v\nreport=%+v", err, run.Report)
	}
	if run.Status != domain.RunCompleted || run.Report == nil || !run.Report.ReadyForReview {
		t.Fatalf("graph run not ready: status=%s report=%+v", run.Status, run.Report)
	}
	if len(run.Report.Tasks) != 3 || run.Report.Budget.TasksUsed != 3 {
		t.Fatalf("graph task accounting is wrong: %+v", run.Report)
	}

	persisted, err := h.repos.Tasks.ListByRun(context.Background(), testTenantID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*domain.Task, len(persisted))
	for _, task := range persisted {
		byName[task.Name] = task
		if task.Status != domain.TaskDone {
			t.Fatalf("task %s not done: %+v", task.Name, task)
		}
	}
	for _, root := range []string{"implement-add", "implement-multiply"} {
		if task := byName[root]; task == nil || task.Depth != 0 || len(task.DependsOn) != 0 {
			t.Fatalf("root task %s has wrong graph metadata: %+v", root, task)
		}
	}
	documentation := byName["document-arithmetic"]
	if documentation == nil || documentation.Depth != 1 || len(documentation.DependsOn) != 2 {
		t.Fatalf("dependent task has wrong graph metadata: %+v", documentation)
	}
	dependencyNames := make([]string, 0, len(documentation.DependsOn))
	for _, dependencyID := range documentation.DependsOn {
		for name, task := range byName {
			if task.ID == dependencyID {
				dependencyNames = append(dependencyNames, name)
			}
		}
	}
	if strings.Join(dependencyNames, ",") != "implement-add,implement-multiply" {
		t.Fatalf("dependent task points at wrong tasks: %v", dependencyNames)
	}

	files, err := recording.Files(context.Background(), recording.lastHandle, planner.BranchForTask("document-arithmetic"))
	if err != nil {
		t.Fatal(err)
	}
	for _, filePath := range []string{"lib_add.sh", "lib_mul.sh", "OPERATIONS.md"} {
		if _, ok := files[filePath]; !ok {
			t.Fatalf("dependent branch missing %s; files=%v", filePath, fileNames(files))
		}
	}
}

func TestTaskGraphBlocksDependentsButRunsIndependentTasks(t *testing.T) {
	h := newHarness(t)
	h.engine.deps.Planner = plannerFunc(func(context.Context, planner.PlanInput) (*planner.Output, error) {
		return &planner.Output{
			Spec: domain.SpecContent{
				Outcome:         "dependency failure is contained",
				Requirements:    []string{"independent work may finish while dependents are blocked"},
				SuccessCriteria: []string{"integrated verification would pass"},
			},
			Tasks: []planner.TaskTemplate{
				{Name: "failing-root", CommitMessage: "root", Files: map[string]string{"root.txt": "root"}, VerifyCommands: [][]string{{"verify", "root"}}},
				{Name: "independent", CommitMessage: "independent", Files: map[string]string{"independent.txt": "independent"}, VerifyCommands: [][]string{{"verify", "independent"}}},
				{Name: "dependent", DependsOn: []string{"failing-root"}, CommitMessage: "dependent", Files: map[string]string{"dependent.txt": "dependent"}, VerifyCommands: [][]string{{"verify", "dependent"}}},
			},
			IntegratedVerification: [][]string{{"verify", "integrated"}},
		}, nil
	})
	h.fake.Script("verify root", sandbox.ExecResult{ExitCode: 1})
	h.fake.Script("verify independent", exit0())
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if err == nil {
		t.Fatal("a failed root must fail the run")
	}
	if run.Status != domain.RunFailed || run.Failure == nil || run.Failure.Code != "task_verification_failed" {
		t.Fatalf("root failure classification was lost: %+v", run)
	}
	byName := make(map[string]domain.ReportTask, len(run.Report.Tasks))
	for _, task := range run.Report.Tasks {
		byName[task.Name] = task
	}
	if independent := byName["independent"]; independent.Status != domain.TaskVerifying || independent.Attempts != 1 {
		t.Fatalf("independent task did not finish its Builder verification: %+v", independent)
	}
	dependent := byName["dependent"]
	if dependent.Status != domain.TaskBlocked || dependent.Attempts != 0 || dependent.Failure == nil || dependent.Failure.Code != "task_dependency_failed" {
		t.Fatalf("dependent task was not explicitly blocked: %+v", dependent)
	}
	for _, call := range h.fake.ExecCalls() {
		if sandbox.JoinCommand(call.Command) == "verify dependent" {
			t.Fatal("blocked dependent task executed anyway")
		}
	}
}

func TestOrchestrationRevalidatesPlannerOutputBeforeCreatingTasks(t *testing.T) {
	h := newHarness(t)
	h.engine.deps.Planner = plannerFunc(func(context.Context, planner.PlanInput) (*planner.Output, error) {
		return &planner.Output{
			Spec: domain.SpecContent{Outcome: "invalid graph", Requirements: []string{"none"}, SuccessCriteria: []string{"none"}},
			Tasks: []planner.TaskTemplate{
				{Name: "one", CommitMessage: "one", Writes: []string{"shared.txt"}, Files: map[string]string{"shared.txt": "one"}, VerifyCommands: [][]string{{"true"}}},
				{Name: "two", CommitMessage: "two", Writes: []string{"shared.txt"}, Files: map[string]string{"shared.txt": "two"}, VerifyCommands: [][]string{{"true"}}},
			},
			IntegratedVerification: [][]string{{"true"}},
		}, nil
	})
	_, thread := h.seedWorld(t)

	run, err := startAndExecute(t, h, thread)
	if err == nil {
		t.Fatal("invalid provider graph must fail closed")
	}
	if run.Status != domain.RunFailed || run.Failure == nil || run.Failure.Code != "plan_failed" {
		t.Fatalf("invalid graph must fail in planning: %+v", run)
	}
	tasks, listErr := h.repos.Tasks.ListByRun(context.Background(), testTenantID, run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("invalid graph created task rows: %+v", tasks)
	}
}

func fileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	return names
}
