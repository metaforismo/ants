// Package correlation is the single owner of the correlation-identifier
// vocabulary shared by request logs, event trace_id slots, audit records,
// and operator provenance (ADR-0017/0018): one grammar, one typed carrier,
// one precedence rule. It imports nothing internal, so every layer — server,
// orchestration, policy, ports, CLI — can depend on it without cycles.
//
// The carrier travels inside the standard context using an unexported key,
// mirroring the unit-of-work transaction (ADR-0010). Only the HTTP
// middleware produces it, and only after resolving the effective identifier
// at the trust boundary; emission seams consume it through TraceID.
package correlation

import "context"

// MaxLength bounds accepted identifiers; the grammar admits external
// systems' ids (UUIDs, foreign trace ids) while refusing control characters,
// whitespace, quotes, and oversized input.
const MaxLength = 128

// ID is a validated correlation identifier. Values are checked at the trust
// boundary (HTTP header acceptance or operator-input validation) and are
// opaque afterwards; no code path may construct one from unvalidated bytes.
type ID string

// Valid reports whether v satisfies the shared acceptance grammar:
// 1–MaxLength characters of [A-Za-z0-9._~:@-] (ADR-0017). This function is
// the only copy of the grammar; drift between header echo, event trace_id,
// audit correlation, and operator --trace-id is structurally impossible.
func Valid(v string) bool {
	if v == "" || len(v) > MaxLength {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '~' || r == ':' || r == '@' || r == '-':
		default:
			return false
		}
	}
	return true
}

type contextKey struct{}

// With returns a context carrying id. Callers must pass a Valid identifier;
// the middleware guarantees this by construction.
func With(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// From returns the correlation identifier ctx carries, if any. Contexts
// outside HTTP request scope (worker execution, dispatch, retention, demo,
// plain CLI runs) carry none — their durable records keep empty trace ids.
func From(ctx context.Context) (ID, bool) {
	id, ok := ctx.Value(contextKey{}).(ID)
	return id, ok && id != ""
}

// TraceID resolves the value for a record's trace_id field: an explicitly
// set value always wins over the ambient carrier (operator-declared
// provenance must never be overwritten), and work outside any request
// leaves the field empty rather than fabricating an identity.
func TraceID(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if id, ok := From(ctx); ok {
		return string(id)
	}
	return ""
}
