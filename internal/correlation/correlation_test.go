package correlation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/correlation"
)

func TestValidGrammar(t *testing.T) {
	cases := map[string]bool{
		"req_abc123XYZ09":                            true,
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301":       true,
		"github.run:9981@worker.v2":                  true,
		"a.b_c~d:e@f-g":                              true,
		strings.Repeat("a", correlation.MaxLength):   true,
		strings.Repeat("a", correlation.MaxLength+1): false,
		"":          false,
		"has space": false,
		"new\nline": false,
		"tab\there": false,
		"sl/ash":    false,
		"quote\"id": false,
		"unicodeé":  false,
	}
	for input, want := range cases {
		if got := correlation.Valid(input); got != want {
			t.Errorf("Valid(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	if _, ok := correlation.From(context.Background()); ok {
		t.Error("background context must carry no correlation")
	}

	const id correlation.ID = "external-trace.42"
	ctx := correlation.With(context.Background(), id)
	got, ok := correlation.From(ctx)
	if !ok || got != id {
		t.Fatalf("From(With(ctx)) = %q,%v want %q,true", got, ok, id)
	}

	// Derived contexts keep the carrier; sibling contexts stay isolated.
	if derived, _ := correlation.From(context.WithValue(ctx, struct{}{}, "x")); derived != id {
		t.Errorf("derived context lost its carrier: %q", derived)
	}
	if _, ok := correlation.From(context.Background()); ok {
		t.Error("unrelated context must not observe another context's carrier")
	}
}

func TestTraceIDPrecedence(t *testing.T) {
	ctx := correlation.With(context.Background(), "req_carryme000000000000001")

	if got := correlation.TraceID(ctx, "operator-set"); got != "operator-set" {
		t.Errorf("explicit value must win over the carrier, got %q", got)
	}
	if got := correlation.TraceID(ctx, ""); got != "req_carryme000000000000001" {
		t.Errorf("empty explicit value must fall through to the carrier, got %q", got)
	}
	if got := correlation.TraceID(context.Background(), ""); got != "" {
		t.Errorf("no carrier and no explicit value must yield empty trace id, got %q", got)
	}
	if got := correlation.TraceID(context.Background(), "kept"); got != "kept" {
		t.Errorf("explicit value must survive without a carrier, got %q", got)
	}
}
