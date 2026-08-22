// Package review implements the deterministic reviewer: it evaluates a diff
// and the recorded evidence against the spec's success criteria and emits
// structured findings. It is the tranche 1 ready gate; model-based reviewers
// plug into the same interface later.
package review

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/metaforismo/ants/internal/domain"
)

type Input struct {
	Spec     domain.SpecContent
	Diff     []byte
	Evidence []domain.Evidence
}

type Verdict struct {
	Passed        bool
	Findings      []domain.Finding
	UnmetCriteria []string
}

type Reviewer interface {
	Review(ctx context.Context, in Input) (*Verdict, error)
}

// Deterministic applies fixed checks. Every check names its failure scenario:
// a finding without one is noise, not evidence.
type Deterministic struct {
	maxDiffLines int
}

func NewDeterministic(maxDiffLines int) *Deterministic {
	if maxDiffLines <= 0 {
		maxDiffLines = 2000
	}
	return &Deterministic{maxDiffLines: maxDiffLines}
}

var _ Reviewer = (*Deterministic)(nil)

var forbiddenPatterns = []forbiddenPattern{
	{
		re:       regexp.MustCompile(`(?i)\bTODO\b|\bFIXME\b|\bHACK\b`),
		category: "unfinished-work",
		scenario: "the diff contains TODO/FIXME/HACK markers, so the change may be incomplete and silently shipped",
	},
	{
		re:       regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		category: "secret-leak",
		scenario: "a private key block appears in the diff; committing credentials exposes them to every reader of the repository",
	},
	{
		re:       regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		category: "secret-leak",
		scenario: "the diff contains an AWS access key id pattern",
	},
}

type forbiddenPattern struct {
	re       *regexp.Regexp
	category string
	scenario string
}

func (r *Deterministic) Review(_ context.Context, in Input) (*Verdict, error) {
	v := &Verdict{Passed: true}

	passedByCriterion := map[string]bool{}
	for _, ev := range in.Evidence {
		if ev.Passed {
			passedByCriterion[ev.Criterion] = true
		}
	}
	for _, criterion := range in.Spec.SuccessCriteria {
		if !passedByCriterion[criterion] {
			v.UnmetCriteria = append(v.UnmetCriteria, criterion)
		}
	}
	if len(in.Spec.SuccessCriteria) > 0 && len(v.UnmetCriteria) > 0 {
		v.Findings = append(v.Findings, domain.Finding{
			Category:   "verification-gap",
			Severity:   domain.SeverityBlocker,
			Confidence: domain.ConfidenceHigh,
			Location:   "evidence",
			Scenario:   "success criteria lack passing evidence, so 'done' would rest on agent confidence instead of observed facts",
		})
	}
	if len(in.Spec.SuccessCriteria) == 0 {
		v.Findings = append(v.Findings, domain.Finding{
			Category:   "spec-quality",
			Severity:   domain.SeverityBlocker,
			Confidence: domain.ConfidenceHigh,
			Location:   "spec",
			Scenario:   "the spec declares no success criteria, so completion cannot be verified at all",
		})
	}

	diffText := string(in.Diff)
	for _, pat := range forbiddenPatterns {
		loc := pat.find(diffText)
		if loc == "" {
			continue
		}
		v.Findings = append(v.Findings, domain.Finding{
			Category:   pat.category,
			Severity:   domain.SeverityBlocker,
			Confidence: domain.ConfidenceHigh,
			Location:   loc,
			Scenario:   pat.scenario,
		})
	}

	lines := countAddedLines(diffText)
	if lines > r.maxDiffLines {
		v.Findings = append(v.Findings, domain.Finding{
			Category:   "diff-size",
			Severity:   domain.SeverityWarning,
			Confidence: domain.ConfidenceMedium,
			Location:   "diff",
			Scenario:   "the diff adds more lines than the review budget covers, increasing the chance unreviewed behavior reaches the integration branch",
		})
	}

	for _, f := range v.Findings {
		if err := f.Validate(); err != nil {
			return nil, err
		}
		if f.Blocking() {
			v.Passed = false
		}
	}
	return v, nil
}

// find returns a coarse location ("diff:<line>") for the first match.
func (p forbiddenPattern) find(diff string) string {
	m := p.re.FindStringIndex(diff)
	if m == nil {
		return ""
	}
	line := strings.Count(diff[:m[0]], "\n") + 1
	return "diff:" + strconv.Itoa(line)
}

func countAddedLines(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			n++
		}
	}
	return n
}
