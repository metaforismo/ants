package ports

// The operator mutation request validates its optional provenance field
// against the shared correlation grammar (ADR-0018): durable event and audit
// records must never receive unvalidated operator bytes, and CLI
// --trace-id cannot drift from header acceptance rules.

import (
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/correlation"
	"github.com/metaforismo/ants/internal/domain"
)

func TestOutboxMutationRequestValidatesTraceIDGrammar(t *testing.T) {
	valid := func(trace string) OutboxMutationRequest {
		return OutboxMutationRequest{
			TenantID:           domain.TenantID("ten_contracttenant000000"),
			MessageID:          "obx_evt_abc",
			ExpectedGeneration: 1,
			Actor:              domain.Actor{Type: domain.PrincipalHuman, ID: "prn_operator000000000000"},
			TraceID:            trace,
		}
	}

	for _, trace := range []string{"", "tr-1", "trace-123", "run-start." + strings.Repeat("a", 32)} {
		if err := valid(trace).Validate(); err != nil {
			t.Errorf("trace %q is grammar-compatible and must pass: %v", trace, err)
		}
	}
	for _, trace := range []string{"bad id", "new\nline", `"quoted"`, strings.Repeat("a", correlation.MaxLength+1), "unicodeé"} {
		err := valid(trace).Validate()
		if domain.ErrKindOf(err) != domain.ErrKindInvalid {
			t.Errorf("trace %q must be rejected as invalid, got %v", trace, err)
		}
		if dom, ok := err.(*domain.Error); ok && dom.Code != "outbox_trace_id" {
			t.Errorf("rejection must carry the stable code, got %q", dom.Code)
		}
	}
}
