package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/ports"
)

// retentionSeedDelivered drives one message to `delivered` along production's
// own path (publish → lease → ack); the lease claims exactly the row just
// published when it is the only due message.
func retentionSeedDelivered(t *testing.T, ctx context.Context, repos ports.Repositories, id string) {
	t.Helper()
	if err := repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id,
		TenantID: "ten_retentionpgtenant00", Envelope: []byte(`{}`), MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Hour, Limit: 1,
	})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("lease %s: %d %v", id, len(batch), err)
	}
	if err := repos.Outbox.MarkDelivered(ctx, id, "w1"); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionNeverDeletesRowsWithoutTerminalTimestamp proves the ADR-0016
// fail-safe: a delivered row that cannot prove when it became terminal
// survives every sweep, however old its creation. The state is only
// reachable through raw SQL (both adapters always stamp terminal times), so
// this check is PostgreSQL-specific by construction.
func TestRetentionNeverDeletesRowsWithoutTerminalTimestamp(t *testing.T) {
	w := newPGWorld(t)
	ctx := context.Background()

	legacyID := "obx_evt_retlegacy"
	if _, err := w.Pool.ExecContext(ctx,
		`INSERT INTO outbox (id, dedup_key, tenant_id, envelope, status, attempts, max_attempts, available_at, created_at)
		 VALUES ($1, 'event:retlegacy', 'ten_retentionpgtenant00', '{}'::jsonb, 'delivered', 1, 3, now(), now() - interval '30 days')`,
		legacyID); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	res, err := w.Repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: time.Hour, Limit: 100,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.DeletedDelivered != 0 {
		t.Fatalf("NULL delivered_at must never be eligible: %+v", res)
	}
	var n int
	if err := w.Pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox WHERE id = $1`, legacyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("legacy delivered row must survive sweeps")
	}
}

// TestRetentionOldestFirstVictimOrder pins the within-class deletion order:
// with a one-victim budget the store must consume rows oldest-terminal-first
// — the observable contract behind deterministic (terminal_at, id) ordering.
func TestRetentionOldestFirstVictimOrder(t *testing.T) {
	w := newPGWorld(t)
	ctx := context.Background()
	repos := w.Repos

	for _, id := range []string{"obx_evt_retorder_a", "obx_evt_retorder_b", "obx_evt_retorder_c"} {
		retentionSeedDelivered(t, ctx, repos, id)
		w.Advance(time.Hour)
	}

	req := ports.RetentionSweepRequest{DeliveredOlderThan: time.Minute, Limit: 1}
	wantGone := []string{"obx_evt_retorder_a", "obx_evt_retorder_b"}
	for i, oldest := range wantGone {
		res, err := repos.Outbox.SweepRetention(ctx, req)
		if err != nil || res.DeletedDelivered != 1 {
			t.Fatalf("round %d: %+v %v", i, res, err)
		}
		var survivor string
		if err := w.Pool.QueryRowContext(ctx,
			`SELECT id FROM outbox WHERE status = 'delivered'`).Scan(&survivor); err != nil {
			t.Fatalf("round %d: read survivor: %v", i, err)
		}
		if survivor == oldest {
			t.Fatalf("round %d deleted out of order: oldest row %q still present", i, oldest)
		}
	}
}

// TestRetentionIndexesExist pins migration 0008's partial indexes so bounded
// rounds keep their index-supported scans as the table grows.
func TestRetentionIndexesExist(t *testing.T) {
	w := newPGWorld(t)
	for _, name := range []string{"outbox_retention_delivered_idx", "outbox_retention_discarded_idx"} {
		var n int
		if err := w.Pool.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'outbox' AND indexname = $1`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s must exist after migrations", name)
		}
	}
}
