package memory

import (
	"context"
	"sort"

	"github.com/metaforismo/ants/internal/domain"
)

type WorkspaceRepository struct{ st *storeState }

func (r *WorkspaceRepository) Create(_ context.Context, ws *domain.Workspace) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.workspaces[ws.ID]; exists {
		return domain.Conflictf("workspace_exists", "workspace %s already exists", ws.ID)
	}
	r.st.workspaces[ws.ID] = cloneWorkspace(ws)
	return nil
}

func (r *WorkspaceRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.WorkspaceID) (*domain.Workspace, error) {
	unlock := lockRead(r.st)
	defer unlock()
	ws, ok := r.st.workspaces[id]
	if !ok || ws.TenantID != tenantID {
		return nil, notFound("workspace", id)
	}
	return cloneWorkspace(ws), nil
}

func (r *WorkspaceRepository) Update(_ context.Context, ws *domain.Workspace, expectedVersion int64) error {
	unlock := lockWrite(r.st)
	defer unlock()
	cur, ok := r.st.workspaces[ws.ID]
	if !ok || cur.TenantID != ws.TenantID {
		return notFound("workspace", ws.ID)
	}
	if cur.Version != expectedVersion {
		return domain.NewStaleVersionError("workspace", ws.ID, expectedVersion, cur.Version)
	}
	stored := cloneWorkspace(ws)
	stored.Version = cur.Version + 1
	r.st.workspaces[ws.ID] = stored
	ws.Version = stored.Version
	return nil
}

func (r *WorkspaceRepository) ListByRun(_ context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Workspace, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Workspace{}
	for _, w := range r.st.workspaces {
		if w.RunID == runID && w.TenantID == tenantID {
			out = append(out, cloneWorkspace(w))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

type ArtifactRepository struct{ st *storeState }

func (r *ArtifactRepository) Create(_ context.Context, artifact *domain.Artifact) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.artifacts[artifact.ID]; exists {
		return domain.Conflictf("artifact_exists", "artifact %s already exists", artifact.ID)
	}
	r.st.artifacts[artifact.ID] = cloneArtifact(artifact)
	return nil
}

func (r *ArtifactRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.ArtifactID) (*domain.Artifact, error) {
	unlock := lockRead(r.st)
	defer unlock()
	a, ok := r.st.artifacts[id]
	if !ok || a.TenantID != tenantID {
		return nil, notFound("artifact", id)
	}
	return cloneArtifact(a), nil
}

func (r *ArtifactRepository) ListByRun(_ context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Artifact, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Artifact{}
	for _, a := range r.st.artifacts {
		if a.RunID == runID && a.TenantID == tenantID {
			out = append(out, cloneArtifact(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
