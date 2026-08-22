package postgres

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

type OutboxRepository struct{ st *Store }

var _ ports.OutboxStore = (*OutboxRepository)(nil)

const outboxColumns = `id, dedup_key, tenant_id, envelope, attempts, max_attempts`

// Publish inserts idempotently: a repeated dedup key is a no-op so replays
// of the same logical transition never double-queue. It participates in the
// caller's transaction when one is active.
func (r *OutboxRepository) Publish(ctx context.Context, msg ports.OutboxMessage) error {
	// Same bound the schema's CHECK enforces, surfaced as a typed domain
	// error instead of a raw constraint violation.
	if msg.MaxAttempts < 1 || msg.MaxAttempts > 100 {
		return domain.Invalidf("outbox_max_attempts", "outbox max attempts must be within [1,100], got %d", msg.MaxAttempts)
	}
	now := r.st.now()
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO outbox (id, dedup_key, tenant_id, envelope, status, attempts, max_attempts, available_at, created_at)
		 VALUES ($1,$2,$3,$4,'pending',0,$5,$6,$6)
		 ON CONFLICT (dedup_key) DO NOTHING`,
		msg.ID, msg.DedupKey, string(msg.TenantID), msg.Envelope, msg.MaxAttempts, now)
	if err != nil {
		return wrapWrite(err)
	}
	// A repeated dedup key is an idempotent no-op: replays of the same
	// logical transition must never double-queue.
	return nil
}

// Lease atomically claims up to Limit due messages for one worker. Due means
// publish-visible or whose previous lease expired — both measured on this
// store's clock. FOR UPDATE SKIP LOCKED keeps concurrent dispatchers from
// overlapping claims.
func (r *OutboxRepository) Lease(ctx context.Context, req ports.OutboxLeaseRequest) ([]ports.OutboxMessage, error) {
	now := r.st.now()
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`UPDATE outbox
		 SET status = 'leased', leased_by = $1, lease_until = $2, attempts = attempts + 1
		 WHERE id IN (
		   SELECT id FROM outbox
		   WHERE (status = 'pending' AND available_at <= $3)
		      OR (status = 'leased' AND lease_until <= $3)
		   ORDER BY created_at, id
		   LIMIT $4
		   FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+outboxColumns,
		req.WorkerID, now.Add(req.LeaseFor), now, req.Limit)
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []ports.OutboxMessage{}
	for rows.Next() {
		var (
			m        ports.OutboxMessage
			envelope []byte
			tenantID string
		)
		if err := rows.Scan(&m.ID, &m.DedupKey, &tenantID, &envelope, &m.Attempts, &m.MaxAttempts); err != nil {
			return nil, wrapScan(err)
		}
		m.TenantID = domain.TenantID(tenantID)
		m.Envelope = envelope
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkDelivered acknowledges a message on behalf of its current lessee;
// stale or foreign acknowledgments are rejected.
func (r *OutboxRepository) MarkDelivered(ctx context.Context, id, leasedBy string) error {
	res, err := r.st.q(ctx).ExecContext(ctx,
		`UPDATE outbox SET status = 'delivered', delivered_at = $3, lease_until = NULL
		 WHERE id = $1 AND status = 'leased' AND leased_by = $2`,
		id, leasedBy, r.st.now())
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return domain.NotFoundf("outbox message", id)
	}
	return nil
}

// FailWithBackoff reschedules a failed delivery for its current lessee.
// The retry instant is computed by THIS store from its own clock
// (retryIn from now); callers never contribute timestamps. Exhausting
// attempts dead-letters the message. The decision and the release of the
// lease are one atomic statement: attempts were already incremented at
// claim time, so `attempts >= max_attempts` here is the final verdict. A
// stale or foreign call (lease expired and reclaimed meanwhile) matches no
// row and surfaces as not-found instead of being silently swallowed.
func (r *OutboxRepository) FailWithBackoff(ctx context.Context, id, leasedBy string, retryIn time.Duration, cause string) error {
	now := r.st.now()
	res, err := r.st.q(ctx).ExecContext(ctx,
		`UPDATE outbox
		 SET status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
		     available_at = $3, lease_until = NULL, last_error = $4, leased_by = ''
		 WHERE id = $1 AND status = 'leased' AND leased_by = $2`,
		id, leasedBy, now.Add(retryIn), truncateCause(cause))
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return domain.NotFoundf("outbox message", id)
	}
	return nil
}

func (r *OutboxRepository) Stats(ctx context.Context) (ports.OutboxStats, error) {
	var stats ports.OutboxStats
	err := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE status = 'pending'),
		   COUNT(*) FILTER (WHERE status = 'leased'),
		   COUNT(*) FILTER (WHERE status = 'delivered'),
		   COUNT(*) FILTER (WHERE status = 'dead')
		 FROM outbox`).Scan(&stats.Pending, &stats.Leased, &stats.Delivered, &stats.Dead)
	if err != nil {
		return stats, wrapScan(err)
	}
	return stats, nil
}

func truncateCause(cause string) string {
	const max = 512
	if len(cause) > max {
		return cause[:max]
	}
	return cause
}
