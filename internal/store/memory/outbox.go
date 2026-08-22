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
	Status      string
	Attempts    int
	MaxAttempts int
	AvailableAt time.Time
	LeasedBy    string
	LeaseUntil  *time.Time
	LastError   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
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
		Status:      "pending",
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
		case m.Status == "pending" && !m.AvailableAt.After(now):
			due = append(due, m)
		case m.Status == "leased" && m.LeaseUntil != nil && !m.LeaseUntil.After(now):
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
		m.Status = "leased"
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
	if !ok || m.Status != "leased" || m.LeasedBy != leasedBy {
		// Uniform not-found keeps lease ownership non-enumerable.
		return domain.NotFoundf("outbox message", id)
	}
	now := r.st.clock.Now().UTC()
	m.Status = "delivered"
	m.DeliveredAt = &now
	m.LeaseUntil = nil
	return nil
}

func (r *OutboxRepository) FailWithBackoff(_ context.Context, id, leasedBy string, retryIn time.Duration, cause string) error {
	unlock := lockWrite(r.st)
	defer unlock()
	m, ok := r.st.outboxByID[id]
	if !ok || m.Status != "leased" || m.LeasedBy != leasedBy {
		return domain.NotFoundf("outbox message", id)
	}
	now := r.st.clock.Now().UTC()
	if m.Attempts >= m.MaxAttempts {
		m.Status = "dead"
	} else {
		m.Status = "pending"
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
		case "pending":
			stats.Pending++
		case "leased":
			stats.Leased++
		case "delivered":
			stats.Delivered++
		case "dead":
			stats.Dead++
		}
	}
	return stats, nil
}
