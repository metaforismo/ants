package outboxops

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

var (
	tenantID = domain.TenantID("ten_contracttenant000000")
	operator = domain.Actor{Type: domain.PrincipalHuman, ID: "prn_operator000000000000"}
)

// manualClock is a deterministic time authority owned by the test.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// outcomeRecorder captures observer callbacks verbatim.
type outcomeRecorder struct {
	mu      sync.Mutex
	actions []string
}

func (r *outcomeRecorder) ActionRecorded(action, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action+"/"+outcome)
}

// failingAudit injects persistence failures at the audit seam so rollback of
// an otherwise-successful mutation can be proven.
type failingAudit struct{ err error }

func (f *failingAudit) Append(context.Context, *domain.AuditEvent) error { return f.err }
func (f *failingAudit) ListByTenant(context.Context, domain.TenantID, string, int) ([]*domain.AuditEvent, error) {
	return nil, nil
}

// failingEvents injects persistence failures at the event seam.
type failingEvents struct{ err error }

func (f *failingEvents) Append(context.Context, *domain.Event) error { return f.err }
func (f *failingEvents) ListByTenant(ctx context.Context, tenantID domain.TenantID, afterSeq int64, limit int) ([]*domain.Event, error) {
	return nil, nil
}
func (f *failingEvents) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, afterSeq int64, limit int) ([]*domain.Event, error) {
	return nil, nil
}

type world struct {
	svc   *Service
	repos ports.Repositories
	clock *manualClock
	obs   *outcomeRecorder
}

func newWorld(t *testing.T) world {
	t.Helper()
	return newWorldDeps(t, func(repos ports.Repositories) (ports.EventLog, ports.AuditStore) {
		return repos.Events, repos.Audit
	})
}

// newWorldFailingAudit keeps real event persistence and fails every audit
// append, isolating the rollback path to one seam.
func newWorldFailingAudit(t *testing.T, err error) world {
	t.Helper()
	return newWorldDeps(t, func(repos ports.Repositories) (ports.EventLog, ports.AuditStore) {
		return repos.Events, &failingAudit{err: err}
	})
}

// newWorldFailingEvents fails every event append before its delivery is
// queued, exercising the earliest rollback point of the unit.
func newWorldFailingEvents(t *testing.T, err error) world {
	t.Helper()
	return newWorldDeps(t, func(repos ports.Repositories) (ports.EventLog, ports.AuditStore) {
		return &failingEvents{err: err}, repos.Audit
	})
}

func newWorldDeps(t *testing.T, override func(ports.Repositories) (ports.EventLog, ports.AuditStore)) world {
	t.Helper()
	clock := newManualClock()
	mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	if err != nil {
		t.Fatalf("build memory store: %v", err)
	}
	repos := mem.AsPorts()
	events, audit := override(repos)
	obs := &outcomeRecorder{}
	svc, err := New(Deps{
		Outbox:   repos.Outbox,
		Events:   events,
		Audit:    audit,
		Tx:       mem.NewTransactor(),
		IDs:      ports.RandomIDs{},
		Clock:    clock,
		Observer: obs,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	return world{svc: svc, repos: repos, clock: clock, obs: obs}
}

func (w world) seedTenant(t *testing.T, ctx context.Context) {
	t.Helper()
	tenant, err := domain.NewTenant(tenantID, "acme", "Acme", domain.PlanFree, "local", w.clock.Now())
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := w.repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

// seedDeadLetter drives a message to terminal failure along the production
// path (max_attempts=1) and returns its fencing credential.
func (w world) seedDeadLetter(t *testing.T, ctx context.Context, id string) ports.DeadLetterSummary {
	t.Helper()
	if err := w.repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id, TenantID: tenantID,
		Envelope: []byte(`{"id":"` + id + `"}`), MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	batch, err := w.repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease: %d %v", len(batch), err)
	}
	if err := w.repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "sink unavailable"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	got, err := w.repos.Outbox.GetDeadLetter(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("read dead letter: %v", err)
	}
	return *got
}

func TestRequeueComposesEventDeliveryAndAudit(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.seedTenant(t, ctx)
	id := "obx_evt_requeuecompose01"
	dead := w.seedDeadLetter(t, ctx, id)

	result, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          id,
		ExpectedGeneration: dead.Generation,
		Actor:              operator,
		Reason:             "sink recovered",
		TraceID:            "trace-123",
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if result.Generation != dead.Generation+1 {
		t.Fatalf("post-transition generation mismatch: %+v", result)
	}

	// Exactly one versioned event, addressed to the message aggregate.
	events, err := w.repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("exactly one event expected: %d %v", len(events), err)
	}
	evt := events[0]
	if evt.Type != domain.EventOutboxDeadLetterRequeued {
		t.Fatalf("wrong event type %q", evt.Type)
	}
	if evt.AggregateType != "outbox_message" || evt.AggregateID != id {
		t.Fatalf("event must target the message aggregate: %+v", evt)
	}
	if evt.AggregateVersion != result.Generation {
		t.Fatalf("aggregate version must be the post-op generation: %+v", evt)
	}
	if evt.Actor != operator || evt.TraceID != "trace-123" {
		t.Fatalf("provenance must round-trip: %+v", evt)
	}
	if evt.Data["message_id"] != id || evt.Data["action"] != "requeue" ||
		evt.Data["reason"] != "sink recovered" {
		t.Fatalf("event data incomplete: %+v", evt.Data)
	}

	// The event's durable delivery was enqueued in the same unit: leasing
	// now yields both the restarted original and the notification.
	batch, err := w.repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 2 {
		t.Fatalf("two due messages expected: %d %v", len(batch), err)
	}
	var notification *ports.OutboxMessage
	for i := range batch {
		if batch[i].ID != id {
			notification = &batch[i]
		}
	}
	if notification == nil {
		t.Fatalf("event delivery missing from the queue: %+v", batch)
	}
	if notification.DedupKey != "event:"+string(evt.ID) {
		t.Fatalf("delivery dedup key must derive from the event id: %q", notification.DedupKey)
	}
	var envelope map[string]any
	if err := json.Unmarshal(notification.Envelope, &envelope); err != nil {
		t.Fatalf("envelope must be valid JSON: %v", err)
	}
	if envelope["id"] != string(evt.ID) {
		t.Fatalf("envelope must carry the stable event id")
	}

	// One tenant-scoped audit record with actor, target, and outcome.
	audit, err := w.repos.Audit.ListByTenant(ctx, tenantID, "", 10)
	if err != nil || len(audit) != 1 {
		t.Fatalf("exactly one audit record expected: %d %v", len(audit), err)
	}
	rec := audit[0]
	if rec.Action != domain.ActionOutboxRequeue ||
		rec.ResourceType != "outbox_message" || rec.ResourceID != id ||
		rec.Result != domain.AuditResultAllowed || rec.TraceID != "trace-123" {
		t.Fatalf("audit record shape wrong: %+v", rec)
	}
	if rec.Metadata["action"] != "requeue" {
		t.Fatalf("audit metadata incomplete: %+v", rec.Metadata)
	}
}

func TestDiscardComposesTerminalDecisionTrail(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.seedTenant(t, ctx)
	id := "obx_evt_discardcompose01"
	dead := w.seedDeadLetter(t, ctx, id)

	result, err := w.svc.Discard(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          id,
		ExpectedGeneration: dead.Generation,
		Actor:              operator,
	})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	events, _ := w.repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if len(events) != 1 || events[0].Type != domain.EventOutboxDeadLetterDiscarded {
		t.Fatalf("discard event missing or mistyped: %+v", events)
	}
	audit, _ := w.repos.Audit.ListByTenant(ctx, tenantID, "", 10)
	if len(audit) != 1 || audit[0].Action != domain.ActionOutboxDiscard {
		t.Fatalf("discard audit missing or misactioned: %+v", audit)
	}
	// The discarded row remains queryable history.
	got, gerr := w.svc.GetDeadLetter(ctx, tenantID, id)
	if gerr != nil || got.Status != domain.OutboxDiscarded || got.Generation != result.Generation {
		t.Fatalf("terminal decision not retained: %+v %v", got, gerr)
	}
}

func TestAuditFailureRollsBackWholeUnit(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("audit sink down")
	w := newWorldFailingAudit(t, boom)
	w.seedTenant(t, ctx)
	id := "obx_evt_auditrollback001"
	dead := w.seedDeadLetter(t, ctx, id)

	_, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: id, ExpectedGeneration: dead.Generation, Actor: operator,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("audit failure must abort the mutation, got %v", err)
	}

	w.mustDeadLetter(t, ctx, id, dead.Generation)
	events, _ := w.repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if len(events) != 0 {
		t.Fatalf("rolled-back unit must leave no event: %d", len(events))
	}
	stats, _ := w.repos.Outbox.Stats(ctx)
	if stats.Pending != 0 || stats.Dead != 1 {
		t.Fatalf("rolled-back unit must leave no delivery behind: %+v", stats)
	}
	if len(w.obs.snapshot()) != 1 || w.obs.snapshot()[0] != "requeue/failed" {
		t.Fatalf("observer must see the failed outcome: %+v", w.obs.snapshot())
	}
}

func TestEventFailureRollsBackWholeUnit(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("event log down")
	w := newWorldFailingEvents(t, boom)
	w.seedTenant(t, ctx)
	id := "obx_evt_eventrollback01"
	dead := w.seedDeadLetter(t, ctx, id)

	_, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: id, ExpectedGeneration: dead.Generation, Actor: operator,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("event failure must abort the mutation, got %v", err)
	}

	w.mustDeadLetter(t, ctx, id, dead.Generation)
	stats, _ := w.repos.Outbox.Stats(ctx)
	if stats.Pending != 0 || stats.Dead != 1 {
		t.Fatalf("rolled-back unit must leave no partial state: %+v", stats)
	}
}

func TestObserverSeesTypedOutcomeVocabulary(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.seedTenant(t, ctx)

	// Unknown message → not_found.
	if _, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: "obx_evt_missingcase001",
		ExpectedGeneration: 1, Actor: operator,
	}); kindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}

	// Live message → invalid state.
	liveStr, _ := domain.NewID(domain.PrefixEvent)
	if err := w.repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: "obx_" + liveStr, DedupKey: "event:" + liveStr, TenantID: tenantID,
		Envelope: []byte(`{}`), MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: "obx_" + liveStr,
		ExpectedGeneration: 1, Actor: operator,
	}); kindOf(err) != domain.ErrKindInvalidTransition {
		t.Fatalf("expected invalid_transition, got %v", err)
	}

	// Structurally invalid request → invalid.
	if _, err := w.svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: "",
	}); kindOf(err) != domain.ErrKindInvalid {
		t.Fatalf("expected invalid, got %v", err)
	}

	want := []string{
		"requeue/not_found",
		"requeue/invalid_state",
		"requeue/invalid_request",
	}
	got := w.obs.snapshot()
	if len(got) != len(want) {
		t.Fatalf("observer outcomes mismatch: %+v vs %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outcome %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNilObserverKeepsBehaviorIdentical(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	repos := mem.AsPorts()
	tenant, _ := domain.NewTenant(tenantID, "acme", "Acme", domain.PlanFree, "local", clock.Now())
	if err := repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Deps{
		Outbox: repos.Outbox, Events: repos.Events, Audit: repos.Audit,
		Tx: mem.NewTransactor(), IDs: ports.RandomIDs{}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := "obx_evt_nilobserver001"
	w := world{svc: svc, repos: repos, clock: clock}
	dead := w.seedDeadLetter(t, ctx, id)
	if _, err := svc.Requeue(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: id, ExpectedGeneration: dead.Generation, Actor: operator,
	}); err != nil {
		t.Fatalf("nil observer must change nothing: %v", err)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Pending != 2 { // restarted message + its event delivery
		t.Fatalf("mutation must still compose fully: %+v", stats)
	}
}

func TestNewRequiresEveryDependency(t *testing.T) {
	clock := newManualClock()
	mem, _ := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	repos := mem.AsPorts()
	base := Deps{
		Outbox: repos.Outbox, Events: repos.Events, Audit: repos.Audit,
		Tx: mem.NewTransactor(), IDs: ports.RandomIDs{}, Clock: clock,
	}
	fields := []string{"Outbox", "Events", "Audit", "Tx", "IDs", "Clock"}
	for i := range fields {
		broken := base
		switch fields[i] {
		case "Outbox":
			broken.Outbox = nil
		case "Events":
			broken.Events = nil
		case "Audit":
			broken.Audit = nil
		case "Tx":
			broken.Tx = nil
		case "IDs":
			broken.IDs = nil
		case "Clock":
			broken.Clock = nil
		}
		if _, err := New(broken); err == nil {
			t.Fatalf("%s must be required", fields[i])
		}
	}
	if _, err := New(base); err != nil {
		t.Fatalf("complete deps must construct: %v", err)
	}
}

// mustDeadLetter asserts the row is still dead at exactly the given
// generation — the rollback contract.
func (w world) mustDeadLetter(t *testing.T, ctx context.Context, id string, gen int64) ports.DeadLetterSummary {
	t.Helper()
	got, err := w.repos.Outbox.GetDeadLetter(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("row must remain readable as dead: %v", err)
	}
	if got.Status != domain.OutboxDead || got.Generation != gen {
		t.Fatalf("rolled-back unit must leave the epoch untouched: %+v", got)
	}
	return *got
}

func (r *outcomeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.actions))
	copy(out, r.actions)
	return out
}

func kindOf(err error) domain.ErrorKind { return domain.ErrKindOf(err) }
