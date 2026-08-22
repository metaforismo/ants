package metrics

import (
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/ports"
)

// wantNames lists every instrument ADR-0014 promises, including the runtime
// collectors operators expect from every Prometheus target.
var wantNames = []string{
	"ants_http_requests_total",
	"ants_http_request_duration_seconds",
	"ants_http_requests_in_flight",
	"ants_outbox_messages",
	"ants_outbox_dispatch_rounds_total",
	"ants_outbox_messages_leased_total",
	"ants_outbox_messages_delivered_total",
	"ants_outbox_messages_retry_scheduled_total",
	"ants_outbox_messages_dead_lettered_total",
	"ants_worker_claims_acquired_total",
	"ants_worker_runs_finished_total",
	"ants_worker_runs_converged_total",
	"go_goroutines",
	"process_cpu_seconds_total",
}

// TestRegistryExposesPromisedInstruments exercises every instrument once
// (vec children only appear after first use) and pins the full promised
// exposition, including the bounded label vocabularies.
func TestRegistryExposesPromisedInstruments(t *testing.T) {
	m := New()
	m.HTTPInFlightInc()
	m.HTTPInFlightDec()
	m.HTTPObserveRequest("POST", "/v1/tenants", 201, 12*time.Millisecond)
	m.RoundLeased(3)
	m.OutboxStates(ports.OutboxStats{Pending: 1, Leased: 2, Delivered: 5, Dead: 7})
	m.Delivered()
	m.RetryScheduled()
	m.DeadLettered()
	m.ClaimsAcquired(4)
	m.RunFinished("completed")
	m.RunConverged("exhausted")

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	labels := map[string]bool{}
	for _, mf := range families {
		names[mf.GetName()] = true
		for _, mm := range mf.GetMetric() {
			for _, lp := range mm.GetLabel() {
				labels[lp.GetName()+"="+lp.GetValue()] = true
			}
		}
	}
	for _, want := range wantNames {
		if !names[want] {
			t.Errorf("instrument %q missing from the exposition", want)
		}
	}
	for _, want := range []string{
		"method=POST", "route=/v1/tenants", "status=201",
		"state=pending", "state=dead",
		"outcome=completed", "kind=exhausted",
	} {
		if !labels[want] {
			t.Errorf("exposition missing label %q", want)
		}
	}
}

// TestGaugeValuesTrackLatestSample pins gauge semantics for the outbox state
// gauges and in-flight depth: they reflect the most recent observation.
func TestGaugeValuesTrackLatestSample(t *testing.T) {
	m := New()
	m.OutboxStates(ports.OutboxStats{Dead: 9})
	m.HTTPInFlightInc()
	m.HTTPInFlightInc()

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	values := map[string]float64{}
	for _, mf := range families {
		switch mf.GetName() {
		case "ants_outbox_messages":
			for _, mm := range mf.GetMetric() {
				for _, lp := range mm.GetLabel() {
					if lp.GetName() == "state" && lp.GetValue() == "dead" {
						values["dead"] = mm.GetGauge().GetValue()
					}
				}
			}
		case "ants_http_requests_in_flight":
			values["inflight"] = mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	if values["dead"] != 9 {
		t.Fatalf("outbox dead gauge must track the latest sample, got %v", values["dead"])
	}
	if values["inflight"] != 2 {
		t.Fatalf("in-flight gauge must count active requests, got %v", values["inflight"])
	}
}

// TestRoundCounterCountsRoundsNotMessages pins the semantic split of the two
// dispatch counters: dispatch_rounds_total advances by one per round no
// matter how many messages the batch held, while messages_leased_total
// accumulates the leased-message count.
func TestRoundCounterCountsRoundsNotMessages(t *testing.T) {
	m := New()
	m.RoundLeased(3)
	m.RoundLeased(0)

	want := map[string]float64{
		"ants_outbox_dispatch_rounds_total": 2,
		"ants_outbox_messages_leased_total": 3,
	}
	got := map[string]float64{}
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			got[mf.GetName()] = mf.GetMetric()[0].GetCounter().GetValue()
		}
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %v, want %v", name, got[name], wantValue)
		}
	}
}
