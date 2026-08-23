package authn

// Transport-boundary regression tests from the PR 20 audit: the IdP transport
// rule (https everywhere except literal loopback) must hold for discovery,
// jwks_uri, and redirect chains, and every IdP exchange — including the
// rotation-forced refresh — must be bounded by auth.oidc.http_timeout.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/config"
)

func TestIDPRedirectPolicy(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"https remote allowed", "https://idp.example.com/jwks.json", false},
		{"loopback http allowed", "http://127.0.0.1:8081/jwks.json", false},
		{"localhost http allowed", "http://localhost:8081/jwks.json", false},
		{"remote http refused", "http://203.0.113.7/jwks.json", true},
		{"private http refused", "http://192.168.1.10/jwks.json", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = idpRedirectPolicy(req, []*http.Request{req})
			if tc.wantErr && err == nil {
				t.Fatalf("redirect to %s must be refused", tc.target)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("redirect to %s must be allowed: %v", tc.target, err)
			}
		})
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://idp.example.com/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, 10)
	if err := idpRedirectPolicy(req, via); err == nil {
		t.Fatal("an eleventh hop must be refused")
	}
}

// TestDefaultClientCarriesRedirectPolicy pins that the production wiring
// never falls back to an unrestricted stock client.
func TestDefaultClientCarriesRedirectPolicy(t *testing.T) {
	f := newFixture(t)
	if f.auth.client == nil {
		t.Fatal("authenticator must hold an HTTP client")
	}
	if f.auth.client.CheckRedirect == nil {
		t.Fatal("the default IdP client must carry the transport-rule redirect policy")
	}
}

// TestDiscoveryAppliesTransportRuleToJWKSURI pins the audit fix: a discovery
// document may not walk key fetching onto plaintext remote endpoints.
func TestDiscoveryAppliesTransportRuleToJWKSURI(t *testing.T) {
	cases := []struct {
		name           string
		jwksURI        string
		wantErrContent string // empty means acceptance
	}{
		{"https remote accepted", "https://keys.example.com/jwks.json", ""},
		{"loopback http accepted", "http://127.0.0.1:8081/jwks.json", ""},
		{"plain remote http refused", "http://203.0.113.7/jwks.json", "must use https outside loopback"},
		{"relative garbage refused", "not-a-absolute-url", "no usable jwks_uri"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/realms/ants/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, "http://"+r.Host+"/realms/ants", tc.jwksURI)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			doc, err := discover(context.Background(), srv.Client(), srv.URL+"/realms/ants")
			if tc.wantErrContent == "" {
				if err != nil {
					t.Fatalf("discovery with jwks_uri %s must succeed: %v", tc.jwksURI, err)
				}
				if doc.JWKSURI != tc.jwksURI {
					t.Fatalf("jwks_uri not carried through: %q", doc.JWKSURI)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContent) {
				t.Fatalf("jwks_uri %s must be refused naming the rule, got %v", tc.jwksURI, err)
			}
		})
	}
}

// TestAuthenticateRefusesDowngradeRedirects proves the redirect boundary end
// to end: a provider whose discovery document redirects to a non-loopback
// plaintext host is classified as unavailable instead of followed.
func TestAuthenticateRefusesDowngradeRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://203.0.113.7/.well-known/openid-configuration", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := newFixture(t, func(o *config.OIDC) { o.IssuerURL = srv.URL })
	started := time.Now()
	token := bearer(f.idp.mint("kid-1", "attacker", "acme", f.base.Add(time.Hour)))
	principal, code := f.authenticate(t, token)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("refused redirect must fail fast, took %s", elapsed)
	}
	if principal != nil || code != "auth_provider_unavailable" {
		t.Fatalf("downgrading redirect must be provider_unavailable, got (%v, %q)", principal, code)
	}
}

// TestRotationRefreshBoundedByHTTPTimeout pins the audit fix: when the JWKS
// endpoint hangs during an unknown-kid rotation refresh, the verifier gives
// up after auth.oidc.http_timeout and classifies the token by its stale-cache
// verdict — it never waits on the server-level write deadline.
func TestRotationRefreshBoundedByHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	f := newFixture(t, func(o *config.OIDC) { o.HTTPTimeout = config.Duration{Duration: 250 * time.Millisecond} })

	oldToken := bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute)))
	if _, code := f.authenticate(t, oldToken); code != "accepted" {
		t.Fatalf("precondition: %q", code)
	}

	// Arm the hang only after the cache is filled; the release channel keeps
	// httptest.Server.Close deadlock-free at cleanup.
	f.idp.setJWKSRelease(release)
	t.Cleanup(func() { close(release) })

	f.idp.rotate("kid-2")
	newToken := bearer(f.idp.mint("kid-2", "user-2", "acme", f.base.Add(10*time.Minute)))

	before := f.idp.fetches.Load()
	started := time.Now()
	_, code := f.authenticate(t, newToken)
	elapsed := time.Since(started)
	if elapsed > 3*time.Second {
		t.Fatalf("rotation refresh must be bounded by http_timeout, took %s", elapsed)
	}
	if code != "invalid_token_signature" {
		t.Fatalf("hung rotation refresh must fall back to the stale-cache verdict, got %q", code)
	}
	if got := f.idp.fetches.Load() - before; got != 1 {
		t.Fatalf("exactly one bounded fetch attempt expected, saw %d", got)
	}
}
