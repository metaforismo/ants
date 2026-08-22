package domain

import (
	"time"
)

// SpecStatus tracks the approval lifecycle of a versioned specification.
type SpecStatus string

const (
	SpecDraft      SpecStatus = "draft"
	SpecApproved   SpecStatus = "approved"
	SpecRejected   SpecStatus = "rejected"
	SpecSuperseded SpecStatus = "superseded"
)

var AllSpecStatuses = []SpecStatus{SpecDraft, SpecApproved, SpecRejected, SpecSuperseded}

var specTransitions = transitionTable[SpecStatus]{
	SpecDraft:      {SpecApproved, SpecRejected, SpecSuperseded},
	SpecApproved:   {SpecSuperseded},
	SpecRejected:   {SpecDraft},
	SpecSuperseded: {},
}

func init() {
	if err := checkTransitionTable(AllSpecStatuses, specTransitions); err != nil {
		panic(err)
	}
}

func CanTransitionSpec(from, to SpecStatus) bool {
	return specTransitions.allows(from, to)
}

// SpecContent is the zero-guesswork handoff contract between planning and
// execution (plan section 6.4). Every field is explicit; a planner that cannot
// fill one honestly must leave it empty and raise a blocker instead.
type SpecContent struct {
	Outcome         string   `json:"outcome"`
	Assumptions     []string `json:"assumptions"`
	Requirements    []string `json:"requirements"`
	NonGoals        []string `json:"non_goals"`
	SuccessCriteria []string `json:"success_criteria"`
	Blockers        []string `json:"blockers"`
}

func (c SpecContent) Validate() error {
	if c.Outcome == "" {
		return Invalidf("spec_outcome", "spec outcome must not be empty")
	}
	if len(c.Requirements) == 0 {
		return Invalidf("spec_requirements", "spec must declare at least one requirement")
	}
	return nil
}

type Spec struct {
	ID        SpecID      `json:"id"`
	TenantID  TenantID    `json:"tenant_id"`
	ThreadID  ThreadID    `json:"thread_id"`
	Version   int         `json:"version"`
	Status    SpecStatus  `json:"status"`
	Content   SpecContent `json:"content"`
	CreatedAt time.Time   `json:"created_at"`
}

func NewSpec(id SpecID, tenantID TenantID, threadID ThreadID, version int, content SpecContent, now time.Time) (*Spec, error) {
	if _, err := ParseSpecID(string(id)); err != nil {
		return nil, err
	}
	if version < 1 {
		return nil, Invalidf("spec_version", "spec version must be >= 1")
	}
	if err := content.Validate(); err != nil {
		return nil, err
	}
	return &Spec{
		ID:        id,
		TenantID:  tenantID,
		ThreadID:  threadID,
		Version:   version,
		Status:    SpecDraft,
		Content:   content,
		CreatedAt: now.UTC(),
	}, nil
}

// Approve gates execution on an explicit approval decision.
func (s *Spec) Approve() error {
	if err := s.transition(SpecApproved); err != nil {
		return err
	}
	return nil
}

func (s *Spec) Reject() error    { return s.transition(SpecRejected) }
func (s *Spec) Supersede() error { return s.transition(SpecSuperseded) }

func (s *Spec) transition(next SpecStatus) error {
	if s.Status == next {
		return NewInvalidTransitionError(s.Status, next).WithDetail("reason", "state unchanged")
	}
	if !CanTransitionSpec(s.Status, next) {
		return NewInvalidTransitionError(s.Status, next)
	}
	s.Status = next
	return nil
}
