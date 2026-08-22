package postgres

import (
	"context"
	"database/sql"

	"github.com/metaforismo/ants/internal/domain"
)

type WorkspaceRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Workspace) error
	Get(context.Context, domain.TenantID, domain.WorkspaceID) (*domain.Workspace, error)
	Update(context.Context, *domain.Workspace, int64) error
	ListByRun(context.Context, domain.TenantID, domain.RunID) ([]*domain.Workspace, error)
} = (*WorkspaceRepository)(nil)

const workspaceColumns = `id, tenant_id, task_id, run_id, driver, repo_ref, branch,
	base_sha, head_sha, status, version, created_at`

func scanWorkspace(rows *sql.Rows) (*domain.Workspace, error) {
	var w domain.Workspace
	err := rows.Scan(&w.ID, &w.TenantID, &w.TaskID, &w.RunID, &w.Driver, &w.RepoRef,
		&w.Branch, &w.BaseSHA, &w.HeadSHA, &w.Status, &w.Version, &w.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	return &w, nil
}

func (r *WorkspaceRepository) Create(ctx context.Context, ws *domain.Workspace) error {
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO workspaces (id, tenant_id, task_id, run_id, driver, repo_ref, branch,
		   base_sha, head_sha, status, version, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(ws.ID), string(ws.TenantID), string(ws.TaskID), string(ws.RunID),
		ws.Driver, ws.RepoRef, ws.Branch, ws.BaseSHA, ws.HeadSHA,
		string(ws.Status), ws.Version, ws.CreatedAt)
	if err != nil {
		if mapped := mapUniqueViolation(err, "workspace_exists", "workspace %s already exists", ws.ID); mapped != err {
			return mapped
		}
		return wrapWrite(err)
	}
	return nil
}

func (r *WorkspaceRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.WorkspaceID) (*domain.Workspace, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("workspace", id)
	}
	return scanWorkspace(rows)
}

func (r *WorkspaceRepository) Update(ctx context.Context, ws *domain.Workspace, expectedVersion int64) error {
	res, err := r.st.q(ctx).ExecContext(ctx,
		`UPDATE workspaces
		 SET driver = $3, repo_ref = $4, branch = $5, base_sha = $6, head_sha = $7,
		     status = $8, version = version + 1
		 WHERE id = $1 AND tenant_id = $2 AND version = $9`,
		string(ws.ID), string(ws.TenantID), ws.Driver, ws.RepoRef, ws.Branch,
		ws.BaseSHA, ws.HeadSHA, string(ws.Status), expectedVersion)
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return classifyStaleOrMissing(ctx, "workspace", string(ws.ID), func() error {
			_, gerr := r.Get(ctx, ws.TenantID, ws.ID)
			return gerr
		})
	}
	ws.Version = expectedVersion + 1
	return nil
}

func (r *WorkspaceRepository) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Workspace, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE tenant_id = $1 AND run_id = $2 ORDER BY created_at, id`,
		string(tenantID), string(runID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.Workspace{}
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

type ArtifactRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Artifact) error
	Get(context.Context, domain.TenantID, domain.ArtifactID) (*domain.Artifact, error)
	ListByRun(context.Context, domain.TenantID, domain.RunID) ([]*domain.Artifact, error)
} = (*ArtifactRepository)(nil)

const artifactColumns = `id, tenant_id, run_id, kind, digest, size_bytes,
	retention, producer_task_id, content, created_at`

func scanArtifact(rows *sql.Rows) (*domain.Artifact, error) {
	var (
		a              domain.Artifact
		content        []byte
		producerTaskID sql.NullString
	)
	err := rows.Scan(&a.ID, &a.TenantID, &a.RunID, &a.Kind, &a.Digest, &a.SizeBytes,
		&a.Retention, &producerTaskID, &content, &a.CreatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	if producerTaskID.Valid {
		a.ProducerTaskID = domain.TaskID(producerTaskID.String)
	}
	a.Content = content
	return &a, nil
}

func (r *ArtifactRepository) Create(ctx context.Context, artifact *domain.Artifact) error {
	var producer any
	if artifact.ProducerTaskID != "" {
		producer = string(artifact.ProducerTaskID)
	}
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO artifacts (id, tenant_id, run_id, kind, digest, size_bytes,
		   retention, producer_task_id, content, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		string(artifact.ID), string(artifact.TenantID), string(artifact.RunID),
		string(artifact.Kind), artifact.Digest, artifact.SizeBytes,
		string(artifact.Retention), producer, artifact.Content, artifact.CreatedAt)
	if err != nil {
		if mapped := mapUniqueViolation(err, "artifact_exists", "artifact %s already exists", artifact.ID); mapped != err {
			return mapped
		}
		return wrapWrite(err)
	}
	return nil
}

func (r *ArtifactRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.ArtifactID) (*domain.Artifact, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("artifact", id)
	}
	return scanArtifact(rows)
}

func (r *ArtifactRepository) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Artifact, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE tenant_id = $1 AND run_id = $2 ORDER BY created_at, id`,
		string(tenantID), string(runID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
