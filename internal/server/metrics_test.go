package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/server"
)

// scrapeServer builds a server over the standard composition root with
// metrics wired exactly as production does.
func scrapeServer(t *testing.T, mutate func(*config.Config)) *httptest.Server {
	t.Helper()
	cfg := config.Defaults()
	if mutate != nil {
		mutate(&cfg)
	}
	application := buildApp(t, cfg)
	srv, err := server.New(server.Deps{
		Config:  cfg,
		Repos:   application.Repos,
		Uow:     application.Uow,
		Engine:  application.Engine,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:   application.Ready,
		Metrics: application.Metrics,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestMetricsEndpointServesBoundedSeries pins the ADR-0014 exposition
// contract: scrapes work without authentication like health probes, series
// are labeled with the pinned route pattern (never raw paths), and unmatched
// requests land on one constant label.
func TestMetricsEndpointServesBoundedSeries(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.DevHeaderAuth = true
	ts := scrapeServer(t, func(c *config.Config) { *c = cfg })

	e := &env{t: t, baseURL: ts.URL}
	e.doJSON(t, http.MethodPost, "/v1/tenants", "", map[string]any{
		"name": "metrics-tenant", "slug": "metrics-" + uniqueSuffix(),
	}, nil, http.StatusCreated)

	// One request off every pinned route so the catch-all's constant label
	// appears exactly once in the vocabulary.
	e.doJSON(t, http.MethodGet, "/definitely-not-a-route", "", nil, nil, http.StatusNotFound)

	res, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("/metrics must serve 200, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("exposition must be text/plain, got %q", ct)
	}
	for _, want := range []string{
		`ants_http_requests_total{method="POST",route="/v1/tenants",status="201"}`,
		`ants_http_requests_total{method="GET",route="unmatched",status="404"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q; body:\n%s", want, out)
		}
	}
}

// TestMetricsDisabledRemovesRoute pins that disabling metrics removes the
// route entirely: requests get the uniform not-found problem, never a stub.
func TestMetricsDisabledRemovesRoute(t *testing.T) {
	ts := scrapeServer(t, func(c *config.Config) { c.Metrics.Enabled = false })

	res, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled /metrics must be not found, got %d", res.StatusCode)
	}
}

// TestEnabledMetricsRequireCollector pins the construction guard: a config
// that promises metrics must be given a collector at wiring time.
func TestEnabledMetricsRequireCollector(t *testing.T) {
	cfg := config.Defaults()
	cfg.Metrics.Enabled = true
	application := buildApp(t, cfg)
	_, err := server.New(server.Deps{
		Config: cfg,
		Repos:  application.Repos,
		Uow:    application.Uow,
		Engine: application.Engine,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:  application.Ready,
	})
	if err == nil || !strings.Contains(err.Error(), "no collector was provided") {
		t.Fatalf("enabled metrics without collector must fail construction, got %v", err)
	}
}

// TestDisabledMetricsAllowNilCollector keeps the disabled path honest:
// composition roots that turn metrics off wire nothing.
func TestDisabledMetricsAllowNilCollector(t *testing.T) {
	cfg := config.Defaults()
	cfg.Metrics.Enabled = false
	application := buildApp(t, cfg)
	if application.Metrics != nil {
		t.Fatalf("disabled metrics must leave app.Metrics nil, got %+v", application.Metrics)
	}
	srv, err := server.New(server.Deps{
		Config: cfg,
		Repos:  application.Repos,
		Uow:    application.Uow,
		Engine: application.Engine,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:  application.Ready,
	})
	if err != nil {
		t.Fatalf("disabled metrics with nil collector must construct: %v", err)
	}
	if srv == nil {
		t.Fatal("server must build")
	}
}
