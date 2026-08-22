package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

type RunClaimRepository struct{ st *Store }

var _ ports.RunClaimStore = (*RunClaimRepository)(nil)

const runClaimColumns = `tenant_id, run_id, status, owner, token, generation,
	attempts, acquired_at, heartbeat_at, expires_at, created_at, updated_at`

func scanRunClaim(row *sql.Row) (*domain.RunClaim, error) {
	var (
		c                       domain.RunClaim
		acquiredAt, heartbeatAt sql.NullTime
		expiresAt               sql.NullTime
	)
	err := row.Scan(&c.TenantID, &c.RunID, &c.Status, &c.Owner, &c.Token,
		&c.Generation, &c.Attempts, &acquiredAt, &heartbeatAt, &expiresAt,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NotFoundf("run_claim", "row")
	}
	if err != nil {
		return nil, wrapScan(err)
	}
	if acquiredAt.Valid {
		ts := acquiredAt.Time.UTC()
		c.AcquiredAt = &ts
	}
	if heartbeatAt.Valid {
		ts := heartbeatAt.Time.UTC()
		c.HeartbeatAt = &ts
	}
	if expiresAt.Valid {
		ts := expiresAt.Time.UTC()
		c.ExpiresAt = &ts
	}
	return &c, nil
}

// Create inserts the initial runnable claim. It participates in the caller's
// transaction, so a claim comes into existence atomically with its run.
func (r *RunClaimRepository) Create(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	// Pre-check gives callers the same typed invalid the memory store
	// produces; the FK constraint still guards the concurrent race.
	var exists bool
	if err := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1 AND tenant_id = $2)`,
		string(runID), string(tenantID)).Scan(&exists); err != nil {
		return wrapScan(err)
	}
	if !exists {
		return domain.Invalidf("run_claim_run_unknown", "claim references unknown run %s", runID)
	}
	now := r.st.now()
	_, err := r.st.q(ctx).ExecContext(ctx,
		`INSERT INTO run_claims (tenant_id, run_id, status, created_at, updated_at)
		 VALUES ($1,$2,'runnable',$3,$3)`,
		string(tenantID), string(runID), now)
	if err != nil {
		if mapped := mapUniqueViolation(err, "run_claim_exists",
			"run already has a claim"); mapped != err {
			return mapped
		}
		return wrapWrite(err)
	}
	return nil
}

func (r *RunClaimRepository) Get(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) (*domain.RunClaim, error) {
	c, err := r.rawGet(ctx, tenantID, runID)
	if err != nil {
		return nil, err
	}
	// The token is a bearer secret: read paths redact it; it travels only
	// inside acquisition responses.
	c.Token = ""
	return c, nil
}

func (r *RunClaimRepository) rawGet(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) (*domain.RunClaim, error) {
	return scanRunClaim(r.st.q(ctx).QueryRowContext(ctx,
		`SELECT `+runClaimColumns+` FROM run_claims c WHERE c.run_id = $1 AND c.tenant_id = $2`,
		string(runID), string(tenantID)))
}

// Acquire claims one specific runnable-or-expired run. The conditional UPDATE
// is atomic: concurrent acquirers serialize on the row, exactly one wins, and
// losers observe the committed state as a typed held-conflict.
func (r *RunClaimRepository) Acquire(ctx context.Context, req ports.RunClaimLeaseRequest) (*domain.RunClaim, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	now := r.st.now()
	token, err := domain.NewRunClaimToken()
	if err != nil {
		return nil, err
	}
	row := r.st.q(ctx).QueryRowContext(ctx,
		`UPDATE run_claims c
		 SET status = 'claimed', owner = $3, token = $4, generation = c.generation + 1,
		     attempts = c.attempts + 1, acquired_at = $5, heartbeat_at = $5,
		     expires_at = $6, updated_at = $5
		 WHERE c.tenant_id = $1 AND c.run_id = $2
		   AND (c.status = 'runnable' OR (c.status = 'claimed' AND c.expires_at <= $5))
		 RETURNING `+runClaimColumns,
		string(req.TenantID), string(req.RunID), req.Owner, token, now,
		domain.ClaimExpiry(now, req.LeaseFor))
	c, scanErr := scanRunClaim(row)
	if domain.ErrKindOf(scanErr) == domain.ErrKindNotFound {
		return r.classifyAcquireMiss(ctx, req.TenantID, req.RunID)
	}
	return c, scanErr
}

// classifyAcquireMiss turns a zero-row acquire into its precise cause:
// absent or foreign rows are uniform not-found, a live lease is a typed
// conflict. Never a silent success.
func (r *RunClaimRepository) classifyAcquireMiss(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) (*domain.RunClaim, error) {
	_, err := r.rawGet(ctx, tenantID, runID)
	switch {
	case err == nil:
		return nil, domain.Conflictf("run_claim_held", "run %s is claimed by an active lease", runID)
	case domain.ErrKindOf(err) == domain.ErrKindNotFound:
		return nil, domain.NotFoundf("run_claim", runID)
	default:
		return nil, err
	}
}

// AcquireNext claims up to Limit runnable-or-expired rows in deterministic
// creation order. Each iteration claims exactly one row through an atomic
// UPDATE over SELECT … FOR UPDATE SKIP LOCKED: rows locked by competing
// claimers are skipped rather than awaited, so concurrent dispatchers never
// overlap. Tokens are minted per row in Go, which is why the batch loops one
// atomic single-row statement at a time.
func (r *RunClaimRepository) AcquireNext(ctx context.Context, req ports.RunClaimBatchRequest) ([]*domain.RunClaim, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	out := make([]*domain.RunClaim, 0, req.Limit)
	for len(out) < req.Limit {
		now := r.st.now()
		token, err := domain.NewRunClaimToken()
		if err != nil {
			return nil, err
		}
		row := r.st.q(ctx).QueryRowContext(ctx,
			`UPDATE run_claims c
			 SET status = 'claimed', owner = $1, token = $2, generation = c.generation + 1,
			     attempts = c.attempts + 1, acquired_at = $3, heartbeat_at = $3,
			     expires_at = $4, updated_at = $3
			 WHERE (c.tenant_id, c.run_id) IN (
			   SELECT tenant_id, run_id FROM run_claims
			   WHERE status = 'runnable' OR (status = 'claimed' AND expires_at <= $3)
			   ORDER BY created_at, run_id
			   LIMIT 1
			   FOR UPDATE SKIP LOCKED
			 )
			 RETURNING `+runClaimColumns,
			req.Owner, token, now, domain.ClaimExpiry(now, req.LeaseFor))
		c, scanErr := scanRunClaim(row)
		if domain.ErrKindOf(scanErr) == domain.ErrKindNotFound {
			break // nothing left to claim
		}
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *RunClaimRepository) Heartbeat(ctx context.Context, ref ports.RunClaimRef, extendFor time.Duration) (*domain.RunClaim, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if extendFor <= 0 {
		return nil, domain.Invalidf("run_claim_lease_duration", "heartbeat extension must be positive, got %s", extendFor)
	}
	now := r.st.now()
	row := r.st.q(ctx).QueryRowContext(ctx,
		`UPDATE run_claims c
		 SET heartbeat_at = $6, expires_at = $7, updated_at = $6
		 WHERE c.tenant_id = $1 AND c.run_id = $2 AND c.status = 'claimed'
		   AND c.owner = $3 AND c.token = $4 AND c.generation = $5
		   AND c.expires_at > $6
		 RETURNING `+runClaimColumns,
		string(ref.TenantID), string(ref.RunID), ref.Owner, ref.Token, ref.Generation,
		now, domain.ClaimExpiry(now, extendFor))
	c, err := scanRunClaim(row)
	if domain.ErrKindOf(err) == domain.ErrKindNotFound {
		return nil, r.classifyFenceMiss(ctx, ref, "run_claim_lease_expired",
			"lease expired before the heartbeat")
	}
	return c, err
}

func (r *RunClaimRepository) Release(ctx context.Context, ref ports.RunClaimRef) (*domain.RunClaim, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	now := r.st.now()
	row := r.st.q(ctx).QueryRowContext(ctx,
		`UPDATE run_claims c
		 SET status = 'runnable', owner = '', token = '', expires_at = NULL, updated_at = $6
		 WHERE c.tenant_id = $1 AND c.run_id = $2 AND c.status = 'claimed'
		   AND c.owner = $3 AND c.token = $4 AND c.generation = $5
		 RETURNING `+runClaimColumns,
		string(ref.TenantID), string(ref.RunID), ref.Owner, ref.Token, ref.Generation, now)
	c, err := scanRunClaim(row)
	if domain.ErrKindOf(err) == domain.ErrKindNotFound {
		return nil, r.classifyFenceMiss(ctx, ref, "run_claim_stale_fencing",
			"credentials do not match the current holder")
	}
	return c, err
}

func (r *RunClaimRepository) Complete(ctx context.Context, ref ports.RunClaimRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	res, err := r.st.q(ctx).ExecContext(ctx,
		`DELETE FROM run_claims c
		 WHERE c.tenant_id = $1 AND c.run_id = $2 AND c.status = 'claimed'
		   AND c.owner = $3 AND c.token = $4 AND c.generation = $5`,
		string(ref.TenantID), string(ref.RunID), ref.Owner, ref.Token, ref.Generation)
	if err != nil {
		return wrapWrite(err)
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		return wrapWrite(aerr)
	} else if affected == 0 {
		return r.classifyFenceMiss(ctx, ref, "run_claim_stale_fencing",
			"credentials do not match the current holder")
	}
	return nil
}

// CleanupTerminal deletes any leftover claim once the run itself reached a
// terminal status. Terminality is absorbing (no transition leaves it), so the
// check-then-delete order is race-free; repeating the call stays a no-op.
func (r *RunClaimRepository) CleanupTerminal(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	var status string
	err := r.st.q(ctx).QueryRowContext(ctx,
		`SELECT status FROM runs WHERE id = $1 AND tenant_id = $2`,
		string(runID), string(tenantID)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFoundf("run", runID)
	}
	if err != nil {
		return wrapScan(err)
	}
	if !domain.RunStatus(status).Terminal() {
		return domain.Invalidf("run_claim_not_terminal", "run %s is %s, cleanup requires a terminal status", runID, status)
	}
	if _, err := r.st.q(ctx).ExecContext(ctx,
		`DELETE FROM run_claims WHERE tenant_id = $1 AND run_id = $2`,
		string(tenantID), string(runID)); err != nil {
		return wrapWrite(err)
	}
	return nil
}

// classifyFenceMiss explains a zero-row fenced mutation against the stored
// row: absent or foreign claims are uniform not-found, exact credential
// matches mean the operation-specific condition failed (an expired lease),
// anything else is stale fencing.
func (r *RunClaimRepository) classifyFenceMiss(ctx context.Context, ref ports.RunClaimRef, missCode, missMessage string) error {
	current, err := r.rawGet(ctx, ref.TenantID, ref.RunID)
	switch {
	case err == nil:
		if current.Matches(ref.Owner, ref.Token, ref.Generation) {
			return domain.Conflictf(missCode, "%s (run %s)", missMessage, ref.RunID)
		}
		return domain.Conflictf("run_claim_stale_fencing",
			"credentials do not match the current holder of run %s", ref.RunID)
	case domain.ErrKindOf(err) == domain.ErrKindNotFound:
		return domain.NotFoundf("run_claim", ref.RunID)
	default:
		return err
	}
}
