package domain

import (
	"strings"
	"testing"
	"time"
)

func TestRunClaimStateMachine(t *testing.T) {
	if !CanTransitionRunClaim(ClaimRunnable, ClaimClaimed) || !CanTransitionRunClaim(ClaimClaimed, ClaimRunnable) {
		t.Fatalf("claims must cycle between runnable and claimed")
	}
	for _, illegal := range [][2]RunClaimStatus{
		{ClaimRunnable, ClaimRunnable},
		{ClaimClaimed, ClaimClaimed},
	} {
		if CanTransitionRunClaim(illegal[0], illegal[1]) {
			t.Errorf("self-transition %s -> %s must be rejected", illegal[0], illegal[1])
		}
	}
}

func TestNewRunClaimTokenShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		token, err := NewRunClaimToken()
		if err != nil {
			t.Fatalf("mint token: %v", err)
		}
		if len(token) != ClaimTokenLength {
			t.Fatalf("token length %d, want %d", len(token), ClaimTokenLength)
		}
		if seen[token] {
			t.Fatalf("token repeated across mints")
		}
		seen[token] = true
	}
}

func TestValidateClaimOwner(t *testing.T) {
	if err := ValidateClaimOwner("worker-1"); err != nil {
		t.Fatalf("valid owner rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		strings.Repeat("w", MaxClaimOwnerLen+1),
		"control\x00char",
		"bell\x07",
	} {
		if err := ValidateClaimOwner(bad); ErrKindOf(err) != ErrKindInvalid {
			t.Errorf("owner %q must be invalid, got %v", bad, err)
		}
	}
}

func TestValidateFencing(t *testing.T) {
	valid := &RunClaim{Status: ClaimClaimed, Owner: "w1", Token: strings.Repeat("A", ClaimTokenLength), Generation: 3}
	if err := valid.ValidateFencing(); err != nil {
		t.Fatalf("valid fencing rejected: %v", err)
	}
	cases := []*RunClaim{
		{Status: ClaimRunnable, Token: strings.Repeat("A", ClaimTokenLength), Generation: 1}, // no owner
		{Status: ClaimClaimed, Owner: "w1", Token: "short", Generation: 1},
		{Status: ClaimClaimed, Owner: "w1", Token: strings.Repeat("A", ClaimTokenLength), Generation: 0},
	}
	for i, c := range cases {
		if err := c.ValidateFencing(); ErrKindOf(err) != ErrKindInvalid {
			t.Errorf("case %d must be invalid, got %v", i, err)
		}
	}
	if !valid.Matches("w1", valid.Token, 3) {
		t.Fatalf("matching credentials must match")
	}
	for _, mismatch := range [][3]any{
		{"other", valid.Token, int64(3)},
		{"w1", strings.Repeat("B", ClaimTokenLength), int64(3)},
		{"w1", valid.Token, int64(4)},
	} {
		owner := mismatch[0].(string)
		token := mismatch[1].(string)
		gen := mismatch[2].(int64)
		if valid.Matches(owner, token, gen) {
			t.Errorf("credentials (%s,%s…,%d) must not match", owner, token[:4], gen)
		}
	}
	runnable := &RunClaim{Status: ClaimRunnable, Owner: "w1", Token: valid.Token, Generation: 3}
	if runnable.Matches("w1", valid.Token, 3) {
		t.Fatalf("runnable claims match nothing")
	}
}

func TestClaimExpiryArithmetic(t *testing.T) {
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if got := ClaimExpiry(now, time.Minute); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("normal lease arithmetic broken: %v", got)
	}
	saturated := ClaimExpiry(now, 1<<62)
	if !saturated.After(now.Add(time.Hour)) {
		t.Fatalf("extreme lease must saturate into the future, got %v", saturated)
	}
	at := now.Add(time.Minute)
	if ClaimExpired(&at, now) {
		t.Fatalf("future deadline must not be expired")
	}
	if !ClaimExpired(&at, at) {
		t.Fatalf("deadline reached means expired")
	}
	if ClaimExpired(nil, now) {
		t.Fatalf("nil deadline is never expired")
	}
}
