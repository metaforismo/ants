// Package outboxops composes operator dead-letter actions (ADR-0015): each
// requeue or discard applies its compare-and-swap row mutation and commits
// the versioned domain event, its durable outbox delivery, and the
// tenant-scoped audit record inside ONE unit of work, so an operator action
// can never land without its trail. It contains no authorization logic of
// its own; callers are trusted seams (today the local CLI), and remote
// operator APIs must bring real authenticated principals before reusing it.
package outboxops

import (
	"context"
	"fmt"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

const (
	// ActionRequeue and ActionDiscard name the bounded label vocabulary of
	// observable operator actions.
	ActionRequeue = "requeue"
	ActionDiscard = "discard"

	aggregateOutboxMessage = "outbox_message"
)

// Outcome is the closed vocabulary of observable mutation results. Values
// become Prometheus labels (via the observer) and must never embed tenant,
// message, or principal identifiers.
type Outcome string

const (
	OutcomeSucceeded       Outcome = "succeeded"
	OutcomeStaleCredential Outcome = "stale_credential"
	OutcomeInvalidState    Outcome = "invalid_state"
	OutcomeNotFound        Outcome = "not_found"
	OutcomeInvalidRequest  Outcome = "invalid_request"
	OutcomeFailed          Outcome = "failed"
)

// Observer receives operator action outcomes for instrumentation
// (ADR-0014 seam pattern). It stays Prometheus-free and primitive-typed so
// the metrics package can implement it structurally; outcome values come
// from the closed Outcome vocabulary. Implementations must be cheap and
// non-blocking; a nil observer disables instrumentation only.
type Observer interface {
	ActionRecorded(action string, outcome string)
}

// Deps wires the service over the persistence seams it composes.
type Deps struct {
	Outbox   ports.OutboxStore
	Events   ports.EventLog
	Audit    ports.AuditStore
	Tx       ports.Transactor
	IDs      ports.IDGenerator
	Clock    ports.Clock
	Observer Observer
}

type Service struct {
	outbox ports.OutboxStore
	events ports.EventLog
	audit  ports.AuditStore
	tx     ports.Transactor
	ids    ports.IDGenerator
	clock  ports.Clock
	obs    Observer
}

// New builds the operator service; every dependency except the observer is
// required so wiring mistakes fail at construction instead of mid-mutation.
func New(deps Deps) (*Service, error) {
	if deps.Outbox == nil || deps.Events == nil || deps.Audit == nil ||
		deps.Tx == nil || deps.IDs == nil || deps.Clock == nil {
		return nil, fmt.Errorf("outboxops: outbox, events, audit, transactor, ids and clock are required")
	}
	return &Service{
		outbox: deps.Outbox, events: deps.Events, audit: deps.Audit,
		tx: deps.Tx, ids: deps.IDs, clock: deps.Clock, obs: deps.Observer,
	}, nil
}

// ListDeadLetters pages actionable dead letters for one tenant behind the
// deterministic keyset cursor. Reads need no unit of work.
func (s *Service) ListDeadLetters(ctx context.Context, req ports.ListDeadLettersRequest) ([]ports.DeadLetterSummary, error) {
	return s.outbox.ListDeadLetters(ctx, req)
}

// GetDeadLetter returns one dead or discarded message; unknown and
// foreign-tenant messages are uniform not-found.
func (s *Service) GetDeadLetter(ctx context.Context, tenantID domain.TenantID, messageID string) (*ports.DeadLetterSummary, error) {
	return s.outbox.GetDeadLetter(ctx, tenantID, messageID)
}

// Requeue restarts a bounded delivery lifecycle for one dead letter.
func (s *Service) Requeue(ctx context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	return s.mutate(ctx, req, domain.OutboxPending)
}

// Discard records an explicit terminal operator decision for one dead
// letter; the row is retained as history.
func (s *Service) Discard(ctx context.Context, req ports.OutboxMutationRequest) (ports.OutboxMutationResult, error) {
	return s.mutate(ctx, req, domain.OutboxDiscarded)
}

func (s *Service) mutate(ctx context.Context, req ports.OutboxMutationRequest, target domain.OutboxDeliveryStatus) (ports.OutboxMutationResult, error) {
	action := ActionRequeue
	mutateFn := s.outbox.RequeueDeadLetter
	eventType := domain.EventOutboxDeadLetterRequeued
	if target == domain.OutboxDiscarded {
		action = ActionDiscard
		mutateFn = s.outbox.DiscardDeadLetter
		eventType = domain.EventOutboxDeadLetterDiscarded
	}

	var result ports.OutboxMutationResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		res, merr := mutateFn(ctx, req)
		if merr != nil {
			return merr
		}
		result = res

		evtID, gerr := s.ids.NewID(domain.PrefixEvent)
		if gerr != nil {
			return fmt.Errorf("outboxops: generate event id: %w", gerr)
		}
		eventData := map[string]any{
			"message_id":      req.MessageID,
			"action":          action,
			"attempts_before": res.AttemptsBefore,
			"generation":      res.Generation,
		}
		if req.Reason != "" {
			eventData["reason"] = req.Reason
		}
		if err := s.events.Append(ctx, &domain.Event{
			ID:               domain.EventID(evtID),
			Type:             eventType,
			TenantID:         req.TenantID,
			AggregateType:    aggregateOutboxMessage,
			AggregateID:      req.MessageID,
			AggregateVersion: res.Generation,
			Actor:            req.Actor,
			TraceID:          req.TraceID,
			Data:             eventData,
		}); err != nil {
			return fmt.Errorf("outboxops: append %s event: %w", action, err)
		}

		auditID, gerr := s.ids.NewID(domain.PrefixAuditEvent)
		if gerr != nil {
			return fmt.Errorf("outboxops: generate audit id: %w", gerr)
		}
		auditMeta := map[string]any{
			"action":          action,
			"attempts_before": res.AttemptsBefore,
			"generation":      res.Generation,
		}
		if req.Reason != "" {
			auditMeta["reason"] = req.Reason
		}
		if err := s.audit.Append(ctx, &domain.AuditEvent{
			ID:           domain.AuditEventID(auditID),
			TenantID:     req.TenantID,
			Actor:        req.Actor,
			Action:       auditAction(target),
			ResourceType: aggregateOutboxMessage,
			ResourceID:   req.MessageID,
			Result:       domain.AuditResultAllowed,
			TraceID:      req.TraceID,
			Metadata:     auditMeta,
			At:           s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("outboxops: append %s audit record: %w", action, err)
		}
		return nil
	})
	if s.obs != nil {
		s.obs.ActionRecorded(action, string(outcomeOf(err)))
	}
	return result, err
}

func auditAction(target domain.OutboxDeliveryStatus) domain.PolicyAction {
	if target == domain.OutboxDiscarded {
		return domain.ActionOutboxDiscard
	}
	return domain.ActionOutboxRequeue
}

// outcomeOf maps a mutation failure onto the closed observation vocabulary;
// unclassified errors never leak into labels beyond the fixed "failed".
func outcomeOf(err error) Outcome {
	if err == nil {
		return OutcomeSucceeded
	}
	switch domain.ErrKindOf(err) {
	case domain.ErrKindConflict:
		return OutcomeStaleCredential
	case domain.ErrKindInvalidTransition:
		return OutcomeInvalidState
	case domain.ErrKindNotFound:
		return OutcomeNotFound
	case domain.ErrKindInvalid:
		return OutcomeInvalidRequest
	default:
		return OutcomeFailed
	}
}
