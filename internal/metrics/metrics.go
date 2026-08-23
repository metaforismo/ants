// Package metrics owns the process-wide Prometheus instruments for the
// durable core: HTTP edge, outbox dispatcher, and run worker (ADR-0014).
// The registry is instance-owned, never the global default, so composition
// roots and tests wire exactly one collector per process and nothing leaks
// across binaries in a test run.
//
// Cardinality rule: label values come from fixed vocabularies only — HTTP
// methods, mux route patterns, response statuses, outbox states, terminal
// outcomes, convergence kinds. Tenant, run, task, and principal identifiers
// must never become labels.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/metaforismo/ants/internal/ports"
)

const namespace = "ants"

// RouteUnmatched labels requests that no pinned route claimed (the 404
// catch-all). A constant keeps the series cardinality bounded.
const RouteUnmatched = "unmatched"

// Metrics holds every instrument of the ants namespace on one registry.
type Metrics struct {
	registry *prometheus.Registry

	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	httpInFlight        prometheus.Gauge

	outboxMessages       *prometheus.GaugeVec
	outboxRounds         prometheus.Counter
	outboxMessagesLeased prometheus.Counter
	outboxDelivered      prometheus.Counter
	outboxRetryScheduled prometheus.Counter
	outboxDeadLettered   prometheus.Counter
	// outboxOperatorActions instruments the dead-letter operator surface
	// (ADR-0015); labels are the fixed action and outcome vocabularies.
	outboxOperatorActions *prometheus.CounterVec
	// outboxRetentionDeleted instruments retention/GC rounds (ADR-0016);
	// state is the fixed delivered/discarded vocabulary.
	outboxRetentionDeleted *prometheus.CounterVec
	outboxRetentionRounds  prometheus.Counter

	workerClaimsAcquired prometheus.Counter
	workerRunsFinished   *prometheus.CounterVec
	workerRunsConverged  *prometheus.CounterVec
}

// New builds the collector set and registers it, including the Go runtime
// and process collectors operators expect from every Prometheus target.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "http_requests_total",
				Help:      "Requests served, by method, route pattern, and response status.",
			},
			[]string{"method", "route", "status"},
		),
		httpRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Request handling duration, by method and route pattern.",
				Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			},
			[]string{"method", "route"},
		),
		httpInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "http_requests_in_flight",
				Help:      "Requests currently being served.",
			},
		),
		outboxMessages: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "outbox_messages",
				Help:      "Outbox messages by delivery state (pending, leased, delivered, dead, discarded), sampled after each dispatch round's lease step.",
			},
			[]string{"state"},
		),
		outboxRounds: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_dispatch_rounds_total",
				Help:      "Dispatcher rounds whose lease step succeeded, including rounds that leased an empty batch.",
			},
		),
		outboxMessagesLeased: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_messages_leased_total",
				Help:      "Messages leased across all dispatch rounds, including redeliveries.",
			},
		),
		outboxDelivered: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_messages_delivered_total",
				Help:      "Messages acknowledged as delivered.",
			},
		),
		outboxRetryScheduled: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_messages_retry_scheduled_total",
				Help:      "Failed deliveries rescheduled with backoff before exhausting attempts.",
			},
		),
		outboxDeadLettered: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_messages_dead_lettered_total",
				Help:      "Messages that exhausted their delivery budget and became terminal.",
			},
		),
		outboxOperatorActions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_operator_actions_total",
				Help:      "Dead-letter operator mutations (requeue, discard) by action and outcome.",
			},
			[]string{"action", "outcome"},
		),
		outboxRetentionDeleted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_retention_deleted_total",
				Help:      "Terminal outbox rows deleted by retention sweeps, by state (delivered, discarded).",
			},
			[]string{"state"},
		),
		outboxRetentionRounds: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "outbox_retention_rounds_total",
				Help:      "Retention sweep rounds that completed successfully, including rounds that deleted nothing.",
			},
		),
		workerClaimsAcquired: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "worker_claims_acquired_total",
				Help:      "Run claims acquired across all rounds. Every acquisition counts toward the dispatch budget (ADR-0013).",
			},
		),
		workerRunsFinished: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "worker_runs_finished_total",
				Help:      "Executions whose run reached a terminal state, by terminal outcome.",
			},
			[]string{"outcome"},
		),
		workerRunsConverged: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "worker_runs_converged_total",
				Help:      "Abandoned runs converged to a classified failure instead of executing, by convergence kind.",
			},
			[]string{"kind"},
		),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequestsTotal,
		m.httpRequestDuration,
		m.httpInFlight,
		m.outboxMessages,
		m.outboxRounds,
		m.outboxMessagesLeased,
		m.outboxDelivered,
		m.outboxRetryScheduled,
		m.outboxDeadLettered,
		m.outboxOperatorActions,
		m.outboxRetentionDeleted,
		m.outboxRetentionRounds,
		m.workerClaimsAcquired,
		m.workerRunsFinished,
		m.workerRunsConverged,
	)
	return m
}

// Registry exposes the scrape source behind /metrics.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// HTTPInFlightInc marks one request as being served; pair with InFlightDec.
func (m *Metrics) HTTPInFlightInc() { m.httpInFlight.Inc() }

// HTTPInFlightDec releases one in-flight request slot.
func (m *Metrics) HTTPInFlightDec() { m.httpInFlight.Dec() }

// HTTPObserveRequest records one completed request against its route
// pattern. The status label keeps the exact code: HTTP statuses are a small
// closed set in practice, so cardinality stays bounded without collapsing
// diagnostic detail.
func (m *Metrics) HTTPObserveRequest(method, route string, status int, d time.Duration) {
	m.httpRequestsTotal.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpRequestDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// RoundLeased records one dispatch round whose lease step succeeded and the
// number of messages it leased (outbox.Observer). Rounds count one per call
// regardless of batch size; leased messages accumulate separately.
func (m *Metrics) RoundLeased(leased int) {
	m.outboxRounds.Inc()
	m.outboxMessagesLeased.Add(float64(leased))
}

// OutboxStates refreshes the per-state message gauges from a store stats
// read (outbox.Observer).
func (m *Metrics) OutboxStates(states ports.OutboxStats) {
	m.outboxMessages.WithLabelValues("pending").Set(float64(states.Pending))
	m.outboxMessages.WithLabelValues("leased").Set(float64(states.Leased))
	m.outboxMessages.WithLabelValues("delivered").Set(float64(states.Delivered))
	m.outboxMessages.WithLabelValues("dead").Set(float64(states.Dead))
	m.outboxMessages.WithLabelValues("discarded").Set(float64(states.Discarded))
}

// Delivered records one acknowledged delivery (outbox.Observer).
func (m *Metrics) Delivered() { m.outboxDelivered.Inc() }

// RetryScheduled records one failed delivery that will retry (outbox.Observer).
func (m *Metrics) RetryScheduled() { m.outboxRetryScheduled.Inc() }

// DeadLettered records one message reaching its terminal state
// (outbox.Observer).
func (m *Metrics) DeadLettered() { m.outboxDeadLettered.Inc() }

// ClaimsAcquired records n claim acquisitions in one round (worker.Observer).
func (m *Metrics) ClaimsAcquired(n int) { m.workerClaimsAcquired.Add(float64(n)) }

// RunFinished records an execution whose run reached the given terminal
// status (worker.Observer).
func (m *Metrics) RunFinished(outcome string) {
	m.workerRunsFinished.WithLabelValues(outcome).Inc()
}

// RunConverged records an abandoned run converged instead of executed; kind
// is "interrupted" or "exhausted" (worker.Observer).
func (m *Metrics) RunConverged(kind string) {
	m.workerRunsConverged.WithLabelValues(kind).Inc()
}

// ActionRecorded records one dead-letter operator mutation outcome
// (outboxops.Observer). action and outcome are fixed vocabularies owned by
// the outboxops package; identifiers never become labels.
func (m *Metrics) ActionRecorded(action, outcome string) {
	m.outboxOperatorActions.WithLabelValues(action, outcome).Inc()
}

// Deleted records retention-sweep deletions for one fixed-vocabulary state
// (outboxgc.Observer, ADR-0016).
func (m *Metrics) Deleted(state string, n int64) {
	m.outboxRetentionDeleted.WithLabelValues(state).Add(float64(n))
}

// RoundsCompleted records one successful retention sweep round
// (outboxgc.Observer).
func (m *Metrics) RoundsCompleted() { m.outboxRetentionRounds.Inc() }
