package memory

import (
	"context"
	"sort"

	"github.com/metaforismo/ants/internal/domain"
)

type ThreadRepository struct{ st *storeState }

func (r *ThreadRepository) Create(_ context.Context, thread *domain.Thread) error {
	unlock := lockWrite(r.st)
	defer unlock()
	p, ok := r.st.projects[thread.ProjectID]
	if !ok || p.TenantID != thread.TenantID {
		return domain.Invalidf("thread_project_unknown", "thread references unknown project %s", thread.ProjectID)
	}
	if _, ok := r.st.threads[thread.ID]; ok {
		return domain.Conflictf("thread_exists", "thread %s already exists", thread.ID)
	}
	r.st.threads[thread.ID] = cloneThread(thread)
	return nil
}

func (r *ThreadRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.ThreadID) (*domain.Thread, error) {
	unlock := lockRead(r.st)
	defer unlock()
	t, ok := r.st.threads[id]
	if !ok || t.TenantID != tenantID {
		return nil, notFound("thread", id)
	}
	return cloneThread(t), nil
}

func (r *ThreadRepository) Update(_ context.Context, thread *domain.Thread, expectedVersion int64) error {
	unlock := lockWrite(r.st)
	defer unlock()
	cur, ok := r.st.threads[thread.ID]
	if !ok || cur.TenantID != thread.TenantID {
		return notFound("thread", thread.ID)
	}
	if cur.Version != expectedVersion {
		return domain.NewStaleVersionError("thread", thread.ID, expectedVersion, cur.Version)
	}
	stored := cloneThread(thread)
	stored.Version = cur.Version + 1
	r.st.threads[thread.ID] = stored
	thread.Version = stored.Version
	return nil
}

func (r *ThreadRepository) AppendMessage(_ context.Context, message *domain.Message) error {
	unlock := lockWrite(r.st)
	defer unlock()
	t, ok := r.st.threads[message.ThreadID]
	if !ok || t.TenantID != message.TenantID {
		return notFound("thread", message.ThreadID)
	}
	message.Seq = int64(len(r.st.messages[message.ThreadID]) + 1)
	r.st.messages[message.ThreadID] = append(r.st.messages[message.ThreadID], cloneMessage(message))
	return nil
}

func (r *ThreadRepository) Messages(_ context.Context, tenantID domain.TenantID, threadID domain.ThreadID, afterSeq int64, limit int) ([]*domain.Message, int64, error) {
	unlock := lockRead(r.st)
	defer unlock()
	t, ok := r.st.threads[threadID]
	if !ok || t.TenantID != tenantID {
		return nil, 0, notFound("thread", threadID)
	}
	all := r.st.messages[threadID]
	totalSeq := int64(len(all))
	out := make([]*domain.Message, 0, len(all))
	for _, m := range all {
		if m.Seq <= afterSeq {
			continue
		}
		out = append(out, cloneMessage(m))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, totalSeq, nil
}

func (r *ThreadRepository) ListByTenant(_ context.Context, tenantID domain.TenantID, limit int) ([]*domain.Thread, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := make([]*domain.Thread, 0, len(r.st.threads))
	for _, t := range r.st.threads {
		if t.TenantID != tenantID {
			continue
		}
		out = append(out, cloneThread(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type SpecRepository struct{ st *storeState }

func (r *SpecRepository) Create(_ context.Context, spec *domain.Spec) error {
	unlock := lockWrite(r.st)
	defer unlock()
	t, ok := r.st.threads[spec.ThreadID]
	if !ok || t.TenantID != spec.TenantID {
		return domain.Invalidf("spec_thread_unknown", "spec references unknown thread %s", spec.ThreadID)
	}
	if _, ok := r.st.specs[spec.ID]; ok {
		return domain.Conflictf("spec_exists", "spec %s already exists", spec.ID)
	}
	r.st.specs[spec.ID] = cloneSpec(spec)
	return nil
}

func (r *SpecRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.SpecID) (*domain.Spec, error) {
	unlock := lockRead(r.st)
	defer unlock()
	s, ok := r.st.specs[id]
	if !ok || s.TenantID != tenantID {
		return nil, notFound("spec", id)
	}
	return cloneSpec(s), nil
}

func (r *SpecRepository) LatestForThread(_ context.Context, tenantID domain.TenantID, threadID domain.ThreadID) (*domain.Spec, error) {
	unlock := lockRead(r.st)
	defer unlock()
	var latest *domain.Spec
	for _, s := range r.st.specs {
		if s.ThreadID != threadID || s.TenantID != tenantID {
			continue
		}
		if latest == nil || s.Version > latest.Version {
			latest = s
		}
	}
	if latest == nil {
		return nil, notFound("spec", threadID)
	}
	return cloneSpec(latest), nil
}
