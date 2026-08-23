package authn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/server"
)

// OIDCBearer verifies OIDC bearer access tokens and maps them to request
// principals (ADR-0019). It holds no secrets: trust comes exclusively from
// public keys fetched over discovery/JWKS, re-derived lazily so nothing
// survives a restart.
type OIDCBearer struct {
	cfg     config.OIDC
	tenants ports.TenantStore
	now     func() time.Time
	client  *http.Client
	obs     Observer

	mu   sync.Mutex
	keys *keyStore
}

// Options wires the authenticator. Tenants is required because a verified
// tenant claim must still resolve to an existing Ants tenant before any
// request proceeds.
type Options struct {
	Config   config.OIDC
	Tenants  ports.TenantStore
	Clock    func() time.Time // optional; defaults to time.Now
	Observer Observer         // optional
	Client   *http.Client     // optional override; tests inject httptest transports
}

// NewBearer constructs the resource-server authenticator. Configuration is
// validated here as well as at startup so direct wiring cannot bypass it.
func NewBearer(opts Options) (*OIDCBearer, error) {
	if opts.Tenants == nil {
		return nil, fmt.Errorf("authn: a tenant store is required")
	}
	if !opts.Config.Configured() {
		return nil, fmt.Errorf("authn: issuer_url is required")
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, fmt.Errorf("authn: %w", err)
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	// The default client refuses redirect chains that leave the transport
	// rule ADR-0019 pins for IdP traffic (https everywhere except literal
	// loopback hosts): Go's stock client would otherwise follow even an
	// https→http downgrade or an arbitrary-host hop. Injected test clients
	// keep full control.
	client := opts.Client
	if client == nil {
		client = &http.Client{CheckRedirect: idpRedirectPolicy}
	}
	return &OIDCBearer{
		cfg:     opts.Config,
		tenants: opts.Tenants,
		now:     clock,
		client:  client,
		obs:     opts.Observer,
	}, nil
}

// failure pairs every rejection with its problem kind, stable code, static
// message, and metric result. Messages never embed token material.
type failure struct {
	kind    domain.ErrorKind
	result  string
	message string
}

var failures = map[string]failure{
	"missing_bearer_token":         {domain.ErrKindUnauthorized, ResultRejectedMissing, "authentication required"},
	"invalid_authorization_header": {domain.ErrKindUnauthorized, ResultRejectedHeader, "Authorization header must carry a Bearer access token"},
	"unsupported_token_algorithm":  {domain.ErrKindUnauthorized, ResultRejectedAlgorithm, "token algorithm is not accepted"},
	"malformed_token":              {domain.ErrKindUnauthorized, ResultRejectedMalformed, "token is malformed"},
	"invalid_token_signature":      {domain.ErrKindUnauthorized, ResultRejectedSignature, "token signature could not be verified"},
	"untrusted_issuer":             {domain.ErrKindUnauthorized, ResultRejectedIssuer, "token issuer is not trusted"},
	"audience_mismatch":            {domain.ErrKindUnauthorized, ResultRejectedAudience, "token audience does not include this API"},
	"token_expired":                {domain.ErrKindUnauthorized, ResultRejectedExpired, "token has expired"},
	"token_not_yet_valid":          {domain.ErrKindUnauthorized, ResultRejectedNotYetValid, "token is not valid yet"},
	"missing_subject":              {domain.ErrKindUnauthorized, ResultRejectedClaims, "token carries no subject"},
	"missing_exp":                  {domain.ErrKindUnauthorized, ResultRejectedClaims, "token carries no expiry"},
	"invalid_tenant_claim":         {domain.ErrKindUnauthorized, ResultRejectedClaims, "tenant claim is missing or malformed"},
	"unknown_tenant":               {domain.ErrKindUnauthorized, ResultRejectedTenant, "tenant not recognized"},
	"auth_provider_unavailable":    {domain.ErrKindTransient, ResultProviderUnavailable, "identity provider metadata is unavailable"},
	"tenant_store_unavailable":     {domain.ErrKindTransient, ResultStoreUnavailable, "tenant store is unavailable"},
}

func (a *OIDCBearer) fail(code string) (*server.Principal, *domain.Error) {
	f := failures[code]
	a.observe(f.result)
	return nil, &domain.Error{Kind: f.kind, Code: code, Message: f.message}
}

func (a *OIDCBearer) observe(result string) {
	if a.obs != nil {
		a.obs.AuthToken(result)
	}
}

// Authenticate verifies the presented bearer token and resolves it to a
// tenant-scoped principal. Every exit either yields a fully verified
// principal or a typed problem; there is no partial-trust path.
func (a *OIDCBearer) Authenticate(r *http.Request) (*server.Principal, *domain.Error) {
	token, af := extractBearer(r)
	if af != nil {
		return a.fail(af.code)
	}

	hdr, err := decodeCompactHeader(token)
	if err != nil {
		return a.fail("malformed_token")
	}
	// Structural allowlist before any key selection: this is what makes
	// algorithm confusion unreachable rather than merely unlikely. Checked
	// before segment completeness so an unsigned `none` token is classified
	// by its attack, not its truncation.
	if hdr.Alg != allowedAlgorithm {
		return a.fail("unsupported_token_algorithm")
	}
	if err := requireCompleteCompact(token); err != nil {
		return a.fail("malformed_token")
	}

	set, keys, derr := a.keySet(r.Context())
	if derr != nil {
		return a.fail(derr.code)
	}

	tok, perr := verifyToken(token, set)
	if perr != nil {
		// Any verification failure may mean the IdP rotated keys since our
		// last fetch; one rate-limited forced refresh followed by a single
		// retry covers rotation while bounding attacker-induced fetches.
		// The forced exchange is bounded by the same HTTP timeout as every
		// other IdP call: without it a hung JWKS endpoint would stall the
		// request until the server-level write deadline instead.
		rctx, rcancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout.Duration)
		refreshed, ferr := keys.refreshForced(rctx, a.now())
		if ferr == nil {
			if tok2, perr2 := verifyToken(token, refreshed); perr2 == nil {
				tok = tok2
				perr = nil
			}
		}
		rcancel()
		if perr != nil {
			return a.fail("invalid_token_signature")
		}
	}
	return a.principalFromClaims(r.Context(), tok)
}

// principalFromClaims applies the claim contract to a signature-verified
// token: exact issuer, containing audience, bounded expiry/not-before, a
// subject, and a well-formed tenant claim that resolves to an existing
// Ants tenant.
func (a *OIDCBearer) principalFromClaims(ctx context.Context, tok jwt.Token) (*server.Principal, *domain.Error) {
	if iss := tok.Issuer(); iss != a.cfg.IssuerURL && iss != strings.TrimSuffix(a.cfg.IssuerURL, "/") {
		return a.fail("untrusted_issuer")
	}
	if !slicesContain(tok.Audience(), a.cfg.Audience) {
		return a.fail("audience_mismatch")
	}
	now := a.now()
	skew := a.cfg.ClockSkew.Duration
	exp := tok.Expiration()
	if exp.IsZero() {
		return a.fail("missing_exp")
	}
	if !exp.After(now.Add(-skew)) {
		return a.fail("token_expired")
	}
	if nbf := tok.NotBefore(); !nbf.IsZero() && nbf.After(now.Add(skew)) {
		return a.fail("token_not_yet_valid")
	}
	subject := tok.Subject()
	if subject == "" {
		return a.fail("missing_subject")
	}

	rawTenant, _ := tok.Get(a.cfg.TenantClaim)
	slug, ok := rawTenant.(string)
	if !ok || domain.ValidateSlug(slug) != nil {
		return a.fail("invalid_tenant_claim")
	}

	tenant, err := a.tenants.GetBySlug(ctx, slug)
	var domErr *domain.Error
	switch {
	case err == nil:
	case errors.As(err, &domErr) && domErr.Kind == domain.ErrKindNotFound:
		return a.fail("unknown_tenant")
	default:
		return a.fail("tenant_store_unavailable")
	}

	a.observe(ResultAccepted)
	return &server.Principal{
		TenantID: tenant.ID,
		Tenant:   tenant,
		ID:       principalIDFor(a.cfg.IssuerURL, subject),
	}, nil
}

// principalIDFor derives a deterministic Ants principal id from the verified
// (issuer, subject) pair. Hashing keeps external identifiers out of stored
// actor fields while remaining stable across requests and restarts;
// SHA-256 collision resistance makes cross-actor aliasing impractical.
func principalIDFor(issuer, subject string) domain.PrincipalID {
	sum := sha256.Sum256([]byte("oidc\x00" + issuer + "\x00" + subject))
	return domain.PrincipalID(domain.PrefixPrincipal + "_" + hex.EncodeToString(sum[:]))
}

// keySet lazily establishes discovery and the key store, returning the
// current key set together with the store handle used to fetch it (the
// rotation path reuses it for forced refreshes). All IdP traffic runs under
// the caller's context bounded by the configured HTTP timeout.
func (a *OIDCBearer) keySet(ctx context.Context) (jwk.Set, *keyStore, *authFailure) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout.Duration)
	defer cancel()

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys == nil {
		doc, err := discover(ctx, a.client, a.cfg.IssuerURL)
		if err != nil {
			return nil, nil, &authFailure{code: "auth_provider_unavailable", result: ResultProviderUnavailable}
		}
		a.keys = newKeyStore(a.client, doc.JWKSURI, a.cfg.JWKSRefreshInterval.Duration)
	}
	set, err := a.keys.current(ctx, a.now())
	if err != nil {
		return nil, nil, &authFailure{code: "auth_provider_unavailable", result: ResultProviderUnavailable}
	}
	return set, a.keys, nil
}

// Ready reports whether the identity provider is reachable and serving keys,
// joining the /readyz dependency chain when OIDC is configured.
func (a *OIDCBearer) Ready(ctx context.Context) error {
	set, _, af := a.keySet(ctx)
	if af != nil {
		return fmt.Errorf("oidc provider unavailable: %s", af.code)
	}
	if set.Len() == 0 {
		return fmt.Errorf("oidc provider serves no signing keys")
	}
	return nil
}

// Issuer exposes the configured issuer for diagnostics.
func (a *OIDCBearer) Issuer() string { return a.cfg.IssuerURL }

// idpRedirectPolicy bounds redirect chains for IdP metadata traffic: at most
// ten hops, and every target must satisfy the same transport rule as the
// configured issuer — https anywhere, plaintext only for literal loopback
// hosts. This keeps a hostile or mis-pointed provider from walking the fetch
// onto a downgrade or an arbitrary internal endpoint.
func idpRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if req.URL.Scheme == "https" {
		return nil
	}
	if req.URL.Scheme == "http" && config.IsLoopbackHost(req.URL.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing redirect to %s: identity provider traffic must use https outside loopback", requestTarget(req.URL.String()))
}

// verifyToken checks the compact JWS signature against the key set without
// running claim validators: claims are classified by principalFromClaims so
// every failure mode has its own typed problem instead of a library error.
func verifyToken(token string, set jwk.Set) (jwt.Token, error) {
	return jwt.Parse(
		[]byte(token),
		jwt.WithValidate(false),
		jwt.WithKeySet(set, jws.WithRequireKid(true)),
	)
}

func slicesContain(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
