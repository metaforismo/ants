package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/outboxgc"
	"github.com/metaforismo/ants/internal/ports"
)

// newRetentionFixture rewires the fixture's retention service to horizons
// that collect freshly seeded terminal rows after a short real-time wait.
// The wait is the point: eligibility is measured against the store's system
// clock by design (ADR-0016), so aging rows requires real elapsed time;
// 250ms of waiting against 100ms horizons leaves an order-of-magnitude
// margin.
func newRetentionFixture(t *testing.T) *outboxFixture {
	t.Helper()
	f := newOutboxFixture(t)
	svc, err := outboxgc.New(f.app.Repos.Outbox, f.app.Logger, outboxgc.Config{
		DeliveredAfter: 100 * time.Millisecond,
		DiscardedAfter: 100 * time.Millisecond,
		BatchSize:      500,
		Interval:       time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("build retention service: %v", err)
	}
	f.app.Retention = svc
	return f
}

func (f *outboxFixture) seedDeliveredRow(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	if err := f.app.Repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id, TenantID: f.tenantID,
		Envelope: []byte(`{}`), MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := f.app.Repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease %s: %d %v", id, len(batch), err)
	}
	if err := f.app.Repos.Outbox.MarkDelivered(ctx, id, "w1"); err != nil {
		t.Fatal(err)
	}
}

func (f *outboxFixture) seedDiscardedRow(t *testing.T, id string) {
	t.Helper()
	seeded := f.seedDead(t, id)
	if _, err := f.app.Repos.Outbox.DiscardDeadLetter(context.Background(), ports.OutboxMutationRequest{
		TenantID:           f.tenantID,
		MessageID:          id,
		ExpectedGeneration: seeded.Generation,
		Actor:              domain.Actor{Type: domain.PrincipalHuman, ID: "prn_clifixtureoperator"},
	}); err != nil {
		t.Fatalf("discard %s: %v", id, err)
	}
}

// ageRows waits out the short real-time horizons of newRetentionFixture.
func ageRows(t *testing.T) {
	t.Helper()
	time.Sleep(250 * time.Millisecond)
}

func TestRetentionPreviewReportsWithoutDeleting(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	f.seedDeliveredRow(t, "obx_evt_retprev_d")
	f.seedDiscardedRow(t, "obx_evt_retprev_c")
	ageRows(t)

	var stdout, stderr bytes.Buffer
	if code := retentionSweep(f.app, false, false, &stdout, &stderr); code != exitOK {
		t.Fatalf("preview exit = %d, stderr=%s", code, stderr.String())
	}
	line := stdout.String()
	if !strings.HasPrefix(line, "retention preview delivered=1 discarded=1 cutoff=") {
		t.Fatalf("preview must report both eligible victims: %q", line)
	}
	stats, _ := f.app.Repos.Outbox.Stats(ctx)
	if stats.Delivered != 1 || stats.Discarded != 1 {
		t.Fatalf("preview must not delete anything: %+v", stats)
	}
}

func TestRetentionIsInertByDefault(t *testing.T) {
	f := newOutboxFixture(t) // default configuration: zero horizons
	f.seedDeliveredRow(t, "obx_evt_retinert_d")

	var stdout, stderr bytes.Buffer
	if code := retentionSweep(f.app, false, false, &stdout, &stderr); code != exitOK {
		t.Fatalf("preview exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "delivered=0 discarded=0") {
		t.Fatalf("inert defaults must report nothing eligible: %q", stdout.String())
	}
	if code := retentionSweep(f.app, true, false, &stdout, &stderr); code != exitOK {
		t.Fatalf("confirmed sweep under inert defaults must still succeed: %d", code)
	}
	stats, _ := f.app.Repos.Outbox.Stats(context.Background())
	if stats.Delivered != 1 {
		t.Fatalf("inert sweeps must delete nothing: %+v", stats)
	}
}

func TestUnconfirmedSweepRefusesWithUsageExit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runOutboxRetention([]string{"sweep"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("unconfirmed sweep must exit usage, got %d", code)
	}
	if !strings.Contains(stderr.String(), "refusing to sweep") ||
		!strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("refusal must explain the gate: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("failures must keep stdout clean: %q", stdout.String())
	}
}

func TestConfirmedSweepDeletesEligibleTerminalRowsOnly(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()

	f.seedDeliveredRow(t, "obx_evt_retdel_d")
	f.seedDiscardedRow(t, "obx_evt_retdel_c")
	f.seedDead(t, "obx_evt_retdel_dead")
	ageRows(t)

	var stdout, stderr bytes.Buffer
	if code := retentionSweep(f.app, true, false, &stdout, &stderr); code != exitOK {
		t.Fatalf("confirmed sweep exit = %d, stderr=%s", code, stderr.String())
	}
	line := stdout.String()
	if !strings.Contains(line, "retention swept delivered=1 discarded=1 cutoff=") {
		t.Fatalf("sweep must report exactly the two eligible victims: %q", line)
	}

	stats, _ := f.app.Repos.Outbox.Stats(ctx)
	if stats.Dead != 1 {
		t.Fatalf("dead letters are operator work and must survive sweeps: %+v", stats)
	}
	if stats.Delivered+stats.Discarded != 0 {
		t.Fatalf("eligible terminal rows must be gone: %+v", stats)
	}
}

func TestSweepJSONOutputIsTyped(t *testing.T) {
	f := newRetentionFixture(t)

	var stdout, stderr bytes.Buffer
	if code := retentionSweep(f.app, false, true, &stdout, &stderr); code != exitOK {
		t.Fatalf("json preview exit = %d, stderr=%s", code, stderr.String())
	}
	var res ports.RetentionSweepResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("output must be a typed result object: %v (%q)", err, stdout.String())
	}
	if res.Cutoff.IsZero() {
		t.Fatalf("result must carry the store-owned cutoff: %+v", res)
	}
}
