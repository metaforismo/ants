package memory

import (
	"context"
	"encoding/json"

	"github.com/metaforismo/ants/internal/domain"
)

type EventRepository struct{ st *storeState }

func (r *EventRepository) Append(_ context.Context, evt *domain.Event) error {
	unlock := lockWrite(r.st)
	defer unlock()
	r.st.eventSeq++
	evt.Seq = r.st.eventSeq

	// Dual write (ADR-0011): the durable delivery joins its state change —
	// one lock guards both records here, mirroring the PostgreSQL adapter's
	// single-transaction append. The dedup key derives from the event ID so
	// at-least-once redelivery stays consumer-deduplicable.
	envelope, err := json.Marshal(evt)
	if err != nil {
		return domain.Internalf(err, "outbox_envelope", "serialize outbox envelope")
	}
	dedupKey := "event:" + string(evt.ID)
	if _, exists := r.st.outboxByDedup[dedupKey]; !exists {
		now := r.st.clock.Now().UTC()
		stored := &outboxMessage{
			ID:          "obx_" + string(evt.ID),
			DedupKey:    dedupKey,
			TenantID:    evt.TenantID,
			Envelope:    envelope,
			Status:      "pending",
			Attempts:    0,
			MaxAttempts: r.st.outboxMaxAttempts,
			AvailableAt: now,
			CreatedAt:   now,
		}
		r.st.outbox = append(r.st.outbox, stored)
		r.st.outboxByID[stored.ID] = stored
		r.st.outboxByDedup[stored.DedupKey] = stored
	}

	r.st.events = append(r.st.events, cloneEvent(evt))
	return nil
}

func (r *EventRepository) ListByTenant(_ context.Context, tenantID domain.TenantID, afterSeq int64, limit int) ([]*domain.Event, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Event{}
	for _, e := range r.st.events {
		if e.Seq <= afterSeq || e.TenantID != tenantID {
			continue
		}
		out = append(out, cloneEvent(e))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *EventRepository) ListByRun(_ context.Context, tenantID domain.TenantID, runID domain.RunID, afterSeq int64, limit int) ([]*domain.Event, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Event{}
	for _, e := range r.st.events {
		if e.Seq <= afterSeq || e.TenantID != tenantID || e.RunID != runID {
			continue
		}
		out = append(out, cloneEvent(e))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
