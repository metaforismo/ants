package domain

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// RunClaim is the tenant-scoped execution lease for one run. It is a
// separate aggregate from Run by design (ADR-0012): Run.Status describes the
// pipeline stage and never encodes ownership, so claiming, expiry, and
// fencing cannot corrupt run lifecycle state.
//
// Fencing model: every successful acquisition — first claim or reclaim of an
// expired lease — increments Generation and Attempts and mints a fresh opaque
// Token. A holder may act on a claim only with the exact credential tuple
// (owner, token, generation) of its own epoch; any mismatch is a typed
// conflict, never a silent success.
type RunClaim struct {
	TenantID TenantID       `json:"tenant_id"`
	RunID    RunID          `json:"run_id"`
	Status   RunClaimStatus `json:"status"`
	// Owner identifies the current holder; empty while runnable.
	Owner string `json:"owner,omitempty"`
	// Token is the bearer secret minted at acquisition time. It is returned
	// only to the acquirer; read paths redact it.
	Token string `json:"-"`
	// Generation is the fencing epoch: it starts at 0 on creation and grows
	// by one per acquisition, strictly monotonically.
	Generation int64 `json:"generation"`
	// Attempts counts acquisitions, including reclaims after expiry.
	Attempts int `json:"attempts"`
	// AcquiredAt / HeartbeatAt / ExpiresAt are owned by the store clock;
	// callers never supply them. They are NULL while runnable.
	AcquiredAt  *time.Time `json:"acquired_at,omitempty"`
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type RunClaimStatus string

const (
	ClaimRunnable RunClaimStatus = "runnable"
	ClaimClaimed  RunClaimStatus = "claimed"
)

var AllRunClaimStatuses = []RunClaimStatus{
	ClaimRunnable,
	ClaimClaimed,
}

// runClaimTransitions encodes the closed claim machine:
// runnable ⇄ claimed. Terminality is deletion of the row itself (complete /
// terminal cleanup), not a third status, so no zombie rows can be resurrected
// by recovery.
var runClaimTransitions = transitionTable[RunClaimStatus]{
	ClaimRunnable: {ClaimClaimed},
	ClaimClaimed:  {ClaimRunnable},
}

func init() {
	if err := checkTransitionTable(AllRunClaimStatuses, runClaimTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionRunClaim(from, to RunClaimStatus) bool {
	return runClaimTransitions.allows(from, to)
}

func RunClaimEdgesFrom(from RunClaimStatus) []RunClaimStatus {
	return runClaimTransitions.edgesFrom(from)
}

const (
	// MaxClaimOwnerLen bounds worker identity strings supplied to claim ports.
	MaxClaimOwnerLen = 256
	// claimTokenEntropyBytes yields 43 base64url characters; guessing one
	// token is ~2^-256.
	claimTokenEntropyBytes = 32
	// ClaimTokenLength is the exact encoded length of a valid token.
	ClaimTokenLength = 43
)

// NewRunClaimToken mints an unguessable opaque bearer token for one
// acquisition epoch. Both store adapters use this single source so tokens are
// adapter-independent.
func NewRunClaimToken() (string, error) {
	buf := make([]byte, claimTokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", Internalf(err, "run_claim_token", "generate run claim token entropy")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ValidateClaimOwner enforces the shape of caller-supplied owner identities.
func ValidateClaimOwner(owner string) error {
	if owner == "" {
		return Invalidf("run_claim_owner", "claim owner must not be empty")
	}
	if len(owner) > MaxClaimOwnerLen {
		return Invalidf("run_claim_owner", "claim owner longer than %d characters", MaxClaimOwnerLen)
	}
	for _, r := range owner {
		if r < 0x20 || r == 0x7f {
			return Invalidf("run_claim_owner", "claim owner must not contain control characters")
		}
	}
	return nil
}

// ValidateFencing checks that a credential reference is structurally usable
// before it reaches a store; semantic correctness is decided against the
// stored row.
func (c *RunClaim) ValidateFencing() error {
	if err := ValidateClaimOwner(c.Owner); err != nil {
		return err
	}
	if len(c.Token) != ClaimTokenLength {
		return Invalidf("run_claim_token", "claim token must be %d characters", ClaimTokenLength)
	}
	if c.Generation < 1 {
		return Invalidf("run_claim_generation", "claim generation must be at least 1, got %d", c.Generation)
	}
	return nil
}

// Matches reports whether c carries exactly the credentials of one
// acquisition epoch.
func (c *RunClaim) Matches(owner, token string, generation int64) bool {
	return c.Status == ClaimClaimed && c.Owner == owner && c.Token == token && c.Generation == generation
}

// ClaimExpiry computes a lease deadline from the store clock. Extreme
// durations saturate at the far future instead of wrapping into the past,
// which would instantly expire the claim it was meant to protect.
func ClaimExpiry(now time.Time, lease time.Duration) time.Time {
	later := now.Add(lease)
	if !later.After(now) {
		return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	return later
}

// ClaimExpired reports whether an existing deadline has lapsed at now.
func ClaimExpired(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && !expiresAt.After(now)
}
