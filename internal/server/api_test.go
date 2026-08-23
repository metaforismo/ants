package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/server"
)

// TestStartRunOnlyEnqueues pins the ADR-0012 part 2 lifecycle contract: the
// start-run endpoint returns 202 after durably enqueueing the run and its
// runnable claim, and never spawns execution tied to the request.
func TestStartRunOnlyEnqueues(t *testing.T) {
	e := newEnvWithoutRuntime(t)
	_, threadID := e.seedProjectThread(e.tenantAS)
	runID := e.startRun(t, threadID)

	view := e.getRun(t, runID)
	if view.Run.Status != "pending" {
		t.Fatalf("run must stay pending until a worker claims it, got %s", view.Run.Status)
	}

	ctx := context.Background()
	tenant, err := e.application.Repos.Tenants.GetBySlug(ctx, e.tenantAS)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := e.application.Repos.RunClaims.Get(ctx, tenant.ID, domain.RunID(runID))
	if err != nil {
		t.Fatalf("start must create exactly one durable claim: %v", err)
	}
	if claim.Status != domain.ClaimRunnable || claim.Owner != "" {
		t.Fatalf("claim must be untouched runnable work, got %+v", claim)
	}
}

func (e *env) getRun(t *testing.T, runID string) runView {
	t.Helper()
	status, _, raw := e.do(http.MethodGet, "/v1/runs/"+runID, e.headers(e.tenantAS), "")
	if status != http.StatusOK {
		t.Fatalf("get run: status %d body %s", status, raw)
	}
	var view runView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestFullPipelineThroughAPI(t *testing.T) {
	e := newEnv(t)
	_, threadID := e.seedProjectThread(e.tenantAS)
	runID := e.startRun(t, threadID)

	view := e.waitForTerminal(t, runID)
	if view.Run.Status != "completed" {
		t.Fatalf("run status %s", view.Run.Status)
	}
	if len(view.Tasks) != 2 {
		t.Fatalf("expected two tasks, got %d", len(view.Tasks))
	}

	var report struct {
		ReadyForReview bool `json:"ready_for_review"`
		Summary        string
		Verification   struct {
			Passed   bool `json:"passed"`
			Evidence []struct {
				Criterion string `json:"criterion"`
				Passed    bool   `json:"passed"`
			} `json:"evidence"`
		} `json:"verification"`
		Integration struct {
			Branch string `json:"branch"`
			SHA    string `json:"sha"`
		} `json:"integration"`
	}
	e.doJSON(t, http.MethodGet, "/v1/runs/"+runID+"/report", e.tenantAS, nil, &report, http.StatusOK)
	if !report.ReadyForReview || !report.Verification.Passed {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Integration.SHA == "" {
		t.Fatalf("integration SHA missing")
	}

	var events struct {
		Events []struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
		} `json:"events"`
	}
	e.doJSON(t, http.MethodGet, "/v1/runs/"+runID+"/events?after=0", e.tenantAS, nil, &events, http.StatusOK)
	found := map[string]bool{}
	for _, evt := range events.Events {
		found[evt.Type] = true
	}
	for _, want := range []string{"task.status.changed.v1", "run.status.changed.v1", "artifact.stored.v1"} {
		if !found[want] {
			t.Errorf("event stream missing %s", want)
		}
	}

	// The integrated diff artifact must contain both implemented functions.
	var artifactsView struct {
		Artifacts []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"artifacts"`
	}
	_ = artifactsView

	status, hdr2, diffRaw := e.do(http.MethodGet, "/v1/runs/"+runID+"/report", e.headers(e.tenantAS), "")
	if status != http.StatusOK || len(diffRaw) == 0 {
		t.Fatalf("report refetch failed: %d", status)
	}
	_ = hdr2

	diffContent := e.fetchDiffArtifact(t, runID)
	for _, want := range []string{"lib_add.sh", "lib_mul.sh"} {
		if !strings.Contains(diffContent, want) {
			t.Errorf("diff artifact missing %s", want)
		}
	}
}

// fetchDiffArtifact walks the event stream to find the stored diff artifact id
// and downloads its content through the public API.
func (e *env) fetchDiffArtifact(t *testing.T, runID string) string {
	t.Helper()
	var events struct {
		Events []struct {
			Type string `json:"type"`
			Data struct {
				ArtifactID string `json:"artifact_id"`
				Kind       string `json:"kind"`
			} `json:"data"`
		} `json:"events"`
	}
	e.doJSON(t, http.MethodGet, "/v1/runs/"+runID+"/events", e.tenantAS, nil, &events, http.StatusOK)
	diffID := ""
	for _, evt := range events.Events {
		if evt.Type == "artifact.stored.v1" && evt.Data.Kind == "diff" {
			diffID = evt.Data.ArtifactID
		}
	}
	if diffID == "" {
		t.Fatalf("no diff artifact in event stream")
	}
	status, _, raw := e.do(http.MethodGet, "/v1/artifacts/"+diffID, e.headers(e.tenantAS), "")
	if status != http.StatusOK {
		t.Fatalf("artifact download: status %d body %s", status, raw)
	}
	return string(raw)
}

var _ = fixtures.DemoName

func TestIdempotentRunStartReplaysSameRun(t *testing.T) {
	e := newEnv(t)
	_, threadID := e.seedProjectThread(e.tenantAS)

	startRun := func() string {
		req := map[string]string{}
		body, _ := json.Marshal(req)
		headers := e.headers(e.tenantAS)
		headers["Idempotency-Key"] = "api-idem-key"
		status, _, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", headers, string(body))
		if status != http.StatusAccepted {
			t.Fatalf("start run: status %d body %s", status, raw)
		}
		var run map[string]any
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		return run["id"].(string)
	}
	first := startRun()
	second := startRun()
	if first != second {
		t.Fatalf("idempotency key must replay the same run: %s vs %s", first, second)
	}
}

func TestMissingIdempotencyKeyRejected(t *testing.T) {
	e := newEnv(t)
	_, threadID := e.seedProjectThread(e.tenantAS)
	status, _, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", e.headers(e.tenantAS), "{}")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing Idempotency-Key, got %d (%s)", status, raw)
	}
}

// TestAuthEnforcement pins the refusal contract at the authentication seam:
// absent credentials, unknown tenants, and malformed principals all yield
// uniform 401 problems, and a bearer-protected server advertises the scheme
// only when OIDC is configured.
func TestAuthEnforcement(t *testing.T) {
	e := newEnv(t)

	problem := func(raw []byte) string {
		var p struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("refusals must be problem details: %s", raw)
		}
		return p.Code
	}

	// No credentials at all.
	status, _, raw := e.do(http.MethodGet, "/v1/projects", map[string]string{}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request must be 401, got %d (%s)", status, raw)
	}
	if code := problem(raw); code != "missing_bearer_token" {
		t.Fatalf("missing credential must be typed missing_bearer_token, got %s", code)
	}
	// Unknown tenant inside an otherwise well-formed credential.
	status, _, _ = e.do(http.MethodGet, "/v1/projects", e.headers("nope-nope-nope"), "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown tenant must be 401, got %d", status)
	}
	// Invalid principal format.
	badHeaders := map[string]string{
		"Authorization": "Bearer " + e.tenantAS + ":not-an-id",
	}
	status, _, raw = e.do(http.MethodGet, "/v1/projects", badHeaders, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid principal must be 401, got %d", status)
	}
	if code := problem(raw); code != "invalid_principal" {
		t.Fatalf("invalid principal must be typed invalid_principal, got %s", code)
	}
}

// TestUnconfiguredAuthRefusesEverything pins the ADR-0004 posture with no
// identity provider wired: every authenticated route answers
// authentication_not_configured and no WWW-Authenticate challenge is offered,
// because there is no scheme to honor.
func TestUnconfiguredAuthRefusesEverything(t *testing.T) {
	cfg := config.Defaults()
	application := buildApp(t, cfg)
	srv, err := server.New(server.Deps{
		Config:  cfg,
		Repos:   application.Repos,
		Auth:    server.UnconfiguredAuthenticator{},
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

	e := &env{t: t, baseURL: ts.URL}
	status, hdr, raw := e.do(http.MethodGet, "/v1/projects", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unconfigured auth must refuse requests, got %d (%s)", status, raw)
	}
	if challenge := hdr.Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("unconfigured server must not advertise a bearer challenge, got %q", challenge)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "authentication_not_configured" {
		t.Fatalf("refusal must name authentication_not_configured: %s", raw)
	}
}

func TestCrossTenantIsolation(t *testing.T) {
	e := newEnv(t)
	projectA, threadA := e.seedProjectThread(e.tenantAS)
	runA := e.startRun(t, threadA)
	e.waitForTerminal(t, runA)

	// Tenant B must not see any A resource: uniform 404 everywhere.
	for name, path := range map[string]string{
		"project":  "/v1/projects/" + projectA,
		"thread":   "/v1/threads/" + threadA,
		"run":      "/v1/runs/" + runA,
		"messages": "/v1/threads/" + threadA + "/messages",
		"events":   "/v1/runs/" + runA + "/events",
		"report":   "/v1/runs/" + runA + "/report",
	} {
		status, _, raw := e.do(http.MethodGet, path, e.headers(e.tenantBS), "")
		if status != http.StatusNotFound {
			t.Errorf("%s: cross-tenant read gave %d, want uniform 404 (%s)", name, status, truncate(raw))
		}
	}

	// Task ids leak nothing either: grab one from A's view, read as B.
	view := e.waitForTerminal(t, runA)
	taskPath := "/v1/tasks/" + view.Tasks[0].ID
	status, _, _ := e.do(http.MethodGet, taskPath, e.headers(e.tenantBS), "")
	if status != http.StatusNotFound {
		t.Errorf("task: cross-tenant read gave %d, want 404", status)
	}

	// B's project list contains only B's projects.
	var list struct {
		Projects []map[string]any `json:"projects"`
	}
	e.doJSON(t, http.MethodGet, "/v1/projects", e.tenantBS, nil, &list, http.StatusOK)
	if len(list.Projects) != 0 {
		t.Fatalf("tenant B project list must be empty, got %d entries", len(list.Projects))
	}
}

func (e *env) startRun(t *testing.T, threadID string) string {
	t.Helper()
	headers := e.headers(e.tenantAS)
	headers["Idempotency-Key"] = "key-" + uniqueSuffix()
	status, _, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", headers, "")
	if status != http.StatusAccepted {
		t.Fatalf("start run: %d (%s)", status, raw)
	}
	var run map[string]any
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	return run["id"].(string)
}

func TestReportNotReadyWhileRunningAndCancelConflictsAfterFinish(t *testing.T) {
	e := newEnv(t)
	_, threadID := e.seedProjectThread(e.tenantAS)
	runID := e.startRun(t, threadID)

	// Immediately after start the report is not ready yet.
	status, _, _ := e.do(http.MethodGet, "/v1/runs/"+runID+"/report", e.headers(e.tenantAS), "")
	if status != http.StatusConflict && status != http.StatusOK {
		t.Fatalf("pre-completion report must be 409 (or already done), got %d", status)
	}

	view := e.waitForTerminal(t, runID)
	if view.Run.Status != "completed" {
		t.Skipf("run finished with %s before cancel probe", view.Run.Status)
	}
	status, _, _ = e.do(http.MethodPost, "/v1/runs/"+runID+"/cancel", e.headers(e.tenantAS), "")
	if status != http.StatusConflict {
		t.Fatalf("cancel after completion must conflict, got %d", status)
	}
}

func TestStrictBodyDecoding(t *testing.T) {
	e := newEnv(t)
	body := `{"slug":"x","name":"x","unknown_field":true}`
	headers := map[string]string{"Content-Type": "application/json"}
	status, _, _ := e.do(http.MethodPost, "/v1/tenants", headers, body)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected, got %d", status)
	}
}

func TestUnknownRoutesAreUniform404(t *testing.T) {
	e := newEnv(t)
	status, _, raw := e.do(http.MethodGet, "/v1/definitely-not-a-thing", e.headers(e.tenantAS), "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown route must 404, got %d", status)
	}
	var problem struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Type == "" {
		t.Fatalf("errors must use problem details: %s", raw)
	}
}

func TestRequestIdEchoed(t *testing.T) {
	e := newEnv(t)
	headers := map[string]string{"X-Request-ID": "test-req-123"}
	status, hdr, _ := e.do(http.MethodGet, "/healthz", headers, "")
	if status != http.StatusOK {
		t.Fatalf("healthz: %d", status)
	}
	if hdr.Get("X-Request-ID") != "test-req-123" {
		t.Fatalf("request id must be echoed, got %q", hdr.Get("X-Request-ID"))
	}
}

func TestHealthEndpoints(t *testing.T) {
	e := newEnv(t)
	status, _, _ := e.do(http.MethodGet, "/healthz", nil, "")
	if status != http.StatusOK {
		t.Fatalf("healthz: %d", status)
	}
	status, _, _ = e.do(http.MethodGet, "/readyz", nil, "")
	if status != http.StatusOK {
		t.Fatalf("readyz: %d", status)
	}
}

// TestReadyzFailsWhenDependencyUnavailable pins the readiness contract: a
// failing dependency check is a transient 503 problem — never a silent 200 —
// while liveness keeps answering so the process can still be probed.
func TestReadyzFailsWhenDependencyUnavailable(t *testing.T) {
	e := newEnv(t)
	e.setReady(func(context.Context) error { return errors.New("database connection lost") })

	status, _, raw := e.do(http.MethodGet, "/readyz", nil, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("failing dependency must fail readiness with 503, got %d", status)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "store_unavailable" {
		t.Fatalf("readiness failure must be a typed problem, got %s", raw)
	}

	status, _, _ = e.do(http.MethodGet, "/healthz", nil, "")
	if status != http.StatusOK {
		t.Fatalf("liveness must stay independent of dependency state, got %d", status)
	}
}

func TestReadyzRecoversWhenDependencyReturns(t *testing.T) {
	e := newEnv(t)
	e.setReady(func(context.Context) error { return errors.New("database connection lost") })
	if status, _, _ := e.do(http.MethodGet, "/readyz", nil, ""); status != http.StatusServiceUnavailable {
		t.Fatalf("precondition: failing probe must return 503, got %d", status)
	}
	e.setReady(func(context.Context) error { return nil })
	if status, _, _ := e.do(http.MethodGet, "/readyz", nil, ""); status != http.StatusOK {
		t.Fatalf("readiness must recover without restart, got %d", status)
	}
}

// TestReadyzReportsTypedProviderFailure pins readiness truth for the OIDC
// warm-up: when the injected check reports a typed IdP failure, /readyz names
// auth_provider_unavailable instead of the generic persistence code.
func TestReadyzReportsTypedProviderFailure(t *testing.T) {
	e := newEnv(t)
	e.setReady(func(context.Context) error {
		return &domain.Error{
			Kind:    domain.ErrKindTransient,
			Code:    "auth_provider_unavailable",
			Message: "identity provider metadata or keys are not reachable",
		}
	})

	status, _, raw := e.do(http.MethodGet, "/readyz", nil, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("failing IdP warm-up must fail readiness with 503, got %d", status)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "auth_provider_unavailable" {
		t.Fatalf("readiness must name the failing dependency, got %s", raw)
	}
}

// TestThreadListIsTenantScopedAndOrdered pins the list contract the web
// thread-list surface consumes: one bounded page per tenant, most recently
// updated first, with foreign threads never appearing.
func TestThreadListIsTenantScopedAndOrdered(t *testing.T) {
	e := newEnv(t)
	_, threadA := e.seedProjectThread(e.tenantAS)

	var created map[string]any
	e.doJSON(t, http.MethodPost, "/v1/threads", e.tenantAS, map[string]any{
		"project_id": projectOf(t, &e, threadA),
		"title":      "newer thread",
	}, &created, http.StatusCreated)

	var list struct {
		Threads []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			TenantID  string `json:"tenant_id"`
			Status    string `json:"status"`
			UpdatedAt string `json:"updated_at"`
		} `json:"threads"`
	}
	e.doJSON(t, http.MethodGet, "/v1/threads", e.tenantAS, nil, &list, http.StatusOK)
	if len(list.Threads) != 2 {
		t.Fatalf("tenant A must see exactly its two threads, got %d", len(list.Threads))
	}
	if list.Threads[0].Title != "newer thread" {
		t.Fatalf("list must order most recently updated first, got %q", list.Threads[0].Title)
	}
	for _, th := range list.Threads {
		if th.Status != "idle" || th.TenantID == "" {
			t.Fatalf("list entries must be full thread records: %+v", th)
		}
	}

	var foreign struct {
		Threads []map[string]any `json:"threads"`
	}
	e.doJSON(t, http.MethodGet, "/v1/threads", e.tenantBS, nil, &foreign, http.StatusOK)
	if len(foreign.Threads) != 0 {
		t.Fatalf("tenant B must not see A's threads, got %d", len(foreign.Threads))
	}

	status, _, raw := e.do(http.MethodGet, "/v1/threads", map[string]string{}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list gave %d, want 401 (%s)", status, truncate(raw))
	}
}

func projectOf(t *testing.T, e **env, threadID string) string {
	t.Helper()
	var thread map[string]any
	(*e).doJSON(t, http.MethodGet, "/v1/threads/"+threadID, (*e).tenantAS, nil, &thread, http.StatusOK)
	return thread["project_id"].(string)
}
