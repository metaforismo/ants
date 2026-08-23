package postgres

import (
	"context"
	"database/sql"
	"errors"
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
		return domain.NotFoundf("outbox_message", id)
	}
	return nil
}

// FailWithBackoff reschedules a failed delivery for its current lessee.
// The retry instant is computed by THIS store from its own clock
// (retryIn from now); callers never contribute timestamps. Exhausting
// attempts dead-letters the message — which also opens the next operator
// fencing epoch by bumping generation and stamping dead_at (ADR-0015).
// The decision and the release of the lease are one atomic statement:
// attempts were already incremented at claim time, so
// `attempts >= max_attempts` here is the final verdict. A stale or foreign
// call (lease expired and reclaimed meanwhile) matches no row and surfaces
// as not-found instead of being silently swallowed.
func (r *OutboxRepository) FailWithBackoff(ctx context.Context, id, leasedBy string, retryIn time.Duration, cause string) error {
	now := r.st.now()
	res, err := r.st.q(ctx).ExecContext(ctx,
		`UPDATE outbox
		 SET status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
		     available_at = $3,
		     lease_until = NULL,
		     last_error = $4,
		     leased_by = '',
		     generation = generation + CASE WHEN attempts >= max_attempts THEN 1 ELSE 0 END,
		     dead_at = CASE WHEN attempts >= max_attempts THEN $5 ELSE dead_at END
		 WHERE id = $1 AND status = 'leased' AND leased_by = $2`,
		id, leasedBy, now.Add(retryIn), truncateCause(cause), now)
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return domain.NotFoundf("outbox_message", id)
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
		   COUNT(*) FILTER (WHERE status = 'dead'),
		   COUNT(*) FILTER (WHERE status = 'discarded')
		 FROM outbox`).Scan(&stats.Pending, &stats.Leased, &stats.Delivered, &stats.Dead, &stats.Discarded)
	if err != nil {
		return stats, wrapScan(err)
	}
	return stats, nil
}

const deadLetterColumns = `id, dedup_key, tenant_id, status, attempts, max_attempts,
	generation, last_error, created_at, dead_at, discarded_at`

// ListDeadLetters pages actionable (dead) messages in deterministic
// (created_at, id) order behind the request's keyset cursor; the partial
// index on dead rows serves the whole scan. Discarded rows are history, not
// work: they appear through GetDeadLetter, never here.
func (r *OutboxRepository) ListDeadLetters(ctx context.Context, req ports.ListDeadLettersRequest) ([]ports.DeadLetterSummary, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	rows, err := r.st.q(ctx).QueryContext(ctx,
		`SELECT `+deadLetterColumns+` FROM outbox
		 WHERE tenant_id = $1 AND status = 'dead'
		   AND (created_at, id) > ($2, $3)
		 ORDER BY created_at, id
		 LIMIT $4`,
		string(req.TenantID), req.AfterCreatedAt, req.AfterID, req.Limit)
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []ports.DeadLetterSummary{}
	for rows.Next() {
		s, err := scanDeadLetter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// GetDeadLetter returns one dead or discarded message so operators can
// confirm terminal decisions landed; anything else is uniformly not-found —
// including foreign tenants and live messages, whose existence stays
// non-enumerable through this surface.
func (r *OutboxRepository) GetDeadLetter(ctx context.Context, tenantID domain.TenantID, messageID string) (*ports.DeadLetterSummary, error) {
	row := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+deadLetterColumns+` FROM outbox
		 WHERE id = $1 AND tenant_id = $2 AND status IN ('dead', 'discarded')`,
		messageID, string(tenantID))
	s, err := scanDeadLetter(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NotFoundf("outbox_message", messageID)
	}
	return s, err
}

// RequeueDeadLetter restarts a bounded delivery lifecycle from dead under
// the request's compare-and-swap credential. The whole operation joins the
// caller's unit of work so the composing service can commit the row change,
// its event, and its audit record together (ADR-0015); a rolled-back unit
// leaves the row untouched.
//
// Classification runs before the guarded UPDATE so precondition misses carry
// precise typed errors; the UPDATE re-checks every condition anyway, so a
// concurrent transition between the two statements still fails closed — the
// reclassification then reports what actually happened instead of a generic
// conflict.
func (r *OutboxRepository) RequeueDeadLetter(ctx context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	if err := req.Validate(); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	var result ports.OutboxMutationResult
	err := r.st.Do(ctx, func(ctx context.Context) error {
		state, cerr := r.loadOperatorTarget(ctx, req.TenantID, req.MessageID)
		if cerr != nil {
			return cerr
		}
		before := state.attempts
		if cerr := classifyOperatorTarget(state, domain.OutboxPending, req.ExpectedGeneration, req.MessageID); cerr != nil {
			return cerr
		}
		res, uerr := r.st.q(ctx).ExecContext(ctx,
			`UPDATE outbox
			 SET status = 'pending', attempts = 0, available_at = $4,
			     leased_by = '', lease_until = NULL,
			     generation = generation + 1, dead_at = NULL
			 WHERE id = $1 AND tenant_id = $2 AND status = 'dead' AND generation = $3`,
			req.MessageID, string(req.TenantID), req.ExpectedGeneration, r.st.now())
		if uerr != nil {
			return wrapWrite(uerr)
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return wrapWrite(aerr)
		}
		if affected == 0 {
			// Lost a race against another operator or dispatcher write;
			// classify again to report the current truth.
			state, cerr := r.loadOperatorTarget(ctx, req.TenantID, req.MessageID)
			if cerr != nil {
				return cerr
			}
			return classifyOperatorTarget(state, domain.OutboxPending, req.ExpectedGeneration, req.MessageID)
		}
		result = ports.OutboxMutationResult{
			MessageID:      req.MessageID,
			Action:         "requeue",
			AttemptsBefore: before,
			Generation:     req.ExpectedGeneration + 1,
		}
		return nil
	})
	return result, err
}

// DiscardDeadLetter terminates a dead letter explicitly under the same
// compare-and-swap discipline as requeue. The row is never deleted: the
// decision is recorded on the row itself and history stays queryable.
func (r *OutboxRepository) DiscardDeadLetter(ctx context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	if err := req.Validate(); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	var result ports.OutboxMutationResult
	err := r.st.Do(ctx, func(ctx context.Context) error {
		state, cerr := r.loadOperatorTarget(ctx, req.TenantID, req.MessageID)
		if cerr != nil {
			return cerr
		}
		before := state.attempts
		if cerr := classifyOperatorTarget(state, domain.OutboxDiscarded, req.ExpectedGeneration, req.MessageID); cerr != nil {
			return cerr
		}
		res, uerr := r.st.q(ctx).ExecContext(ctx,
			`UPDATE outbox
			 SET status = 'discarded', leased_by = '', lease_until = NULL,
			     generation = generation + 1, discarded_at = $4
			 WHERE id = $1 AND tenant_id = $2 AND status = 'dead' AND generation = $3`,
			req.MessageID, string(req.TenantID), req.ExpectedGeneration, r.st.now())
		if uerr != nil {
			return wrapWrite(uerr)
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return wrapWrite(aerr)
		}
		if affected == 0 {
			state, cerr := r.loadOperatorTarget(ctx, req.TenantID, req.MessageID)
			if cerr != nil {
				return cerr
			}
			return classifyOperatorTarget(state, domain.OutboxDiscarded, req.ExpectedGeneration, req.MessageID)
		}
		result = ports.OutboxMutationResult{
			MessageID:      req.MessageID,
			Action:         "discard",
			AttemptsBefore: before,
			Generation:     req.ExpectedGeneration + 1,
		}
		return nil
	})
	return result, err
}

// SweepRetention deletes at most Limit terminal rows beyond their class
// horizon (ADR-0016), oldest-terminal-first. The whole round is one unit of
// work: it joins the caller's transaction when present, so a rolled-back
// unit restores deleted rows exactly like the memory adapter's snapshot.
// Each class deletion selects its victims with FOR UPDATE SKIP LOCKED so
// concurrent sweeps, dispatchers, and operator mutations can neither collide
// nor observe a half-deleted round. A non-positive horizon exempts its class.
func (r *OutboxRepository) SweepRetention(ctx context.Context, req ports.RetentionSweepRequest) (ports.RetentionSweepResult, error) {
	if err := req.Validate(); err != nil {
		return ports.RetentionSweepResult{}, err
	}
	var result ports.RetentionSweepResult
	err := r.st.Do(ctx, func(ctx context.Context) error {
		cutoff := r.st.now()
		result = ports.RetentionSweepResult{Cutoff: cutoff}

		if req.DryRun {
			var err error
			result.DeletedDelivered, err = countRetainable(ctx, r.st.q(ctx),
				"delivered", "delivered_at", cutoff.Add(-req.DeliveredOlderThan))
			if err != nil {
				return err
			}
			result.DeletedDiscarded, err = countRetainable(ctx, r.st.q(ctx),
				"discarded", "discarded_at", cutoff.Add(-req.DiscardedOlderThan))
			return err
		}

		budget := req.Limit
		deletedDelivered := int64(0)
		if req.DeliveredOlderThan > 0 {
			n, derr := deleteRetainableBatch(ctx, r.st.q(ctx),
				"delivered", "delivered_at", cutoff.Add(-req.DeliveredOlderThan), budget)
			if derr != nil {
				return derr
			}
			deletedDelivered = n
			budget -= int(n)
		}
		deletedDiscarded := int64(0)
		if req.DiscardedOlderThan > 0 && budget > 0 {
			n, derr := deleteRetainableBatch(ctx, r.st.q(ctx),
				"discarded", "discarded_at", cutoff.Add(-req.DiscardedOlderThan), budget)
			if derr != nil {
				return derr
			}
			deletedDiscarded = n
		}
		result.DeletedDelivered = deletedDelivered
		result.DeletedDiscarded = deletedDiscarded
		return nil
	})
	return result, err
}

// The status/column literal pairs passed to countRetainable and
// deleteRetainableBatch live only at the two call sites above; request data
// is always parameterized.

func countRetainable(ctx context.Context, q executor, status, column string, eligibleBefore time.Time) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox WHERE status = $1 AND `+column+` IS NOT NULL AND `+column+` <= $2`,
		status, eligibleBefore).Scan(&n)
	if err != nil {
		return 0, wrapScan(err)
	}
	return n, nil
}

// deleteRetainableBatch removes at most limit rows of one terminal class,
// oldest-terminal-first, returning the truthful deleted count via RETURNING.
func deleteRetainableBatch(ctx context.Context, q executor, status, column string, eligibleBefore time.Time, limit int) (int64, error) {
	rows, err := q.QueryContext(ctx,
		`DELETE FROM outbox
		 WHERE id IN (
		   SELECT id FROM outbox
		   WHERE status = $1 AND `+column+` IS NOT NULL AND `+column+` <= $2
		   ORDER BY `+column+`, id
		   LIMIT $3
		   FOR UPDATE SKIP LOCKED
		 )
		 RETURNING 1`,
		status, eligibleBefore, limit)
	if err != nil {
		return 0, wrapWrite(err)
	}
	defer rows.Close()
	var deleted int64
	for rows.Next() {
		deleted++
	}
	return deleted, rows.Err()
}

// operatorTargetState is the minimal read-back classification needs.
type operatorTargetState struct {
	status     domain.OutboxDeliveryStatus
	attempts   int
	generation int64
}

func (r *OutboxRepository) loadOperatorTarget(ctx context.Context, tenantID domain.TenantID, messageID string) (*operatorTargetState, error) {
	var s operatorTargetState
	var status string
	err := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT status, attempts, generation FROM outbox WHERE id = $1 AND tenant_id = $2`,
		messageID, string(tenantID)).Scan(&status, &s.attempts, &s.generation)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown and foreign-tenant messages are uniform not-found.
		return nil, domain.NotFoundf("outbox_message", messageID)
	}
	if err != nil {
		return nil, wrapScan(err)
	}
	s.status = domain.OutboxDeliveryStatus(status)
	return &s, nil
}

// classifyOperatorTarget turns precondition misses into typed errors:
// a row outside the operator lifecycle entirely (generation 0 — never died,
// so never an operator target) is an invalid transition whatever credential
// arrives; on lifecycle rows a mismatched compare-and-swap credential wins
// the diagnosis because "your view is stale" is the actionable truth.
func classifyOperatorTarget(s *operatorTargetState, target domain.OutboxDeliveryStatus, expectedGeneration int64, messageID string) error {
	if s.generation == 0 {
		return domain.NewInvalidTransitionError(s.status, target)
	}
	if s.generation != expectedGeneration {
		return domain.NewStaleVersionError("outbox message", messageID, expectedGeneration, s.generation)
	}
	if s.status != domain.OutboxDead {
		return domain.NewInvalidTransitionError(s.status, target)
	}
	return nil
}

func scanDeadLetter(row interface{ Scan(dest ...any) error }) (*ports.DeadLetterSummary, error) {
	var (
		s           ports.DeadLetterSummary
		tenantID    string
		status      string
		lastError   sql.NullString
		deadAt      sql.NullTime
		discardedAt sql.NullTime
	)
	err := row.Scan(&s.ID, &s.DedupKey, &tenantID, &status, &s.Attempts, &s.MaxAttempts,
		&s.Generation, &lastError, &s.CreatedAt, &deadAt, &discardedAt)
	if err != nil {
		return nil, wrapScan(err)
	}
	s.TenantID = domain.TenantID(tenantID)
	s.Status = domain.OutboxDeliveryStatus(status)
	if lastError.Valid {
		s.Cause = lastError.String
	}
	if deadAt.Valid {
		t := deadAt.Time
		s.DeadAt = &t
	}
	if discardedAt.Valid {
		t := discardedAt.Time
		s.DiscardedAt = &t
	}
	return &s, nil
}

func truncateCause(cause string) string {
	const max = 512
	if len(cause) > max {
		return cause[:max]
	}
	return cause
}
