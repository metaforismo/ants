package policy

// Audit correlation (ADR-0018): audit records fill their trace_id slot from
// the ambient correlation carrier when one is being served, and stay empty
// for work outside any request.

import (
	"context"
	"testing"

	"github.com/metaforismo/ants/internal/correlation"
	"github.com/metaforismo/ants/internal/domain"
)

func TestAuditTraceIDFollowsRequestCarrier(t *testing.T) {
	engine, rec := newTestEngine(t, true)

	const served = "audit-corr.test-42"
	requestScoped := correlation.With(context.Background(), served)
	if _, err := engine.Authorize(requestScoped, baseRequest(domain.ActionSCMLocalCommit)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Authorize(context.Background(), baseRequest(domain.ActionSCMLocalCommit)); err != nil {
		t.Fatal(err)
	}

	events := rec.auditEvents()
	if len(events) != 2 {
		t.Fatalf("two decisions must produce two audit records, got %d", len(events))
	}
	if events[0].TraceID != served {
		t.Errorf("request-scoped audit must carry the served correlation, got %q", events[0].TraceID)
	}
	if events[1].TraceID != "" {
		t.Errorf("background audit must keep an empty trace id, got %q", events[1].TraceID)
	}
}
