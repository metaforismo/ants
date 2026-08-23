// Package authn implements the OIDC resource-server authenticator
// (ADR-0019): strict bearer JWT verification against a configured identity
// provider, with tenant and subject derived exclusively from verified
// claims. It implements server.Authenticator so handlers stay unchanged
// behind the seam ADR-0004 established.
package authn

import (
	"net/http"

	"github.com/metaforismo/ants/internal/server"
)

// Result vocabulary for Observer, fixed by contract so metric label
// cardinality stays bounded (ADR-0014). Values pair 1:1 with rejection codes;
// identifiers and token material never become labels or log fields.
const (
	ResultAccepted            = "accepted"
	ResultRejectedMissing     = "rejected_missing"
	ResultRejectedHeader      = "rejected_header"
	ResultRejectedAlgorithm   = "rejected_algorithm"
	ResultRejectedSignature   = "rejected_signature"
	ResultRejectedMalformed   = "rejected_malformed"
	ResultRejectedExpired     = "rejected_expired"
	ResultRejectedNotYetValid = "rejected_not_yet_valid"
	ResultRejectedIssuer      = "rejected_issuer"
	ResultRejectedAudience    = "rejected_audience"
	ResultRejectedClaims      = "rejected_claims"
	ResultRejectedTenant      = "rejected_tenant"
	ResultProviderUnavailable = "provider_unavailable"
	ResultStoreUnavailable    = "store_unavailable"
)

// Observer records one authentication outcome per verification attempt.
type Observer interface {
	AuthToken(result string)
}

// compile-time proof that the OIDC bearer authenticator satisfies the seam
// ADR-0004 pinned; handlers depend on this interface only.
var _ server.Authenticator = (*OIDCBearer)(nil)

// maxTokenBytes bounds the accepted bearer credential. Real RS256 access
// tokens are a few KiB at most; anything larger is hostile input and must
// never reach signature processing.
const maxTokenBytes = 8 << 10

// allowedAlgorithms is the structural allowlist checked on the compact JWS
// header before any key material is selected. Only asymmetric RS256 is
// accepted, which removes algorithm-confusion attacks (none, HS*) from the
// protocol surface entirely.
const allowedAlgorithm = "RS256"

// extractBearer returns the raw token from an Authorization header carrying
// the Bearer scheme (case-insensitive per RFC 7235).
func extractBearer(r *http.Request) (string, *authFailure) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", &authFailure{code: "missing_bearer_token", result: ResultRejectedMissing}
	}
	const scheme = "bearer"
	if len(header) < len(scheme)+1 || !equalFold(header[:len(scheme)], scheme) || header[len(scheme)] != ' ' {
		return "", &authFailure{code: "invalid_authorization_header", result: ResultRejectedHeader}
	}
	token := header[len(scheme)+1:]
	if token == "" {
		return "", &authFailure{code: "invalid_authorization_header", result: ResultRejectedHeader}
	}
	if len(token) > maxTokenBytes {
		return "", &authFailure{code: "invalid_authorization_header", result: ResultRejectedHeader}
	}
	return token, nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// authFailure pairs a stable problem code with its metric result so the two
// vocabularies cannot drift apart.
type authFailure struct {
	code   string
	result string
}
