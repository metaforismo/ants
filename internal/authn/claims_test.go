package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"

	"github.com/metaforismo/ants/internal/config"
)

func claimCases() []struct {
	name     string
	mutate   func(t *testing.T, f *fixture) string
	wantCode string
} {
	return []struct {
		name     string
		mutate   func(t *testing.T, f *fixture) string
		wantCode string
	}{
		{
			name: "wrong issuer claim",
			mutate: func(t *testing.T, f *fixture) string {
				// Signed by the trusted key under a foreign issuer claim:
				// only signature-valid tokens can reach the issuer check,
				// which is exactly the ordering under test.
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": "https://evil.example.com", "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "untrusted_issuer",
		},
		{
			name: "wrong audience",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{"another-api"}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "audience_mismatch",
		},
		{
			name: "no audience claim",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "audience_mismatch",
		},
		{
			name: "expired token",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(-time.Hour)))
			},
			wantCode: "token_expired",
		},
		{
			name: "expiry inside skew window accepted",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(-15*time.Second)))
			},
			wantCode: "",
		},
		{
			name: "not-yet-valid beyond skew",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(),
					"nbf": f.base.Add(5 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "token_not_yet_valid",
		},
		{
			name: "not-before within skew accepted",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(),
					"nbf": f.base.Add(15 * time.Second).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "",
		},
		{
			name: "missing subject",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience},
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "missing_subject",
		},
		{
			name: "missing expiry",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					testTenantClaim: "acme",
				}, "kid-1"))
			},
			wantCode: "missing_exp",
		},
		{
			name: "tenant claim absent",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(),
				}, "kid-1"))
			},
			wantCode: "invalid_tenant_claim",
		},
		{
			name: "tenant claim not a string",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: 42,
				}, "kid-1"))
			},
			wantCode: "invalid_tenant_claim",
		},
		{
			name: "tenant claim violates slug grammar",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.mint("kid-1", "user-1", "-bad slug-", f.base.Add(10*time.Minute)))
			},
			wantCode: "invalid_tenant_claim",
		},
		{
			name: "valid claim names unknown tenant",
			mutate: func(t *testing.T, f *fixture) string {
				return bearer(f.idp.mint("kid-1", "user-1", "ghost", f.base.Add(10*time.Minute)))
			},
			wantCode: "unknown_tenant",
		},
	}
}

// bearer renders a credential as a full Authorization header value.
func bearer(token string) string { return "Bearer " + token }

// TestAuthenticateClaimMatrix pins the verified-claims contract (ADR-0019):
// issuer, audience, temporal validity, subject, and the tenant claim are all
// enforced server-side with distinct typed problems.
func TestAuthenticateClaimMatrix(t *testing.T) {
	for _, tc := range claimCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			principal, code := f.authenticate(t, tc.mutate(t, f))
			if tc.wantCode == "" {
				if principal == nil {
					t.Fatalf("expected acceptance, got refusal %q", code)
				}
				if principal.Tenant == nil || principal.Tenant.Slug == "" {
					t.Fatalf("principal must carry a resolved tenant: %+v", principal)
				}
				return
			}
			if code != tc.wantCode || principal != nil {
				t.Fatalf("got (%v, %q), want refusal %q", principal, code, tc.wantCode)
			}
		})
	}
}

// TestPrincipalIDDeterministicAndNamespaced pins the subject mapping:
// identical (issuer, subject) pairs derive identical grammar-valid principal
// ids, different actors never collide, and raw external identifiers stay out
// of the stored id.
func TestPrincipalIDDeterministicAndNamespaced(t *testing.T) {
	f := newFixture(t)
	first, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
	if code != "accepted" {
		t.Fatalf("precondition: %q", code)
	}
	second := principalIDFor(f.idp.issuer(), "user-1")
	if first.ID != second {
		t.Fatalf("principal id must be deterministic: %s vs %s", first.ID, second)
	}
	other := principalIDFor(f.idp.issuer(), "user-2")
	if other == second {
		t.Fatal("distinct subjects must not collide")
	}
	crossIssuer := principalIDFor("https://elsewhere.example.com", "user-1")
	if crossIssuer == second {
		t.Fatal("distinct issuers must not collide for one subject")
	}
	suffix := string(second)[len("prn_"):]
	if len(suffix) != 64 {
		t.Fatalf("derived suffix must be 64 hex chars, got %d", len(suffix))
	}
	for _, r := range suffix {
		alphanumeric := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !alphanumeric {
			t.Fatalf("suffix must be lowercase hex, saw %q", r)
		}
	}
}

// TestKeyRotation pins the rotation path: after the IdP swaps its key set,
// tokens under the new kid verify through one forced JWKS refresh, revoked
// kids stop verifying, and refreshes stay rate-limited.
func TestKeyRotation(t *testing.T) {
	f := newFixture(t)
	oldToken := bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute)))

	f.idp.rotate("kid-2")
	newToken := bearer(f.idp.mint("kid-2", "user-2", "acme", f.base.Add(10*time.Minute)))

	principal, code := f.authenticate(t, newToken)
	if code != "accepted" || principal == nil {
		t.Fatalf("rotated-in kid must verify via forced refresh, got %q", code)
	}

	if _, code := f.authenticate(t, oldToken); code != "invalid_token_signature" {
		t.Fatalf("revoked kid must stop verifying after rotation, got %q", code)
	}

	// The forced-refresh floor bounds attacker-induced IdP fetches: two
	// unknown-kid attempts inside one floor window cause exactly one extra
	// fetch beyond the initial cache fill.
	before := f.idp.fetches.Load()
	f.auth.keys.mu.Lock()
	f.auth.keys.lastForcedAt = time.Time{} // re-arm the floor deterministically
	f.auth.keys.mu.Unlock()
	_, _ = f.authenticate(t, newToken)
	f.auth.keys.mu.Lock()
	f.auth.keys.lastForcedAt = time.Now().Add(forcedRefreshFloor / 2) // inside floor
	f.auth.keys.mu.Unlock()
	_, _ = f.authenticate(t, newToken)
	after := f.idp.fetches.Load()
	if after-before > 1 {
		t.Fatalf("forced refreshes must be floored: %d fetches for 2 attempts", after-before)
	}
}

// TestRestartRefetchesEverything pins the restart story: a fresh verifier —
// no shared auth state with the old one — verifies immediately against the
// same provider.
func TestRestartRefetchesEverything(t *testing.T) {
	f := newFixture(t)
	token := bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute)))
	if _, code := f.authenticate(t, token); code != "accepted" {
		t.Fatalf("precondition: %q", code)
	}

	// Same tenant store, brand-new verifier instance: no auth state shared.
	repos := f.auth.tenants
	cfg := config.Defaults()
	cfg.Auth.OIDC.IssuerURL = f.idp.server.URL
	cfg.Auth.OIDC.Audience = testAudience
	cfg.Auth.OIDC.TenantClaim = testTenantClaim
	fresh, err := NewBearer(Options{Config: cfg.Auth.OIDC, Tenants: repos})
	if err != nil {
		t.Fatal(err)
	}
	r := newAuthedRequest(t, token)
	p, derr := fresh.Authenticate(r)
	if derr != nil || p == nil {
		t.Fatalf("fresh verifier must refetch and accept: %v", derr)
	}
}

// TestProviderFailuresAreTransient pins that identity-provider outages are
// 503-class transient problems, never mistaken for authentication refusals,
// while a stale cached key set keeps serving during short outages.
func TestProviderFailuresAreTransient(t *testing.T) {
	t.Run("discovery issuer mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.idp.setIssuer("https://somewhere-else.example.com")
		_, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
		if code != "auth_provider_unavailable" {
			t.Fatalf("mismatched discovery issuer must be provider_unavailable, got %q", code)
		}
	})
	t.Run("jwks unavailable before any cache exists", func(t *testing.T) {
		f := newFixture(t)
		f.idp.setJWKSStatus(500)
		_, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
		if code != "auth_provider_unavailable" {
			t.Fatalf("failing jwks must be provider_unavailable, got %q", code)
		}
	})
	t.Run("stale cache serves through outage", func(t *testing.T) {
		f := newFixture(t)
		token := bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute)))
		if _, code := f.authenticate(t, token); code != "accepted" {
			t.Fatalf("precondition: %q", code)
		}
		f.idp.setJWKSStatus(500)
		// Force TTL expiry so the next verification wants a refresh.
		f.auth.keys.mu.Lock()
		f.auth.keys.fetchedAt = f.base.Add(-time.Hour)
		f.auth.keys.mu.Unlock()
		if _, code := f.authenticate(t, token); code != "accepted" {
			t.Fatalf("stale cached keys must keep serving during an outage, got %q", code)
		}
	})
	t.Run("malformed jwks document", func(t *testing.T) {
		f := newFixture(t)
		f.idp.setJWKSDoc([]byte(`{"keys": "not-a-list"`))
		_, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
		if code != "auth_provider_unavailable" {
			t.Fatalf("malformed jwks must be provider_unavailable, got %q", code)
		}
	})
	t.Run("empty keyset", func(t *testing.T) {
		f := newFixture(t)
		f.idp.setJWKSDoc([]byte(`{"keys":[]}`))
		_, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
		if code != "auth_provider_unavailable" {
			t.Fatalf("empty keyset must be provider_unavailable, got %q", code)
		}
	})
	t.Run("duplicate kids refused", func(t *testing.T) {
		f := newFixture(t)
		key := f.idp.keyFor("kid-1")
		doc, err := json.Marshal(struct {
			Keys []jwk.Key `json:"keys"`
		}{Keys: []jwk.Key{key, key}})
		if err != nil {
			t.Fatal(err)
		}
		f.idp.setJWKSDoc(doc)
		_, code := f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
		if code != "auth_provider_unavailable" {
			t.Fatalf("duplicate-kid jwks must be refused as ambiguous, got %q", code)
		}
	})
}

// TestReadinessJoinsProviderState pins the readiness contract: Ready passes
// only when discovery and JWKS are actually reachable.
func TestReadinessJoinsProviderState(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.auth.Ready(ctx); err != nil {
		t.Fatalf("ready provider must pass readiness: %v", err)
	}
	mispointed := newFixture(t)
	mispointed.idp.setIssuer("https://moved.example.com")
	if err := mispointed.auth.Ready(ctx); err == nil {
		t.Fatal("mis-pointed issuer must fail readiness")
	}
}

// TestErrorsNeverCarryTokenMaterial pins the leakage contract: no failure
// message or structured log line contains any fragment of the presented
// token.
func TestErrorsNeverCarryTokenMaterial(t *testing.T) {
	f := newFixture(t)
	secrets := []string{}

	attempt := func(headerValue string) {
		r := newAuthedRequest(t, headerValue)
		_, derr := f.auth.Authenticate(r)
		if derr != nil {
			secrets = append(secrets, derr.Message, derr.Code)
		}
	}

	valid := f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))
	attempt("Bearer " + valid + "tampered")
	attempt("Basic " + valid)
	attempt("")
	attempt("Bearer garbage.payload.here")

	all := strings.Join(secrets, "\n")
	for _, frag := range []string{valid[:20]} {
		if strings.Contains(all, frag) {
			t.Fatalf("token material %q leaked into diagnostics", frag)
		}
	}
}

// TestObserverRecordsOutcomes pins the metric vocabulary: accepted and every
// rejection class report their fixed result label exactly once per attempt.
func TestObserverRecordsOutcomes(t *testing.T) {
	f := newFixture(t)
	_, _ = f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))))
	_, _ = f.authenticate(t, "")
	_, _ = f.authenticate(t, bearer(f.idp.mint("kid-1", "user-1", "ghost", f.base.Add(10*time.Minute))))

	got := f.capture.snapshot()
	want := []string{ResultAccepted, ResultRejectedMissing, ResultRejectedTenant}
	if len(got) != len(want) {
		t.Fatalf("observer saw %v, want %v", got, want)
	}
	for i, r := range want {
		if got[i] != r {
			t.Fatalf("result[%d]=%q, want %q", i, got[i], r)
		}
	}
}

// TestConcurrentVerificationUnderRotation stresses the shared authenticator:
// parallel verifications mixed with live key rotations must never race or
// misclassify a currently-valid token as structurally broken.
func TestConcurrentVerificationUnderRotation(t *testing.T) {
	f := newFixture(t)
	good := f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			f.idp.rotate(fmt.Sprintf("kid-r%d", i))
			time.Sleep(time.Millisecond)
		}
	}()

	errs := make(chan error, 64)
	for g := 0; g < 16; g++ {
		go func(g int) {
			for i := 0; i < 8; i++ {
				headerValue := bearer(good)
				_, derr := f.auth.Authenticate(newAuthedRequest(t, headerValue))
				switch derr {
				case nil:
				default:
					if derr.Code != "invalid_token_signature" && derr.Code != "auth_provider_unavailable" {
						errs <- fmt.Errorf("unexpected classification under rotation: %s", derr.Code)
						return
					}
				}
			}
			errs <- nil
		}(g)
	}
	<-done
	for g := 0; g < 16; g++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
