package orchestration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/planner"
)

func (e *Engine) persistPlannedTasks(ctx context.Context, st *runState, templates []planner.TaskTemplate) error {
	idsByName := make(map[string]domain.TaskID, len(templates))
	for _, tmpl := range templates {
		if err := st.reserveTaskBudget(); err != nil {
			return e.failRunBudgetExhausted(ctx, st, err)
		}
		taskID, err := e.newID(domain.PrefixTask)
		if err != nil {
			return err
		}
		idsByName[tmpl.Name] = domain.TaskID(taskID)
	}

	for _, tmpl := range templates {
		dependencies := make([]domain.TaskID, 0, len(tmpl.DependsOn))
		for _, dependencyName := range tmpl.DependsOn {
			dependencyID, ok := idsByName[dependencyName]
			if !ok {
				return domain.Internalf(nil, "plan_dependency_resolution", "task %q dependency %q was not allocated", tmpl.Name, dependencyName)
			}
			dependencies = append(dependencies, dependencyID)
		}
		task, err := domain.NewTask(
			idsByName[tmpl.Name],
			st.run.TenantID,
			st.run.ID,
			st.run.ThreadID,
			tmpl.Name,
			domain.TaskKindCodeChange,
			tmpl.Depth,
			dependencies,
			e.cfg.MaxAttempts,
			e.deps.Clock.Now(),
		)
		if err != nil {
			return e.failRun(ctx, st, "task_invalid", err)
		}
		if err := e.deps.Tasks.Create(ctx, task); err != nil {
			return e.failRun(ctx, st, "task_persist", err)
		}
		st.taskTemplates[task.ID] = tmpl
		st.tasks = append(st.tasks, task)
		if err := e.emitEvent(ctx, evtFromTask(task, domain.EventTaskCreated, map[string]any{
			"name":       tmpl.Name,
			"depth":      tmpl.Depth,
			"depends_on": taskIDStrings(dependencies),
			"writes":     append([]string(nil), tmpl.Writes...),
		})); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) executeTaskGraph(ctx context.Context, st *runState) error {
	maxDepth := 0
	for _, task := range st.tasks {
		if task.Depth > maxDepth {
			maxDepth = task.Depth
		}
	}

	for depth := 0; depth <= maxDepth; depth++ {
		wave := make([]*domain.Task, 0)
		for _, task := range st.tasks {
			if task.Depth == depth {
				wave = append(wave, task)
			}
		}
		if len(wave) == 0 {
			return fmt.Errorf("task graph has no tasks at depth %d", depth)
		}

		runnable := make([]*domain.Task, 0, len(wave))
		for _, task := range wave {
			blockedBy, err := st.failedDependencies(task)
			if err != nil {
				return err
			}
			if len(blockedBy) == 0 {
				runnable = append(runnable, task)
				continue
			}
			if err := e.markTaskBlocked(ctx, task, blockedBy); err != nil {
				return err
			}
		}
		if err := e.executeTaskWave(ctx, st, runnable); err != nil {
			return err
		}
	}

	sort.SliceStable(st.results, func(i, j int) bool {
		return indexIn(st.tasks, st.results[i].task) < indexIn(st.tasks, st.results[j].task)
	})
	var stuck []string
	for _, task := range st.tasks {
		switch task.Status {
		case domain.TaskVerifying, domain.TaskDone, domain.TaskFailed, domain.TaskBlocked, domain.TaskCancelled:
		default:
			stuck = append(stuck, fmt.Sprintf("%s=%s", task.Name, task.Status))
		}
	}
	if len(stuck) > 0 {
		return fmt.Errorf("tasks left in intermediate states: %s", strings.Join(stuck, ", "))
	}
	return nil
}

func (e *Engine) executeTaskWave(ctx context.Context, st *runState, tasks []*domain.Task) error {
	results := make([]*taskResult, len(tasks))
	failures := make([]error, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.cfg.MaxParallelTasks)
	for i, task := range tasks {
		wg.Add(1)
		go func(index int, current *domain.Task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index], failures[index] = e.executeTask(ctx, st, current, st.taskTemplates[current.ID])
		}(i, task)
	}
	wg.Wait()

	for i, result := range results {
		if result != nil {
			st.results = append(st.results, result)
			st.evidence = append(st.evidence, result.evidence...)
		}
		if failures[i] != nil {
			return fmt.Errorf("execute task %s: %w", tasks[i].Name, failures[i])
		}
	}
	return nil
}

func (st *runState) failedDependencies(task *domain.Task) ([]*domain.Task, error) {
	blockedBy := make([]*domain.Task, 0)
	for _, dependencyID := range task.DependsOn {
		dependency := st.taskByID(dependencyID)
		if dependency == nil {
			return nil, fmt.Errorf("task %s references missing dependency %s", task.Name, dependencyID)
		}
		switch dependency.Status {
		case domain.TaskVerifying, domain.TaskDone:
			// A dependency has completed its Builder verification. Integration
			// remains a later run-wide stage.
		case domain.TaskFailed, domain.TaskBlocked, domain.TaskCancelled:
			blockedBy = append(blockedBy, dependency)
		default:
			return nil, fmt.Errorf("task %s dependency %s ended its wave in state %s", task.Name, dependency.Name, dependency.Status)
		}
	}
	return blockedBy, nil
}

func (e *Engine) markTaskBlocked(ctx context.Context, task *domain.Task, blockedBy []*domain.Task) error {
	if err := e.transitionTask(ctx, task, domain.TaskQueued); err != nil {
		return err
	}
	names := make([]string, 0, len(blockedBy))
	ids := make([]string, 0, len(blockedBy))
	for _, dependency := range blockedBy {
		names = append(names, fmt.Sprintf("%s (%s)", dependency.Name, dependency.Status))
		ids = append(ids, string(dependency.ID))
	}
	sort.Strings(names)
	sort.Strings(ids)
	failure := &domain.FailureInfo{
		Code:      "task_dependency_failed",
		Message:   "dependency tasks did not complete: " + strings.Join(names, ", "),
		Transient: false,
	}
	return e.deps.Uow.Do(ctx, func(ctx context.Context) error {
		expected := task.Version
		task.Failure = failure
		if err := task.TransitionTo(domain.TaskBlocked); err != nil {
			return err
		}
		if err := e.deps.Tasks.Update(ctx, task, expected); err != nil {
			return err
		}
		return e.emitEvent(ctx, evtFromTask(task, domain.EventTaskStatusChanged, map[string]any{
			"to":         string(domain.TaskBlocked),
			"code":       failure.Code,
			"blocked_by": ids,
		}))
	})
}

func (e *Engine) prepareTaskBranch(ctx context.Context, st *runState, task *domain.Task, tmpl planner.TaskTemplate) (string, error) {
	branch := tmpl.Branch
	if branch != planner.BranchForTask(task.Name) {
		return "", domain.Internalf(nil, "plan_branch_mismatch", "task %q branch %q is not canonical", task.Name, branch)
	}
	if len(task.DependsOn) == 0 {
		return branch, e.deps.SCM.CreateBranch(ctx, st.repo, branch, st.project.DefaultBranch)
	}

	first := st.taskByID(task.DependsOn[0])
	if first == nil {
		return "", domain.Internalf(nil, "plan_dependency_resolution", "task %q first dependency is missing", task.Name)
	}
	firstTemplate, ok := st.taskTemplates[first.ID]
	if !ok {
		return "", domain.Internalf(nil, "plan_dependency_resolution", "task %q first dependency template is missing", task.Name)
	}
	if err := e.deps.SCM.CreateBranch(ctx, st.repo, branch, firstTemplate.Branch); err != nil {
		return "", err
	}
	for _, dependencyID := range task.DependsOn[1:] {
		dependency := st.taskByID(dependencyID)
		if dependency == nil {
			return "", domain.Internalf(nil, "plan_dependency_resolution", "task %q dependency %s is missing", task.Name, dependencyID)
		}
		dependencyTemplate, ok := st.taskTemplates[dependency.ID]
		if !ok {
			return "", domain.Internalf(nil, "plan_dependency_resolution", "task %q dependency %q template is missing", task.Name, dependency.Name)
		}
		merged, err := e.deps.SCM.Merge(ctx, st.repo, branch, dependencyTemplate.Branch,
			fmt.Sprintf("compose dependency %s for %s", dependency.Name, task.Name))
		if err != nil {
			return "", err
		}
		if len(merged.Conflicts) > 0 {
			return "", domain.Conflictf(
				"dependency_merge_conflict",
				"task %q dependency branches conflict in: %s",
				task.Name,
				strings.Join(merged.Conflicts, ", "),
			)
		}
	}
	return branch, nil
}

func (st *runState) taskByID(id domain.TaskID) *domain.Task {
	for _, task := range st.tasks {
		if task.ID == id {
			return task
		}
	}
	return nil
}

func taskIDStrings(ids []domain.TaskID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
