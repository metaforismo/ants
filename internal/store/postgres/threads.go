package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/metaforismo/ants/internal/domain"
)

type ThreadRepository struct{ st *Store }

var _ interface {
	Create(context.Context, *domain.Thread) error
	Get(context.Context, domain.TenantID, domain.ThreadID) (*domain.Thread, error)
	Update(context.Context, *domain.Thread, int64) error
	AppendMessage(context.Context, *domain.Message) error
	Messages(context.Context, domain.TenantID, domain.ThreadID, int64, int) ([]*domain.Message, int64, error)
} = (*ThreadRepository)(nil)

const threadColumns = `id, tenant_id, project_id, title, status, creator_id, version, created_at, updated_at`

func scanThread(row *sql.Row) (*domain.Thread, error) {
	var t domain.Thread
	err := row.Scan(&t.ID, &t.TenantID, &t.ProjectID, &t.Title, &t.Status, &t.CreatorID,
		&t.Version, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NotFoundf("thread", "row")
	}
	if err != nil {
		return nil, wrapScan(err)
	}
	return &t, nil
}

func (r *ThreadRepository) Create(ctx context.Context, thread *domain.Thread) error {
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO threads (id, tenant_id, project_id, title, status, creator_id, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		string(thread.ID), string(thread.TenantID), string(thread.ProjectID), thread.Title,
		string(thread.Status), string(thread.CreatorID), thread.Version, thread.CreatedAt, thread.UpdatedAt)
	if err != nil {
		return wrapWrite(err)
	}
	return nil
}

func (r *ThreadRepository) Get(ctx context.Context, tenantID domain.TenantID, id domain.ThreadID) (*domain.Thread, error) {
	return scanThread(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+threadColumns+` FROM threads WHERE id = $1 AND tenant_id = $2`,
		string(id), string(tenantID)))
}

// Update applies an optimistic-concurrency state change: the write only lands
// when the stored version still equals expectedVersion.
func (r *ThreadRepository) Update(ctx context.Context, thread *domain.Thread, expectedVersion int64) error {
	res, err := r.st.q(ctx).ExecContext(ctx,
		`UPDATE threads
		 SET title = $3, status = $4, creator_id = $5, updated_at = $6, version = version + 1
		 WHERE id = $1 AND tenant_id = $2 AND version = $7`,
		string(thread.ID), string(thread.TenantID), thread.Title, string(thread.Status),
		string(thread.CreatorID), timeNow(), expectedVersion)
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return classifyStaleOrMissing(ctx, "thread", string(thread.ID), func() error {
			_, gerr := r.Get(ctx, thread.TenantID, thread.ID)
			return gerr
		})
	}
	thread.Version = expectedVersion + 1
	return nil
}

// AppendMessage allocates the per-thread sequence inside a transaction that
// locks the parent thread row, so concurrent appends can never collide on
// (thread_id, seq).
func (r *ThreadRepository) AppendMessage(ctx context.Context, message *domain.Message) error {
	return withinAutoTx(ctx, r.st, func(ctx context.Context) error {
		if _, err := r.st.q(ctx).ExecContext(ctx,
			`SELECT 1 FROM threads WHERE id = $1 AND tenant_id = $2 FOR UPDATE`,
			string(message.ThreadID), string(message.TenantID)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.NotFoundf("thread", message.ThreadID)
			}
			return wrapWrite(err)
		}
		var nextSeq int64
		err := r.st.q(ctx).QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM thread_messages WHERE thread_id = $1`,
			string(message.ThreadID)).Scan(&nextSeq)
		if err != nil {
			return wrapScan(err)
		}
		message.Seq = nextSeq
		if _, err := r.st.q(ctx).ExecContext(ctx,
			`INSERT INTO thread_messages (thread_id, tenant_id, seq, id, role, delivery_mode, content, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			string(message.ThreadID), string(message.TenantID), message.Seq, string(message.ID),
			string(message.Role), string(message.DeliveryMode), message.Content, message.CreatedAt); err != nil {
			return wrapWrite(err)
		}
		return nil
	})
}

func (r *ThreadRepository) Messages(ctx context.Context, tenantID domain.TenantID, threadID domain.ThreadID, afterSeq int64, limit int) ([]*domain.Message, int64, error) {
	var total int64
	err := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM thread_messages WHERE thread_id = $1 AND tenant_id = $2`,
		string(threadID), string(tenantID)).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && total == 0) {
		// Distinguish unknown thread from a known-but-empty one.
		if _, getErr := r.Get(ctx, tenantID, threadID); getErr != nil {
			return nil, 0, getErr
		}
		if err != nil {
			return nil, 0, wrapScan(err)
		}
		return []*domain.Message{}, 0, nil
	}
	if err != nil {
		return nil, 0, wrapScan(err)
	}

	query := `SELECT id, tenant_id, thread_id, seq, role, delivery_mode, content, created_at
	          FROM thread_messages
	          WHERE thread_id = $1 AND tenant_id = $2 AND seq > $3
	          ORDER BY seq ASC`
	args := []any{string(threadID), string(tenantID), afterSeq}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := r.st.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, wrapScan(err)
	}
	defer rows.Close()

	out := []*domain.Message{}
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.TenantID, &m.ThreadID, &m.Seq, &m.Role, &m.DeliveryMode, &m.Content, &m.CreatedAt); err != nil {
			return nil, 0, wrapScan(err)
		}
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrapScan(err)
	}
	return out, total, nil
}
