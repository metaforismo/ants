package memory

import (
	"context"
	"sort"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// outboxMessage is the mutable stored form; fields are guarded by the
// shared store mutex.
type outboxMessage struct {
	ID          string
	DedupKey    string
	TenantID    domain.TenantID
	Envelope    []byte
	Status      domain.OutboxDeliveryStatus
	Attempts    int
	MaxAttempts int
	AvailableAt time.Time
	LeasedBy    string
	LeaseUntil  *time.Time
	LastError   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
	// Generation fences operator mutations (ADR-0015): it increments on
	// every transition into dead, requeued-pending, or discarded.
	Generation  int64
	DeadAt      *time.Time
	DiscardedAt *time.Time
}

type OutboxRepository struct{ st *storeState }

var _ ports.OutboxStore = (*OutboxRepository)(nil)

func (r *OutboxRepository) Publish(_ context.Context, msg ports.OutboxMessage) error {
	if msg.MaxAttempts < 1 || msg.MaxAttempts > 100 {
		return domain.Invalidf("outbox_max_attempts", "outbox max attempts must be within [1,100], got %d", msg.MaxAttempts)
	}
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.outboxByDedup[msg.DedupKey]; exists {
		// Idempotent no-op: replays of the same logical transition must
		// never double-queue.
		return nil
	}
	now := r.st.clock.Now().UTC()
	stored := &outboxMessage{
		ID:          msg.ID,
		DedupKey:    msg.DedupKey,
		TenantID:    msg.TenantID,
		Envelope:    append([]byte(nil), msg.Envelope...),
		Status:      domain.OutboxPending,
		Attempts:    0,
		MaxAttempts: msg.MaxAttempts,
		AvailableAt: now,
		CreatedAt:   now,
	}
	r.st.outbox = append(r.st.outbox, stored)
	r.st.outboxByID[msg.ID] = stored
	r.st.outboxByDedup[msg.DedupKey] = stored
	return nil
}

// Lease claims due messages using this store's clock as the single time
// authority: due means pending with availability passed or leased with an
// expired lease. Attempts count at claim time.
func (r *OutboxRepository) Lease(_ context.Context, req ports.OutboxLeaseRequest) ([]ports.OutboxMessage, error) {
	unlock := lockWrite(r.st)
	defer unlock()
	now := r.st.clock.Now().UTC()
	var due []*outboxMessage
	for _, m := range r.st.outbox {
		switch {
		case m.Status == domain.OutboxPending && !m.AvailableAt.After(now):
			due = append(due, m)
		case m.Status == domain.OutboxLeased && m.LeaseUntil != nil && !m.LeaseUntil.After(now):
			due = append(due, m)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].CreatedAt.Before(due[j].CreatedAt) })
	if len(due) > req.Limit {
		due = due[:req.Limit]
	}
	out := make([]ports.OutboxMessage, 0, len(due))
	leaseUntil := now.Add(req.LeaseFor)
	for _, m := range due {
		m.Status = domain.OutboxLeased
		m.LeasedBy = req.WorkerID
		m.LeaseUntil = &leaseUntil
		m.Attempts++
		out = append(out, ports.OutboxMessage{
			ID: m.ID, DedupKey: m.DedupKey, TenantID: m.TenantID,
			Envelope: append([]byte(nil), m.Envelope...),
			Attempts: m.Attempts, MaxAttempts: m.MaxAttempts,
		})
	}
	return out, nil
}

func (r *OutboxRepository) MarkDelivered(_ context.Context, id, leasedBy string) error {
	unlock := lockWrite(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[id]
	if !ok || m.Status != domain.OutboxLeased || m.LeasedBy != leasedBy {
		// Uniform not-found keeps lease ownership non-enumerable.
		return domain.NotFoundf("outbox_message", id)
	}
	now := r.st.clock.Now().UTC()
	m.Status = domain.OutboxDelivered
	m.DeliveredAt = &now
	m.LeaseUntil = nil
	return nil
}

func (r *OutboxRepository) FailWithBackoff(_ context.Context, id, leasedBy string, retryIn time.Duration, cause string) error {
	unlock := lockWrite(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[id]
	if !ok || m.Status != domain.OutboxLeased || m.LeasedBy != leasedBy {
		return domain.NotFoundf("outbox_message", id)
	}
	now := r.st.clock.Now().UTC()
	if m.Attempts >= m.MaxAttempts {
		m.Status = domain.OutboxDead
		// Entering dead opens a new operator-visible fencing epoch.
		m.Generation++
		m.DeadAt = &now
	} else {
		m.Status = domain.OutboxPending
		m.AvailableAt = now.Add(retryIn)
	}
	m.LeaseUntil = nil
	m.LeasedBy = ""
	const maxLen = 512
	if len(cause) > maxLen {
		cause = cause[:maxLen]
	}
	m.LastError = cause
	return nil
}

func (r *OutboxRepository) Stats(_ context.Context) (ports.OutboxStats, error) {
	unlock := lockRead(r.st)
	defer unlock()
	var stats ports.OutboxStats
	for _, m := range r.st.outbox {
		switch m.Status {
		case domain.OutboxPending:
			stats.Pending++
		case domain.OutboxLeased:
			stats.Leased++
		case domain.OutboxDelivered:
			stats.Delivered++
		case domain.OutboxDead:
			stats.Dead++
		case domain.OutboxDiscarded:
			stats.Discarded++
		}
	}
	return stats, nil
}

// ListDeadLetters pages actionable (dead) messages in deterministic
// (created_at, id) order behind the request's keyset cursor. Discarded rows
// are history, not work: they appear through Get, never here.
func (r *OutboxRepository) ListDeadLetters(_ context.Context, req ports.ListDeadLettersRequest) ([]ports.DeadLetterSummary, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	unlock := lockRead(r.st)
	defer unlock()
	dead := make([]*outboxMessage, 0, len(r.st.outbox))
	for _, m := range r.st.outbox {
		if m.Status == domain.OutboxDead && m.TenantID == req.TenantID {
			dead = append(dead, m)
		}
	}
	sort.Slice(dead, func(i, j int) bool {
		if !dead[i].CreatedAt.Equal(dead[j].CreatedAt) {
			return dead[i].CreatedAt.Before(dead[j].CreatedAt)
		}
		return dead[i].ID < dead[j].ID
	})
	out := []ports.DeadLetterSummary{}
	for _, m := range dead {
		if afterKeyset(m.CreatedAt, m.ID, req.AfterCreatedAt, req.AfterID) {
			continue
		}
		out = append(out, deadLetterSummary(m))
		if len(out) >= req.Limit {
			break
		}
	}
	return out, nil
}

// GetDeadLetter returns one dead or discarded message so operators can
// confirm terminal decisions landed; anything else is uniformly not-found.
func (r *OutboxRepository) GetDeadLetter(_ context.Context, tenantID domain.TenantID, messageID string) (*ports.DeadLetterSummary, error) {
	unlock := lockRead(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[messageID]
	if !ok || m.TenantID != tenantID ||
		(m.Status != domain.OutboxDead && m.Status != domain.OutboxDiscarded) {
		return nil, domain.NotFoundf("outbox_message", messageID)
	}
	s := deadLetterSummary(m)
	return &s, nil
}

func (r *OutboxRepository) RequeueDeadLetter(_ context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	if err := req.Validate(); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[req.MessageID]
	if !ok || m.TenantID != req.TenantID {
		return ports.OutboxMutationResult{}, domain.NotFoundf("outbox_message", req.MessageID)
	}
	before := m.Attempts
	if err := classifyOperatorMutation(m, domain.OutboxPending, req); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	now := r.st.clock.Now().UTC()
	applyRequeue(m, now)
	return ports.OutboxMutationResult{
		MessageID:      m.ID,
		Action:         "requeue",
		AttemptsBefore: before,
		Generation:     m.Generation,
	}, nil
}

func (r *OutboxRepository) DiscardDeadLetter(_ context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	if err := req.Validate(); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[req.MessageID]
	if !ok || m.TenantID != req.TenantID {
		return ports.OutboxMutationResult{}, domain.NotFoundf("outbox_message", req.MessageID)
	}
	before := m.Attempts
	if err := classifyOperatorMutation(m, domain.OutboxDiscarded, req); err != nil {
		return ports.OutboxMutationResult{}, err
	}
	now := r.st.clock.Now().UTC()
	applyDiscard(m, now)
	return ports.OutboxMutationResult{
		MessageID:      m.ID,
		Action:         "discard",
		AttemptsBefore: before,
		Generation:     m.Generation,
	}, nil
}

// classifyOperatorMutation turns precondition misses into typed errors:
// a row outside the operator lifecycle entirely (generation 0 — never died,
// so never an operator target) is an invalid transition whatever credential
// arrives; on lifecycle rows a mismatched compare-and-swap credential wins
// the diagnosis because "your view is stale" is the actionable truth.
func classifyOperatorMutation(m *outboxMessage, target domain.OutboxDeliveryStatus, req ports.OutboxMutationRequest) error {
	if m.Generation == 0 {
		return domain.NewInvalidTransitionError(m.Status, target)
	}
	if m.Generation != req.ExpectedGeneration {
		return domain.NewStaleVersionError("outbox message", m.ID, req.ExpectedGeneration, m.Generation)
	}
	if m.Status != domain.OutboxDead {
		return domain.NewInvalidTransitionError(m.Status, target)
	}
	return nil
}

func applyRequeue(m *outboxMessage, now time.Time) {
	// Reset exactly what a fresh bounded delivery lifecycle needs; identity,
	// dedup key, envelope, max_attempts, and last_error stay untouched.
	m.Status = domain.OutboxPending
	m.Attempts = 0
	m.AvailableAt = now
	m.LeaseUntil = nil
	m.LeasedBy = ""
	m.Generation++
	m.DeadAt = nil
}

func applyDiscard(m *outboxMessage, now time.Time) {
	m.Status = domain.OutboxDiscarded
	m.LeaseUntil = nil
	m.LeasedBy = ""
	m.Generation++
	m.DiscardedAt = &now
}

// afterKeyset reports whether a row sorts at-or-before the cursor position
// and must therefore be skipped on a subsequent page.
func afterKeyset(createdAt time.Time, id string, afterAt time.Time, afterID string) bool {
	if afterID == "" && afterAt.IsZero() {
		return false
	}
	if createdAt.Equal(afterAt) {
		return id <= afterID
	}
	return createdAt.Before(afterAt)
}

// SweepRetention deletes at most Limit terminal rows beyond their class
// horizon (ADR-0016) — delivered victims claim the budget first, then
// discarded victims, oldest-terminal-first within each class — under the
// store's single write lock, the same atomicity PostgreSQL gets from its
// unit of work. Eligibility is measured against this store's clock; NULL
// terminal timestamps are never eligible. DryRun applies the identical
// selection without mutating.
func (r *OutboxRepository) SweepRetention(_ context.Context, req ports.RetentionSweepRequest) (ports.RetentionSweepResult, error) {
	if err := req.Validate(); err != nil {
		return ports.RetentionSweepResult{}, err
	}
	unlock := lockWrite(r.st)
	defer unlock()
	cutoff := r.st.clock.Now().UTC()
	result := ports.RetentionSweepResult{Cutoff: cutoff}

	// Budget allocation mirrors the PostgreSQL adapter: delivered victims
	// first in (delivered_at, id) order, discarded with what remains.
	budget := req.Limit
	var victims []*outboxMessage
	if req.DeliveredOlderThan > 0 {
		eligible := selectRetentionVictims(r.st.outbox, domain.OutboxDelivered,
			func(m *outboxMessage) *time.Time { return m.DeliveredAt }, cutoff, req.DeliveredOlderThan)
		take := min(budget, len(eligible))
		victims = append(victims, eligible[:take]...)
		budget -= take
		result.DeletedDelivered = int64(take)
	}
	if req.DiscardedOlderThan > 0 && budget > 0 {
		eligible := selectRetentionVictims(r.st.outbox, domain.OutboxDiscarded,
			func(m *outboxMessage) *time.Time { return m.DiscardedAt }, cutoff, req.DiscardedOlderThan)
		if len(eligible) > budget {
			eligible = eligible[:budget]
		}
		victims = append(victims, eligible...)
	}
	result.DeletedDiscarded = int64(len(victims)) - result.DeletedDelivered
	if req.DryRun {
		return result, nil
	}

	remove := make(map[*outboxMessage]struct{}, len(victims))
	for _, m := range victims {
		remove[m] = struct{}{}
		delete(r.st.outboxByID, m.ID)
		delete(r.st.outboxByDedup, m.DedupKey)
	}
	kept := r.st.outbox[:0:0]
	for _, m := range r.st.outbox {
		if _, dead := remove[m]; !dead {
			kept = append(kept, m)
		}
	}
	r.st.outbox = kept
	return result, nil
}

// selectRetentionVictims returns the rows of one terminal status whose
// terminal timestamp is non-NULL and at or before cutoff-horizon, ordered
// oldest-terminal-first with the id tiebreak.
func selectRetentionVictims(rows []*outboxMessage, status domain.OutboxDeliveryStatus, terminalAt func(*outboxMessage) *time.Time, cutoff time.Time, horizon time.Duration) []*outboxMessage {
	eligibleAt := cutoff.Add(-horizon)
	var victims []*outboxMessage
	for _, m := range rows {
		if m.Status != status {
			continue
		}
		at := terminalAt(m)
		if at == nil || at.After(eligibleAt) {
			continue
		}
		victims = append(victims, m)
	}
	sort.Slice(victims, func(i, j int) bool {
		a, b := *terminalAt(victims[i]), *terminalAt(victims[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return victims[i].ID < victims[j].ID
	})
	return victims
}

func deadLetterSummary(m *outboxMessage) ports.DeadLetterSummary {
	return ports.DeadLetterSummary{
		ID:          m.ID,
		DedupKey:    m.DedupKey,
		TenantID:    m.TenantID,
		Status:      m.Status,
		Attempts:    m.Attempts,
		MaxAttempts: m.MaxAttempts,
		Generation:  m.Generation,
		Cause:       m.LastError,
		CreatedAt:   m.CreatedAt,
		DeadAt:      m.DeadAt,
		DiscardedAt: m.DiscardedAt,
	}
}
