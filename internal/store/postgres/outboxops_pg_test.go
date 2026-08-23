package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/outboxops"
	"github.com/metaforismo/ants/internal/ports"
)

// failingAuditStore injects persistence failures at the audit seam while
// delegating reads, proving that a real PostgreSQL transaction rolls back
// the guarded row mutation and its event delivery when the unit fails.
type failingAuditStore struct{ err error }

func (f *failingAuditStore) Append(context.Context, *domain.AuditEvent) error { return f.err }
func (f *failingAuditStore) ListByTenant(context.Context, domain.TenantID, string, int) ([]*domain.AuditEvent, error) {
	return nil, nil
}

var (
	pgTenantID = domain.TenantID("ten_pgoperatorfixture0001")
	pgOperator = domain.Actor{Type: domain.PrincipalHuman, ID: "prn_pgoperator00000001"}
)

func newPGOperatorService(t *testing.T, w *pgWorld, audit ports.AuditStore) *outboxops.Service {
	t.Helper()
	if audit == nil {
		audit = w.Repos.Audit
	}
	svc, err := outboxops.New(outboxops.Deps{
		Outbox: w.Repos.Outbox,
		Events: w.Repos.Events,
		Audit:  audit,
		Tx:     w.Store,
		IDs:    ports.RandomIDs{},
		Clock:  w.Clock,
	})
	if err != nil {
		t.Fatalf("build operator service: %v", err)
	}
	return svc
}

func newPGOperatorTenant(t *testing.T, ctx context.Context, w *pgWorld) {
	t.Helper()
	tenant, err := domain.NewTenant(pgTenantID, "pg-ops", "PG Ops", domain.PlanFree, "local", time.Now())
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := w.Repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func seedPGDeadLetter(t *testing.T, ctx context.Context, w *pgWorld, id string) ports.DeadLetterSummary {
	t.Helper()
	if err := w.Repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id, TenantID: pgTenantID,
		Envelope: []byte(`{"id":"` + id + `"}`), MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	batch, err := w.Repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease: %d %v", len(batch), err)
	}
	if err := w.Repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "pg sink down"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	got, err := w.Repos.Outbox.GetDeadLetter(ctx, pgTenantID, id)
	if err != nil {
		t.Fatalf("read dead letter: %v", err)
	}
	return *got
}

// The operator service commits the row mutation, the versioned event, the
// durable notification delivery, and the audit record as one PostgreSQL
// transaction — proven here against a real database.
func TestOutboxOperatorMutationComposesAtomicallyOnPostgres(t *testing.T) {
	ctx := context.Background()
	w := newPGWorld(t)
	newPGOperatorTenant(t, ctx, w)
	svc := newPGOperatorService(t, w, nil)

	const id = "obx_evt_pgcompose0000001"
	dead := seedPGDeadLetter(t, ctx, w, id)

	result, err := svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID:           pgTenantID,
		MessageID:          id,
		ExpectedGeneration: dead.Generation,
		Actor:              pgOperator,
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if result.Generation != dead.Generation+1 {
		t.Fatalf("generation must advance exactly once: %+v", result)
	}

	events, err := w.Repos.Events.ListByTenant(ctx, pgTenantID, 0, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("exactly one event committed: %d %v", len(events), err)
	}
	if events[0].Type != domain.EventOutboxDeadLetterRequeued ||
		events[0].AggregateType != "outbox_message" ||
		events[0].AggregateVersion != result.Generation {
		t.Fatalf("event envelope wrong: %+v", events[0])
	}

	batch, err := w.Repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 2 {
		t.Fatalf("restarted message plus its delivery must be due: %d %v", len(batch), err)
	}

	audit, err := w.Repos.Audit.ListByTenant(ctx, pgTenantID, "", 10)
	if err != nil || len(audit) != 1 || audit[0].Action != domain.ActionOutboxRequeue {
		t.Fatalf("audit record wrong: %+v %v", audit, err)
	}
}

func TestOutboxOperatorDiscardComposesOnPostgres(t *testing.T) {
	ctx := context.Background()
	w := newPGWorld(t)
	newPGOperatorTenant(t, ctx, w)
	svc := newPGOperatorService(t, w, nil)

	const id = "obx_evt_pgdiscard000001"
	dead := seedPGDeadLetter(t, ctx, w, id)

	if _, err := svc.Discard(ctx, ports.OutboxMutationRequest{
		TenantID:           pgTenantID,
		MessageID:          id,
		ExpectedGeneration: dead.Generation,
		Actor:              pgOperator,
	}); err != nil {
		t.Fatalf("discard: %v", err)
	}

	got, err := w.Repos.Outbox.GetDeadLetter(ctx, pgTenantID, id)
	if err != nil || got.Status != domain.OutboxDiscarded || got.DiscardedAt == nil {
		t.Fatalf("terminal decision not retained: %+v %v", got, err)
	}
	stats, err := w.Repos.Outbox.Stats(ctx)
	if err != nil || stats.Discarded != 1 {
		t.Fatalf("discarded rows must be counted: %+v %v", stats, err)
	}
	events, _ := w.Repos.Events.ListByTenant(ctx, pgTenantID, 0, 0)
	if len(events) != 1 || events[0].Type != domain.EventOutboxDeadLetterDiscarded {
		t.Fatalf("discard event wrong: %+v", events)
	}
}

func TestOutboxOperatorAuditFailureRollsBackOnPostgres(t *testing.T) {
	ctx := context.Background()
	w := newPGWorld(t)
	newPGOperatorTenant(t, ctx, w)
	svc := newPGOperatorService(t, w, &failingAuditStore{err: errors.New("audit down")})

	const id = "obx_evt_pgrollback00001"
	dead := seedPGDeadLetter(t, ctx, w, id)

	_, err := svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID:           pgTenantID,
		MessageID:          id,
		ExpectedGeneration: dead.Generation,
		Actor:              pgOperator,
	})
	if err == nil {
		t.Fatalf("audit failure must abort the unit")
	}

	got, gerr := w.Repos.Outbox.GetDeadLetter(ctx, pgTenantID, id)
	if gerr != nil || got.Status != domain.OutboxDead || got.Generation != dead.Generation {
		t.Fatalf("row mutation must roll back with its trail: %+v %v", got, gerr)
	}
	stats, _ := w.Repos.Outbox.Stats(ctx)
	if stats.Pending != 0 || stats.Dead != 1 {
		t.Fatalf("no partial state may survive rollback: %+v", stats)
	}
	events, _ := w.Repos.Events.ListByTenant(ctx, pgTenantID, 0, 0)
	if len(events) != 0 {
		t.Fatalf("no event may survive rollback: %d", len(events))
	}
}
