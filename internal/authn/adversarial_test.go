package authn

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestAuthenticateAdversarialMatrix pins every rejection path of the OIDC
// verifier (ADR-0019): each row is a concrete attack or fault, and the code
// it must produce is part of the public problem contract.
func TestAuthenticateAdversarialMatrix(t *testing.T) {
	// valid returns the full Authorization header value for an accepted
	// credential.
	valid := func(f *fixture) string {
		return "Bearer " + f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))
	}

	cases := []struct {
		name     string
		token    func(f *fixture) string
		wantCode string // empty means acceptance
	}{
		{
			name:  "valid token accepted",
			token: valid,
		},
		{
			name: "bearer scheme case-insensitive",
			token: func(f *fixture) string {
				return "BEARER " + f.idp.mint("kid-1", "user-1", "acme", f.base.Add(10*time.Minute))
			},
		},
		{
			name:     "missing header",
			token:    func(*fixture) string { return "" },
			wantCode: "missing_bearer_token",
		},
		{
			name:     "wrong scheme",
			token:    func(*fixture) string { return "Basic dXNlcjpwYXNz" },
			wantCode: "invalid_authorization_header",
		},
		{
			name:     "empty credential",
			token:    func(*fixture) string { return "Bearer " },
			wantCode: "invalid_authorization_header",
		},
		{
			name: "oversized credential",
			token: func(*fixture) string {
				return "Bearer " + strings.Repeat("a", 9<<10)
			},
			wantCode: "invalid_authorization_header",
		},
		{
			name: "alg none rejected before key selection",
			token: func(f *fixture) string {
				payload := fmt.Sprintf(`{"iss":%q,"aud":"%s","sub":"attacker","exp":%d,"%s":"acme"}`,
					f.idp.issuer(), testAudience, f.base.Add(time.Hour).Unix(), testTenantClaim)
				return "Bearer " + rawCompact(`{"alg":"none","kid":"kid-1"}`, payload)
			},
			wantCode: "unsupported_token_algorithm",
		},
		{
			name: "HS256 algorithm confusion rejected",
			token: func(f *fixture) string {
				payload := fmt.Sprintf(`{"iss":%q,"aud":"%s","sub":"attacker","exp":%d,"%s":"acme"}`,
					f.idp.issuer(), testAudience, f.base.Add(time.Hour).Unix(), testTenantClaim)
				return "Bearer " + rawCompact(`{"alg":"HS256","kid":"kid-1"}`, payload)
			},
			wantCode: "unsupported_token_algorithm",
		},
		{
			name:     "non-base64 token malformed",
			token:    func(*fixture) string { return "Bearer not!!a!!token!!here" },
			wantCode: "malformed_token",
		},
		{
			name: "header is not a JSON object",
			token: func(*fixture) string {
				return "Bearer " + rawCompact(`[1,2]`, `{}`)
			},
			wantCode: "malformed_token",
		},
		{
			name: "tampered payload breaks signature",
			token: func(f *fixture) string {
				return tamperHeader(t, valid(f), func(c map[string]any) { c[testTenantClaim] = "other" })
			},
			wantCode: "invalid_token_signature",
		},
		{
			name: "signature by unknown key",
			token: func(f *fixture) string {
				rogue := newIDP(t)
				return "Bearer " + rogue.mint("kid-1", "attacker", "acme", f.base.Add(10*time.Minute))
			},
			wantCode: "invalid_token_signature",
		},
		{
			name: "missing kid header",
			token: func(f *fixture) string {
				signed := f.idp.signWithClaims(map[string]any{
					"iss": f.idp.issuer(), "aud": []string{testAudience}, "sub": "user-1",
					"exp": f.base.Add(10 * time.Minute).Unix(), testTenantClaim: "acme",
				}, "kid-1")
				parts := strings.Split(signed, ".")
				raw, err := b64urlDecode(parts[0])
				if err != nil {
					t.Fatal(err)
				}
				stripped := strings.Replace(string(raw), `,"kid":"kid-1"`, "", 1)
				if stripped == string(raw) {
					t.Fatal("test bug: kid not found in header")
				}
				parts[0] = b64url([]byte(stripped))
				return "Bearer " + strings.Join(parts, ".")
			},
			wantCode: "invalid_token_signature",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			principal, code := f.authenticate(t, tc.token(f))
			if tc.wantCode == "" {
				if principal == nil {
					t.Fatalf("expected acceptance, got refusal %q", code)
				}
				if principal.Tenant == nil {
					t.Fatalf("principal must carry a resolved tenant: %+v", principal)
				}
				return
			}
			if principal != nil {
				t.Fatalf("expected refusal %q, got a principal %+v", tc.wantCode, principal)
			}
			if code != tc.wantCode {
				t.Fatalf("got code %q, want %q", code, tc.wantCode)
			}
		})
	}
}
