package authn_test

// Live-path proofs for ADR-0019 against a real Keycloak running as a
// disposable local container (scripts/test-keycloak.sh). Skipped unless
// TEST_OIDC_ISSUER points at the fixture realm, so plain `go test ./...`
// stays free and offline exactly like the rest of the suite.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/authn"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/server"
)

const fixtureAudience = "ants-api"

func requireKeycloak(t *testing.T) string {
	t.Helper()
	issuer := os.Getenv("TEST_OIDC_ISSUER")
	if issuer == "" {
		t.Skip("TEST_OIDC_ISSUER not set; run scripts/test-keycloak.sh for the live-path suite")
	}
	return issuer
}

type oidcEnv struct {
	t       *testing.T
	baseURL string
	app     *app.App
	auth    *authn.OIDCBearer
}

// newOIDCEnv composes the production stack — app, real OIDC verifier, real
// middleware chain — against the fixture Keycloak, mirroring cli.newServer.
func newOIDCEnv(t *testing.T, issuer string) *oidcEnv {
	t.Helper()
	cfg := config.Defaults()
	cfg.Auth.OIDC.IssuerURL = issuer
	cfg.Auth.OIDC.Audience = fixtureAudience
	cfg.Sandbox.Driver = config.SandboxDriverFake
	cfg.SCM.Driver = config.SCMDriverMemory
	application := buildTestApp(t, cfg)

	verifier, err := authn.NewBearer(authn.Options{
		Config:   cfg.Auth.OIDC,
		Tenants:  application.Repos.Tenants,
		Observer: application.Metrics,
	})
	if err != nil {
		t.Fatalf("wire verifier: %v", err)
	}
	srv, err := server.New(server.Deps{
		Config: cfg,
		Repos:  application.Repos,
		Auth:   verifier,
		Uow:    application.Uow,
		Engine: application.Engine,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready: func(ctx context.Context) error {
			if err := application.Ready(ctx); err != nil {
				return err
			}
			return verifier.Ready(ctx)
		},
		Metrics: application.Metrics,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	e := &oidcEnv{t: t, baseURL: ts.URL, app: application, auth: verifier}
	for _, slug := range []string{"acme", "other"} {
		status, _, raw := e.do(http.MethodPost, "/v1/tenants", "",
			fmt.Sprintf(`{"slug":%q,"name":"%s"}`, slug, strings.ToUpper(slug[:1])+slug[1:]))
		if status != http.StatusCreated {
			t.Fatalf("seed tenant %s: %d (%s)", slug, status, raw)
		}
	}
	return e
}

func buildTestApp(t *testing.T, cfg config.Config) *app.App {
	t.Helper()
	application, err := app.Build(cfg, io.Discard)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	fake, ok := application.Sandbox.(*sandbox.FakeDriver)
	if !ok {
		t.Fatalf("expected fake driver in test wiring")
	}
	if err := fixtures.ScriptFake(fake); err != nil {
		t.Fatalf("script fake driver: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	go func() { _ = application.Worker.Run(ctx) }()
	go func() { _ = application.Outbox.Run(ctx) }()
	t.Cleanup(stop)
	return application
}

func (e *oidcEnv) do(method, path, token, body string) (int, http.Header, []byte) {
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
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

func problemCode(t *testing.T, raw []byte) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("response is not problem details: %s", raw)
	}
	return p.Code
}

// grant requests one token from the fixture realm; the OAuth form stays
// explicit so every proof names the flow it exercised.
func grant(t *testing.T, issuer string, form url.Values) string {
	t.Helper()
	endpoint := strings.TrimSuffix(issuer, "/") + "/protocol/openid-connect/token"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if parsed.AccessToken == "" {
		t.Fatalf("token grant failed: %s %s (%s)", parsed.Error, parsed.Description, form.Get("client_id"))
	}
	return parsed.AccessToken
}

func clientCredentials(t *testing.T, issuer, clientID, secret string) string {
	return grant(t, issuer, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
}

func passwordGrant(t *testing.T, issuer, username, password string) string {
	return grant(t, issuer, url.Values{
		"grant_type":    {"password"},
		"client_id":     {"alice-direct"},
		"client_secret": {"fixture-alice-direct-secret"},
		"username":      {username},
		"password":      {password},
	})
}

func claimsOf(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("token is not compact JWS")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
