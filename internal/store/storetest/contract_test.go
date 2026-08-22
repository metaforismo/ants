// Package storetest defines the behavioral contract every store adapter must
// satisfy. The suite runs against the memory implementation today and against
// the PostgreSQL adapter when it lands; both must observe identical
// semantics, including tenant isolation and optimistic concurrency.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// tid builds a structurally valid identifier of any seed length so tests
// never depend on hand-counted suffixes.
func tid(prefix, seed string) string {
	base := prefix + "_" + seed
	for len(base)-len(prefix)-1 < 20 {
		base += "0"
	}
	return base
}

var (
	tenantID   = domain.TenantID(tid("ten", "contracttenant"))
	otherTenID = domain.TenantID(tid("ten", "othertenant"))
	principal  = domain.PrincipalID(tid("prn", "contractprincipal"))
)

// NewReposFunc constructs a fresh, empty set of stores.
type NewReposFunc func() ports.Repositories

func fixedTime(offset int) time.Time {
	return time.Date(2026, 8, 22, 12, 0, offset, 0, time.UTC)
}

func seedTenant(ctx context.Context, t *testing.T, repos ports.Repositories, id domain.TenantID, slug string) {
	t.Helper()
	tenant, err := domain.NewTenant(id, slug, "Contract Tenant "+slug, domain.PlanFree, "local", fixedTime(0))
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func seedProject(ctx context.Context, t *testing.T, repos ports.Repositories, owner domain.TenantID) *domain.Project {
	t.Helper()
	idStr, _ := domain.NewID(domain.PrefixProject)
	project, err := domain.NewProject(domain.ProjectID(idStr), owner, "calc", "Calculator", "main", "", fixedTime(1))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := repos.Projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func seedThread(ctx context.Context, t *testing.T, repos ports.Repositories, owner domain.TenantID, projectID domain.ProjectID) *domain.Thread {
	t.Helper()
	idStr, _ := domain.NewID(domain.PrefixThread)
	thread, err := domain.NewThread(domain.ThreadID(idStr), owner, projectID, "contract thread", principal, fixedTime(2))
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	msg := &domain.Message{
		ID:           domain.MessageID(tid("msg", "contractmessage")),
		TenantID:     owner,
		ThreadID:     thread.ID,
		Role:         domain.RoleUser,
		DeliveryMode: domain.DeliveryImmediate,
		Content:      "do the thing",
	}
	if err := repos.Threads.AppendMessage(ctx, msg); err != nil {
		t.Fatalf("append message: %v", err)
	}
	return thread
}

// Run executes the full contract against a fresh store instance per subtest.
func Run(t *testing.T, newRepos NewReposFunc) {
	t.Run("TenantCRUDAndSlugUniqueness", func(t *testing.T) { testTenantCRUD(t, newRepos()) })
	t.Run("ThreadLifecycleWithOptimisticConcurrency", func(t *testing.T) { testThreadConcurrency(t, newRepos()) })
	t.Run("RunIdempotencyKeyUniquePerThread", func(t *testing.T) { testRunIdempotency(t, newRepos()) })
	t.Run("TaskVersionGuardRejectsStaleWrites", func(t *testing.T) { testTaskStaleWrite(t, newRepos()) })
	t.Run("CrossTenantReadsAreUniformNotFound", func(t *testing.T) { testCrossTenantIsolation(t, newRepos()) })
	t.Run("EventsCarryMonotonicCursorAndFilterByRun", func(t *testing.T) { testEventPagination(t, newRepos()) })
	t.Run("ArtifactsRoundTripContentAndDigest", func(t *testing.T) { testArtifactRoundTrip(t, newRepos()) })
}

func testTenantCRUD(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	got, err := repos.Tenants.GetBySlug(ctx, "acme")
	if err != nil || got.ID != tenantID {
		t.Fatalf("GetBySlug: %+v %v", got, err)
	}
	dup, dupErr := domain.NewTenant(domain.TenantID(tid("ten", "duptenant")), "acme", "Dup", domain.PlanFree, "", fixedTime(3))
	if dupErr != nil {
		t.Fatalf("construct duplicate tenant: %v", dupErr)
	}
	if ErrKind(repos.Tenants.Create(ctx, dup)) != domain.ErrKindConflict {
		t.Fatalf("duplicate slug must conflict")
	}
	if _, err := repos.Tenants.GetBySlug(ctx, "missing"); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("missing slug must be not_found")
	}
}

func testThreadConcurrency(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	stale := *thread
	if err := repos.Threads.Update(ctx, thread, thread.Version); err != nil {
		t.Fatalf("first update: %v", err)
	}
	err := repos.Threads.Update(ctx, &stale, stale.Version)
	if ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("stale write must conflict, got %v", err)
	}
	messages, total, err := repos.Threads.Messages(ctx, tenantID, thread.ID, 0, 0)
	if err != nil || len(messages) != 1 || total != 1 {
		t.Fatalf("messages: %d/%d %v", len(messages), total, err)
	}
}

func testRunIdempotency(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	runA, _ := domain.NewID(domain.PrefixRun)
	first, err := domain.NewRun(domain.RunID(runA), tenantID, thread.ID, "key-1", fixedTime(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Runs.Create(ctx, first); err != nil {
		t.Fatalf("create run: %v", err)
	}

	runB, _ := domain.NewID(domain.PrefixRun)
	replay, _ := domain.NewRun(domain.RunID(runB), tenantID, thread.ID, "key-1", fixedTime(5))
	if ErrKind(repos.Runs.Create(ctx, replay)) != domain.ErrKindConflict {
		t.Fatalf("same idempotency key must conflict")
	}

	got, err := repos.Runs.GetByIdempotencyKey(ctx, tenantID, thread.ID, "key-1")
	if err != nil || got.ID != first.ID {
		t.Fatalf("replay lookup returned wrong run: %+v %v", got, err)
	}
}

func testTaskStaleWrite(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	runID, _ := domain.NewID(domain.PrefixRun)
	run, _ := domain.NewRun(domain.RunID(runID), tenantID, thread.ID, "k", fixedTime(4))
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	taskID, _ := domain.NewID(domain.PrefixTask)
	task, _ := domain.NewTask(domain.TaskID(taskID), tenantID, run.ID, thread.ID, "t", domain.TaskKindCodeChange, 0, nil, 2, fixedTime(5))
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	expected := task.Version
	if err := task.TransitionTo(domain.TaskQueued); err != nil {
		t.Fatal(err)
	}
	if err := repos.Tasks.Update(ctx, task, expected); err != nil {
		t.Fatalf("fresh update: %v", err)
	}
	stale := *task
	stale.Version--
	if err := task.TransitionTo(domain.TaskProvisioning); err != nil {
		t.Fatal(err)
	}
	if ErrKind(repos.Tasks.Update(ctx, &stale, expected)) != domain.ErrKindConflict {
		t.Fatalf("outdated version must be rejected")
	}
}

func testCrossTenantIsolation(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	runID, _ := domain.NewID(domain.PrefixRun)
	run, _ := domain.NewRun(domain.RunID(runID), tenantID, thread.ID, "iso", fixedTime(4))
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	artID, _ := domain.NewID(domain.PrefixArtifact)
	artifact, _ := domain.NewArtifact(domain.ArtifactID(artID), tenantID, run.ID, domain.ArtifactLog, domain.RetentionEphemeral, []byte("log"), fixedTime(5))
	if err := repos.Artifacts.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}

	checks := map[string]error{
		"thread":   try(func() error { _, err := repos.Threads.Get(ctx, otherTenID, thread.ID); return err }),
		"run":      try(func() error { _, err := repos.Runs.Get(ctx, otherTenID, run.ID); return err }),
		"project":  try(func() error { _, err := repos.Projects.Get(ctx, otherTenID, project.ID); return err }),
		"artifact": try(func() error { _, err := repos.Artifacts.Get(ctx, otherTenID, artifact.ID); return err }),
		"idem-key": try(func() error { _, err := repos.Runs.GetByIdempotencyKey(ctx, otherTenID, thread.ID, "iso"); return err }),
	}
	for name, err := range checks {
		if ErrKind(err) != domain.ErrKindNotFound {
			t.Errorf("%s: foreign-tenant read must be uniform not-found, got %v", name, err)
		}
	}
	projects, err := repos.Projects.ListByTenant(ctx, otherTenID)
	if err != nil || len(projects) != 0 {
		t.Errorf("foreign-tenant list must be empty: %d %v", len(projects), err)
	}
}

func testEventPagination(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	runID, _ := domain.NewID(domain.PrefixRun)
	run, _ := domain.NewRun(domain.RunID(runID), tenantID, thread.ID, "evt", fixedTime(4))
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}

	var lastSeq int64 = -1
	for i := range 5 {
		evt := &domain.Event{
			Type:          domain.EventRunStatusChanged,
			TenantID:      tenantID,
			AggregateType: "run",
			AggregateID:   string(run.ID),
			RunID:         run.ID,
			Data:          map[string]any{"i": i},
		}
		if i == 4 {
			evt.RunID = "" // unrelated event must not appear in the run stream
		}
		if err := repos.Events.Append(ctx, evt); err != nil {
			t.Fatal(err)
		}
		if evt.Seq <= lastSeq {
			t.Fatalf("cursor must increase monotonically: %d after %d", evt.Seq, lastSeq)
		}
		lastSeq = evt.Seq
	}

	stream, err := repos.Events.ListByRun(ctx, tenantID, run.ID, 0, 0)
	if err != nil || len(stream) != 4 {
		t.Fatalf("run stream should hold exactly 4 events: %d %v", len(stream), err)
	}
	page, err := repos.Events.ListByRun(ctx, tenantID, run.ID, stream[1].Seq, 0)
	if err != nil || len(page) != 2 || page[0].Seq != stream[2].Seq {
		t.Fatalf("after-cursor pagination broken: %d events", len(page))
	}
	all, err := repos.Events.ListByTenant(ctx, otherTenID, 0, 0)
	if err != nil || len(all) != 0 {
		t.Fatalf("events must be tenant-scoped: %d", len(all))
	}
}

func testArtifactRoundTrip(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)
	runID, _ := domain.NewID(domain.PrefixRun)
	run, _ := domain.NewRun(domain.RunID(runID), tenantID, thread.ID, "art", fixedTime(4))
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}

	content := []byte("unified diff content")
	idStr, _ := domain.NewID(domain.PrefixArtifact)
	artifact, err := domain.NewArtifact(domain.ArtifactID(idStr), tenantID, run.ID, domain.ArtifactDiff, domain.RetentionEphemeral, content, fixedTime(5))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Artifacts.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	got, err := repos.Artifacts.Get(ctx, tenantID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != string(content) {
		t.Fatalf("content mismatch after round trip")
	}
	if got.Digest != domain.Digest(content) {
		t.Fatalf("digest mismatch")
	}
	listed, err := repos.Artifacts.ListByRun(ctx, tenantID, run.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list by run: %d %v", len(listed), err)
	}
}

// ErrKind tolerates wrapped errors from adapters.
func ErrKind(err error) domain.ErrorKind {
	return domain.ErrKindOf(err)
}

func try(f func() error) error { return f() }
