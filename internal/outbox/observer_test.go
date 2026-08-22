package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// recordingObserver captures dispatcher instrumentation callbacks so tests
// pin which outcomes surface as metrics without pulling in Prometheus.
type recordingObserver struct {
	mu        sync.Mutex
	leased    []int
	states    []ports.OutboxStats
	delivered int
	retries   int
	dead      int
}

func (o *recordingObserver) RoundLeased(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.leased = append(o.leased, n)
}

func (o *recordingObserver) OutboxStates(s ports.OutboxStats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, s)
}

func (o *recordingObserver) Delivered() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.delivered++
}

func (o *recordingObserver) RetryScheduled() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retries++
}

func (o *recordingObserver) DeadLettered() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dead++
}

func (o *recordingObserver) snapshot() (leased []int, states []ports.OutboxStats, delivered, retries, dead int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int(nil), o.leased...), append([]ports.OutboxStats(nil), o.states...),
		o.delivered, o.retries, o.dead
}

// TestObserverRecordsDeliveryLifecycle pins that a successful round surfaces
// the leased count, one delivered observation, and a state refresh sampled
// right after leasing (messages held by the round appear as leased).
func TestObserverRecordsDeliveryLifecycle(t *testing.T) {
	sink := newRecordingSink()
	obs := &recordingObserver{}
	d, repos, _ := newTestWorldWithObserver(t, sink, Config{
		BatchSize: 10, Interval: time.Minute, Lease: time.Second,
		MaxAttempts: 3, RetryBackoffBase: time.Second,
	}, obs)
	publishN(t, repos, 2)

	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	leased, states, delivered, retries, dead := obs.snapshot()
	if len(leased) != 1 || leased[0] != 2 {
		t.Fatalf("round must observe the leased batch size 2, got %v", leased)
	}
	if len(states) != 1 || states[0].Leased != 2 || states[0].Delivered != 0 || states[0].Dead != 0 {
		t.Fatalf("state sample must be the post-lease instant, got %+v", states)
	}
	if delivered != 2 || retries != 0 || dead != 0 {
		t.Fatalf("delivery observations wrong: delivered=%d retries=%d dead=%d", delivered, retries, dead)
	}
}

// TestNilObserverKeepsDispatchBehavior pins the observer contract: nil only
// disables instrumentation, dispatch outcomes are unchanged.
func TestNilObserverKeepsDispatchBehavior(t *testing.T) {
	d, repos, _ := newTestWorldWithConfig(t, newRecordingSink(), Config{
		BatchSize: 10, Interval: time.Minute, Lease: time.Second,
		MaxAttempts: 3, RetryBackoffBase: time.Second,
	})
	publishN(t, repos, 2)

	if n, err := d.DispatchOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("dispatch must deliver both messages, got n=%d err=%v", n, err)
	}
	states, err := repos.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if states.Delivered != 2 || states.Pending != 0 {
		t.Fatalf("delivery must be unaffected by the absent observer, got %+v", states)
	}
}

// TestObserverRecordsRetryAndDeadLetter pins that failing deliveries are
// observed as retry scheduling until attempts exhaust, then as dead-letter,
// with the next round's state sample confirming the terminal state.
func TestObserverRecordsRetryAndDeadLetter(t *testing.T) {
	obs := &recordingObserver{}
	d, repos, clock := newTestWorldWithObserver(t, failingSink{}, Config{
		BatchSize: 10, Interval: time.Minute, Lease: time.Second,
		MaxAttempts: 2, RetryBackoffBase: time.Second,
	}, obs)
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
	clock.Advance(time.Hour)
	if _, err := d.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, _, delivered, retries, dead := obs.snapshot()
	if delivered != 0 {
		t.Fatalf("a failing sink must not record deliveries, got %d", delivered)
	}
	if retries != 1 || dead != 1 {
		t.Fatalf("one retry then one dead-letter expected, got retries=%d dead=%d", retries, dead)
	}
	lastStates := obs.states[len(obs.states)-1]
	if lastStates.Dead != 1 || lastStates.Leased != 0 {
		t.Fatalf("idle-round state sample must show the message dead, got %+v", lastStates)
	}
}
