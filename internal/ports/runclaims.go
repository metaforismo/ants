package ports

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// Run claim contract (ants.store.runclaims.v1, ADR-0012).
//
// A run claim is the durable, tenant-scoped execution lease for one run. The
// store's injected Clock is the single time authority: every scheduling
// instant (acquisition time, heartbeat time, expiry) is computed by the
// adapter, and public signatures carry durations only — never caller
// timestamps.
//
// Fencing: mutating operations require the full credential tuple of the
// holder's acquisition epoch — tenant, run, owner, token, generation. A
// mismatched or stale tuple is rejected with a typed conflict; an unknown or
// foreign-tenant claim is a uniform not-found. Neither is ever a silent
// success.

// RunClaimRef carries the fencing credentials of one acquisition epoch.
type RunClaimRef struct {
	TenantID   domain.TenantID
	RunID      domain.RunID
	Owner      string
	Token      string
	Generation int64
}

// Validate rejects structurally unusable credentials before they reach a
// store; whether they match the stored epoch is decided per operation.
func (r RunClaimRef) Validate() error {
	if err := domain.ValidateClaimOwner(r.Owner); err != nil {
		return err
	}
	if len(r.Token) != domain.ClaimTokenLength {
		return domain.Invalidf("run_claim_token", "claim token must be %d characters", domain.ClaimTokenLength)
	}
	if r.Generation < 1 {
		return domain.Invalidf("run_claim_generation", "claim generation must be at least 1, got %d", r.Generation)
	}
	return nil
}

// RunClaimLeaseRequest bounds one single-run acquisition. LeaseFor is a
// duration from the store clock's present instant; the adapter computes the
// absolute deadline itself.
type RunClaimLeaseRequest struct {
	TenantID domain.TenantID
	RunID    domain.RunID
	Owner    string
	LeaseFor time.Duration
}

// Validate rejects structurally unusable acquisition requests.
func (r RunClaimLeaseRequest) Validate() error {
	if err := domain.ValidateClaimOwner(r.Owner); err != nil {
		return err
	}
	return positiveDuration(r.LeaseFor)
}

// RunClaimBatchRequest bounds one scheduler round over all claimable runs:
// up to Limit claims, each leased LeaseFor from the store clock.
type RunClaimBatchRequest struct {
	Owner    string
	Limit    int
	LeaseFor time.Duration
}

// Validate rejects structurally unusable batch requests.
func (r RunClaimBatchRequest) Validate() error {
	if err := domain.ValidateClaimOwner(r.Owner); err != nil {
		return err
	}
	if r.Limit <= 0 {
		return domain.Invalidf("run_claim_limit", "batch limit must be positive, got %d", r.Limit)
	}
	return positiveDuration(r.LeaseFor)
}

func positiveDuration(d time.Duration) error {
	if d <= 0 {
		return domain.Invalidf("run_claim_lease_duration", "lease must be positive, got %s", d)
	}
	return nil
}

// RunClaimStore persists run execution leases.
type RunClaimStore interface {
	// Create inserts the initial runnable claim for a run. It participates
	// in the caller's unit of work so a claim comes into existence atomically
	// with its run (StartRun); a rolled-back unit leaves no claim behind.
	Create(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error

	// Get returns the claim; the token is redacted because it is a bearer
	// secret that flows only through Acquire/AcquireNext responses. Unknown
	// and foreign-tenant claims are uniform not-found.
	Get(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) (*domain.RunClaim, error)

	// Acquire claims one specific runnable run, or reclaims one whose lease
	// expired: generation and attempts increment and a fresh token mints on
	// every success. A live (unexpired) claim held by anyone — including the
	// caller — conflicts. Unknown runs are invalid.
	Acquire(ctx context.Context, req RunClaimLeaseRequest) (*domain.RunClaim, error)

	// AcquireNext claims up to Limit runnable-or-expired runs in deterministic
	// creation order. Concurrent claimers never overlap: rows locked by other
	// transactions are skipped rather than awaited. Each claimed row is atomic;
	// a batch interrupted mid-way leaves earlier claims intact.
	AcquireNext(ctx context.Context, req RunClaimBatchRequest) ([]*domain.RunClaim, error)

	// Heartbeat extends the live lease extendFor from the store clock's
	// present instant. Requires the exact current credentials AND an
	// unexpired lease: a holder that missed its deadline has forfeited the
	// epoch and must not silently revive it.
	Heartbeat(ctx context.Context, ref RunClaimRef, extendFor time.Duration) (*domain.RunClaim, error)

	// Release voluntarily returns a held claim to runnable so another worker
	// can take over. Requires the exact current credentials; generation and
	// attempts are preserved as history.
	Release(ctx context.Context, ref RunClaimRef) (*domain.RunClaim, error)

	// Complete removes the claim after the holder finished the run. Requires
	// the exact current credentials; repeating a completed completion is
	// not-found, not success.
	Complete(ctx context.Context, ref RunClaimRef) error

	// CleanupTerminal deletes any remaining claim once the run itself reached
	// a terminal status. It is idempotent: repeated calls succeed. Cleaning
	// up a non-terminal run is refused; unknown runs are not-found.
	CleanupTerminal(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) error
}
