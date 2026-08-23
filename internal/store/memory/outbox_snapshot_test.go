package memory

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// manualClock is a deterministic time authority owned by this test.
type manualClock struct {
	t time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time { return c.t }

func (c *manualClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// TestOutboxRollbackRestoresCanonicalRow pins the snapshot invariant that
// makes rolled-back units real for outbox rows: backup and restore must map
// each stored row to exactly one clone shared by all three views (slice,
// ID index, dedup index). If the views ever forked, a post-rollback mutation
// through one view would be invisible to the others.
func TestOutboxRollbackRestoresCanonicalRow(t *testing.T) {
	clock := newManualClock()
	repos, err := NewReposWithOptions(Options{Clock: clock})
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	st := repos.st
	ctx := context.Background()

	tenant := domain.TenantID("ten_canonicaltenant000000")
	const id = "obx_evt_canonicalrow"
	const dedup = "event:canonical"
	originalEnvelope := []byte(`{"type":"demo.created.v1","body":"original"}`)

	publishedAt := clock.Now()
	if err := repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID:          id,
		DedupKey:    dedup,
		TenantID:    tenant,
		Envelope:    originalEnvelope,
		MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	clock.Advance(time.Minute)
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease: %d %v", len(batch), err)
	}
	clock.Advance(time.Minute)
	deathAt := clock.Now()
	if err := repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "sink unavailable"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	canonical := func() *outboxMessage {
		t.Helper()
		row := st.outboxByID[id]
		if row == nil {
			t.Fatalf("row %s missing from ID index", id)
		}
		if st.outbox[0] != row || st.outboxByDedup[dedup] != row {
			t.Fatalf("outbox views must alias one canonical row: slice=%p byID=%p byDedup=%p",
				st.outbox[0], row, st.outboxByDedup[dedup])
		}
		return row
	}
	before := canonical()
	if before.Status != domain.OutboxDead || before.Generation != 1 || before.Attempts != 1 ||
		before.DeadAt == nil || !before.DeadAt.Equal(deathAt) {
		t.Fatalf("seed must leave a dead letter with a fresh epoch: %+v", before)
	}

	// Inside one unit: corrupt the envelope bytes in place and requeue the
	// row, then roll back. The backup must have detached both the struct and
	// its envelope backing array, or the corruption and epoch bump survive.
	txErr := repos.NewTransactor().Do(ctx, func(ctx context.Context) error {
		live := st.outboxByID[id]
		live.Envelope[0] = 'X'
		if _, err := repos.Outbox.RequeueDeadLetter(ctx, ports.OutboxMutationRequest{
			TenantID:           tenant,
			MessageID:          id,
			ExpectedGeneration: 1,
			Actor:              domain.Actor{Type: domain.PrincipalHuman, ID: "prn_operator"},
		}); err != nil {
			return err
		}
		if live.Status != domain.OutboxPending || live.Generation != 2 {
			t.Fatalf("mutation must be visible inside the unit: %+v", live)
		}
		return errors.New("force rollback")
	})
	if txErr == nil || txErr.Error() != "force rollback" {
		t.Fatalf("unit must fail with the injected error, got %v", txErr)
	}

	// (b) Rollback restored every mutated field, including deep state.
	restored := canonical()
	if restored.Status != domain.OutboxDead ||
		restored.Generation != 1 ||
		restored.Attempts != 1 ||
		!bytes.Equal(restored.Envelope, originalEnvelope) ||
		restored.DeadAt == nil || !restored.DeadAt.Equal(deathAt) ||
		restored.AvailableAt != publishedAt ||
		restored.LeasedBy != "" || restored.LeaseUntil != nil {
		t.Fatalf("rollback must restore status/generation/timestamps/envelope exactly: %+v", restored)
	}
	if string(restored.Envelope) == "" || restored.Envelope[0] == 'X' {
		t.Fatalf("in-unit envelope corruption must not leak past rollback: %q", restored.Envelope)
	}

	// (c) A normal mutation after the rollback must act on the same canonical
	// row and stay consistent across every view. Discard resolves via the ID
	// index; Stats/List/Lease read the slice; dedup reads the dedup index.
	result, err := repos.Outbox.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           tenant,
		MessageID:          id,
		ExpectedGeneration: 1,
		Actor:              domain.Actor{Type: domain.PrincipalHuman, ID: "prn_operator"},
	})
	if err != nil {
		t.Fatalf("discard after rollback: %v", err)
	}
	if result.Generation != 2 || result.AttemptsBefore != 1 || result.Action != "discard" {
		t.Fatalf("discard result must carry the post-op epoch: %+v", result)
	}

	final := canonical()
	if final.Status != domain.OutboxDiscarded || final.Generation != 2 || final.DiscardedAt == nil {
		t.Fatalf("discard must land on the canonical row: %+v", final)
	}

	stats, err := repos.Outbox.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Dead != 0 || stats.Discarded != 1 || stats.Pending+stats.Leased+stats.Delivered != 0 {
		t.Fatalf("stats must observe the same row the discard hit: %+v", stats)
	}

	page, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
		TenantID: tenant, Limit: ports.MaxDeadLetterPageSize,
	})
	if err != nil || len(page) != 0 {
		t.Fatalf("discarded row must leave the dead listing: %d %v", len(page), err)
	}

	got, err := repos.Outbox.GetDeadLetter(ctx, tenant, id)
	if err != nil || got.Status != domain.OutboxDiscarded || got.Generation != 2 || got.Cause != "sink unavailable" {
		t.Fatalf("ID-indexed read must see the terminal decision: %+v %v", got, err)
	}

	// Dedup uniqueness still holds on the canonical row: replaying publish is
	// an idempotent no-op that forks nothing.
	if err := repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: "obx_evt_replay", DedupKey: dedup, TenantID: tenant,
		Envelope: []byte("replay"), MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if len(st.outbox) != 1 {
		t.Fatalf("dedup replay must not enqueue a second row: %d", len(st.outbox))
	}
	canonical()

	batch, err = repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 0 {
		t.Fatalf("terminal history must never be claimable: %d %v", len(batch), err)
	}
}
