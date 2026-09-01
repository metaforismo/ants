package planner

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/metaforismo/ants/internal/domain"
)

// NormalizeOutput treats planner output as untrusted structured input. It
// returns a deep copy with canonical branches, normalized ownership, a stable
// topological order, and computed task depths.
func NormalizeOutput(out *Output) (*Output, error) {
	if out == nil {
		return nil, domain.Invalidf("plan_nil_output", "planner returned no output")
	}
	if len(out.Tasks) == 0 {
		return nil, domain.Invalidf("plan_no_tasks", "plan must contain at least one task")
	}
	if len(out.IntegratedVerification) == 0 {
		return nil, domain.Invalidf("plan_integrated_verification", "plan must contain integrated verification commands")
	}
	for i, cmd := range out.IntegratedVerification {
		if len(cmd) == 0 {
			return nil, domain.Invalidf("plan_integrated_verification", "integrated verification command %d is empty", i)
		}
	}

	tasks := make([]TaskTemplate, len(out.Tasks))
	indexByName := make(map[string]int, len(out.Tasks))
	nameByBranch := make(map[string]string, len(out.Tasks))
	for i, raw := range out.Tasks {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return nil, domain.Invalidf("plan_task_name", "task %d has an empty name", i)
		}
		if previous, exists := indexByName[name]; exists {
			return nil, domain.Invalidf("plan_duplicate_task", "task name %q is duplicated at positions %d and %d", name, previous, i)
		}
		indexByName[name] = i

		branch := BranchForTask(name)
		if other, exists := nameByBranch[branch]; exists {
			return nil, domain.Invalidf("plan_branch_collision", "tasks %q and %q map to the same branch %q", other, name, branch)
		}
		nameByBranch[branch] = name

		commitMessage := strings.TrimSpace(raw.CommitMessage)
		if commitMessage == "" {
			return nil, domain.Invalidf("plan_task_commit", "task %q has an empty commit message", name)
		}
		if len(raw.Files) == 0 {
			return nil, domain.Invalidf("plan_task_files", "task %q has no deterministic file changes", name)
		}
		if len(raw.VerifyCommands) == 0 {
			return nil, domain.Invalidf("plan_task_verification", "task %q has no verification commands", name)
		}
		for j, cmd := range raw.VerifyCommands {
			if len(cmd) == 0 {
				return nil, domain.Invalidf("plan_task_verification", "task %q verification command %d is empty", name, j)
			}
		}

		files, err := normalizeFiles(name, raw.Files)
		if err != nil {
			return nil, err
		}
		writes, err := normalizeWrites(name, raw.Writes, files)
		if err != nil {
			return nil, err
		}
		dependencies, err := normalizeDependencies(name, raw.DependsOn)
		if err != nil {
			return nil, err
		}
		tasks[i] = TaskTemplate{
			Name:           name,
			Branch:         branch,
			CommitMessage:  commitMessage,
			Writes:         writes,
			DependsOn:      dependencies,
			Files:          files,
			VerifyCommands: cloneCommands(raw.VerifyCommands),
		}
	}

	for _, task := range tasks {
		for _, dependency := range task.DependsOn {
			if _, exists := indexByName[dependency]; !exists {
				return nil, domain.Invalidf("plan_missing_dependency", "task %q depends on unknown task %q", task.Name, dependency)
			}
		}
	}

	ordered, ancestors, err := topologicalTasks(tasks, indexByName)
	if err != nil {
		return nil, err
	}
	if err := validateWriteOwnership(ordered, ancestors); err != nil {
		return nil, err
	}

	return &Output{
		Spec:                   cloneSpec(out.Spec),
		Tasks:                  ordered,
		IntegratedVerification: cloneCommands(out.IntegratedVerification),
	}, nil
}

func normalizeFiles(taskName string, raw map[string]string) (map[string]string, error) {
	files := make(map[string]string, len(raw))
	for rawPath, content := range raw {
		filePath, err := canonicalRepoPath(rawPath)
		if err != nil {
			return nil, domain.Invalidf("plan_write_path", "task %q file path %q is invalid: %v", taskName, rawPath, err)
		}
		files[filePath] = content
	}
	return files, nil
}

func normalizeWrites(taskName string, raw []string, files map[string]string) ([]string, error) {
	writes := raw
	if len(writes) == 0 {
		writes = make([]string, 0, len(files))
		for filePath := range files {
			writes = append(writes, filePath)
		}
	}

	seen := make(map[string]bool, len(writes))
	normalized := make([]string, 0, len(writes))
	for _, rawPath := range writes {
		writePath, err := canonicalRepoPath(rawPath)
		if err != nil {
			return nil, domain.Invalidf("plan_write_path", "task %q write path %q is invalid: %v", taskName, rawPath, err)
		}
		if seen[writePath] {
			return nil, domain.Invalidf("plan_duplicate_write", "task %q declares write path %q more than once", taskName, writePath)
		}
		seen[writePath] = true
		normalized = append(normalized, writePath)
	}
	for filePath := range files {
		if !seen[filePath] {
			return nil, domain.Invalidf("plan_write_ownership", "task %q changes %q without owning it in writes", taskName, filePath)
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeDependencies(taskName string, raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	dependencies := make([]string, 0, len(raw))
	for _, dependency := range raw {
		name := strings.TrimSpace(dependency)
		if name == "" {
			return nil, domain.Invalidf("plan_dependency_name", "task %q has an empty dependency", taskName)
		}
		if name == taskName {
			return nil, domain.Invalidf("plan_self_dependency", "task %q cannot depend on itself", taskName)
		}
		if seen[name] {
			return nil, domain.Invalidf("plan_duplicate_dependency", "task %q depends on %q more than once", taskName, name)
		}
		seen[name] = true
		dependencies = append(dependencies, name)
	}
	return dependencies, nil
}

func topologicalTasks(tasks []TaskTemplate, indexByName map[string]int) ([]TaskTemplate, map[string]map[string]bool, error) {
	ordered := make([]TaskTemplate, 0, len(tasks))
	emitted := make(map[string]bool, len(tasks))
	depthByName := make(map[string]int, len(tasks))
	ancestors := make(map[string]map[string]bool, len(tasks))

	for len(ordered) < len(tasks) {
		progressed := false
		for _, task := range tasks {
			if emitted[task.Name] {
				continue
			}
			ready := true
			depth := 0
			for _, dependency := range task.DependsOn {
				if !emitted[dependency] {
					ready = false
					break
				}
				if candidate := depthByName[dependency] + 1; candidate > depth {
					depth = candidate
				}
			}
			if !ready {
				continue
			}

			copyTask := task
			copyTask.Depth = depth
			ordered = append(ordered, copyTask)
			emitted[task.Name] = true
			depthByName[task.Name] = depth
			ancestors[task.Name] = make(map[string]bool)
			for _, dependency := range task.DependsOn {
				ancestors[task.Name][dependency] = true
				for ancestor := range ancestors[dependency] {
					ancestors[task.Name][ancestor] = true
				}
			}
			progressed = true
		}
		if progressed {
			continue
		}

		cyclic := make([]string, 0)
		for name := range indexByName {
			if !emitted[name] {
				cyclic = append(cyclic, name)
			}
		}
		sort.Slice(cyclic, func(i, j int) bool { return indexByName[cyclic[i]] < indexByName[cyclic[j]] })
		return nil, nil, domain.Invalidf("plan_task_cycle", "task graph contains a cycle involving: %s", strings.Join(cyclic, ", "))
	}
	return ordered, ancestors, nil
}

func validateWriteOwnership(tasks []TaskTemplate, ancestors map[string]map[string]bool) error {
	owners := make(map[string][]string)
	for _, task := range tasks {
		for _, writePath := range task.Writes {
			for _, other := range owners[writePath] {
				if !ancestors[task.Name][other] && !ancestors[other][task.Name] {
					return domain.Invalidf(
						"plan_parallel_write_collision",
						"tasks %q and %q both own %q without a dependency ordering",
						other,
						task.Name,
						writePath,
					)
				}
			}
			owners[writePath] = append(owners[writePath], task.Name)
		}
	}
	return nil
}

func canonicalRepoPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("path must be repository-relative and use forward slashes")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes the repository")
	}
	if cleaned != raw {
		return "", fmt.Errorf("path must be canonical")
	}
	return cleaned, nil
}

func cloneSpec(in domain.SpecContent) domain.SpecContent {
	return domain.SpecContent{
		Outcome:         in.Outcome,
		Requirements:    append([]string(nil), in.Requirements...),
		Assumptions:     append([]string(nil), in.Assumptions...),
		NonGoals:        append([]string(nil), in.NonGoals...),
		SuccessCriteria: append([]string(nil), in.SuccessCriteria...),
		Blockers:        append([]string(nil), in.Blockers...),
	}
}

func cloneCommands(in [][]string) [][]string {
	out := make([][]string, len(in))
	for i := range in {
		out[i] = append([]string(nil), in[i]...)
	}
	return out
}
