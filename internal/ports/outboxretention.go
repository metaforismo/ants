package ports

import (
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// Outbox retention contract (ants.store.outboxretention.v1, ADR-0016).
//
// A sweep is one bounded garbage-collection round over terminal outbox rows:
// only `delivered` and `discarded` rows whose terminal age has reached their
// class horizon are eligible. Pending, leased, and dead rows are never
// eligible; rows with a NULL terminal timestamp are never eligible; events
// and audit records are unreachable through this port.
//
// Scheduling stays store-owned per ADR-0011: requests carry durations, the
// adapter computes ONE cutoff instant per round from its own clock, and the
// result reports that instant so operators can interpret the counts.

const (
	// MaxRetentionSweepBatch bounds one round's total deletions.
	MaxRetentionSweepBatch = 1000
)

// RetentionSweepRequest describes one bounded round. A non-positive horizon
// exempts its class entirely — the same rule that makes default
// configurations inert, enforced here so a caller bug can never widen GC
// into "everything now". DryRun runs the identical selection logic without
// deleting, so previews cannot drift from sweeps.
type RetentionSweepRequest struct {
	DeliveredOlderThan time.Duration
	DiscardedOlderThan time.Duration
	Limit              int
	DryRun             bool
}

// Validate rejects structurally unusable rounds before they reach a store.
func (r RetentionSweepRequest) Validate() error {
	if r.DeliveredOlderThan < 0 || r.DiscardedOlderThan < 0 {
		return domain.Invalidf("outbox_retention_horizon", "retention horizons must not be negative")
	}
	if r.Limit < 1 || r.Limit > MaxRetentionSweepBatch {
		return domain.Invalidf("outbox_retention_batch", "retention batch limit must be within [1,%d], got %d",
			MaxRetentionSweepBatch, r.Limit)
	}
	return nil
}

// RetentionSweepResult reports one round truthfully: Cutoff is the
// store-clock instant eligibility was measured against, the counts are rows
// actually deleted (or that would be deleted under DryRun), and the total is
// always within the request's Limit.
type RetentionSweepResult struct {
	Cutoff           time.Time `json:"cutoff"`
	DeletedDelivered int64     `json:"deleted_delivered"`
	DeletedDiscarded int64     `json:"deleted_discarded"`
}
