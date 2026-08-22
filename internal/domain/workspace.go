package domain

import "time"

// WorkspaceStatus is the lifecycle of one task's isolated working copy.
type WorkspaceStatus string

const (
	WorkspaceCreating  WorkspaceStatus = "creating"
	WorkspaceReady     WorkspaceStatus = "ready"
	WorkspaceCommitted WorkspaceStatus = "committed"
	WorkspaceFailed    WorkspaceStatus = "failed"
	WorkspaceReleased  WorkspaceStatus = "released"
)

var AllWorkspaceStatuses = []WorkspaceStatus{
	WorkspaceCreating,
	WorkspaceReady,
	WorkspaceCommitted,
	WorkspaceFailed,
	WorkspaceReleased,
}

var workspaceTransitions = transitionTable[WorkspaceStatus]{
	WorkspaceCreating:  {WorkspaceReady, WorkspaceFailed, WorkspaceReleased},
	WorkspaceReady:     {WorkspaceCommitted, WorkspaceFailed, WorkspaceReleased},
	WorkspaceCommitted: {WorkspaceReleased},
	WorkspaceFailed:    {WorkspaceReleased},
	WorkspaceReleased:  {},
}

func init() {
	if err := checkTransitionTable(AllWorkspaceStatuses, workspaceTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionWorkspace(from, to WorkspaceStatus) bool {
	return workspaceTransitions.allows(from, to)
}

type Workspace struct {
	ID        WorkspaceID     `json:"id"`
	TenantID  TenantID        `json:"tenant_id"`
	TaskID    TaskID          `json:"task_id"`
	RunID     RunID           `json:"run_id"`
	Driver    string          `json:"driver"`
	RepoRef   string          `json:"repo_ref"`
	Branch    string          `json:"branch"`
	BaseSHA   string          `json:"base_sha,omitempty"`
	HeadSHA   string          `json:"head_sha,omitempty"`
	Status    WorkspaceStatus `json:"status"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
}

func NewWorkspace(id WorkspaceID, tenantID TenantID, taskID TaskID, runID RunID, driver, repoRef, branch string, now time.Time) (*Workspace, error) {
	if _, err := ParseWorkspaceID(string(id)); err != nil {
		return nil, err
	}
	if driver == "" || repoRef == "" || branch == "" {
		return nil, Invalidf("workspace_identity", "workspace requires driver, repo ref and branch")
	}
	return &Workspace{
		ID:        id,
		TenantID:  tenantID,
		TaskID:    taskID,
		RunID:     runID,
		Driver:    driver,
		RepoRef:   repoRef,
		Branch:    branch,
		Status:    WorkspaceCreating,
		Version:   1,
		CreatedAt: now.UTC(),
	}, nil
}

func (w *Workspace) TransitionTo(next WorkspaceStatus) error {
	if w.Status == next {
		return NewInvalidTransitionError(w.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionWorkspace(w.Status, next) {
		return NewInvalidTransitionError(w.Status, next)
	}
	w.Status = next
	w.Version++
	return nil
}
