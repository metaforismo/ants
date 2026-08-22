package review

import (
	"strings"
	"testing"

	"github.com/metaforismo/ants/internal/domain"
)

func passingEvidence(criterion string) domain.Evidence {
	return domain.Evidence{Criterion: criterion, ExitCode: 0, Passed: true}
}

func TestReviewPassesWithCompleteEvidence(t *testing.T) {
	r := NewDeterministic(2000)
	v, err := r.Review(t.Context(), Input{
		Spec: domain.SpecContent{
			Outcome:         "feature",
			Requirements:    []string{"r"},
			SuccessCriteria: []string{"suite passes"},
		},
		Diff:     []byte("+++ b/file.sh\n+add() { echo $(($1 + $2)); }\n"),
		Evidence: []domain.Evidence{passingEvidence("suite passes")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Passed || len(v.Findings) != 0 {
		t.Fatalf("expected clean pass, got %+v", v)
	}
}

func TestReviewBlocksOnUnmetCriteria(t *testing.T) {
	r := NewDeterministic(2000)
	v, err := r.Review(t.Context(), Input{
		Spec: domain.SpecContent{
			Requirements:    []string{"r"},
			SuccessCriteria: []string{"suite passes"},
		},
		Evidence: []domain.Evidence{{Criterion: "suite passes", ExitCode: 1, Passed: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed || len(v.UnmetCriteria) != 1 {
		t.Fatalf("unmet criteria must block: %+v", v)
	}
	if v.Findings[0].Severity != domain.SeverityBlocker {
		t.Fatalf("verification gap must be a blocker")
	}
}

func TestReviewBlocksWithoutAnyCriteria(t *testing.T) {
	r := NewDeterministic(2000)
	v, _ := r.Review(t.Context(), Input{Spec: domain.SpecContent{Requirements: []string{"r"}}})
	if v.Passed {
		t.Fatalf("spec without success criteria must never pass")
	}
}

func TestReviewDetectsForbiddenPatterns(t *testing.T) {
	cases := map[string]string{
		"todo":    "+++ b/x.sh\n+// TODO finish this\n",
		"fixme":   "+++ b/x.sh\n+# FIXME broken\n",
		"privkey": "+++ b/key.pem\n+-----BEGIN RSA PRIVATE KEY-----\n",
		"awskey":  "+++ b/config\n+aws_id = AKIAIOSFODNN7EXAMPLE\n",
	}
	r := NewDeterministic(2000)
	for name, diff := range cases {
		v, err := r.Review(t.Context(), Input{
			Spec:     domain.SpecContent{Requirements: []string{"r"}},
			Diff:     []byte(diff),
			Evidence: nil,
		})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range v.Findings {
			if f.Category == "secret-leak" || f.Category == "unfinished-work" {
				if !strings.HasPrefix(f.Location, "diff:") {
					t.Errorf("%s: finding must carry a location", name)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("%s: forbidden pattern not detected in %q", name, diff)
		}
	}
}

func TestReviewWarnsOnOversizedDiff(t *testing.T) {
	var b strings.Builder
	b.WriteString("+++ b/big.txt\n")
	for range 50 {
		b.WriteString("+line\n")
	}
	r := NewDeterministic(10)
	v, _ := r.Review(t.Context(), Input{
		Spec:     domain.SpecContent{Requirements: []string{"r"}, SuccessCriteria: []string{"c"}},
		Diff:     []byte(b.String()),
		Evidence: []domain.Evidence{passingEvidence("c")},
	})
	warned := false
	for _, f := range v.Findings {
		if f.Category == "diff-size" && f.Severity == domain.SeverityWarning {
			warned = true
		}
	}
	// Warnings alone do not block.
	if !warned || !v.Passed {
		t.Fatalf("oversized diff should warn without blocking: %+v", v)
	}
}
