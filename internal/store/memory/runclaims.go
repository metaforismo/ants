package memory

import (
	"context"
	"sort"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

type RunClaimRepository struct{ st *storeState }

var _ ports.RunClaimStore = (*RunClaimRepository)(nil)

// Create inserts the initial runnable claim. It runs under the shared write
// lock, so inside a unit of work it commits and rolls back together with
// everything else in that unit.
func (r *RunClaimRepository) Create(_ context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	unlock := lockWrite(r.st)
	defer unlock()
	run, ok := r.st.runs[runID]
	if !ok || run.TenantID != tenantID {
		return domain.Invalidf("run_claim_run_unknown", "claim references unknown run %s", runID)
	}
	if _, exists := r.st.runClaims[runID]; exists {
		return domain.Conflictf("run_claim_exists", "run %s already has a claim", runID)
	}
	now := r.st.clock.Now().UTC()
	r.st.runClaims[runID] = &domain.RunClaim{
		TenantID:  tenantID,
		RunID:     runID,
		Status:    domain.ClaimRunnable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return nil
}

func (r *RunClaimRepository) Get(_ context.Context, tenantID domain.TenantID, runID domain.RunID) (*domain.RunClaim, error) {
	unlock := lockRead(r.st)
	defer unlock()
	c, ok := r.st.runClaims[runID]
	if !ok || c.TenantID != tenantID {
		return nil, notFound("run_claim", runID)
	}
	// The token is a bearer secret: read paths redact it; it travels only
	// inside acquisition responses.
	out := cloneRunClaim(c)
	out.Token = ""
	return out, nil
}

// Acquire claims one specific run if it is runnable or its lease expired.
// The whole decision is one locked step, so concurrent acquirers resolve to
// exactly one winner.
func (r *RunClaimRepository) Acquire(_ context.Context, req ports.RunClaimLeaseRequest) (*domain.RunClaim, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	c, ok := r.st.runClaims[req.RunID]
	if !ok || c.TenantID != req.TenantID {
		return nil, notFound("run_claim", req.RunID)
	}
	now := r.st.clock.Now().UTC()
	if !claimEligible(c, now) {
		return nil, domain.Conflictf("run_claim_held", "run %s is claimed by an active lease", req.RunID)
	}
	token, err := domain.NewRunClaimToken()
	if err != nil {
		return nil, err
	}
	acquireClaim(c, req.Owner, token, now, req.LeaseFor)
	return cloneRunClaim(c), nil
}

// AcquireNext claims up to Limit runnable-or-expired rows in deterministic
// creation order. The scan-and-mutate happens under the write lock, which is
// the memory adapter's equivalent of FOR UPDATE SKIP LOCKED: concurrent
// callers serialize and can never claim the same row twice.
func (r *RunClaimRepository) AcquireNext(_ context.Context, req ports.RunClaimBatchRequest) ([]*domain.RunClaim, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	now := r.st.clock.Now().UTC()

	eligible := make([]*domain.RunClaim, 0, len(r.st.runClaims))
	for _, c := range r.st.runClaims {
		if claimEligible(c, now) {
			eligible = append(eligible, c)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if !eligible[i].CreatedAt.Equal(eligible[j].CreatedAt) {
			return eligible[i].CreatedAt.Before(eligible[j].CreatedAt)
		}
		return eligible[i].RunID < eligible[j].RunID
	})
	if len(eligible) > req.Limit {
		eligible = eligible[:req.Limit]
	}
	out := make([]*domain.RunClaim, 0, len(eligible))
	for _, c := range eligible {
		token, err := domain.NewRunClaimToken()
		if err != nil {
			return nil, err
		}
		acquireClaim(c, req.Owner, token, now, req.LeaseFor)
		out = append(out, cloneRunClaim(c))
	}
	return out, nil
}

func (r *RunClaimRepository) Heartbeat(_ context.Context, ref ports.RunClaimRef, extendFor time.Duration) (*domain.RunClaim, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if extendFor <= 0 {
		return nil, domain.Invalidf("run_claim_lease_duration", "heartbeat extension must be positive, got %s", extendFor)
	}
	unlock := lockWrite(r.st)
	defer unlock()
	c, stale := r.fenced(ref)
	if stale != nil {
		return nil, stale
	}
	now := r.st.clock.Now().UTC()
	if domain.ClaimExpired(c.ExpiresAt, now) {
		return nil, domain.Conflictf("run_claim_lease_expired", "lease on run %s expired before the heartbeat", ref.RunID)
	}
	extension := domain.ClaimExpiry(now, extendFor)
	c.HeartbeatAt = &now
	c.ExpiresAt = &extension
	touch(c, now)
	return cloneRunClaim(c), nil
}

func (r *RunClaimRepository) Release(_ context.Context, ref ports.RunClaimRef) (*domain.RunClaim, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	c, stale := r.fenced(ref)
	if stale != nil {
		return nil, stale
	}
	releaseClaim(c, r.st.clock.Now().UTC())
	return cloneRunClaim(c), nil
}

func (r *RunClaimRepository) Complete(_ context.Context, ref ports.RunClaimRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	if _, stale := r.fenced(ref); stale != nil {
		return stale
	}
	delete(r.st.runClaims, ref.RunID)
	return nil
}

// CleanupTerminal removes any leftover claim once the run reached a terminal
// status; terminality is absorbing, so a check-then-delete under the same
// lock cannot resurrect or race.
func (r *RunClaimRepository) CleanupTerminal(_ context.Context, tenantID domain.TenantID, runID domain.RunID) error {
	unlock := lockWrite(r.st)
	defer unlock()
	run, ok := r.st.runs[runID]
	if !ok || run.TenantID != tenantID {
		return notFound("run", runID)
	}
	if !run.Status.Terminal() {
		return domain.Invalidf("run_claim_not_terminal", "run %s is %s, cleanup requires a terminal status", runID, run.Status)
	}
	delete(r.st.runClaims, runID)
	return nil
}

// fenced resolves the referenced claim or returns the typed rejection:
// not-found for unknown/foreign rows, conflict for any credential mismatch.
func (r *RunClaimRepository) fenced(ref ports.RunClaimRef) (*domain.RunClaim, error) {
	c, ok := r.st.runClaims[ref.RunID]
	if !ok || c.TenantID != ref.TenantID {
		return nil, notFound("run_claim", ref.RunID)
	}
	if !c.Matches(ref.Owner, ref.Token, ref.Generation) {
		return nil, domain.Conflictf("run_claim_stale_fencing",
			"credentials do not match the current holder of run %s", ref.RunID)
	}
	return c, nil
}

func claimEligible(c *domain.RunClaim, now time.Time) bool {
	switch c.Status {
	case domain.ClaimRunnable:
		return true
	case domain.ClaimClaimed:
		return domain.ClaimExpired(c.ExpiresAt, now)
	default:
		return false
	}
}

// acquireClaim moves a runnable-or-expired claim into its next ownership
// epoch: generation and attempts increment monotonically and the freshly
// minted bearer token becomes the only valid credential.
func acquireClaim(c *domain.RunClaim, owner, token string, now time.Time, leaseFor time.Duration) {
	c.Status = domain.ClaimClaimed
	c.Owner = owner
	c.Token = token
	c.Generation++
	c.Attempts++
	expiry := domain.ClaimExpiry(now, leaseFor)
	c.AcquiredAt = &now
	c.HeartbeatAt = &now
	c.ExpiresAt = &expiry
	touch(c, now)
}

func releaseClaim(c *domain.RunClaim, now time.Time) {
	c.Status = domain.ClaimRunnable
	c.Owner = ""
	c.Token = ""
	c.ExpiresAt = nil
	touch(c, now)
}

func touch(c *domain.RunClaim, now time.Time) { c.UpdatedAt = now }
