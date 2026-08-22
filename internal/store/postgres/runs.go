package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

type RunRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Run) error
	Get(context.Context, domain.TenantID, domain.RunID) (*domain.Run, error)
	Update(context.Context, *domain.Run, int64) error
	GetByIdempotencyKey(context.Context, domain.TenantID, domain.ThreadID, string) (*domain.Run, error)
} = (*RunRepository)(nil)

const runColumns = `id, tenant_id, thread_id, spec_id, status, idempotency_key,
	task_ids, report, principal, failure, version, created_at, updated_at, finished_at`

func scanRun(row *sql.Row) (*domain.Run, error) {
	var (
		r               domain.Run
		specID          sql.NullString
		taskIDs, report []byte
		failure         []byte
		principal       sql.NullString
		finishedAt      sql.NullTime
	)
	err := row.Scan(&r.ID, &r.TenantID, &r.ThreadID, &specID, &r.Status,
		&r.IdempotencyKey, &taskIDs, &report, &principal, &failure,
		&r.Version, &r.CreatedAt, &r.UpdatedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NotFoundf("run", "row")
	}
	if err != nil {
		return nil, wrapScan(err)
	}
	if specID.Valid {
		r.SpecID = domain.SpecID(specID.String)
	}
	if principal.Valid {
		r.Principal = domain.PrincipalID(principal.String)
	}
	if finishedAt.Valid {
		ts := finishedAt.Time.UTC()
		r.FinishedAt = &ts
	}
	if err := unmarshalJSONColumn(taskIDs, &r.TaskIDs); err != nil {
		return nil, err
	}
	if len(report) > 0 {
		r.Report = &domain.RunReport{}
		if err := unmarshalJSONColumn(report, r.Report); err != nil {
			return nil, err
		}
	}
	if len(failure) > 0 {
		r.Failure = &domain.FailureInfo{}
		if err := unmarshalJSONColumn(failure, r.Failure); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func (s *Store) insertRun(ctx context.Context, run *domain.Run) error {
	taskIDs, err := marshalJSONColumn(nonNilTaskIDs(run.TaskIDs))
	if err != nil {
		return err
	}
	var report any
	if run.Report != nil {
		b, merr := marshalJSONColumn(run.Report)
		if merr != nil {
			return merr
		}
		report = b
	}
	var failure any
	if run.Failure != nil {
		b, merr := marshalJSONColumn(run.Failure)
		if merr != nil {
			return merr
		}
		failure = b
	}
	_, werr := s.q(ctx).ExecContext(ctx,
		`INSERT INTO runs (id, tenant_id, thread_id, spec_id, status, idempotency_key,
		   task_ids, report, principal, failure, version, created_at, updated_at, finished_at)
		 VALUES ($1,$2,$3,NULLIF($4,'')::text,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		string(run.ID), string(run.TenantID), string(run.ThreadID), string(run.SpecID),
		string(run.Status), run.IdempotencyKey, taskIDs, report,
		string(run.Principal), failure, run.Version, run.CreatedAt, run.UpdatedAt, nullTime(run.FinishedAt))
	if werr != nil {
		if mapped := mapUniqueViolation(werr, "run_idempotency_key_taken",
			"idempotency key already used for this thread"); mapped != werr {
			return mapped
		}
		return wrapWrite(werr)
	}
	return nil
}

func (r *RunRepository) Create(ctx context.Context, run *domain.Run) error {
	// Pre-check gives callers the same friendly conflict the memory store
	// produces; the unique index still guards the concurrent-insert race.
	if _, err := r.GetByIdempotencyKey(ctx, run.TenantID, run.ThreadID, run.IdempotencyKey); err == nil {
		return domain.Conflictf("run_idempotency_key_taken", "idempotency key already used for this thread")
	} else if domain.ErrKindOf(err) != domain.ErrKindNotFound {
		return err
	}
	return r.st.insertRun(ctx, run)
}

func (r *RunRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.RunID) (*domain.Run, error) {
	return scanRun(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID)))
}

func (r *RunRepository) Update(ctx context.Context, run *domain.Run, expectedVersion int64) error {
	taskIDs, err := marshalJSONColumn(nonNilTaskIDs(run.TaskIDs))
	if err != nil {
		return err
	}
	var report any
	if run.Report != nil {
		b, merr := marshalJSONColumn(run.Report)
		if merr != nil {
			return merr
		}
		report = b
	}
	var failure any
	if run.Failure != nil {
		b, merr := marshalJSONColumn(run.Failure)
		if merr != nil {
			return merr
		}
		failure = b
	}
	res, uerr := r.st.q(ctx).ExecContext(ctx,
		`UPDATE runs
		 SET spec_id = NULLIF($3,'')::text, status = $4, task_ids = $5, report = $6,
		     principal = $7, failure = $8, updated_at = $9,
		     version = version + 1, finished_at = $10
		 WHERE id = $1 AND tenant_id = $2 AND version = $11`,
		string(run.ID), string(run.TenantID), string(run.SpecID), string(run.Status),
		taskIDs, report, string(run.Principal), failure, timeNow(), nullTime(run.FinishedAt),
		expectedVersion)
	if uerr != nil {
		return wrapWrite(uerr)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return classifyStaleOrMissing(ctx, "run", string(run.ID), func() error {
			_, gerr := r.Get(ctx, run.TenantID, run.ID)
			return gerr
		})
	}
	run.Version = expectedVersion + 1
	return nil
}

func (r *RunRepository) GetByIdempotencyKey(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID, key string) (*domain.Run, error) {
	return scanRun(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs
		 WHERE tenant_id = $1 AND thread_id = $2 AND idempotency_key = $3`,
		string(tenantID), string(threadID), key))
}

func nonNilTaskIDs(in []domain.TaskID) []domain.TaskID {
	if in == nil {
		return []domain.TaskID{}
	}
	return in
}

func nullTime(ts *time.Time) any {
	if ts == nil {
		return nil
	}
	return *ts
}
