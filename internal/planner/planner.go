// Package planner defines the planning seam and its deterministic tranche 1
// implementation. The planner is read-only over repository facts: it matches
// a user request against capabilities the project explicitly declares in
// .ants/capabilities.yaml and produces a versioned spec plus a task graph.
// When no declared capability covers the request it reports blockers instead
// of guessing.
package planner

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metaforismo/ants/internal/domain"
)

// PlanInput carries everything a planner may look at. Repository files are a
// read-only snapshot; mutating them is exclusively the writers' job.
type PlanInput struct {
	TenantID  domain.TenantID
	ThreadID  domain.ThreadID
	Request   string
	RepoFiles map[string][]byte
}

// TaskTemplate is one unit of isolated work produced by planning. DependsOn
// names other templates in the same output; NormalizeOutput resolves the DAG,
// computes Depth, and assigns the canonical branch.
type TaskTemplate struct {
	Name           string            `json:"name"`
	Branch         string            `json:"branch"`
	CommitMessage  string            `json:"commit_message"`
	Writes         []string          `json:"writes"`
	DependsOn      []string          `json:"depends_on"`
	Depth          int               `json:"depth"`
	Files          map[string]string `json:"files"`
	VerifyCommands [][]string        `json:"verify_commands"`
}

// Output couples the spec content with the task graph derived from it.
type Output struct {
	Spec  domain.SpecContent `json:"spec"`
	Tasks []TaskTemplate     `json:"tasks"`

	// IntegratedVerification runs on the integration branch after all tasks
	// merge; these commands produce the spec's success-criteria evidence.
	IntegratedVerification [][]string `json:"integrated_verification"`
}

type Planner interface {
	Plan(ctx context.Context, input PlanInput) (*Output, error)
}

// Capability mirrors one entry of .ants/capabilities.yaml.
type Capability struct {
	ID              string           `yaml:"id"`
	RequestKeywords []string         `yaml:"request_keywords"`
	Spec            capabilitySpec   `yaml:"spec"`
	Tasks           []capabilityTask `yaml:"tasks"`
	VerifyAll       [][]string       `yaml:"verify_all"`
}

type capabilitySpec struct {
	Outcome         string   `yaml:"outcome"`
	Requirements    []string `yaml:"requirements"`
	Assumptions     []string `yaml:"assumptions"`
	NonGoals        []string `yaml:"non_goals"`
	SuccessCriteria []string `yaml:"success_criteria"`
	Blockers        []string `yaml:"blockers"`
}

type capabilityTask struct {
	Name           string            `yaml:"name"`
	Writes         []string          `yaml:"writes"`
	DependsOn      []string          `yaml:"depends_on"`
	CommitMessage  string            `yaml:"commit_message"`
	Files          map[string]string `yaml:"files"`
	VerifyCommands [][]string        `yaml:"verify"`
}

// Catalog is the parsed project-side declaration of supported change kinds.
type Catalog struct {
	Version      int          `yaml:"version"`
	Capabilities []Capability `yaml:"capabilities"`
}

const catalogPath = ".ants/capabilities.yaml"

// ParseCatalog reads the capability declaration from repo files. A missing or
// malformed catalog is an error: planning refuses to invent capabilities.
func ParseCatalog(files map[string][]byte) (*Catalog, error) {
	raw, ok := files[catalogPath]
	if !ok {
		return nil, domain.NotFoundf("capabilities catalog", catalogPath)
	}
	var cat Catalog
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cat); err != nil {
		return nil, domain.Invalidf("capabilities_catalog", "%s is malformed: %v", catalogPath, err)
	}
	if err := cat.validate(); err != nil {
		return nil, err
	}
	return &cat, nil
}

func (c *Catalog) validate() error {
	if c.Version != 1 {
		return domain.Invalidf("capabilities_catalog", "catalog version %d unsupported (want 1)", c.Version)
	}
	if len(c.Capabilities) == 0 {
		return domain.Invalidf("capabilities_catalog", "catalog declares no capabilities")
	}
	seen := map[string]bool{}
	for i := range c.Capabilities {
		capability := &c.Capabilities[i]
		capability.ID = strings.TrimSpace(capability.ID)
		if capability.ID == "" || seen[capability.ID] {
			return domain.Invalidf("capabilities_catalog", "capability ids must be unique and non-empty")
		}
		seen[capability.ID] = true
		if len(capability.RequestKeywords) == 0 {
			return domain.Invalidf("capabilities_catalog", "capability %q declares no request keywords", capability.ID)
		}
		for keywordIndex, keyword := range capability.RequestKeywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" {
				return domain.Invalidf("capabilities_catalog", "capability %q request keyword %d is empty", capability.ID, keywordIndex)
			}
			capability.RequestKeywords[keywordIndex] = keyword
		}
		if strings.TrimSpace(capability.Spec.Outcome) == "" {
			return domain.Invalidf("capabilities_catalog", "capability %q has no outcome", capability.ID)
		}
		if len(capability.Spec.SuccessCriteria) == 0 {
			return domain.Invalidf("capabilities_catalog", "capability %q has no success criteria", capability.ID)
		}
		if _, err := NormalizeOutput(outputForCapability(capability)); err != nil {
			if typed, ok := err.(*domain.Error); ok {
				return typed.WithDetail("capability_id", capability.ID)
			}
			return err
		}
	}
	return nil
}

// Deterministic implements Planner using only the catalog and the request.
type Deterministic struct{}

func NewDeterministic() *Deterministic { return &Deterministic{} }

var _ Planner = (*Deterministic)(nil)

func (d *Deterministic) Plan(_ context.Context, input PlanInput) (*Output, error) {
	cat, err := ParseCatalog(input.RepoFiles)
	if err != nil {
		return nil, err
	}
	request := strings.ToLower(input.Request)
	var candidates []*Capability
	for i := range cat.Capabilities {
		capability := &cat.Capabilities[i]
		matches := true
		for _, keyword := range capability.RequestKeywords {
			if !strings.Contains(request, strings.ToLower(keyword)) {
				matches = false
				break
			}
		}
		if matches {
			candidates = append(candidates, capability)
		}
	}
	switch len(candidates) {
	case 1:
		return d.render(candidates[0])
	case 0:
		return nil, &domain.Error{
			Kind:    domain.ErrKindInvalid,
			Code:    "plan_no_matching_capability",
			Message: fmt.Sprintf("request does not match any capability declared in %s", catalogPath),
			Details: map[string]any{"declared": declaredIDs(cat)},
		}
	default:
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.ID)
		}
		return nil, &domain.Error{
			Kind:    domain.ErrKindConflict,
			Code:    "plan_ambiguous_request",
			Message: "request matches multiple declared capabilities; disambiguate before execution",
			Details: map[string]any{"matched": ids},
		}
	}
}

func (d *Deterministic) render(capability *Capability) (*Output, error) {
	return NormalizeOutput(outputForCapability(capability))
}

func outputForCapability(capability *Capability) *Output {
	tasks := make([]TaskTemplate, 0, len(capability.Tasks))
	for _, task := range capability.Tasks {
		tasks = append(tasks, TaskTemplate{
			Name:           task.Name,
			CommitMessage:  task.CommitMessage,
			Writes:         task.Writes,
			DependsOn:      task.DependsOn,
			Files:          task.Files,
			VerifyCommands: task.VerifyCommands,
		})
	}
	return &Output{
		Spec: domain.SpecContent{
			Outcome:         capability.Spec.Outcome,
			Requirements:    capability.Spec.Requirements,
			Assumptions:     capability.Spec.Assumptions,
			NonGoals:        capability.Spec.NonGoals,
			SuccessCriteria: capability.Spec.SuccessCriteria,
			Blockers:        capability.Spec.Blockers,
		},
		Tasks:                  tasks,
		IntegratedVerification: capability.VerifyAll,
	}
}

// BranchForTask derives the stable branch assigned to one task name.
func BranchForTask(taskName string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, taskName)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "run"
	}
	return "ants/task-" + slug
}

func branchFor(taskName string) string { return BranchForTask(taskName) }

func declaredIDs(cat *Catalog) []string {
	out := make([]string, 0, len(cat.Capabilities))
	for _, capability := range cat.Capabilities {
		out = append(out, capability.ID)
	}
	return out
}
