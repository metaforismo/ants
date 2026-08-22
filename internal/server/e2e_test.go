package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/server"
)

type env struct {
	t        *testing.T
	baseURL  string
	tenantAS string // slug of primary tenant
	tenantBS string // slug of isolation-check tenant
}

var suffixMu sync.Mutex
var suffixCount int

func uniqueSuffix() string {
	suffixMu.Lock()
	defer suffixMu.Unlock()
	suffixCount++
	return fmt.Sprintf("%d-%d", suffixCount, time.Now().UnixNano()%1_000_000)
}

const testPrincipal = "prn_e2etestprincipal00000"

func buildServer(t *testing.T, cfg config.Config, application *app.App) *httptest.Server {
	t.Helper()
	srv, err := server.New(server.Deps{
		Config: cfg,
		Repos:  application.Repos,
		Engine: application.Engine,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func buildApp(t *testing.T, cfg config.Config) *app.App {
	t.Helper()
	// E2E tests use the fully scripted drivers so runs are deterministic
	// and fast; the real process/git path is covered by the demo and the
	// orchestration integration cases.
	cfg.Sandbox.Driver = config.SandboxDriverFake
	cfg.SCM.Driver = config.SCMDriverMemory
	application, err := app.Build(cfg, io.Discard)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	fake, ok := application.Sandbox.(*sandbox.FakeDriver)
	if !ok {
		t.Fatalf("expected fake driver in default wiring")
	}
	if err := fixtures.ScriptFake(fake); err != nil {
		t.Fatalf("script fake driver: %v", err)
	}
	return application
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.DevHeaderAuth = true
	application := buildApp(t, cfg)
	ts := buildServer(t, cfg, application)

	e := &env{t: t, baseURL: ts.URL}
	var created map[string]any
	e.doJSON(t, http.MethodPost, "/v1/tenants", "",
		map[string]any{"slug": "acme-" + uniqueSuffix(), "name": "Acme"}, &created, 0)
	e.tenantAS = created["slug"].(string)

	e.doJSON(t, http.MethodPost, "/v1/tenants", "",
		map[string]any{"slug": "other-" + uniqueSuffix(), "name": "Other"}, &created, 0)
	e.tenantBS = created["slug"].(string)
	return e
}

func (e *env) headers(tenantSlug string) map[string]string {
	return map[string]string{
		"X-Ants-Dev-Tenant":    tenantSlug,
		"X-Ants-Dev-Principal": testPrincipal,
		"Content-Type":         "application/json",
	}
}

// do performs a request and returns status, headers and raw body.
func (e *env) do(method, path string, headers map[string]string, body string) (int, http.Header, []byte) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

// doJSON performs a JSON request and decodes the response into out when set.
func (e *env) doJSON(t *testing.T, method, path, tenantSlug string, in any, out any, wantStatus int) (http.Header, []byte) {
	t.Helper()
	body := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = string(b)
	}
	headers := map[string]string{"Content-Type": "application/json"}
	if tenantSlug != "" {
		headers = e.headers(tenantSlug)
	}
	status, hdr, raw := e.do(method, path, headers, body)
	if wantStatus != 0 && status != wantStatus {
		t.Fatalf("%s %s: status %d, want %d (body: %s)", method, path, status, wantStatus, truncate(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: response is not JSON (%v): %s", method, path, err, truncate(raw))
		}
	}
	return hdr, raw
}

func truncate(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}

func (e *env) seedProjectThread(tenantSlug string) (projectID, threadID string) {
	t := e.t
	slug := "calc-" + uniqueSuffix()

	var project map[string]any
	e.doJSON(t, http.MethodPost, "/v1/projects", tenantSlug, map[string]any{
		"slug":           slug,
		"name":           "Calculator",
		"default_branch": "main",
		"seed_name":      fixtures.DemoName,
	}, &project, http.StatusCreated)
	projectID = project["id"].(string)

	var thread map[string]any
	e.doJSON(t, http.MethodPost, "/v1/threads", tenantSlug, map[string]any{
		"project_id": projectID,
		"title":      "arithmetic",
	}, &thread, http.StatusCreated)
	threadID = thread["id"].(string)

	e.doJSON(t, http.MethodPost, "/v1/threads/"+threadID+"/messages", tenantSlug, map[string]any{
		"content": "implement add and multiply",
	}, &map[string]any{}, http.StatusCreated)
	return projectID, threadID
}

type runView struct {
	Run struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"run"`
	Tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Name   string `json:"name"`
	} `json:"tasks"`
}

func (e *env) waitForTerminal(t *testing.T, runID string) runView {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last runView
	for time.Now().Before(deadline) {
		status, _, raw := e.do(http.MethodGet, "/v1/runs/"+runID, e.headers(e.tenantAS), "")
		if status != http.StatusOK {
			t.Fatalf("get run: status %d body %s", status, raw)
		}
		if err := json.Unmarshal(raw, &last); err != nil {
			t.Fatal(err)
		}
		switch last.Run.Status {
		case "completed", "failed", "cancelled":
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not reach a terminal state in time; last=%+v", last)
	return last
}
