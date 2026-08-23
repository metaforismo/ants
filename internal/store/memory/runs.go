package memory

import (
	"context"
	"sort"

	"github.com/metaforismo/ants/internal/domain"
)

type RunRepository struct{ st *storeState }

func (r *RunRepository) Create(_ context.Context, run *domain.Run) error {
	unlock := lockWrite(r.st)
	defer unlock()
	t, ok := r.st.threads[run.ThreadID]
	if !ok || t.TenantID != run.TenantID {
		return domain.Invalidf("run_thread_unknown", "run references unknown thread %s", run.ThreadID)
	}
	key := idemKey{tenant: run.TenantID, thread: run.ThreadID, key: run.IdempotencyKey}
	if _, exists := r.st.runIdemKeys[key]; exists {
		return domain.Conflictf("run_idempotency_key_taken", "idempotency key already used for this thread")
	}
	if _, exists := r.st.runs[run.ID]; exists {
		return domain.Conflictf("run_exists", "run %s already exists", run.ID)
	}
	// The per-thread sequence is allocated under the same write lock as the
	// insert, so it is dense and strictly increasing in insertion order —
	// the append-stable key run-history pagination orders by.
	stored := cloneRun(run)
	r.st.runSeqs[run.ThreadID]++
	run.Seq = r.st.runSeqs[run.ThreadID]
	stored.Seq = run.Seq
	r.st.runIdemKeys[key] = run.ID
	r.st.runs[run.ID] = stored
	return nil
}

func (r *RunRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.RunID) (*domain.Run, error) {
	unlock := lockRead(r.st)
	defer unlock()
	run, ok := r.st.runs[id]
	if !ok || run.TenantID != tenantID {
		return nil, notFound("run", id)
	}
	return cloneRun(run), nil
}

func (r *RunRepository) Update(_ context.Context, run *domain.Run, expectedVersion int64) error {
	unlock := lockWrite(r.st)
	defer unlock()
	cur, ok := r.st.runs[run.ID]
	if !ok || cur.TenantID != run.TenantID {
		return notFound("run", run.ID)
	}
	if cur.Version != expectedVersion {
		return domain.NewStaleVersionError("run", run.ID, expectedVersion, cur.Version)
	}
	stored := cloneRun(run)
	stored.Version = cur.Version + 1
	r.st.runs[run.ID] = stored
	run.Version = stored.Version
	return nil
}

func (r *RunRepository) GetByIdempotencyKey(_ context.Context, tenantID domain.TenantID, threadID domain.ThreadID, key string) (*domain.Run, error) {
	unlock := lockRead(r.st)
	defer unlock()
	id, ok := r.st.runIdemKeys[idemKey{tenant: tenantID, thread: threadID, key: key}]
	if !ok {
		return nil, notFound("run", key)
	}
	run := r.st.runs[id]
	if run == nil || run.TenantID != tenantID || run.ThreadID != threadID {
		return nil, notFound("run", id)
	}
	return cloneRun(run), nil
}

// ListByThread serves one keyset page of the thread's run history, oldest
// first in the store-assigned per-thread sequence order (true creation
// order). `after` is a sequence value: only runs whose Seq is strictly
// greater are returned. Because sequences are allocated once at insert time
// and never change, concurrent or backdated creations can never reorder
// entries a reader has already consumed.
func (r *RunRepository) ListByThread(_ context.Context, tenantID domain.TenantID, threadID domain.ThreadID, after int64, limit int) ([]*domain.Run, int64, error) {
	unlock := lockRead(r.st)
	defer unlock()
	t, ok := r.st.threads[threadID]
	if !ok || t.TenantID != tenantID {
		return nil, 0, notFound("thread", threadID)
	}
	owned := make([]*domain.Run, 0, len(r.st.runs))
	for _, run := range r.st.runs {
		if run.ThreadID == threadID && run.TenantID == tenantID {
			owned = append(owned, run)
		}
	}
	total := int64(len(owned))
	sort.Slice(owned, func(i, j int) bool { return owned[i].Seq < owned[j].Seq })
	out := make([]*domain.Run, 0, len(owned))
	for _, run := range owned {
		if limit > 0 && len(out) >= limit {
			break
		}
		if run.Seq > after {
			out = append(out, cloneRun(run))
		}
	}
	return out, total, nil
}

type TaskRepository struct{ st *storeState }

func (r *TaskRepository) Create(_ context.Context, task *domain.Task) error {
	unlock := lockWrite(r.st)
	defer unlock()
	if _, exists := r.st.tasks[task.ID]; exists {
		return domain.Conflictf("task_exists", "task %s already exists", task.ID)
	}
	r.st.tasks[task.ID] = cloneTask(task)
	return nil
}

func (r *TaskRepository) Get(_ context.Context, tenantID domain.TenantID, id domain.TaskID) (*domain.Task, error) {
	unlock := lockRead(r.st)
	defer unlock()
	t, ok := r.st.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, notFound("task", id)
	}
	return cloneTask(t), nil
}

func (r *TaskRepository) Update(_ context.Context, task *domain.Task, expectedVersion int64) error {
	unlock := lockWrite(r.st)
	defer unlock()
	cur, ok := r.st.tasks[task.ID]
	if !ok || cur.TenantID != task.TenantID {
		return notFound("task", task.ID)
	}
	if cur.Version != expectedVersion {
		return domain.NewStaleVersionError("task", task.ID, expectedVersion, cur.Version)
	}
	stored := cloneTask(task)
	stored.Version = cur.Version + 1
	r.st.tasks[task.ID] = stored
	task.Version = stored.Version
	return nil
}

func (r *TaskRepository) ListByRun(_ context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Task, error) {
	unlock := lockRead(r.st)
	defer unlock()
	out := []*domain.Task{}
	for _, t := range r.st.tasks {
		if t.RunID == runID && t.TenantID == tenantID {
			out = append(out, cloneTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
