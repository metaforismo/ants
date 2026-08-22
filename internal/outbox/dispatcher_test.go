package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
	"github.com/metaforismo/ants/internal/store/storetest"
)

// recordingSink records deliveries per message ID and never fails.
type recordingSink struct {
	mu        sync.Mutex
	delivered map[string]int
}

func newRecordingSink() *recordingSink {
	return &recordingSink{delivered: map[string]int{}}
}

func (r *recordingSink) Deliver(_ context.Context, msg ports.OutboxMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered[msg.ID]++
	return nil
}

func (r *recordingSink) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered[id]
}

func (r *recordingSink) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.delivered {
		n += c
	}
	return n
}

// scriptedSink fails the first N deliveries per dedup key, then delegates.
type scriptedSink struct {
	mu       sync.Mutex
	failures map[string]int
	fallback Sink
}

func newScriptedSink(fallback Sink) *scriptedSink {
	return &scriptedSink{failures: map[string]int{}, fallback: fallback}
}

func (s *scriptedSink) failFirst(key string, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[key] = times
}

func (s *scriptedSink) Deliver(ctx context.Context, msg ports.OutboxMessage) error {
	s.mu.Lock()
	remaining := s.failures[msg.DedupKey]
	if remaining > 0 {
		s.failures[msg.DedupKey] = remaining - 1
	}
	s.mu.Unlock()
	if remaining > 0 {
		return fmt.Errorf("scripted failure for %s", msg.DedupKey)
	}
	return s.fallback.Deliver(ctx, msg)
}

// failingSink always fails.
type failingSink struct{}

func (failingSink) Deliver(context.Context, ports.OutboxMessage) error {
	return fmt.Errorf("sink permanently unavailable")
}

// newTestWorld pairs an advancing-clock memory store with a dispatcher over
// the default test configuration.
func newTestWorld(t *testing.T, sink Sink) (*Dispatcher, ports.Repositories, *storetest.AdvancingClock) {
	t.Helper()
	return newTestWorldWithConfig(t, sink, Config{
		BatchSize: 10, Interval: time.Minute, Lease: time.Second,
		MaxAttempts: 3, RetryBackoffBase: time.Second,
	})
}

func newTestWorldWithConfig(t *testing.T, sink Sink, cfg Config) (*Dispatcher, ports.Repositories, *storetest.AdvancingClock) {
	t.Helper()
	clock := storetest.NewAdvancingClock()
	repos, err := memorystore.NewReposWithOptions(memorystore.Options{Clock: clock})
	if err != nil {
		t.Fatalf("memory repos: %v", err)
	}
	d, err := New(repos.Outbox, sink, testLogger(), cfg,
		fmt.Sprintf("test-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	return d, repos.AsPorts(), clock
}

// publishN publishes n messages due immediately on the store's clock.
func publishN(t *testing.T, repos ports.Repositories, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for range n {
		idStr, _ := domain.NewID(domain.PrefixEvent)
		msg := ports.OutboxMessage{
			ID: idStr, DedupKey: "event:" + idStr, TenantID: domain.TenantID("ten_dispatchtenant000"),
			Envelope: []byte(`{}`), MaxAttempts: 3,
		}
		if err := repos.Outbox.Publish(ctx, msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, idStr)
	}
	return ids
}

func TestDispatchDeliversEachDueMessageOnce(t *testing.T) {
	d, repos, _ := newTestWorld(t, newRecordingSink())
	sink := d.sink.(*recordingSink)

	ids := publishN(t, repos, 3)
	handled, err := d.DispatchOnce(context.Background())
	if err != nil || handled != 3 {
		t.Fatalf("first round: %d %v", handled, err)
	}
	for _, id := range ids {
		if got := sink.count(id); got != 1 {
			t.Errorf("message %s delivered %d times", id, got)
		}
	}
	stats, _ := repos.Outbox.Stats(context.Background())
	if stats.Delivered != 3 || stats.Pending != 0 {
		t.Fatalf("stats after delivery: %+v", stats)
	}
	handled, err = d.DispatchOnce(context.Background())
	if err != nil || handled != 0 {
		t.Fatalf("second round must be empty: %d %v", handled, err)
	}
	if sink.total() != 3 {
		t.Fatalf("no duplicate deliveries allowed: %d", sink.total())
	}
}

func TestDispatchRetriesWithBackoffThenSucceeds(t *testing.T) {
	recorder := newRecordingSink()
	scripted := newScriptedSink(recorder)
	d, repos, clock := newTestWorld(t, scripted)
	ids := publishN(t, repos, 1)
	key := "event:" + ids[0]
	scripted.failFirst(key, 2)

	// Attempt 1 fails and reschedules one backoff step out.
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.count(ids[0]) != 0 {
		t.Fatalf("scripted failure must reach no fallback")
	}
	// Still inside the backoff window: nothing due.
	clock.Advance(500 * time.Millisecond)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.count(ids[0]) != 0 {
		t.Fatalf("backoff window must hide the message")
	}
	// Attempt 2 fails again; attempt 3 succeeds.
	clock.Advance(2 * time.Second)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(4 * time.Second)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := recorder.count(ids[0]); got != 1 {
		t.Fatalf("eventual delivery expected exactly once, got %d", got)
	}
	stats, _ := repos.Outbox.Stats(context.Background())
	if stats.Delivered != 1 || stats.Pending != 0 || stats.Dead != 0 {
		t.Fatalf("final stats: %+v", stats)
	}
}

func TestDispatchDeadLettersAfterMaxAttempts(t *testing.T) {
	d, repos, clock := newTestWorldWithConfig(t, failingSink{}, Config{
		BatchSize: 10, Interval: time.Minute, Lease: time.Second,
		MaxAttempts: 2, RetryBackoffBase: time.Second,
	})
	idStr, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(context.Background(), ports.OutboxMessage{
		ID: idStr, DedupKey: "event:" + idStr, TenantID: domain.TenantID("ten_dispatchtenant000"),
		Envelope: []byte(`{}`), MaxAttempts: 2,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, _ := repos.Outbox.Stats(context.Background())
	if stats.Dead != 1 {
		t.Fatalf("message must dead-letter after max attempts: %+v", stats)
	}
	clock.Advance(time.Hour)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats, _ := repos.Outbox.Stats(context.Background()); stats.Dead != 1 || stats.Pending != 0 {
		t.Fatalf("dead-letter must be terminal: %+v", stats)
	}
}

// TestExpiredLeaseRedeliversAfterWorkerDeath simulates a dispatcher that
// claimed a message and died before acknowledging: after the lease expires,
// another worker must redeliver it (at-least-once).
func TestExpiredLeaseRedeliversAfterWorkerDeath(t *testing.T) {
	sink := newRecordingSink()
	d, repos, clock := newTestWorld(t, sink)
	ctx := context.Background()

	ids := publishN(t, repos, 1)

	crashedLease, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "crashed-worker", LeaseFor: 30 * time.Second, Limit: 1,
	})
	if err != nil || len(crashedLease) != 1 {
		t.Fatalf("crashed worker lease: %d %v", len(crashedLease), err)
	}
	if got := sink.count(ids[0]); got != 0 {
		t.Fatalf("crashed worker never delivered: %d", got)
	}

	clock.Advance(time.Minute) // lease expired
	if _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.count(ids[0]); got != 1 {
		t.Fatalf("recovery worker must redeliver after lease expiry, saw %d", got)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Delivered != 1 {
		t.Fatalf("recovered message must end delivered: %+v", stats)
	}
}

// TestConcurrentDispatchersPartitionWork proves lease exclusivity under real
// parallelism: N dispatchers drain M messages with no overlaps or losses.
func TestConcurrentDispatchersPartitionWork(t *testing.T) {
	sink := newRecordingSink()
	d0, repos, _ := newTestWorld(t, sink)

	const workers = 4
	dispatchers := []*Dispatcher{d0}
	for w := 1; w < workers; w++ {
		d, err := New(repos.Outbox, sink, testLogger(), Config{
			BatchSize: 5, Interval: time.Minute, Lease: time.Hour,
			MaxAttempts: 3, RetryBackoffBase: time.Second,
		}, fmt.Sprintf("w%d", w))
		if err != nil {
			t.Fatal(err)
		}
		dispatchers = append(dispatchers, d)
	}
	const total = 40
	ids := publishN(t, repos, total)

	var wg sync.WaitGroup
	for _, disp := range dispatchers {
		wg.Add(1)
		go func(disp *Dispatcher) {
			defer wg.Done()
			for range 20 {
				_, _ = disp.DispatchOnce(context.Background())
			}
		}(disp)
	}
	wg.Wait()

	if sink.total() != total {
		t.Fatalf("every message delivered exactly once: total=%d want=%d", sink.total(), total)
	}
	for _, id := range ids {
		if got := sink.count(id); got != 1 {
			t.Fatalf("message %s delivered %d times", id, got)
		}
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	d, _, _ := newTestWorld(t, newRecordingSink())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run must exit cleanly: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not stop on cancellation")
	}
}
