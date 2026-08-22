package domain

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorKind is the closed taxonomy every domain error is classified into.
// HTTP mapping and retry classification derive from it; no error string
// matching is allowed anywhere in control flow.
type ErrorKind string

const (
	ErrKindInvalid           ErrorKind = "invalid"
	ErrKindUnauthorized      ErrorKind = "unauthorized"
	ErrKindForbidden         ErrorKind = "forbidden"
	ErrKindNotFound          ErrorKind = "not_found"
	ErrKindConflict          ErrorKind = "conflict"
	ErrKindInvalidTransition ErrorKind = "invalid_transition"
	ErrKindPolicyDenied      ErrorKind = "policy_denied"
	ErrKindBudgetExhausted   ErrorKind = "budget_exhausted"
	ErrKindCancelled         ErrorKind = "cancelled"
	ErrKindTimeout           ErrorKind = "timeout"
	ErrKindTransient         ErrorKind = "transient"
	ErrKindInternal          ErrorKind = "internal"
)

// ProblemTypeURIs are stable identifiers serialized in RFC 9457 responses.
const (
	problemTypeBase          = "https://ants.dev/problems/"
	ProblemInvalid           = problemTypeBase + "invalid"
	ProblemUnauthorized      = problemTypeBase + "unauthorized"
	ProblemForbidden         = problemTypeBase + "forbidden"
	ProblemNotFound          = problemTypeBase + "not-found"
	ProblemConflict          = problemTypeBase + "conflict"
	ProblemInvalidTransition = problemTypeBase + "invalid-transition"
	ProblemPolicyDenied      = problemTypeBase + "policy-denied"
	ProblemBudgetExhausted   = problemTypeBase + "budget-exhausted"
	ProblemCancelled         = problemTypeBase + "cancelled"
	ProblemTimeout           = problemTypeBase + "timeout"
	ProblemTransient         = problemTypeBase + "transient"
	ProblemInternal          = problemTypeBase + "internal"
)

func (k ErrorKind) ProblemType() string {
	switch k {
	case ErrKindInvalid:
		return ProblemInvalid
	case ErrKindUnauthorized:
		return ProblemUnauthorized
	case ErrKindForbidden:
		return ProblemForbidden
	case ErrKindNotFound:
		return ProblemNotFound
	case ErrKindConflict:
		return ProblemConflict
	case ErrKindInvalidTransition:
		return ProblemInvalidTransition
	case ErrKindPolicyDenied:
		return ProblemPolicyDenied
	case ErrKindBudgetExhausted:
		return ProblemBudgetExhausted
	case ErrKindCancelled:
		return ProblemCancelled
	case ErrKindTimeout:
		return ProblemTimeout
	case ErrKindTransient:
		return ProblemTransient
	default:
		return ProblemInternal
	}
}

func (k ErrorKind) HTTPStatus() int {
	switch k {
	case ErrKindInvalid:
		return http.StatusBadRequest
	case ErrKindUnauthorized:
		return http.StatusUnauthorized
	case ErrKindForbidden:
		return http.StatusForbidden
	case ErrKindNotFound:
		return http.StatusNotFound
	case ErrKindConflict:
		return http.StatusConflict
	case ErrKindInvalidTransition:
		return http.StatusUnprocessableEntity
	case ErrKindPolicyDenied:
		return http.StatusForbidden
	case ErrKindBudgetExhausted:
		return http.StatusForbidden
	case ErrKindCancelled:
		return http.StatusRequestTimeout
	case ErrKindTimeout:
		return http.StatusGatewayTimeout
	case ErrKindTransient:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Error carries a machine-readable Kind, a stable short Code for clients,
// and an optional cause. Details must never contain secrets or prompt bodies.
type Error struct {
	Kind    ErrorKind      `json:"kind"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	Cause   error          `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

func NewError(kind ErrorKind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func Wrap(kind ErrorKind, code, message string, cause error) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Cause: cause}
}

func Invalidf(code, format string, args ...any) *Error {
	return &Error{Kind: ErrKindInvalid, Code: code, Message: fmt.Sprintf(format, args...)}
}
func NotFoundf(entity string, id any) *Error {
	return &Error{Kind: ErrKindNotFound, Code: entity + "_not_found", Message: fmt.Sprintf("%s not found", entity), Details: map[string]any{"id": id}}
}
func Conflictf(code, format string, args ...any) *Error {
	return &Error{Kind: ErrKindConflict, Code: code, Message: fmt.Sprintf(format, args...)}
}
func Transientf(code, format string, args ...any) *Error {
	return &Error{Kind: ErrKindTransient, Code: code, Message: fmt.Sprintf(format, args...)}
}
func Internalf(cause error, code, format string, args ...any) *Error {
	return &Error{Kind: ErrKindInternal, Code: code, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// ErrKindOf reports the taxonomy kind of err, defaulting to internal for
// foreign errors so unclassified failures never look retryable or client-side.
func ErrKindOf(err error) ErrorKind {
	var dom *Error
	if errors.As(err, &dom) {
		return dom.Kind
	}
	return ErrKindInternal
}

func IsRetryable(err error) bool {
	switch ErrKindOf(err) {
	case ErrKindTransient, ErrKindTimeout:
		return true
	default:
		return false
	}
}

// StaleStateError marks optimistic-concurrency conflicts on persisted state.
func NewStaleVersionError(entity string, id, expected, actual any) *Error {
	return &Error{
		Kind:    ErrKindConflict,
		Code:    "stale_version",
		Message: fmt.Sprintf("%s was modified concurrently", entity),
		Details: map[string]any{"id": id, "expected_version": expected, "actual_version": actual},
	}
}

func NewInvalidTransitionError[S ~string](from, to S) *Error {
	return &Error{
		Kind:    ErrKindInvalidTransition,
		Code:    "invalid_state_transition",
		Message: fmt.Sprintf("transition %s -> %s is not allowed", from, to),
		Details: map[string]any{"from": string(from), "to": string(to)},
	}
}
