package postgres

import (
	"context"
	"database/sql"

	"github.com/metaforismo/ants/internal/domain"
)

type TaskRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Task) error
	Get(context.Context, domain.TenantID, domain.TaskID) (*domain.Task, error)
	Update(context.Context, *domain.Task, int64) error
	ListByRun(context.Context, domain.TenantID, domain.RunID) ([]*domain.Task, error)
} = (*TaskRepository)(nil)

const taskColumns = `id, tenant_id, run_id, thread_id, name, kind, status,
	depth, depends_on, attempts, max_attempts, failure, version, created_at, updated_at`

func scanTask(rows *sql.Rows) (*domain.Task, error) {
	var (
		t                       domain.Task
		dependsOn, failureBytes []byte
	)
	err := rows.Scan(&t.ID, &t.TenantID, &t.RunID, &t.ThreadID, &t.Name, &t.Kind,
		&t.Status, &t.Depth, &dependsOn, &t.Attempts, &t.MaxAttempts, &failureBytes,
		&t.Version, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	if err := unmarshalJSONColumn(dependsOn, &t.DependsOn); err != nil {
		return nil, err
	}
	if len(failureBytes) > 0 {
		t.Failure = &domain.FailureInfo{}
		if err := unmarshalJSONColumn(failureBytes, t.Failure); err != nil {
			return nil, err
		}
	}
	return &t, nil
}

func (r *TaskRepository) Create(ctx context.Context, task *domain.Task) error {
	dependsOn, err := marshalJSONColumn(nonNilTaskIDs(task.DependsOn))
	if err != nil {
		return err
	}
	var failure any
	if task.Failure != nil {
		b, merr := marshalJSONColumn(task.Failure)
		if merr != nil {
			return merr
		}
		failure = b
	}
	_, werr := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO tasks (id, tenant_id, run_id, thread_id, name, kind, status,
		   depth, depends_on, attempts, max_attempts, failure, version, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		string(task.ID), string(task.TenantID), string(task.RunID), string(task.ThreadID),
		task.Name, string(task.Kind), string(task.Status), task.Depth, dependsOn,
		task.Attempts, task.MaxAttempts, failure, task.Version, task.CreatedAt, task.UpdatedAt)
	if werr != nil {
		if mapped := mapUniqueViolation(werr, "task_exists", "task %s already exists", task.ID); mapped != werr {
			return mapped
		}
		return wrapWrite(werr)
	}
	return nil
}

func (r *TaskRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.TaskID) (*domain.Task, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.NotFoundf("task", id)
	}
	return scanTask(rows)
}

func (r *TaskRepository) Update(ctx context.Context, task *domain.Task, expectedVersion int64) error {
	dependsOn, err := marshalJSONColumn(nonNilTaskIDs(task.DependsOn))
	if err != nil {
		return err
	}
	var failure any
	if task.Failure != nil {
		b, merr := marshalJSONColumn(task.Failure)
		if merr != nil {
			return merr
		}
		failure = b
	}
	res, uerr := r.st.q(ctx).ExecContext(ctx,
		`UPDATE tasks
		 SET name = $3, kind = $4, status = $5, depth = $6, depends_on = $7,
		     attempts = $8, max_attempts = $9, failure = $10, updated_at = $11,
		     version = version + 1
		 WHERE id = $1 AND tenant_id = $2 AND version = $12`,
		string(task.ID), string(task.TenantID), task.Name, string(task.Kind),
		string(task.Status), task.Depth, dependsOn, task.Attempts, task.MaxAttempts,
		failure, timeNow(), expectedVersion)
	if uerr != nil {
		return wrapWrite(uerr)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return classifyStaleOrMissing(ctx, "task", string(task.ID), func() error {
			_, gerr := r.Get(ctx, task.TenantID, task.ID)
			return gerr
		})
	}
	task.Version = expectedVersion + 1
	return nil
}

func (r *TaskRepository) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Task, error) {
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE tenant_id = $1 AND run_id = $2 ORDER BY created_at, id`,
		string(tenantID), string(runID))
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
