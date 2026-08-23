package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// outboxFixture builds one application over the deterministic memory store
// with a seeded tenant and a configurable number of dead letters.
type outboxFixture struct {
	app      *app.App
	tenantID domain.TenantID
}

func newOutboxFixture(t *testing.T) *outboxFixture {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	application, err := app.Build(cfg, bytes.NewBuffer(nil))
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	f := &outboxFixture{app: application, tenantID: domain.TenantID("ten_clifixture0000000001")}
	tenant, err := domain.NewTenant(f.tenantID, "acme", "Acme", domain.PlanFree, "local", time.Now())
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := application.Repos.Tenants.Create(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return f
}

// seedDead drives one message to terminal failure through the production path.
func (f *outboxFixture) seedDead(t *testing.T, id string) ports.DeadLetterSummary {
	t.Helper()
	ctx := context.Background()
	if err := f.app.Repos.Outbox.Publish(ctx, ports.OutboxMessage{
		ID: id, DedupKey: "event:" + id, TenantID: f.tenantID,
		Envelope: []byte(`{"id":"` + id + `"}`), MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	batch, err := f.app.Repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease: %d %v", len(batch), err)
	}
	if err := f.app.Repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	got, err := f.app.Repos.Outbox.GetDeadLetter(ctx, f.tenantID, id)
	if err != nil {
		t.Fatalf("read dead letter: %v", err)
	}
	return *got
}

func (f *outboxFixture) mutateParams(id string, confirmed bool) mutateParams {
	return mutateParams{
		tenant:    f.tenantID,
		message:   id,
		actor:     "prn_operator000000000000",
		reason:    "triaged by fixture",
		asJSON:    false,
		confirmed: confirmed,
	}
}

func TestDeadLetterListHumanOutputAndCursor(t *testing.T) {
	f := newOutboxFixture(t)
	first := f.seedDead(t, "obx_evt_cli_list_first")
	second := f.seedDead(t, "obx_evt_cli_listsecond")

	var stdout, stderr bytes.Buffer
	req := ports.ListDeadLettersRequest{TenantID: f.tenantID, Limit: 1}
	if code := deadLetterList(f.app, req, false, &stdout, &stderr); code != exitOK {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 { // one row plus the next-page hint
		t.Fatalf("expected one row and a page hint, got %q", stdout.String())
	}
	if !strings.Contains(lines[0], first.ID) || strings.Contains(lines[0], second.ID) {
		t.Fatalf("first row mismatched: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "-- next page: --after ") {
		t.Fatalf("page hint missing: %q", lines[1])
	}

	token := strings.TrimPrefix(lines[1], "-- next page: --after ")
	at, id, err := decodeCursor(token)
	if err != nil || id != first.ID {
		t.Fatalf("cursor token must round-trip to the last row: %v %q", err, id)
	}
	var stdout2 bytes.Buffer
	req.AfterCreatedAt, req.AfterID = at, id
	if code := deadLetterList(f.app, req, false, &stdout2, &stderr); code != exitOK {
		t.Fatalf("second page exit = %d", code)
	}
	if !strings.Contains(stdout2.String(), second.ID) || strings.Contains(stdout2.String(), first.ID) {
		t.Fatalf("second page contents wrong: %q", stdout2.String())
	}
}

func TestDeadLetterListJSONOutputIsTyped(t *testing.T) {
	f := newOutboxFixture(t)
	seeded := f.seedDead(t, "obx_evt_cli_json_row01")

	var stdout, stderr bytes.Buffer
	req := ports.ListDeadLettersRequest{TenantID: f.tenantID, Limit: 10}
	if code := deadLetterList(f.app, req, true, &stdout, &stderr); code != exitOK {
		t.Fatalf("list exit = %d", code)
	}
	var summaries []ports.DeadLetterSummary
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var s ports.DeadLetterSummary
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("json line undecodable: %v (%q)", err, line)
		}
		summaries = append(summaries, s)
	}
	if len(summaries) != 1 || summaries[0].ID != seeded.ID || summaries[0].Generation != seeded.Generation {
		t.Fatalf("json summaries mismatch: %+v", summaries)
	}
}

func TestRequeueMutationPrintsStableLineAndAudits(t *testing.T) {
	f := newOutboxFixture(t)
	id := "obx_evt_cli_requeue001"
	seeded := f.seedDead(t, id)

	var stdout, stderr bytes.Buffer
	code := deadLetterMutate(f.app, actionRequeue, f.mutateParams(id, false), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("requeue exit = %d, stderr=%s", code, stderr.String())
	}
	wantGen := fmt.Sprintf("(generation %d,", seeded.Generation+1)
	line := stdout.String()
	if !strings.HasPrefix(line, "requeued "+id+" ") || !strings.Contains(line, wantGen) {
		t.Fatalf("unstable output line %q, want suffix %q", line, wantGen)
	}

	// Provenance landed atomically: one versioned event, its delivery, one
	// audit record.
	events, err := f.app.Repos.Events.ListByTenant(context.Background(), f.tenantID, 0, 0)
	if err != nil || len(events) != 1 || events[0].Type != domain.EventOutboxDeadLetterRequeued {
		t.Fatalf("event trail wrong: %+v %v", events, err)
	}
	audit, err := f.app.Repos.Audit.ListByTenant(context.Background(), f.tenantID, "", 10)
	if err != nil || len(audit) != 1 || audit[0].Action != domain.ActionOutboxRequeue {
		t.Fatalf("audit trail wrong: %+v %v", audit, err)
	}
	if audit[0].Metadata["reason"] != "triaged by fixture" {
		t.Fatalf("audit reason missing: %+v", audit[0].Metadata)
	}
}

func TestDiscardRefusesWithoutConfirmation(t *testing.T) {
	f := newOutboxFixture(t)
	id := "obx_evt_cli_discardrefuse"
	f.seedDead(t, id)

	var stdout, stderr bytes.Buffer
	code := deadLetterMutate(f.app, actionDiscard, f.mutateParams(id, false), &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("unconfirmed discard must exit usage, got %d", code)
	}
	if !strings.Contains(stderr.String(), "refusing to discard") ||
		!strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("refusal must explain the gate: %q", stderr.String())
	}
	got, err := f.app.Repos.Outbox.GetDeadLetter(context.Background(), f.tenantID, id)
	if err != nil || got.Status != domain.OutboxDead {
		t.Fatalf("unconfirmed discard must not mutate: %+v %v", got, err)
	}
	audit, _ := f.app.Repos.Audit.ListByTenant(context.Background(), f.tenantID, "", 10)
	if len(audit) != 0 {
		t.Fatalf("refused discard must leave no audit record: %+v", audit)
	}
}

func TestConfirmedDiscardIsTerminalAndRepeatDetectable(t *testing.T) {
	f := newOutboxFixture(t)
	id := "obx_evt_cli_discardconfirm"
	f.seedDead(t, id)

	var stdout, stderr bytes.Buffer
	if code := deadLetterMutate(f.app, actionDiscard, f.mutateParams(id, true), &stdout, &stderr); code != exitOK {
		t.Fatalf("confirmed discard exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "discarded "+id+" ") {
		t.Fatalf("discard output unstable: %q", stdout.String())
	}

	// A replay with the stale credential is a typed conflict, never a silent
	// double decision.
	params := f.mutateParams(id, true)
	params.message = id
	code := deadLetterMutate(f.app, actionDiscard, params, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("repeat discard must fail, got %d", code)
	}
	// After the terminal decision the row left the dead view entirely.
	if !strings.Contains(stderr.String(), "error:") {
		t.Fatalf("repeat discard must render a typed error: %q", stderr.String())
	}
}

func TestUnknownMessageRendersTypedErrorTriple(t *testing.T) {
	f := newOutboxFixture(t)

	var stdout, stderr bytes.Buffer
	code := deadLetterMutate(f.app, actionRequeue,
		f.mutateParams("obx_evt_cli_missingrow01", false), &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("unknown message must fail, got %d", code)
	}
	const wantPrefix = "error: not_found: outbox_message_not_found:"
	if !strings.HasPrefix(stderr.String(), wantPrefix) {
		t.Fatalf("typed error triple missing: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("failures must keep stdout clean: %q", stdout.String())
	}
}

func TestCursorRoundTripAndInvalidToken(t *testing.T) {
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	token := encodeCursor(at, "obx_evt_x")
	gotAt, gotID, err := decodeCursor(token)
	if err != nil || gotID != "obx_evt_x" || !gotAt.Equal(at) {
		t.Fatalf("cursor round trip broken: %v %q %q", err, gotID, gotAt)
	}
	if _, _, err := decodeCursor("%%%not-base64%%%"); err == nil {
		t.Fatalf("garbage token must be rejected")
	}
}

// TestFlagsAreAcceptedOnBothSidesOfThePositional pins the operator-facing
// argument grammar: stdlib flag parsing stops at the first positional, so
// `requeue <id> --reason R` would otherwise die with a usage error even
// though the usage text and the runbook show exactly that shape.
func TestFlagsAreAcceptedOnBothSidesOfThePositional(t *testing.T) {
	fs := flag.NewFlagSet("outbox dead-letter requeue", flag.ContinueOnError)
	tenant := fs.String("tenant", "", "")
	actor := fs.String("actor", "", "")
	reason := fs.String("reason", "", "")
	traceID := fs.String("trace-id", "", "")
	confirmed := fs.Bool("yes", false, "")

	args := []string{
		"obx_evt_msgid001",
		"--reason", "flags after the id",
		"-tenant", "ten_regressionsuite000001",
		"--actor=prn_afterpositional01",
		"--yes",
		"--trace-id", "tr-1",
	}
	flagArgs, positional := reorderPositionalsLast(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(positional) != 1 || positional[0] != "obx_evt_msgid001" {
		t.Fatalf("exactly one positional expected, got %q", positional)
	}
	if *reason != "flags after the id" || *tenant != "ten_regressionsuite000001" ||
		*actor != "prn_afterpositional01" || !*confirmed || *traceID != "tr-1" {
		t.Fatalf("flag values lost in reorder: %q %q %q %v %q",
			*tenant, *actor, *reason, *confirmed, *traceID)
	}
}
