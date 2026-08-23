package authn

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
)

// forcedRefreshFloor is the minimum spacing between rotation-triggered
// (unknown-kid) JWKS fetches. Without it, a flood of forged kids would turn
// every request into an IdP fetch; with it, at most one request pays the
// refresh per floor window.
const forcedRefreshFloor = time.Second

// maxKeysetBytes bounds JWKS responses; real sets are a few KiB.
const maxKeysetBytes = 256 << 10

// keyStore caches the IdP's public key set. Refresh is deliberately lazy and
// synchronous — driven by readiness warm-up, TTL expiry on later requests,
// and unknown-kid rotation — so there are no background goroutines and no
// auth state that outlives a restart.
type keyStore struct {
	client *http.Client
	url    string
	ttl    time.Duration

	mu           sync.Mutex
	set          jwk.Set
	fetchedAt    time.Time
	lastForcedAt time.Time
}

func newKeyStore(client *http.Client, jwksURI string, ttl time.Duration) *keyStore {
	return &keyStore{client: client, url: jwksURI, ttl: ttl}
}

// current returns a fresh-enough key set, fetching synchronously when none is
// cached or the TTL has lapsed.
func (k *keyStore) current(ctx context.Context, now time.Time) (jwk.Set, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.set != nil && now.Sub(k.fetchedAt) < k.ttl {
		return k.set, nil
	}
	if err := k.fetch(ctx, now); err != nil {
		// A stale cached set still verifies signatures correctly; serving it
		// beats refusing all traffic because one refresh failed. Rotation
		// keeps working through refreshForced.
		if k.set != nil {
			return k.set, nil
		}
		return nil, err
	}
	return k.set, nil
}

// refreshForced bypasses the TTL for key rotation (a token whose kid is
// missing from the cached set), rate-limited to once per forcedRefreshFloor.
func (k *keyStore) refreshForced(ctx context.Context, now time.Time) (jwk.Set, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.lastForcedAt.IsZero() && now.Sub(k.lastForcedAt) < forcedRefreshFloor {
		if k.set != nil {
			return k.set, nil
		}
		return nil, fmt.Errorf("jwks fetch rate limited")
	}
	k.lastForcedAt = now
	if err := k.fetch(ctx, now); err != nil {
		if k.set != nil {
			return k.set, nil
		}
		return nil, err
	}
	return k.set, nil
}

// fetch performs the network call and parses the document as a JWK set,
// rejecting structurally ambiguous inputs before they can influence key
// selection. Callers must hold k.mu.
func (k *keyStore) fetch(ctx context.Context, now time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeysetBytes+1))
	if err != nil {
		return fmt.Errorf("read jwks: %w", err)
	}
	if len(body) > maxKeysetBytes {
		return fmt.Errorf("jwks exceeds %d bytes", maxKeysetBytes)
	}
	parsed, err := jwk.Parse(body)
	if err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}
	if parsed.Len() == 0 {
		return fmt.Errorf("jwks contains no keys")
	}
	if err := rejectDuplicateKids(parsed); err != nil {
		return err
	}
	k.set = parsed
	k.fetchedAt = now
	return nil
}

// rejectDuplicateKids refuses ambiguous key sets: a duplicated kid would let
// key selection depend on iteration order inside the JOSE library.
func rejectDuplicateKids(set jwk.Set) error {
	seen := map[string]int{}
	for i := 0; i < set.Len(); i++ {
		key, ok := set.Key(i)
		if !ok {
			continue
		}
		kid := key.KeyID()
		if kid == "" {
			continue
		}
		seen[kid]++
		if seen[kid] > 1 {
			return fmt.Errorf("jwks contains duplicate kid %q", kid)
		}
	}
	return nil
}
