package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// TestEventAppendDualWritesOutboxAtomically proves the two properties the
// delivery seam depends on (ADR-0011):
//
//  1. a committed event always has exactly one queued outbox message with a
//     stable dedup key derived from the event ID;
//  2. a rolled-back unit of work leaves NEITHER the event NOR the outbox
//     row behind — notifications can never outlive their state change.
func TestEventAppendDualWritesOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	w := newPGWorld(t)
	store, repos := w.Store, w.Repos

	tenIDStr, _ := domain.NewID(domain.PrefixTenant)
	tenant, terr := domain.NewTenant(domain.TenantID(tenIDStr), "atomicity", "Atomicity", domain.PlanFree, "", time.Now().UTC())
	if terr != nil {
		t.Fatal(terr)
	}
	if err := repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	tenantID := tenant.ID

	evt := &domain.Event{
		ID:            domain.EventID("evt_dualwrite00000000000"),
		Type:          domain.EventRunStatusChanged,
		TenantID:      tenantID,
		AggregateType: "run",
		AggregateID:   "run_dualwriterun000000000",
		RunID:         domain.RunID("run_dualwriterun000000000"),
		Data:          map[string]any{"to": "planning"},
	}
	if err := repos.Events.Append(ctx, evt); err != nil {
		t.Fatalf("append: %v", err)
	}

	stats, err := repos.Outbox.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Fatalf("event append must enqueue exactly one delivery: %+v", stats)
	}

	leased, err := repos.Outbox.Lease(ctx, leaseAllNow())
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease enqueued message: %d %v", len(leased), err)
	}
	if got := string(leased[0].DedupKey); got != "event:"+string(evt.ID) {
		t.Fatalf("dedup key must derive from the event id, got %q", got)
	}
	var envelope map[string]any
	if err := unmarshalTestJSON(leased[0].Envelope, &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if envelope["id"] != string(evt.ID) {
		t.Fatalf("envelope must carry the stable event id")
	}

	// Rolled-back unit: neither row may survive.
	err = store.Do(ctx, func(ctx context.Context) error {
		if err := repos.Events.Append(ctx, &domain.Event{
			ID: domain.EventID("evt_rollbackcase00000000"), Type: domain.EventRunStarted,
			TenantID: tenantID, AggregateType: "run", AggregateID: "run_x", Data: map[string]any{},
		}); err != nil {
			return err
		}
		return errors.New("rollback the whole unit")
	})
	if err == nil {
		t.Fatalf("unit must fail as scripted")
	}
	statsAfter, _ := repos.Outbox.Stats(ctx)
	if statsAfter.Pending+statsAfter.Leased != 1 {
		t.Fatalf("rolled-back unit must leave no outbox row: %+v", statsAfter)
	}
	events, _ := repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if len(events) != 1 {
		t.Fatalf("rolled-back unit must leave no event row: %d", len(events))
	}
}
