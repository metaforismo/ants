package ports

import (
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// Dead-letter operator contract (ants.store.outboxops.v1, ADR-0015).
//
// Dead letters are outbox messages that exhausted their delivery budget.
// Operators inspect them and either requeue (restart a bounded delivery
// lifecycle) or discard (explicit terminal decision; the row is never
// deleted by this feature — retention is a separate, deferred policy).
//
// Fencing: every mutation carries ExpectedGeneration, the row's generation
// read from a prior list/show. The generation increments on each transition
// into dead, requeued-pending, or discarded, so a credential from one epoch
// can never act on a newer retry or delivery transition. Mismatches are
// typed conflicts; wrong state is an invalid transition; unknown and
// foreign-tenant messages are uniform not-found (ADR-0004).
const (
	// MaxDeadLetterPageSize bounds one listing page.
	MaxDeadLetterPageSize = 200
	// maxOperatorFreeText bounds operator-supplied reason strings so audit
	// and event payloads stay bounded regardless of input.
	maxOperatorFreeText = 512
	// maxMessageIDLen bounds message identifiers accepted on operator paths.
	maxMessageIDLen = 128
)

// DeadLetterSummary describes one dead or discarded outbox message for
// operators. It intentionally excludes the envelope: raw event payloads stay
// in the store and are never echoed through operator output.
type DeadLetterSummary struct {
	ID          string                      `json:"id"`
	DedupKey    string                      `json:"dedup_key"`
	TenantID    domain.TenantID             `json:"tenant_id"`
	Status      domain.OutboxDeliveryStatus `json:"status"`
	Attempts    int                         `json:"attempts"`
	MaxAttempts int                         `json:"max_attempts"`
	Generation  int64                       `json:"generation"`
	Cause       string                      `json:"cause,omitempty"`
	CreatedAt   time.Time                   `json:"created_at"`
	DeadAt      *time.Time                  `json:"dead_at,omitempty"`
	DiscardedAt *time.Time                  `json:"discarded_at,omitempty"`
}

// ListDeadLettersRequest bounds one dead-letter page. AfterCreatedAt and
// AfterID form the keyset cursor (the previous page's last row); their zero
// values mean "from the start". They are echoes of prior responses used only
// for ordering — scheduling instants remain store-owned per ADR-0011.
type ListDeadLettersRequest struct {
	TenantID       domain.TenantID
	AfterCreatedAt time.Time
	AfterID        string
	Limit          int
}

// Validate rejects structurally unusable page requests before they reach a
// store.
func (r ListDeadLettersRequest) Validate() error {
	if err := validateTenantID(r.TenantID); err != nil {
		return err
	}
	if r.Limit < 1 || r.Limit > MaxDeadLetterPageSize {
		return domain.Invalidf("outbox_page_limit", "dead-letter page limit must be within [1,%d], got %d",
			MaxDeadLetterPageSize, r.Limit)
	}
	return nil
}

// OutboxMutationRequest carries one operator mutation with its fencing
// credential and provenance. Actor and reason land verbatim in the versioned
// event and the tenant-scoped audit record committed in the same unit.
type OutboxMutationRequest struct {
	TenantID  domain.TenantID
	MessageID string
	// ExpectedGeneration is the compare-and-swap credential read from a
	// prior list/show of this message.
	ExpectedGeneration int64
	Actor              domain.Actor
	Reason             string
	TraceID            string
}

// Validate rejects structurally unusable mutations before they reach a
// store. Dead rows always carry generation >= 1 because entering dead bumps
// it, so credentials below that bound cannot match anything.
func (r OutboxMutationRequest) Validate() error {
	if err := validateTenantID(r.TenantID); err != nil {
		return err
	}
	if err := validateMessageID(r.MessageID); err != nil {
		return err
	}
	if r.ExpectedGeneration < 1 {
		return domain.Invalidf("outbox_generation", "expected generation must be at least 1, got %d", r.ExpectedGeneration)
	}
	if err := r.Actor.Validate(); err != nil {
		return err
	}
	if len(r.Reason) > maxOperatorFreeText {
		return domain.Invalidf("outbox_reason", "reason must be at most %d characters", maxOperatorFreeText)
	}
	return nil
}

// OutboxMutationResult reports one applied mutation: the post-transition
// generation becomes the credential any FURTHER action must present.
type OutboxMutationResult struct {
	MessageID      string `json:"message_id"`
	Action         string `json:"action"`
	AttemptsBefore int    `json:"attempts_before"`
	Generation     int64  `json:"generation"`
}

func validateTenantID(id domain.TenantID) error {
	if _, err := domain.ParseTenantID(string(id)); err != nil {
		return domain.Invalidf("tenant_id", "tenant id %q is invalid", string(id))
	}
	return nil
}

// validateMessageID applies a loose structural check rather than the typed
// prefixed-ID validator: outbox identifiers are store-assigned composite
// keys ("obx_" + event id), not a domain entity prefix.
func validateMessageID(id string) error {
	switch {
	case id == "":
		return domain.Invalidf("outbox_message_id", "message id must not be empty")
	case len(id) > maxMessageIDLen:
		return domain.Invalidf("outbox_message_id", "message id must be at most %d characters", maxMessageIDLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return domain.Invalidf("outbox_message_id", "message id %q contains unsupported characters", id)
		}
	}
	return nil
}
