package outboxgc

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testConfig uses production-shaped horizons; tests age rows by moving the
// manual clock instead of relying on wall-time granularity.
func testConfig() Config {
	return Config{
		DeliveredAfter: time.Hour,
		DiscardedAfter: 24 * time.Hour,
		BatchSize:      10,
		Interval:       time.Second,
	}
}

// manualClock is the deterministic time authority for these tests.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestService builds a service over a fresh memory store whose scheduling
// clock the test owns.
func newTestService(t *testing.T, cfg Config, obs Observer) (*Service, ports.OutboxStore, *manualClock) {
	t.Helper()
	clock := newManualClock()
	mem, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	if err != nil {
		t.Fatalf("build memory store: %v", err)
	}
	outbox := mem.AsPorts().Outbox
	svc, err := New(outbox, testLogger(), cfg, obs)
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	return svc, outbox, clock
}

const gcTenant = domain.TenantID("ten_gcservicetenant00000")

func seedDelivered(t *testing.T, ctx context.Context, store ports.OutboxStore, id string) {
	t.Helper()
	if err := store.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id,
		TenantID: gcTenant, Envelope: []byte(`{}`), MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := store.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease %s: %d %v", id, len(batch), err)
	}
	if err := store.MarkDelivered(ctx, id, "w1"); err != nil {
		t.Fatal(err)
	}
}

func seedDiscarded(t *testing.T, ctx context.Context, store ports.OutboxStore, id string) {
	t.Helper()
	if err := store.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id,
		TenantID: gcTenant, Envelope: []byte(`{}`), MaxAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := store.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease %s: %d %v", id, len(batch), err)
	}
	if err := store.FailWithBackoff(ctx, id, "w1", time.Second, "poison"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           gcTenant,
		MessageID:          id,
		ExpectedGeneration: 1,
		Actor:              domain.Actor{Type: domain.PrincipalHuman, ID: "prn_gcoperator000000"},
	}); err != nil {
		t.Fatal(err)
	}
}

type recorder struct {
	mu      sync.Mutex
	deleted map[string]int64
	rounds  int
}

func newRecorder() *recorder { return &recorder{deleted: map[string]int64{}} }

func (r *recorder) Deleted(state string, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted[state] += n
}

func (r *recorder) RoundsCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds++
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	mem := memorystore.NewRepos()
	for name, cfg := range map[string]Config{
		"zero batch":     {BatchSize: 0, Interval: time.Second},
		"huge batch":     {BatchSize: ports.MaxRetentionSweepBatch + 1, Interval: time.Second},
		"tiny interval":  {BatchSize: 5, Interval: time.Millisecond},
		"negative limit": {DeliveredAfter: -time.Second, BatchSize: 5, Interval: time.Second},
	} {
		if _, err := New(mem.AsPorts().Outbox, testLogger(), cfg, nil); err == nil {
			t.Fatalf("%s must be rejected at construction", name)
		}
	}
}

func TestPreviewReportsWithoutDeletingOrObserving(t *testing.T) {
	ctx := context.Background()
	rec := newRecorder()
	svc, store, clock := newTestService(t, testConfig(), rec)

	seedDelivered(t, ctx, store, "obx_evt_gcprev_d")
	seedDiscarded(t, ctx, store, "obx_evt_gcprev_c")
	clock.Advance(48 * time.Hour)

	res, err := svc.Preview(ctx)
	if err != nil || res.DeletedDelivered != 1 || res.DeletedDiscarded != 1 {
		t.Fatalf("preview must report both eligible classes: %+v %v", res, err)
	}
	stats, _ := store.Stats(ctx)
	if stats.Delivered != 1 || stats.Discarded != 1 {
		t.Fatalf("preview must not delete anything: %+v", stats)
	}
	if rec.rounds != 0 || len(rec.deleted) != 0 {
		t.Fatalf("previews must never fire observers: %+v", rec)
	}
}

func TestRoundDeletesWithinBoundsAndObserves(t *testing.T) {
	ctx := context.Background()
	rec := newRecorder()
	cfg := testConfig()
	cfg.BatchSize = 2 // one delivered victim + one discarded victim max
	svc, store, clock := newTestService(t, cfg, rec)

	seedDelivered(t, ctx, store, "obx_evt_gcround_d1")
	seedDiscarded(t, ctx, store, "obx_evt_gcround_c1")
	seedDelivered(t, ctx, store, "obx_evt_gcround_d2")
	clock.Advance(48 * time.Hour)

	first, err := svc.Round(ctx)
	if err != nil || first.DeletedDelivered+first.DeletedDiscarded != int64(cfg.BatchSize) {
		t.Fatalf("round must stop exactly at the batch bound: %+v %v", first, err)
	}
	// Delivered victims claim the whole budget first (ADR-0016 class
	// priority); the observer sees exactly that.
	if rec.deleted["delivered"] != 2 || rec.deleted["discarded"] != 0 || rec.rounds != 1 {
		t.Fatalf("observer must see truthful fixed-vocabulary counts: %+v", rec)
	}

	second, err := svc.Round(ctx)
	if err != nil || second.DeletedDelivered != 0 || second.DeletedDiscarded != 1 {
		t.Fatalf("continuation round collects the rest: %+v %v", second, err)
	}
	stats, _ := store.Stats(ctx)
	if stats.Delivered+stats.Discarded != 0 {
		t.Fatalf("eligible population must be drained: %+v", stats)
	}
}

func TestNilObserverKeepsBehaviorIdentical(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := newTestService(t, testConfig(), nil)
	seedDelivered(t, ctx, store, "obx_evt_gcnil_d")
	clock.Advance(2 * time.Hour)

	res, err := svc.Round(ctx)
	if err != nil || res.DeletedDelivered != 1 {
		t.Fatalf("nil observer must change nothing: %+v %v", res, err)
	}
}

func TestInactiveConfigRefusesScheduledRuns(t *testing.T) {
	inert := Config{BatchSize: 5, Interval: time.Second}
	if inert.Active() {
		t.Fatal("zero horizons must be inactive")
	}
	svc, _, _ := newTestService(t, inert, nil)
	if err := svc.Run(context.Background()); err == nil {
		t.Fatal("inactive configuration must refuse scheduled operation")
	}
	// Manual rounds stay available but cannot delete anything: both classes
	// are exempt.
	res, err := svc.Round(context.Background())
	if err != nil || res.DeletedDelivered+res.DeletedDiscarded != 0 {
		t.Fatalf("inert config must collect nothing: %+v %v", res, err)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	svc, _, _ := newTestService(t, testConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled run must return nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run must return promptly on cancellation")
	}
}
