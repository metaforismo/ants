package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/server"
	memorystore "github.com/metaforismo/ants/internal/store/memory"
)

const (
	testAudience    = "ants-api"
	testTenantClaim = "ants_tenant"
)

// idp is a local OIDC provider stand-in: discovery, JWKS, and RSA signing.
// The JWKS fetch counter lets rotation and rate-limit tests observe exactly
// when network traffic happens.
type idp struct {
	t      *testing.T
	server *httptest.Server

	mu          sync.Mutex
	privKeys    map[string]*rsa.PrivateKey
	jwksDoc     []byte
	issuerValue string // what discovery reports; defaults to the server URL
	jwksStatus  int    // forced failure mode for the JWKS endpoint
	fetches     atomic.Int64
}

func newIDP(t *testing.T, kids ...string) *idp {
	t.Helper()
	if len(kids) == 0 {
		kids = []string{"kid-1"}
	}
	p := &idp{t: t, privKeys: map[string]*rsa.PrivateKey{}}
	p.setKeys(kids)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, p.issuer(), p.server.URL+"/jwks.json")
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		p.fetches.Add(1)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.jwksStatus != 0 {
			http.Error(w, "unavailable", p.jwksStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(p.jwksDoc)
	})
	p.server = httptest.NewServer(mux)
	p.mu.Lock()
	p.issuerValue = p.server.URL
	p.mu.Unlock()
	t.Cleanup(p.server.Close)
	return p
}

// setKeys rebuilds the private key map and served JWKS document.
func (p *idp) setKeys(kids []string) {
	p.t.Helper()
	set := jwk.NewSet()
	for _, kid := range kids {
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			p.t.Fatalf("generate rsa key: %v", err)
		}
		p.privKeys[kid] = priv
		pub, err := jwk.PublicKeyOf(priv)
		if err != nil {
			p.t.Fatalf("public key: %v", err)
		}
		for k, v := range map[string]any{
			jwk.KeyIDKey:     kid,
			jwk.AlgorithmKey: "RS256",
			jwk.KeyUsageKey:  "sig",
		} {
			if err := pub.Set(k, v); err != nil {
				p.t.Fatalf("set %v: %v", k, err)
			}
		}
		if err := set.AddKey(pub); err != nil {
			p.t.Fatalf("add key: %v", err)
		}
	}
	doc, err := json.Marshal(set)
	if err != nil {
		p.t.Fatal(err)
	}
	p.jwksDoc = doc
}

// rotate replaces the served key set: old kids vanish (revocation) and only
// the new kid verifies.
func (p *idp) rotate(kid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.privKeys = map[string]*rsa.PrivateKey{}
	p.setKeys([]string{kid})
}

func (p *idp) issuer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.issuerValue
}

func (p *idp) setIssuer(v string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issuerValue = v
}

func (p *idp) setJWKSStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksStatus = status
}

// mint builds a fully-claimed RS256 token signed under the given kid.
func (p *idp) mint(kid, subject, tenantSlug string, exp time.Time) string {
	return p.signWithClaims(map[string]any{
		jwt.IssuerKey:     p.issuer(),
		jwt.AudienceKey:   []string{testAudience},
		jwt.SubjectKey:    subject,
		jwt.ExpirationKey: exp.Unix(),
		jwt.IssuedAtKey:   time.Now().Add(-time.Minute).Unix(),
		testTenantClaim:   tenantSlug,
	}, kid)
}

// signWithClaims signs an arbitrary claim set, so tests can omit or poison
// specific claims without library interference.
func (p *idp) signWithClaims(claims map[string]any, kid string) string {
	p.t.Helper()
	tok := jwt.New()
	for k, v := range claims {
		if err := tok.Set(k, v); err != nil {
			p.t.Fatalf("set claim %s: %v", k, err)
		}
	}
	key := p.keyFor(kid)
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		p.t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

func (p *idp) keyFor(kid string) jwk.Key {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	priv, ok := p.privKeys[kid]
	if !ok {
		p.t.Fatalf("no private key registered under %q", kid)
	}
	key, err := jwk.FromRaw(priv)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		p.t.Fatal(err)
	}
	return key
}

// rawCompact hand-builds a compact JWS from arbitrary header/payload JSON so
// algorithm-level attacks can be exercised without library help.
func rawCompact(headerJSON, payloadJSON string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(headerJSON)) + "." + enc([]byte(payloadJSON)) + "."
}

// resultCapture is a concurrency-safe Observer: verifications run in
// parallel under -race, so the capture must be too (the production
// observer is a Prometheus counter).
type resultCapture struct {
	mu      sync.Mutex
	results []string
}

func (c *resultCapture) AuthToken(result string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results = append(c.results, result)
}

func (c *resultCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.results...)
}

// fixture pairs an authenticator against the test IdP with a controllable
// clock and captured observer results.
type fixture struct {
	auth    *OIDCBearer
	idp     *idp
	capture *resultCapture
	base    time.Time
}

func newFixture(t *testing.T, mutate ...func(*config.OIDC)) *fixture {
	t.Helper()
	p := newIDP(t)
	repos := memorystore.NewRepos()
	store := repos.AsPorts()
	seedTenant(t, store.Tenants, "acme")
	seedTenant(t, store.Tenants, "other")

	cfg := config.Defaults()
	cfg.Auth.OIDC.IssuerURL = p.server.URL
	cfg.Auth.OIDC.Audience = testAudience
	cfg.Auth.OIDC.TenantClaim = testTenantClaim
	for _, m := range mutate {
		m(&cfg.Auth.OIDC)
	}

	f := &fixture{idp: p, base: time.Now()}
	capture := &resultCapture{}
	f.capture = capture
	auth, err := NewBearer(Options{
		Config:   cfg.Auth.OIDC,
		Tenants:  store.Tenants,
		Clock:    func() time.Time { return f.base },
		Observer: capture,
	})
	if err != nil {
		t.Fatalf("new bearer: %v", err)
	}
	f.auth = auth
	return f
}

func seedTenant(t *testing.T, store ports.TenantStore, slug string) {
	t.Helper()
	idStr, err := domain.NewID(domain.PrefixTenant)
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := domain.NewTenant(domain.TenantID(idStr), slug, slug, domain.PlanFree, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
}

// authenticate runs one request through the authenticator, returning the
// principal (nil on refusal) and the problem code ("accepted" on success).
func (f *fixture) authenticate(t *testing.T, token string) (*server.Principal, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	principal, derr := f.auth.Authenticate(r)
	code := "accepted"
	if derr != nil {
		code = derr.Code
	}
	return principal, code
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64urlDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// tamperPayload decodes a token's payload, applies the mutation, re-encodes,
// and returns the full Authorization header value carrying the original
// signature — a signature-breaking forgery by construction.
func tamperPayload(t *testing.T, token string, mutate func(map[string]any)) string {
	t.Helper()
	parts := strings.Split(token, ".")
	raw, err := b64urlDecode(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	mutate(claims)
	poisoned, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = b64url(poisoned)
	return strings.Join(parts, ".")
}

// tamperHeader applies payload mutation to a full Authorization header value
// and returns the forged header value.
func tamperHeader(t *testing.T, headerValue string, mutate func(map[string]any)) string {
	t.Helper()
	const scheme = "Bearer "
	if !strings.HasPrefix(headerValue, scheme) {
		t.Fatal("tamper expects a Bearer header value")
	}
	return scheme + tamperPayload(t, strings.TrimPrefix(headerValue, scheme), mutate)
}

// setJWKSDoc replaces the served JWKS document verbatim, for malformed and
// ambiguous documents the normal builders cannot produce.
func (p *idp) setJWKSDoc(raw []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksDoc = raw
}

// newAuthedRequest builds a GET against an authenticated route carrying the
// given full Authorization header value (empty means absent).
func newAuthedRequest(t *testing.T, headerValue string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	if headerValue != "" {
		r.Header.Set("Authorization", headerValue)
	}
	return r
}
