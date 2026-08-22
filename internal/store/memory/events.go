package memory

import (
	"context"

	"github.com/metaforismo/ants/internal/domain"
)

type EventRepository struct{ st *storeState }

func (r *EventRepository) Append(_ context.Context, evt *domain.Event) error {
	unlock := lockWrite(r.st)
	defer unlock()
	r.st.eventSeq++
	evt.Seq = r.st.eventSeq
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
