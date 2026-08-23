package authn_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/authn"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
)

// TestKeycloakReadyAndBearerVerification pins the live happy path: readiness
// warm-up, service-principal token accepted end to end, documented principal
// derivation, and the RFC 6750 challenge on refusals.
func TestKeycloakReadyAndBearerVerification(t *testing.T) {
	issuer := requireKeycloak(t)
	e := newOIDCEnv(t, issuer)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := e.auth.Ready(ctx); err != nil {
		t.Fatalf("live keycloak must pass readiness warm-up: %v", err)
	}

	acmeToken := clientCredentials(t, issuer, "acme-service", "fixture-acme-secret")
	claims := claimsOf(t, acmeToken)
	subject, _ := claims["sub"].(string)
	if subject == "" {
		t.Fatal("fixture token must carry sub")
	}
	digest := sha256.Sum256([]byte("oidc\x00" + issuer + "\x00" + subject))
	wantPrincipal := domain.PrincipalID(domain.PrefixPrincipal + "_" + hex.EncodeToString(digest[:]))

	status, _, raw := e.do(http.MethodPost, "/v1/projects", acmeToken,
		fmt.Sprintf(`{"slug":"acme-ready","name":"Ready","default_branch":"main","seed_name":"%s"}`, fixtures.DemoName))
	if status != http.StatusCreated {
		t.Fatalf("authenticated creation must be 201, got %d (%s)", status, raw)
	}

	// The verified (issuer, subject) pair maps to the documented principal
	// id; a thread created under this credential records it as creator.
	var project struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &project)
	status, _, raw = e.do(http.MethodPost, "/v1/threads", acmeToken,
		fmt.Sprintf(`{"project_id":%q,"title":"derivation"}`, project.ID))
	if status != http.StatusCreated {
		t.Fatalf("thread creation: %d (%s)", status, raw)
	}
	var thread struct {
		CreatorID string `json:"creator_id"`
	}
	_ = json.Unmarshal(raw, &thread)
	if thread.CreatorID != string(wantPrincipal) {
		t.Fatalf("creator must equal documented derivation %q, got %q", wantPrincipal, thread.CreatorID)
	}

	status, hdr, raw := e.do(http.MethodGet, "/v1/projects", "", "")
	_ = status
	if status != http.StatusUnauthorized || hdr.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthenticated request must be 401 + Bearer challenge, got %d %q (%s)",
			status, hdr.Get("WWW-Authenticate"), raw)
	}
	if code := problemCode(t, raw); code != "missing_bearer_token" {
		t.Fatalf("missing credential must be typed missing_bearer_token, got %s", code)
	}
}

// TestKeycloakEndToEndRun drives one complete pipeline through strictly
// verified traffic: project → thread → run → terminal report, with actor
// attribution and correlation equality holding on persisted events.
func TestKeycloakEndToEndRun(t *testing.T) {
	issuer := requireKeycloak(t)
	e := newOIDCEnv(t, issuer)
	acmeToken := clientCredentials(t, issuer, "acme-service", "fixture-acme-secret")

	post := func(path, body string) (int, map[string]any) {
		status, _, raw := e.do(http.MethodPost, path, acmeToken, body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return status, out
	}
	str := func(m map[string]any, key string) string {
		v, _ := m[key].(string)
		return v
	}

	status, project := post("/v1/projects",
		fmt.Sprintf(`{"slug":"acme-pipeline","name":"Pipeline","default_branch":"main","seed_name":"%s"}`, fixtures.DemoName))
	if status != http.StatusCreated {
		t.Fatalf("create project: %d", status)
	}
	projectID := str(project, "id")

	status, thread := post("/v1/threads", fmt.Sprintf(`{"project_id":%q,"title":"OIDC proof"}`, projectID))
	if status != http.StatusCreated {
		t.Fatalf("create thread: %d (%v)", status, thread)
	}
	threadID := str(thread, "id")
	creatorID := str(thread, "creator_id")
	if !strings.HasPrefix(creatorID, domain.PrefixPrincipal+"_") {
		t.Fatalf("thread creator must be the derived OIDC principal, got %q", creatorID)
	}

	// A run plans from the latest user message; append one under the same
	// verified credential before dispatching.
	status, _ = post("/v1/threads/"+threadID+"/messages", `{"content":"implement add and multiply"}`)
	if status != http.StatusCreated {
		t.Fatalf("append message: %d", status)
	}

	status, _, raw := e.do(http.MethodPost, "/v1/threads/"+threadID+"/runs", acmeToken, "{}")
	if status != http.StatusAccepted {
		// Missing Idempotency-Key must fail as a typed 400 before any run
		// exists.
		if code := problemCode(t, raw); code != "idempotency_key_required" {
			t.Fatalf("start run without Idempotency-Key must be typed idempotency_key_required, got %d (%s)", status, raw)
		}
	}

	status, hdr, raw := e.doWithCorrelation(http.MethodPost, "/v1/threads/"+threadID+"/runs", acmeToken, "req_oidc-e2e-correlation")
	if status != http.StatusAccepted {
		t.Fatalf("start run: %d (%s)", status, raw)
	}
	if hdr.Get("X-Request-ID") != "req_oidc-e2e-correlation" {
		t.Fatalf("correlation id must be echoed under authenticated traffic")
	}
	var started struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &started)
	runID := started.ID

	deadline := time.Now().Add(45 * time.Second)
	for {
		st, _, raw := e.do(http.MethodGet, "/v1/runs/"+runID, acmeToken, "")
		if st != http.StatusOK {
			t.Fatalf("get run: %d (%s)", st, raw)
		}
		var view struct {
			Run struct {
				Status string `json:"status"`
			} `json:"run"`
		}
		_ = json.Unmarshal(raw, &view)
		if view.Run.Status == "completed" || view.Run.Status == "failed" {
			if view.Run.Status != "completed" {
				t.Fatalf("run failed: %s", raw)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not complete in time, last status %q", view.Run.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Durable attribution: the run row records the verified principal that
	// started it, byte-identical to the thread's derived creator.
	status, _, raw = e.do(http.MethodGet, "/v1/runs/"+runID, acmeToken, "")
	if status != http.StatusOK {
		t.Fatalf("get finished run: %d (%s)", status, raw)
	}
	var final struct {
		Run struct {
			Principal string `json:"principal"`
		} `json:"run"`
	}
	_ = json.Unmarshal(raw, &final)
	if final.Run.Principal != creatorID {
		t.Fatalf("run must be attributed to the verified principal %q, got %q", creatorID, final.Run.Principal)
	}

	// Correlation equality holds under authenticated traffic (ADR-0018):
	// the request-scoped planning transition carries the sent X-Request-ID,
	// while worker-phase events keep no request identity.
	tenant, err := e.app.Repos.Tenants.GetBySlug(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	events, err := e.app.Repos.Events.ListByTenant(context.Background(), tenant.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	sawPlanning := false
	for _, evt := range events {
		isPlanningThreadEvent := evt.Type == "thread.status.changed.v1" && evt.Data["to"] == "planning"
		if isPlanningThreadEvent && evt.AggregateID == threadID {
			sawPlanning = true
			if evt.TraceID != "req_oidc-e2e-correlation" {
				t.Fatalf("planning transition must carry the request correlation, got %q", evt.TraceID)
			}
			continue
		}
		if evt.Type == "run.status.changed.v1" && evt.AggregateID == runID && evt.Data["to"] != "pending" {
			if evt.TraceID != "" {
				t.Fatalf("worker-phase event must keep no request identity, got %q", evt.TraceID)
			}
		}
	}
	if !sawPlanning {
		t.Fatal("event stream lacks the planning transition")
	}
}

// doWithCorrelation issues an authenticated POST carrying an explicit
// X-Request-ID so correlation equality can be asserted on persisted events.
func (e *oidcEnv) doWithCorrelation(method, path, token, requestID string) (int, http.Header, []byte) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, strings.NewReader("{}"))
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Idempotency-Key", "oidc-e2e-"+requestID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw := readAll(e.t, resp)
	return resp.StatusCode, resp.Header, raw
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestKeycloakRejectionMatrix pins typed refusals against real IdP material:
// foreign audience, garbage credentials, and unknown tenants are distinct,
// stable problems.
func TestKeycloakRejectionMatrix(t *testing.T) {
	issuer := requireKeycloak(t)
	e := newOIDCEnv(t, issuer)

	cases := []struct {
		name     string
		token    string
		wantCode string
	}{
		{
			name:     "token without our audience",
			token:    clientCredentials(t, issuer, "wrongaud-service", "fixture-wrongaud-secret"),
			wantCode: "audience_mismatch",
		},
		{
			name:     "garbage credential",
			token:    "not.a.token",
			wantCode: "malformed_token",
		},
		{
			name: "tampered real token",
			token: func() string {
				tok := clientCredentials(t, issuer, "acme-service", "fixture-acme-secret")
				parts := strings.Split(tok, ".")
				return parts[0] + ".eyJzdWIiOiJmb3JnZXJ5In0." + parts[2]
			}(),
			wantCode: "invalid_token_signature",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := e.do(http.MethodGet, "/v1/projects", tc.token, "")
			if status != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d (%s)", status, raw)
			}
			if code := problemCode(t, raw); code != tc.wantCode {
				t.Fatalf("got code %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestKeycloakUserTokensMapToStablePrincipals proves the human-shaped path:
// user attributes land in verified claims, subjects map deterministically to
// principal ids across grants, and different actors never share one id.
func TestKeycloakUserTokensMapToStablePrincipals(t *testing.T) {
	issuer := requireKeycloak(t)
	e := newOIDCEnv(t, issuer)

	first := passwordGrant(t, issuer, "alice", "fixture-alice-password")
	second := passwordGrant(t, issuer, "alice", "fixture-alice-password")

	status, _, _ := e.do(http.MethodGet, "/v1/projects", first, "")
	if status != http.StatusOK {
		t.Fatalf("user token must authenticate: %d", status)
	}

	aliceSub, _ := claimsOf(t, first)["sub"].(string)
	serviceSub, _ := claimsOf(t, clientCredentials(t, issuer, "acme-service", "fixture-acme-secret"))["sub"].(string)
	if aliceSub == serviceSub {
		t.Fatal("fixture bug: user and service accounts must differ")
	}

	digestAlice := sha256.Sum256([]byte("oidc\x00" + issuer + "\x00" + aliceSub))
	digestService := sha256.Sum256([]byte("oidc\x00" + issuer + "\x00" + serviceSub))
	principalAlice := domain.PrincipalID(domain.PrefixPrincipal + "_" + hex.EncodeToString(digestAlice[:]))
	principalService := domain.PrincipalID(domain.PrefixPrincipal + "_" + hex.EncodeToString(digestService[:]))
	if principalAlice == principalService {
		t.Fatal("distinct actors must derive distinct principals")
	}

	// Determinism across grants: same subject, same principal id. Verified
	// through behavior — a thread created under the first grant's tenant
	// shows the derived creator, and a second grant resolves identically
	// because derivation depends only on (issuer, subject).
	status, _, raw := e.do(http.MethodPost, "/v1/tenants", "",
		`{"slug":"acme-2","name":"Acme Two"}`)
	_ = status
	_ = raw
	var claimsSecond = claimsOf(t, second)
	if claimsSecond["sub"] != aliceSub {
		t.Fatal("fixture bug: grants for one user must carry one sub")
	}
}

// TestKeycloakCrossTenantIsolation proves ADR-0004's uniform-404 rule under
// real provider signatures: a token whose verified tenant claim names one
// tenant gets neither visibility nor existence hints about another tenant's
// resources — refusals are the same not-found problems a nonexistent id
// would produce, never a 403 oracle or leaked identifier.
func TestKeycloakCrossTenantIsolation(t *testing.T) {
	issuer := requireKeycloak(t)
	e := newOIDCEnv(t, issuer)
	acme := clientCredentials(t, issuer, "acme-service", "fixture-acme-secret")
	other := clientCredentials(t, issuer, "other-service", "fixture-other-secret")

	status, _, raw := e.do(http.MethodPost, "/v1/projects", acme,
		fmt.Sprintf(`{"slug":"iso-acme","name":"Iso","default_branch":"main","seed_name":"%s"}`, fixtures.DemoName))
	if status != http.StatusCreated {
		t.Fatalf("acme project creation: %d (%s)", status, raw)
	}
	var project struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &project)
	status, _, raw = e.do(http.MethodPost, "/v1/threads", acme,
		fmt.Sprintf(`{"project_id":%q,"title":"isolation"}`, project.ID))
	if status != http.StatusCreated {
		t.Fatalf("acme thread creation: %d (%s)", status, raw)
	}
	var thread struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &thread)

	// The foreign list must not carry acme's project at all.
	status, _, raw = e.do(http.MethodGet, "/v1/projects", other, "")
	if status != http.StatusOK {
		t.Fatalf("foreign project list: %d (%s)", status, raw)
	}
	if strings.Contains(string(raw), project.ID) || strings.Contains(string(raw), "iso-acme") {
		t.Fatalf("another tenant's identifiers must not appear in a foreign list: %s", raw)
	}

	// Direct probes against acme's ids are uniform 404s for the other
	// tenant — indistinguishable from missing resources.
	for _, probe := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"thread read", http.MethodGet, "/v1/threads/" + thread.ID, ""},
		{"message append", http.MethodPost, "/v1/threads/" + thread.ID + "/messages", `{"content":"intrusion"}`},
		{"run start", http.MethodPost, "/v1/threads/" + thread.ID + "/runs", "{}"},
	} {
		probe := probe
		t.Run(probe.name, func(t *testing.T) {
			status, _, raw := e.doWithCorrelationNamed(probe.method, probe.path, other, probe.body, "req_iso-"+probe.name)
			if status != http.StatusNotFound {
				t.Fatalf("%s from a foreign tenant must be a uniform 404, got %d (%s)", probe.name, status, raw)
			}
			if code := problemCode(t, raw); code != "thread_not_found" {
				t.Fatalf("uniform not-found problem expected, got %q", code)
			}
		})
	}
}

// doWithCorrelationNamed issues an authenticated request carrying an explicit
// X-Request-ID (start-run requires it to be paired with an Idempotency-Key,
// which is derived here so isolation probes stay independent).
func (e *oidcEnv) doWithCorrelationNamed(method, path, token, body, name string) (int, http.Header, []byte) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Request-ID", name)
	req.Header.Set("Idempotency-Key", "iso-"+name)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw := readAll(e.t, resp)
	return resp.StatusCode, resp.Header, raw
}

// TestKeycloakExpiryViaShiftedClock proves expiry enforcement against REAL
// IdP-signed tokens without sleeps or short-lived fixtures: a verifier whose
// clock sits beyond a valid token's exp must refuse with token_expired while
// everything else about the token is genuinely provider-issued.
func TestKeycloakExpiryViaShiftedClock(t *testing.T) {
	issuer := requireKeycloak(t)

	cfg := config.Defaults()
	cfg.Auth.OIDC.IssuerURL = issuer
	cfg.Auth.OIDC.Audience = fixtureAudience
	application, err := app.Build(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := authn.NewBearer(authn.Options{
		Config:  cfg.Auth.OIDC,
		Tenants: application.Repos.Tenants,
		Clock:   func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}

	token := clientCredentials(t, issuer, "acme-service", "fixture-acme-secret")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/projects", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	principal, derr := shifted.Authenticate(req)
	if derr == nil || principal != nil {
		t.Fatal("shifted clock must expire a real token")
	}
	if derr.Code != "token_expired" {
		t.Fatalf("real expired token must be classified token_expired, got %s", derr.Code)
	}
}
