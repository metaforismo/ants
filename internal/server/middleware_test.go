package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/server"
)

// logCapture is a slog destination recording every structured record as a
// decoded map so assertions never depend on text formatting details.
type logCapture struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	entries []map[string]any
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// snapshot decodes everything written so far into entries and clears the
// buffer, keeping assertions scoped to the requests under test.
func (c *logCapture) snapshot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range strings.Split(c.buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // non-JSON noise (e.g. net/http internal logs) is ignored
		}
		c.entries = append(c.entries, m)
	}
	c.buf.Reset()
}

func (c *logCapture) httpRequests() []map[string]any {
	var out []map[string]any
	for _, m := range c.records() {
		if m["msg"] == "http_request" {
			out = append(out, m)
		}
	}
	return out
}

func (c *logCapture) records() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any{}, c.entries...)
}

func slogCaptureJSON(c *logCapture) *slog.Logger {
	return slog.New(slog.NewJSONHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("log record %v: field %q missing or not numeric", m, key)
	}
	return v
}

// loggedEnv wires the standard composition root but sends every log record
// into the capture instead of discarding them.
func loggedEnv(t *testing.T) (*env, *logCapture) {
	t.Helper()
	cfg := config.Defaults()
	application := buildApp(t, cfg)
	capture := &logCapture{}
	ready := &flippableReadiness{}
	srv, err := server.New(server.Deps{
		Config:  cfg,
		Repos:   application.Repos,
		Auth:    &fakeAuthenticator{tenants: application.Repos.Tenants},
		Uow:     application.Uow,
		Engine:  application.Engine,
		Logger:  slogCaptureJSON(capture),
		Ready:   ready.Check,
		Metrics: application.Metrics,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, baseURL: ts.URL, application: application, ready: ready}
	return e, capture
}

// TestRequestLogsAreRedactedAndNormalized pins the ADR-0017 field contract
// end to end: every sentinel secret fed through headers, query, and bodies
// is absent from the structured log, resource identifiers never appear even
// though real requests carry them, and the normalized route plus bounded
// correlation fields are present.
func TestRequestLogsAreRedactedAndNormalized(t *testing.T) {
	e, capture := loggedEnv(t)

	const (
		authSentinal   = "sentinel-bearer-token-a1b2c3"
		cookieSentinel = "sentinel-cookie-d4e5f6"
		querySentinel  = "sentinel-query-g7h8i9"
		bodySentinel   = "sentinel-body-j0k1l2"
		principalID    = "prn_sentinelprincipal000000001"
		threadRawID    = "thr_redactme0000000000000001"
	)

	headers := map[string]string{
		"Authorization": "Bearer " + authSentinal,
		"Cookie":        "session=" + cookieSentinel,
		"Content-Type":  "application/json",
	}
	body := fmt.Sprintf(`{"slug":"redact-%s","name":"%s"}`, uniqueSuffix(), bodySentinel)
	status, hdr, raw := e.do(http.MethodPost, "/v1/tenants?token="+querySentinel, headers, body)
	if status != http.StatusCreated {
		t.Fatalf("precondition: tenant creation failed: %d (%s)", status, raw)
	}
	var created struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.Slug == "" {
		t.Fatalf("precondition: tenant creation response undecodable (%v)", err)
	}
	// Authenticate the identifier-bearing probe with the sentinel principal
	// embedded in the credential, so the log must prove identifiers survive
	// real authenticated traffic without leaking.
	headers["Authorization"] = "Bearer " + created.Slug + ":" + principalID

	// A resource-identifier-bearing route: the raw thread ID must never be
	// logged even though the request really carried one.
	status, _, _ = e.do(http.MethodGet, "/v1/threads/"+threadRawID+"?trace="+querySentinel, headers, "")
	if status != http.StatusNotFound {
		t.Fatalf("precondition: unknown thread probe gave %d", status)
	}

	capture.snapshot()
	reqs := capture.httpRequests()
	if len(reqs) != 2 {
		t.Fatalf("expected two request log records, got %d:\n%s", len(reqs), capture.String())
	}

	all := capture.String()
	for _, sentinel := range []string{authSentinal, cookieSentinel, querySentinel, bodySentinel, principalID, threadRawID} {
		if strings.Contains(all, sentinel) {
			t.Errorf("log leaked sentinel %q", sentinel)
		}
	}
	for _, banned := range []string{"authorization", "cookie", "query", "remote_addr"} {
		if _, present := reqs[0][banned]; present {
			t.Errorf("log record carries forbidden field %q", banned)
		}
	}

	var create, missing map[string]any
	for _, m := range reqs {
		switch num(t, m, "status") {
		case 201:
			create = m
		case 404:
			missing = m
		}
	}
	if create == nil || missing == nil {
		t.Fatalf("records must cover 201 and 404 probes:\n%s", capture.String())
	}
	if create["route"] != "/v1/tenants" || create["method"] != http.MethodPost {
		t.Errorf("creation record must carry normalized method/route, got %v %v", create["method"], create["route"])
	}
	id, ok := create["request_id"].(string)
	if !ok || !strings.HasPrefix(id, "req_") {
		t.Errorf("generated correlation id must have req_ prefix, got %v", create["request_id"])
	} else if create["correlation_source"] != "generated" {
		t.Errorf("unsupplied correlation must be marked generated, got %v", create["correlation_source"])
	}
	num(t, create, "duration_ms")
	if create["remote_class"] != "loopback" {
		t.Errorf("httptest peer is loopback, got %v", create["remote_class"])
	}
	if missing["route"] != "/v1/threads/{id}" {
		t.Errorf("resource route must normalize to the pinned pattern, got %v", missing["route"])
	}
	if hdr.Get("X-Request-ID") == "" {
		t.Error("response must always carry the correlation header")
	}
}

// TestCorrelationIDAcceptanceGrammar pins truthful handling of inbound
// identifiers: well-formed external ids are echoed verbatim and credited to
// the header in logs; malformed or oversized values are replaced by a
// generated one and their bytes never reach responses or records.
func TestCorrelationIDAcceptanceGrammar(t *testing.T) {
	e, capture := loggedEnv(t)

	cases := []struct {
		name    string
		inbound string
		wantID  string
		wantSrc string
	}{
		{"uuid-style", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "header"},
		{"dots-colons-at", "github.run:9981@worker.v2", "github.run:9981@worker.v2", "header"},
		{"max-length", strings.Repeat("a", 128), strings.Repeat("a", 128), "header"},
		{"absent", "", "", "generated"},
		{"space", "bad id", "", "generated"},
		{"quote", `"quoted"`, "", "generated"},
		{"unicode", "идентификатор", "", "generated"},
		{"oversized", strings.Repeat("a", 129), "", "generated"},
	}
	// CR/LF and control-byte injections cannot traverse a real HTTP client
	// (transport rejects them before the server sees anything); their
	// rejection is pinned at the grammar level in middleware_internal_test.
	for _, tc := range cases {
		status, respHdr, _ := e.do(http.MethodGet, "/healthz", map[string]string{"X-Request-ID": tc.inbound}, "")
		if status != http.StatusOK {
			t.Fatalf("%s: healthz failed: %d", tc.name, status)
		}
		got := respHdr.Get("X-Request-ID")
		if tc.wantSrc == "header" {
			if got != tc.wantID {
				t.Errorf("%s: valid inbound id must be echoed verbatim, got %q", tc.name, got)
			}
			continue
		}
		if !strings.HasPrefix(got, "req_") {
			t.Errorf("%s: rejected inbound id must yield fresh req_ id, got %q", tc.name, got)
		}
		for _, banned := range []string{"\r", "\n", `"`, "\x07"} {
			if strings.Contains(got, banned) {
				t.Errorf("%s: injected byte %q reached the response header", tc.name, banned)
			}
		}
	}

	capture.snapshot()
	generated := 0
	for _, m := range capture.httpRequests() {
		id, _ := m["request_id"].(string)
		src, _ := m["correlation_source"].(string)
		switch src {
		case "header":
			if id == "" {
				t.Errorf("header-sourced record lost its id: %v", m)
			}
		case "generated":
			generated++
			if !strings.HasPrefix(id, "req_") {
				t.Errorf("generated record must carry req_ id, got %q", id)
			}
		default:
			t.Errorf("unknown correlation_source %q", src)
		}
	}
	if generated != 5 { // absent + four malformed inputs
		t.Errorf("exactly five rejections generate fresh ids, got %d", generated)
	}
}

var errReadinessBroken = errors.New("database connection lost")

// TestRequestLogLevelsFollowStatus pins the level contract: success at info,
// client errors at warn, server errors at error.
func TestRequestLogLevelsFollowStatus(t *testing.T) {
	e, capture := loggedEnv(t)

	e.do(http.MethodGet, "/healthz", nil, "")
	e.do(http.MethodGet, "/v1/projects", nil, "") // unauthenticated → 401
	e.do(http.MethodGet, "/definitely-not-a-route", nil, "")
	e.setReady(func(context.Context) error { return errReadinessBroken })
	e.do(http.MethodGet, "/readyz", nil, "") // dependency down → 503

	capture.snapshot()
	levels := map[string]string{}
	for _, m := range capture.httpRequests() {
		key := fmt.Sprintf("%v/%v", m["route"], int(num(t, m, "status")))
		levels[key] = fmt.Sprintf("%v", m["level"])
	}
	checks := map[string]string{
		"/healthz/200":     "INFO",
		"/v1/projects/401": "WARN",
		"unmatched/404":    "WARN",
		"/readyz/503":      "ERROR",
	}
	for key, wantLvl := range checks {
		got, ok := levels[key]
		if !ok {
			t.Errorf("missing log record for %s (have %v)", key, levels)
			continue
		}
		if got != wantLvl {
			t.Errorf("record %s: level %s, want %s", key, got, wantLvl)
		}
	}
}

// TestConcurrentRequestsKeepDistinctCorrelations hammers the chain from many
// goroutines and proves correlations never cross requests (run under -race
// by the gate).
func TestConcurrentRequestsKeepDistinctCorrelations(t *testing.T) {
	e, capture := loggedEnv(t)

	const workers, perWorker = 16, 12
	sent := make([]string, 0, workers*perWorker)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("load-%d-%d-%s", w, i, uniqueSuffix())
				status, hdr, _ := e.do(http.MethodGet, "/healthz", map[string]string{"X-Request-ID": id}, "")
				if status != http.StatusOK {
					errs <- fmt.Errorf("request %s: status %d", id, status)
					return
				}
				if hdr.Get("X-Request-ID") != id {
					errs <- fmt.Errorf("request %s echoed as %q", id, hdr.Get("X-Request-ID"))
					return
				}
				mu.Lock()
				sent = append(sent, id)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	capture.snapshot()
	loggedIDs := map[string]bool{}
	for _, m := range capture.httpRequests() {
		id, _ := m["request_id"].(string)
		loggedIDs[id] = true
	}
	for _, id := range sent {
		if !loggedIDs[id] {
			t.Errorf("request id %s never logged (or logged under another identity)", id)
		}
		delete(loggedIDs, id)
	}
	if len(loggedIDs) != 0 {
		t.Errorf("%d log records carry unknown request ids: %v", len(loggedIDs), loggedIDs)
	}
}

// TestMetricsCardinalityUnchangedByLogging guards the ADR-0014 posture while
// the logging layer wraps every route: the exposition keeps serving the
// pinned series shape with route patterns, never raw paths.
func TestMetricsCardinalityUnchangedByLogging(t *testing.T) {
	e, _ := loggedEnv(t)
	e.do(http.MethodGet, "/healthz", nil, "")

	res, err := http.Get(e.baseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, series := range []string{
		`ants_http_requests_total{method="GET",route="/healthz",status="200"}`,
		`ants_http_request_duration_seconds_count{method="GET",route="/healthz"}`,
	} {
		if !strings.Contains(out, series) {
			t.Errorf("exposition lost %s after logging rework", series)
		}
	}
}
