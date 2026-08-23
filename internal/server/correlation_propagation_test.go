package server_test

// Correlation propagation proofs (ADR-0018): the effective request
// identifier resolved by the middleware is the one echoed in the response,
// written to the request log, and persisted in the trace_id slot of every
// event committed while serving the request. Work performed outside any
// request (run execution by the worker) keeps empty trace ids — no
// fabricated HTTP identities.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// tenantEvents returns the durable events persisted for a tenant slug.
func tenantEvents(t *testing.T, e *env, slug string) []*domain.Event {
	t.Helper()
	ctx := context.Background()
	tenant, err := e.application.Repos.Tenants.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("load tenant %s: %v", slug, err)
	}
	events, err := e.application.Repos.Events.ListByTenant(ctx, tenant.ID, 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return events
}

// createTenantWithCorrelation POSTs /v1/tenants with an explicit X-Request-ID
// (empty means none) and returns status plus the response's effective id.
func createTenantWithCorrelation(e *env, slug, requestID string) (int, string) {
	e.t.Helper()
	headers := map[string]string{"Content-Type": "application/json"}
	if requestID != "" {
		headers["X-Request-ID"] = requestID
	}
	status, hdr, _ := e.do(http.MethodPost, "/v1/tenants", headers,
		fmt.Sprintf(`{"slug":%q,"name":"Correlation"}`, slug))
	return status, hdr.Get("X-Request-ID")
}

// devHeaders builds authenticated headers carrying an optional correlation id.
func devHeaders(tenantSlug, requestID string) map[string]string {
	h := map[string]string{
		"X-Ants-Dev-Tenant":    tenantSlug,
		"X-Ants-Dev-Principal": testPrincipal,
		"Content-Type":         "application/json",
	}
	if requestID != "" {
		h["X-Request-ID"] = requestID
	}
	return h
}

// TestCorrelationFlowsIntoPersistedEvents pins the three-way equality —
// response header == request-log id == persisted event trace_id — for
// generated, accepted-external, and rejected-malformed inbound identifiers,
// including truthful correlation-source logging.
func TestCorrelationFlowsIntoPersistedEvents(t *testing.T) {
	e, capture := loggedEnv(t)
	suffix := uniqueSuffix()

	cases := []struct {
		name       string
		inbound    string
		wantSource string
	}{
		{"generated", "", "generated"},
		{"external-verbatim", "3f2504e0-4f89-11d3-9a0c-" + suffix, "header"},
		{"malformed-replaced", "bad " + suffix, "generated"},
	}
	for _, tc := range cases {
		slug := fmt.Sprintf("corr-%s-%s", tc.name, suffix)
		status, got := createTenantWithCorrelation(e, slug, tc.inbound)
		if status != http.StatusCreated {
			t.Fatalf("%s: tenant creation failed: %d", tc.name, status)
		}
		switch tc.wantSource {
		case "header":
			if got != tc.inbound {
				t.Fatalf("%s: external id must be echoed verbatim, got %q", tc.name, got)
			}
		case "generated":
			if !strings.HasPrefix(got, "req_") || got == tc.inbound {
				t.Fatalf("%s: absent/malformed id must yield a fresh req_ id, got %q", tc.name, got)
			}
		}

		capture.snapshot()
		logged := 0
		for _, m := range capture.httpRequests() {
			if m["request_id"] == got {
				logged++
				if m["correlation_source"] != tc.wantSource {
					t.Errorf("%s: log source %v, want %s", tc.name, m["correlation_source"], tc.wantSource)
				}
			}
		}
		if logged != 1 {
			t.Fatalf("%s: request id %q logged %d times, want exactly 1", tc.name, got, logged)
		}

		events := tenantEvents(t, e, slug)
		if len(events) != 1 || events[0].Type != domain.EventTenantCreated {
			t.Fatalf("%s: expected exactly one tenant-created event, got %+v", tc.name, events)
		}
		if events[0].TraceID != got {
			t.Errorf("%s: event trace_id %q must equal the effective request id %q", tc.name, events[0].TraceID, got)
		}
		if tc.wantSource == "generated" && strings.Contains(events[0].TraceID, " ") {
			t.Error("malformed bytes must never reach a durable record")
		}
	}
}

// TestStartRunCarriesCorrelationIntoTransitionEvent proves the engine seam:
// the synchronous idle→planning transition emitted by StartRun over HTTP
// persists the run request's correlation id, and no sibling event picks up
// a stranger's identity.
func TestStartRunCarriesCorrelationIntoTransitionEvent(t *testing.T) {
	e, capture := loggedEnv(t)

	slug := "corr-run-" + uniqueSuffix()
	createTenantWithCorrelation(e, slug, "")
	_, threadID := e.seedProjectThread(slug)

	runReqID := "run-start." + uniqueSuffix()
	startHeaders := devHeaders(slug, runReqID)
	startHeaders["Idempotency-Key"] = "key-" + uniqueSuffix()
	status, hdr, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", startHeaders, "")
	if status != http.StatusAccepted {
		t.Fatalf("start run: %d (%s)", status, truncate(raw))
	}
	if hdr.Get("X-Request-ID") != runReqID {
		t.Fatalf("run start must echo its correlation id, got %q", hdr.Get("X-Request-ID"))
	}

	var planning *domain.Event
	for _, evt := range tenantEvents(t, e, slug) {
		if evt.Type == domain.EventThreadStatusChanged && evt.Data["to"] == string(domain.ThreadPlanning) {
			planning = evt
		}
		if evt.TraceID == runReqID && evt != planning {
			t.Errorf("event %s (%s) claims the run-start correlation, but only the transition may", evt.ID, evt.Type)
		}
	}
	if planning == nil {
		t.Fatal("no planning transition persisted")
	}
	if planning.TraceID != runReqID {
		t.Errorf("planning transition trace_id %q must equal the run-start correlation %q",
			planning.TraceID, runReqID)
	}

	capture.snapshot()
	found := false
	for _, m := range capture.httpRequests() {
		if m["request_id"] == runReqID && m["route"] == "/v1/threads/{id}/runs" {
			found = true
		}
	}
	if !found {
		t.Error("run-start correlation id never reached the request log")
	}
}

// TestConcurrentRequestsKeepEventCorrelationsDistinct hammers concurrent
// creations and proves correlations never cross between requests or rows.
func TestConcurrentRequestsKeepEventCorrelationsDistinct(t *testing.T) {
	e, _ := loggedEnv(t)

	const workers = 12
	type outcome struct {
		slug, echo string
	}
	results := make([]outcome, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("concurrent-%d-%s", w, uniqueSuffix())
			slug := fmt.Sprintf("conc-%d-%s", w, uniqueSuffix())
			status, echo := createTenantWithCorrelation(e, slug, id)
			if status != http.StatusCreated {
				errs <- fmt.Errorf("worker %d: status %d", w, status)
				return
			}
			if echo != id {
				errs <- fmt.Errorf("worker %d: echoed %q", w, echo)
				return
			}
			results[w] = outcome{slug: slug, echo: echo}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	seen := map[string]string{} // correlation -> owning slug
	for _, r := range results {
		events := tenantEvents(t, e, r.slug)
		if len(events) != 1 {
			t.Fatalf("tenant %s: expected 1 event, got %d", r.slug, len(events))
		}
		if events[0].TraceID != r.echo {
			t.Errorf("tenant %s: trace_id %q, want own request id %q", r.slug, events[0].TraceID, r.echo)
		}
		if other, clash := seen[r.echo]; clash {
			t.Errorf("correlation %q shared between %s and %s", r.echo, other, r.slug)
		}
		seen[r.echo] = r.slug
	}
}

// TestFailedEventUnitLeavesNoMismatchedCorrelation proves transaction
// behavior: a rolled-back unit leaves no orphan records under any identity,
// and the retrying successful request stamps its OWN correlation on the one
// surviving event.
func TestFailedEventUnitLeavesNoMismatchedCorrelation(t *testing.T) {
	flaky := &flakyEvents{}
	ts, application := auditServer(t, func(r *ports.Repositories) {
		flaky.EventLog = r.Events
		r.Events = flaky
	})
	ctx := context.Background()
	slug := "corr-fail-" + uniqueSuffix()
	e := &env{t: t, baseURL: ts.URL}

	failedAttempt := "failed-attempt." + uniqueSuffix()
	flaky.setFail(true)
	if status, _ := createTenantWithCorrelation(e, slug, failedAttempt); status != http.StatusServiceUnavailable {
		t.Fatalf("scripted outage must surface as 503, got %d", status)
	}
	if _, err := application.Repos.Tenants.GetBySlug(ctx, slug); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back unit must leave no tenant row, got %v", err)
	}
	stats, serr := application.Repos.Outbox.Stats(ctx)
	if serr != nil || stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 0 {
		t.Fatalf("rolled-back unit must enqueue no delivery: %+v %v", stats, serr)
	}

	successAttempt := "success-attempt." + uniqueSuffix()
	flaky.setFail(false)
	if status, _ := createTenantWithCorrelation(e, slug, successAttempt); status != http.StatusCreated {
		t.Fatalf("recovered creation must succeed, got %d", status)
	}
	tenant, terr := application.Repos.Tenants.GetBySlug(ctx, slug)
	if terr != nil {
		t.Fatal(terr)
	}
	persisted, perr := application.Repos.Events.ListByTenant(ctx, tenant.ID, 0, 0)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(persisted) != 1 {
		t.Fatalf("exactly one event after recovery, got %d", len(persisted))
	}
	if persisted[0].TraceID != successAttempt {
		t.Errorf("surviving event must carry the successful attempt's correlation %q, got %q",
			successAttempt, persisted[0].TraceID)
	}
	stats, serr = application.Repos.Outbox.Stats(ctx)
	if serr != nil || stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 1 {
		t.Fatalf("exactly one delivery joins the committed unit: %+v %v", stats, serr)
	}
}

// TestAuthDenialCarriesCorrelationWithoutDurableRecords proves the denial
// path observes the same identity in header and log while persisting no
// event or audit record under that identity.
func TestAuthDenialCarriesCorrelationWithoutDurableRecords(t *testing.T) {
	e, capture := loggedEnv(t)

	slug := "corr-denied-" + uniqueSuffix()
	if status, _ := createTenantWithCorrelation(e, slug, ""); status != http.StatusCreated {
		t.Fatal("precondition: tenant creation failed")
	}
	before := tenantEvents(t, e, slug)

	deniedID := "denied." + uniqueSuffix()
	status, hdr, _ := e.do(http.MethodGet, "/v1/projects",
		map[string]string{"X-Request-ID": deniedID}, "") // no dev-auth headers → 401
	if status != http.StatusUnauthorized {
		t.Fatalf("expected auth denial, got %d", status)
	}
	if hdr.Get("X-Request-ID") != deniedID {
		t.Errorf("denial response must still echo the correlation id, got %q", hdr.Get("X-Request-ID"))
	}

	capture.snapshot()
	found := false
	for _, m := range capture.httpRequests() {
		if m["request_id"] == deniedID {
			found = true
			if num(t, m, "status") != http.StatusUnauthorized {
				t.Errorf("denial record status mismatch: %v", m["status"])
			}
		}
	}
	if !found {
		t.Error("denied request never logged under its correlation id")
	}

	after := tenantEvents(t, e, slug)
	if len(after) != len(before) {
		t.Errorf("auth denial must persist no events: before=%d after=%d", len(before), len(after))
	}
	tenant, terr := e.application.Repos.Tenants.GetBySlug(context.Background(), slug)
	if terr != nil {
		t.Fatal(terr)
	}
	audit, aerr := e.application.Repos.Audit.ListByTenant(context.Background(), tenant.ID, "", 100)
	if aerr != nil || len(audit) != 0 {
		t.Errorf("auth denial must persist no audit records: %+v %v", audit, aerr)
	}
}

// TestCrossTenantEventListsNeverShareCorrelations pins ADR-0004 alongside
// correlation: each tenant's durable history contains only correlations of
// requests served on its behalf; foreign identities are invisible.
func TestCrossTenantEventListsNeverShareCorrelations(t *testing.T) {
	e, _ := loggedEnv(t)

	idA := "tenant-a." + uniqueSuffix()
	idB := "tenant-b." + uniqueSuffix()
	slugA := "corra-" + uniqueSuffix()
	slugB := "corrb-" + uniqueSuffix()
	if status, echo := createTenantWithCorrelation(e, slugA, idA); status != http.StatusCreated || echo != idA {
		t.Fatalf("tenant A creation: %d %q", status, echo)
	}
	if status, echo := createTenantWithCorrelation(e, slugB, idB); status != http.StatusCreated || echo != idB {
		t.Fatalf("tenant B creation: %d %q", status, echo)
	}

	eventsA := tenantEvents(t, e, slugA)
	eventsB := tenantEvents(t, e, slugB)
	if len(eventsA) != 1 || len(eventsB) != 1 {
		t.Fatalf("each tenant holds exactly one event, got %d and %d", len(eventsA), len(eventsB))
	}
	if eventsA[0].TraceID != idA || eventsB[0].TraceID != idB {
		t.Errorf("cross-tenant leak: A=%q B=%q", eventsA[0].TraceID, eventsB[0].TraceID)
	}
}

// TestWorkerExecutionKeepsNoRequestIdentity runs the full pipeline with the
// real runtime and proves the boundary precisely: only the StartRun
// request-scoped emission (the planning transition) carries the request's
// correlation id — every event written later by the worker stays
// unattributed instead of inheriting the long-finished request's identity.
func TestWorkerExecutionKeepsNoRequestIdentity(t *testing.T) {
	e := newEnv(t)
	_, threadID := e.seedProjectThread(e.tenantAS)

	runReqID := "worker-isolation." + uniqueSuffix()
	headers := devHeaders(e.tenantAS, runReqID)
	headers["Idempotency-Key"] = "key-" + uniqueSuffix()
	status, hdr, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", headers, "")
	if status != http.StatusAccepted {
		t.Fatalf("start run: %d (%s)", status, truncate(raw))
	}
	if hdr.Get("X-Request-ID") != runReqID {
		t.Fatalf("run start must echo its correlation id, got %q", hdr.Get("X-Request-ID"))
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.ID == "" {
		t.Fatalf("run response undecodable (%v): %s", err, truncate(raw))
	}

	view := e.waitForTerminal(t, started.ID)
	if view.Run.Status != "completed" {
		t.Fatalf("precondition: deterministic pipeline must complete, got %s", view.Run.Status)
	}

	ctx := context.Background()
	tenant, terr := e.application.Repos.Tenants.GetBySlug(ctx, e.tenantAS)
	if terr != nil {
		t.Fatal(terr)
	}
	runEvents, rerr := e.application.Repos.Events.ListByRun(ctx, tenant.ID, domain.RunID(started.ID), 0, 0)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(runEvents) == 0 {
		t.Fatal("execution produced no run-scoped events")
	}
	for _, evt := range runEvents {
		if evt.TraceID != "" {
			t.Errorf("worker-phase event %s (%s) must keep an empty trace id, got %q",
				evt.ID, evt.Type, evt.TraceID)
		}
	}

	allEvents, aerr := e.application.Repos.Events.ListByTenant(ctx, tenant.ID, 0, 0)
	if aerr != nil {
		t.Fatal(aerr)
	}
	// Occurrences per trace, not a deduplicated map: an identity stamped
	// onto more than one event is exactly the leak this test exists to catch,
	// so every correlated trace must account for exactly one event.
	correlated := map[string][]domain.EventType{} // trace -> types carrying it
	for _, evt := range allEvents {
		if evt.TraceID != "" {
			correlated[evt.TraceID] = append(correlated[evt.TraceID], evt.Type)
		}
	}
	if got := correlated[runReqID]; len(got) != 1 || got[0] != domain.EventThreadStatusChanged {
		t.Errorf("exactly the planning transition must carry the run-start correlation, got %v", got)
	}
	for trace, types := range correlated {
		if trace == runReqID {
			continue // asserted above: the one request-scoped emission
		}
		// Every other correlation belongs to a tenant-created event of this
		// env's own setup requests — served requests too, just not this one.
		if len(types) != 1 || types[0] != domain.EventTenantCreated {
			t.Errorf("unexpected correlated event outside any served request: %s -> %v", trace, types)
		}
	}
}
