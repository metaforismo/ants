package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

func outboxMsg(id, dedup string, maxAttempts int) ports.OutboxMessage {
	return ports.OutboxMessage{
		ID:          id,
		DedupKey:    dedup,
		TenantID:    tenantID,
		Envelope:    []byte(fmt.Sprintf(`{"id":%q}`, id)),
		MaxAttempts: maxAttempts,
	}
}

// RunOutbox executes the durable queue contract against a fresh store per
// subtest; both adapters must satisfy the identical assertions. All
// time-based behavior is driven through the world's Advance hook, so lease
// expiry and backoff windows are deterministic without sleeping.
func RunOutbox(t *testing.T, f Factory) {
	t.Run("PublishIsIdempotentOnDedupKey", func(t *testing.T) {
		repos, _, _ := world(f)
		testOutboxPublishIdempotent(t, repos)
	})
	t.Run("PublishRejectsInvalidMaxAttempts", func(t *testing.T) {
		repos, _, _ := world(f)
		ctx := context.Background()
		idStr, _ := domain.NewID(domain.PrefixEvent)
		err := repos.Outbox.Publish(ctx, outboxMsg(idStr, "event:"+idStr, 0))
		if ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("max attempts 0 must be rejected as invalid, got %v", err)
		}
		stats, _ := repos.Outbox.Stats(ctx)
		if stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 0 {
			t.Fatalf("rejected publish must leave no row: %+v", stats)
		}
	})
	t.Run("LeaseClaimsAreExclusiveAndCounted", func(t *testing.T) {
		repos, _, _ := world(f)
		testOutboxLeaseExclusive(t, repos)
	})
	t.Run("ConcurrentLeasesNeverOverlap", func(t *testing.T) {
		repos, _, _ := world(f)
		testOutboxConcurrentLease(t, repos)
	})
	t.Run("AckRequiresActiveLease", func(t *testing.T) {
		repos, _, _ := world(f)
		testOutboxAckSemantics(t, repos)
	})
	t.Run("FailureReschedulesThenDeadLetters", func(t *testing.T) {
		repos, _, advance := world(f)
		testOutboxBackoffAndDeadLetter(t, repos, advance)
	})
	t.Run("ExpiredLeasesAreReclaimable", func(t *testing.T) {
		repos, _, advance := world(f)
		testOutboxExpiredLeaseReclaim(t, repos, advance)
	})
	t.Run("StatsReflectEveryState", func(t *testing.T) {
		repos, _, advance := world(f)
		testOutboxStats(t, repos, advance)
	})
	t.Run("EventAppendEnqueuesExactlyOneDelivery", func(t *testing.T) {
		repos, _, _ := world(f)
		testOutboxAppendDualWrite(t, repos)
	})
	t.Run("RolledBackAppendLeavesNoEventAndNoDelivery", func(t *testing.T) {
		repos, tx, _ := world(f)
		testOutboxRollbackDualWrite(t, repos, tx)
	})
}

func testOutboxPublishIdempotent(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	msg := outboxMsg(tid("obx", "publish"), "event:evt_one", 3)
	if err := repos.Outbox.Publish(ctx, msg); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := repos.Outbox.Publish(ctx, msg); err != nil {
		t.Fatalf("duplicate publish must be an idempotent no-op, got %v", err)
	}
	stats, err := repos.Outbox.Stats(ctx)
	if err != nil || stats.Pending != 1 {
		t.Fatalf("exactly one message must exist: %+v %v", stats, err)
	}
}

func testOutboxLeaseExclusive(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	for range 3 {
		idStr, _ := domain.NewID(domain.PrefixEvent)
		if err := repos.Outbox.Publish(ctx, outboxMsg(idStr, fmt.Sprintf("event:%s", idStr), 3)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(first) != 3 {
		t.Fatalf("lease all: %d %v", len(first), err)
	}
	for _, m := range first {
		if m.Attempts != 1 {
			t.Errorf("attempt must count at claim time: %s has %d", m.ID, m.Attempts)
		}
	}
	// A second claim before expiry sees nothing due.
	second, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(second) != 0 {
		t.Fatalf("leased messages must not reappear before expiry: %d %v", len(second), err)
	}
}

func testOutboxConcurrentLease(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	const total = 20
	for range total {
		idStr, _ := domain.NewID(domain.PrefixEvent)
		if err := repos.Outbox.Publish(ctx, outboxMsg(idStr, "event:"+idStr, 3)); err != nil {
			t.Fatal(err)
		}
	}
	const workers = 8
	var mu sync.Mutex
	seen := map[string]string{} // message id -> claiming worker
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			worker := fmt.Sprintf("w%d", n)
			for round := 0; round < total; round++ {
				batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
					WorkerID: worker, LeaseFor: time.Hour, Limit: 5,
				})
				if err != nil {
					t.Errorf("worker %s lease: %v", worker, err)
					return
				}
				mu.Lock()
				for _, m := range batch {
					if owner, dup := seen[m.ID]; dup {
						t.Errorf("message %s claimed by %s and %s", m.ID, owner, worker)
					} else {
						seen[m.ID] = worker
					}
				}
				mu.Unlock()
				if len(batch) == 0 {
					break
				}
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("every message must be claimed exactly once: %d/%d", len(seen), total)
	}
}

func testOutboxAckSemantics(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	idStr, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg(idStr, "event:"+idStr, 3)); err != nil {
		t.Fatal(err)
	}
	// Ack without a lease must fail.
	if ErrKind(repos.Outbox.MarkDelivered(ctx, idStr, "w1")) != domain.ErrKindNotFound {
		t.Fatalf("ack of unleased message must be rejected")
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease: %d %v", len(batch), err)
	}
	if err := repos.Outbox.MarkDelivered(ctx, batch[0].ID, "w1"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Delivered != 1 || stats.Leased != 0 {
		t.Fatalf("ack must move to delivered: %+v", stats)
	}
	// Double ack and wrong-lessee ack are invalid.
	if ErrKind(repos.Outbox.MarkDelivered(ctx, batch[0].ID, "w1")) != domain.ErrKindNotFound {
		t.Fatalf("double ack must be rejected")
	}
	if ErrKind(repos.Outbox.MarkDelivered(ctx, batch[0].ID, "intruder")) != domain.ErrKindNotFound {
		t.Fatalf("foreign-lessee ack must be rejected")
	}
}

func testOutboxBackoffAndDeadLetter(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	idStr, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg(idStr, "event:"+idStr, 2)); err != nil {
		t.Fatal(err)
	}
	failOnce := func() {
		t.Helper()
		batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
			WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
		})
		if err != nil || len(batch) != 1 {
			t.Fatalf("expected one due message: %d %v", len(batch), err)
		}
		if err := repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "sink unavailable"); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	// Attempt 1 fails -> pending again after backoff.
	failOnce()
	tooEarly, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(tooEarly) != 0 {
		t.Fatalf("backoff window must hide the message: %d %v", len(tooEarly), err)
	}
	// Past the window: attempt 2 fails -> attempts exhausted -> dead.
	advance(2 * time.Second)
	due, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(due) != 1 {
		t.Fatalf("retry lease: %d %v", len(due), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, due[0].ID, "w1", 3*time.Second, "still down"); err != nil {
		t.Fatal(err)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Dead != 1 {
		t.Fatalf("exhausted message must dead-letter: %+v", stats)
	}
	advance(time.Hour)
	// Dead messages are never claimed again.
	afterDead, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(afterDead) != 0 {
		t.Fatalf("dead-letter must not be reclaimable: %d %v", len(afterDead), err)
	}
}

func testOutboxExpiredLeaseReclaim(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	idStr, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg(idStr, "event:"+idStr, 3)); err != nil {
		t.Fatal(err)
	}
	first, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "crashed-worker", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(first) != 1 {
		t.Fatal("initial lease failed")
	}
	// The worker died without acknowledging: after the lease window the
	// message must be claimable again (at-least-once delivery).
	advance(2 * time.Minute)
	reclaimed, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "recovery-worker", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("expired lease must be reclaimable: %d %v", len(reclaimed), err)
	}
	if reclaimed[0].ID != first[0].ID {
		t.Fatalf("reclaimed message identity mismatch")
	}
	if reclaimed[0].Attempts != 2 {
		t.Fatalf("redelivery must count attempts: %d", reclaimed[0].Attempts)
	}
}

func testOutboxStats(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	a, _ := domain.NewID(domain.PrefixEvent)
	b, _ := domain.NewID(domain.PrefixEvent)
	c, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg(a, "event:"+a, 3)); err != nil {
		t.Fatal(err)
	}
	if err := repos.Outbox.Publish(ctx, outboxMsg(b, "event:"+b, 3)); err != nil {
		t.Fatal(err)
	}
	// c dies on its first failure (maxAttempts=1).
	if err := repos.Outbox.Publish(ctx, outboxMsg(c, "event:"+c, 1)); err != nil {
		t.Fatal(err)
	}

	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 3 {
		t.Fatalf("lease all three: %d %v", len(batch), err)
	}
	if err := repos.Outbox.MarkDelivered(ctx, a, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, b, "w1", time.Second, "x"); err != nil {
		t.Fatal(err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, c, "w1", time.Second, "x"); err != nil {
		t.Fatal(err)
	}
	// Past b's backoff window it becomes leasable again.
	advance(2 * time.Second)
	next, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 5,
	})
	if err != nil || len(next) != 1 || next[0].ID != b {
		t.Fatalf("only b must be due after backoff: %+v %v", next, err)
	}
	stats, err := repos.Outbox.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 3 {
		t.Fatalf("stats must partition all messages: %+v", stats)
	}
	if stats.Delivered != 1 || stats.Leased != 1 || stats.Dead != 1 {
		t.Fatalf("every state must be observable exactly once: %+v", stats)
	}
}

// testOutboxAppendDualWrite proves the transactional-outbox seam itself:
// appending an event enqueues exactly one delivery whose dedup key derives
// from the event ID and whose envelope carries that stable ID (ADR-0011).
func testOutboxAppendDualWrite(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "outboxdw")

	idStr, _ := domain.NewID(domain.PrefixEvent)
	evt := &domain.Event{
		ID:            domain.EventID(idStr),
		Type:          domain.EventRunStatusChanged,
		TenantID:      tenantID,
		AggregateType: "run",
		AggregateID:   tid("run", "dualwrite"),
		Data:          map[string]any{"to": "planning"},
	}
	if err := repos.Events.Append(ctx, evt); err != nil {
		t.Fatalf("append: %v", err)
	}

	stats, err := repos.Outbox.Stats(ctx)
	if err != nil || stats.Pending != 1 {
		t.Fatalf("event append must enqueue exactly one delivery: %+v %v", stats, err)
	}
	leased, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease enqueued message: %d %v", len(leased), err)
	}
	if got := leased[0].DedupKey; got != "event:"+string(evt.ID) {
		t.Fatalf("dedup key must derive from the event id, got %q", got)
	}
	var envelope map[string]any
	if err := json.Unmarshal(leased[0].Envelope, &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if envelope["id"] != string(evt.ID) {
		t.Fatalf("envelope must carry the stable event id")
	}
}

// testOutboxRollbackDualWrite proves a rolled-back unit leaves NEITHER the
// event NOR its queued delivery behind — notifications can never outlive
// their state change (ADR-0011).
func testOutboxRollbackDualWrite(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "outboxrb")

	idStr, _ := domain.NewID(domain.PrefixEvent)
	boom := errors.New("rollback the whole unit")
	err := tx.Do(ctx, func(ctx context.Context) error {
		if aerr := repos.Events.Append(ctx, &domain.Event{
			ID:            domain.EventID(idStr),
			Type:          domain.EventRunStarted,
			TenantID:      tenantID,
			AggregateType: "run",
			AggregateID:   tid("run", "rollback"),
			Data:          map[string]any{},
		}); aerr != nil {
			return aerr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unit must fail as scripted, got %v", err)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Pending+stats.Leased != 0 {
		t.Fatalf("rolled-back unit must leave no outbox row: %+v", stats)
	}
	events, _ := repos.Events.ListByTenant(ctx, tenantID, 0, 0)
	if len(events) != 0 {
		t.Fatalf("rolled-back unit must leave no event row: %d", len(events))
	}
}
