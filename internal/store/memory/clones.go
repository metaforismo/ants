package memory

import (
	"github.com/metaforismo/ants/internal/domain"
)

// Clone-on-read/write keeps stored entities isolated from caller mutation so
// optimistic version checks observe real state transitions only.

func cloneTenant(t *domain.Tenant) *domain.Tenant {
	out := *t
	return &out
}

func cloneProject(p *domain.Project) *domain.Project {
	out := *p
	return &out
}

func cloneThread(t *domain.Thread) *domain.Thread {
	out := *t
	return &out
}

func cloneMessage(m *domain.Message) *domain.Message {
	out := *m
	return &out
}

func cloneSpec(s *domain.Spec) *domain.Spec {
	out := *s
	out.Content.Assumptions = cloneStrings(s.Content.Assumptions)
	out.Content.Requirements = cloneStrings(s.Content.Requirements)
	out.Content.NonGoals = cloneStrings(s.Content.NonGoals)
	out.Content.SuccessCriteria = cloneStrings(s.Content.SuccessCriteria)
	out.Content.Blockers = cloneStrings(s.Content.Blockers)
	return &out
}

func cloneTask(t *domain.Task) *domain.Task {
	out := *t
	out.DependsOn = cloneTaskIDs(t.DependsOn)
	if t.Failure != nil {
		f := *t.Failure
		out.Failure = &f
	}
	return &out
}

func cloneRun(r *domain.Run) *domain.Run {
	out := *r
	out.TaskIDs = cloneTaskIDs(r.TaskIDs)
	if r.FinishedAt != nil {
		ts := *r.FinishedAt
		out.FinishedAt = &ts
	}
	if r.Failure != nil {
		f := *r.Failure
		out.Failure = &f
	}
	return &out
}

func cloneWorkspace(w *domain.Workspace) *domain.Workspace {
	out := *w
	return &out
}

func cloneArtifact(a *domain.Artifact) *domain.Artifact {
	out := *a
	out.Content = append([]byte(nil), a.Content...)
	return &out
}

func clonePolicyDecision(d *domain.PolicyDecision) *domain.PolicyDecision {
	out := *d
	return &out
}

func cloneAuditEvent(e *domain.AuditEvent) *domain.AuditEvent {
	out := *e
	if e.Metadata != nil {
		out.Metadata = make(map[string]any, len(e.Metadata))
		for k, v := range e.Metadata {
			out.Metadata[k] = v
		}
	}
	return &out
}

func cloneIntegration(c *domain.IntegrationConnection) *domain.IntegrationConnection {
	out := *c
	out.Scopes = cloneStrings(c.Scopes)
	return &out
}

func cloneRunClaim(c *domain.RunClaim) *domain.RunClaim {
	out := *c
	if c.AcquiredAt != nil {
		ts := *c.AcquiredAt
		out.AcquiredAt = &ts
	}
	if c.HeartbeatAt != nil {
		ts := *c.HeartbeatAt
		out.HeartbeatAt = &ts
	}
	if c.ExpiresAt != nil {
		ts := *c.ExpiresAt
		out.ExpiresAt = &ts
	}
	return &out
}

func cloneEvent(e *domain.Event) *domain.Event {
	out := *e
	if e.Data != nil {
		out.Data = make(map[string]any, len(e.Data))
		for k, v := range e.Data {
			out.Data[k] = v
		}
	}
	return &out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneTaskIDs(in []domain.TaskID) []domain.TaskID {
	if in == nil {
		return nil
	}
	out := make([]domain.TaskID, len(in))
	copy(out, in)
	return out
}
