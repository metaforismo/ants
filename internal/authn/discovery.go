package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/metaforismo/ants/internal/config"
)

// discoveryDoc is the subset of the OIDC discovery document the verifier
// consumes. Only fields with security meaning are modeled; everything else is
// ignored rather than parsed and forgotten.
type discoveryDoc struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// discover fetches {issuer}/.well-known/openid-configuration and enforces the
// properties that make discovery safe: the document's issuer must match the
// configured value byte-for-byte up to one optional trailing slash (OIDC
// Discovery section 4.3 — otherwise a hostile host could re-point trust), and
// jwks_uri must be an absolute URL obeying the same transport rule as the
// issuer itself: https everywhere, plaintext only for literal loopback hosts.
// Without that rule an https issuer's document could walk key fetching onto a
// plaintext remote endpoint.
func discover(ctx context.Context, client *http.Client, issuerURL string) (*discoveryDoc, error) {
	wellKnown := strings.TrimSuffix(issuerURL, "/") + "/.well-known/openid-configuration"
	var doc discoveryDoc
	if err := fetchJSON(ctx, client, wellKnown, &doc); err != nil {
		return nil, err
	}
	if doc.Issuer != issuerURL && doc.Issuer != strings.TrimSuffix(issuerURL, "/") {
		return nil, fmt.Errorf("discovery document issuer %q does not match configured issuer %q", doc.Issuer, issuerURL)
	}
	u, err := url.Parse(doc.JWKSURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("discovery document has no usable jwks_uri")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && config.IsLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("jwks_uri %s must use https outside loopback", requestTarget(doc.JWKSURI))
	}
	return &doc, nil
}

// maxDiscoveryBytes bounds IdP metadata responses; real documents are a few
// KiB.
const maxDiscoveryBytes = 256 << 10

func fetchJSON(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", requestTarget(url), err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", requestTarget(url), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: status %d", requestTarget(url), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", requestTarget(url), err)
	}
	if len(body) > maxDiscoveryBytes {
		return fmt.Errorf("response from %s exceeds %d bytes", requestTarget(url), maxDiscoveryBytes)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode %s: %w", requestTarget(url), err)
	}
	return nil
}

// requestTarget reduces an IdP URL to scheme+host for error text so failures
// name the endpoint class without echoing paths or query material.
func requestTarget(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	host, _, _ := strings.Cut(rest, "/")
	return scheme + "://" + host
}

// compactHeader is the protected header of a JWS compact serialization. Only
// fields with security meaning are modeled.
type compactHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// decodeCompactHeader parses the protected header of a JWS compact
// serialization. Only the first segment must be well-formed: the algorithm
// allowlist is enforced on it before anything else about the token matters.
func decodeCompactHeader(token string) (*compactHeader, error) {
	first, _, ok := strings.Cut(token, ".")
	if !ok || first == "" {
		return nil, fmt.Errorf("token is not a compact JWS")
	}
	raw, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		return nil, fmt.Errorf("token header is not base64url")
	}
	var hdr compactHeader
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return nil, fmt.Errorf("token header is not JSON")
	}
	return &hdr, nil
}

// requireCompleteCompact enforces the full three-segment shape with non-empty
// payload and signature. It runs after the algorithm allowlist so an unsigned
// `none` token is classified by its attack, not its truncation.
func requireCompleteCompact(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return fmt.Errorf("token is not a complete three-segment compact JWS")
	}
	return nil
}
