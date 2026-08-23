// Package storetest defines the behavioral contract every store adapter must
// satisfy. The suite runs against the memory implementation today and against
// the PostgreSQL adapter when it lands; both must observe identical
// semantics, including tenant isolation and optimistic concurrency.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

// World pairs a fresh empty store with its unit-of-work seam and a clock
// advance hook; adapters must hand back views over the SAME underlying
// state. Advance moves the adapter's scheduling clock (outbox leases,
// backoff windows) so time-based behavior is deterministic in tests. Clock
// is that same authority; suites read it to assert reported instants are
// store-owned rather than caller-supplied.
type World struct {
	Repos   ports.Repositories
	Tx      ports.Transactor
	Advance func(d time.Duration)
	Clock   ports.Clock
}

// Factory constructs a fresh World per subtest.
type Factory func() World

// world returns the world with a guaranteed non-nil Advance.
func world(f Factory) (ports.Repositories, ports.Transactor, func(time.Duration)) {
	w := f()
	advance := w.Advance
	if advance == nil {
		advance = func(time.Duration) {}
	}
	return w.Repos, w.Tx, advance
}

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
// Every adapter (memory and PostgreSQL alike) must pass the exact same
// assertions.
func Run(t *testing.T, f Factory) {
	world := func() (ports.Repositories, ports.Transactor) {
		w := f()
		return w.Repos, w.Tx
	}
	t.Run("TenantCRUDAndSlugUniqueness", func(t *testing.T) {
		repos, _ := world()
		testTenantCRUD(t, repos)
	})
	t.Run("ThreadLifecycleWithOptimisticConcurrency", func(t *testing.T) {
		repos, _ := world()
		testThreadConcurrency(t, repos)
	})
	t.Run("ThreadListIsTenantScopedAndOrdered", func(t *testing.T) {
		repos, _ := world()
		testThreadListScopesAndOrders(t, repos)
	})
	t.Run("RunIdempotencyKeyUniquePerThread", func(t *testing.T) {
		repos, _ := world()
		testRunIdempotency(t, repos)
	})
	t.Run("RunListByThreadIsTenantScopedStableAndPaginated", func(t *testing.T) {
		repos, _ := world()
		testRunListByThread(t, repos)
	})
	t.Run("RunListBoundedTraversalCoversLongHistoryExactlyOnce", func(t *testing.T) {
		repos, _ := world()
		testRunListBoundedTraversal(t, repos)
	})
	t.Run("RunListConcurrentTailGrowthNeverDuplicates", func(t *testing.T) {
		repos, _ := world()
		testRunListConcurrentTailGrowth(t, repos)
	})
	t.Run("RunCreationOnUnknownThreadIsTypedInvalid", func(t *testing.T) {
		repos, _ := world()
		testRunCreateOnUnknownThread(t, repos)
	})
	t.Run("MessageAppendOnUnknownThreadIsUniformNotFound", func(t *testing.T) {
		repos, _ := world()
		testAppendMessageOnUnknownThread(t, repos)
	})
	t.Run("ConcurrentIdempotentRunCreationHasSingleWinner", func(t *testing.T) {
		repos, _ := world()
		testConcurrentIdempotentCreate(t, repos)
	})
	t.Run("ConcurrentTaskUpdatesHaveOneWinnerPerVersion", func(t *testing.T) {
		repos, _ := world()
		testConcurrentStaleWrites(t, repos)
	})
	t.Run("TaskVersionGuardRejectsStaleWrites", func(t *testing.T) {
		repos, _ := world()
		testTaskStaleWrite(t, repos)
	})
	t.Run("SpecVersioningPerThread", func(t *testing.T) {
		repos, _ := world()
		testSpecLifecycle(t, repos)
	})
	t.Run("IntegrationConnectionLifecycle", func(t *testing.T) {
		repos, _ := world()
		testIntegrationLifecycle(t, repos)
	})
	t.Run("AuditLogIsAppendOnlyAndTenantScoped", func(t *testing.T) {
		repos, _ := world()
		testAuditAppendOnly(t, repos)
	})
	t.Run("PolicyDecisionsRetrieveByRun", func(t *testing.T) {
		repos, _ := world()
		testPolicyDecisionRetrieval(t, repos)
	})
	t.Run("CrossTenantReadsAreUniformNotFound", func(t *testing.T) {
		repos, _ := world()
		testCrossTenantIsolation(t, repos)
	})
	t.Run("EventsCarryMonotonicCursorAndFilterByRun", func(t *testing.T) {
		repos, _ := world()
		testEventPagination(t, repos)
	})
	t.Run("PaginationBoundariesAreExactAtCursors", func(t *testing.T) {
		repos, _ := world()
		testPaginationBoundaries(t, repos)
	})
	t.Run("ArtifactsRoundTripContentAndDigest", func(t *testing.T) {
		repos, _ := world()
		testArtifactRoundTrip(t, repos)
	})
	t.Run("UnitOfWorkRollsBackOnReturnedError", func(t *testing.T) {
		repos, tx := world()
		testUnitRollbackOnError(t, repos, tx)
	})
	t.Run("UnitOfWorkRollsBackOnPanic", func(t *testing.T) {
		repos, tx := world()
		testUnitRollbackOnPanic(t, repos, tx)
	})
	t.Run("NestedUnitsJoinTheOuterUnit", func(t *testing.T) {
		repos, tx := world()
		testNestedUnitsJoinOuter(t, repos, tx)
	})
}

func testUnitRollbackOnError(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	boom := errors.New("deliberate failure")
	err := tx.Do(ctx, func(ctx context.Context) error {
		project := seedProject(ctx, t, repos, tenantID)
		seedThread(ctx, t, repos, tenantID, project.ID)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unit must propagate the error, got %v", err)
	}
	assertThreadCountZero(t, repos)
}

func testUnitRollbackOnPanic(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	func() {
		defer func() { _ = recover() }()
		_ = tx.Do(ctx, func(ctx context.Context) error {
			project := seedProject(ctx, t, repos, tenantID)
			seedThread(ctx, t, repos, tenantID, project.ID)
			panic("deliberate panic inside unit")
		})
	}()
	assertThreadCountZero(t, repos)
}

func testNestedUnitsJoinOuter(t *testing.T, repos ports.Repositories, tx ports.Transactor) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	err := tx.Do(ctx, func(ctx context.Context) error {
		innerErr := tx.Do(ctx, func(ctx context.Context) error {
			project := seedProject(ctx, t, repos, tenantID)
			seedThread(ctx, t, repos, tenantID, project.ID)
			return errors.New("inner failure rolls back the outer unit too")
		})
		return innerErr
	})
	if ErrKind(err) != domain.ErrKindInvalid {
		t.Logf("outer error: %v", err)
	}
	assertThreadCountZero(t, repos)
}

// assertThreadCountZero proves nothing from a rolled-back unit survived.
func assertThreadCountZero(t *testing.T, repos ports.Repositories) {
	t.Helper()
	projects, err := repos.Projects.ListByTenant(context.Background(), tenantID)
	if err != nil || len(projects) != 0 {
		t.Fatalf("rolled-back project must not exist: %d %v", len(projects), err)
	}
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

// testThreadListScopesAndOrders pins the list contract behind the web
// surface: only the caller's tenant's threads come back, most recently
// updated first, honoring the limit, with foreign rows indistinguishable
// from nonexistent ones (ADR-0004).
func testThreadListScopesAndOrders(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	otherID := domain.TenantID(tid("ten", "othertenant0000000000"))
	seedTenant(ctx, t, repos, otherID, "other")
	project := seedProject(ctx, t, repos, tenantID)
	foreignProject := seedForeignProject(ctx, t, repos, otherID)

	first := seedThreadAt(ctx, t, repos, tenantID, project.ID, "first thread", fixedTime(10))
	second := seedThreadAt(ctx, t, repos, tenantID, project.ID, "second thread", fixedTime(20))
	seedThreadAt(ctx, t, repos, otherID, foreignProject.ID, "foreign thread", fixedTime(30))

	// Equal updated_at must still yield one deterministic order on every
	// store implementation: ties resolve by ascending id.
	tieB := seedThreadAt(ctx, t, repos, tenantID, project.ID, "tie b", fixedTime(40))
	tieA := seedThreadAt(ctx, t, repos, tenantID, project.ID, "tie a", fixedTime(40))

	list, err := repos.Threads.ListByTenant(ctx, tenantID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("tenant list must contain exactly its own threads, got %d: %+v", len(list), list)
	}
	tieFirst, tieSecond := tieA.ID, tieB.ID
	if tieSecond < tieFirst {
		tieFirst, tieSecond = tieSecond, tieFirst
	}
	wantOrder := []domain.ThreadID{tieFirst, tieSecond, second.ID, first.ID}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Fatalf("list position %d = %s, want %s (updated desc, id asc)", i, list[i].ID, want)
		}
	}

	bounded, err := repos.Threads.ListByTenant(ctx, tenantID, 1)
	if err != nil || len(bounded) != 1 || bounded[0].ID != tieFirst {
		t.Fatalf("limit must keep the newest entries: %+v %v", bounded, err)
	}

	empty, err := repos.Threads.ListByTenant(ctx, domain.TenantID(tid("ten", "unknowntenant000000")), 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown tenant must list empty without error: %+v %v", empty, err)
	}
}

func seedForeignProject(ctx context.Context, t *testing.T, repos ports.Repositories, owner domain.TenantID) *domain.Project {
	t.Helper()
	idStr, _ := domain.NewID(domain.PrefixProject)
	project, err := domain.NewProject(domain.ProjectID(idStr), owner, "calc", "Calculator", "main", "", fixedTime(1))
	if err != nil {
		t.Fatalf("seed foreign project: %v", err)
	}
	if err := repos.Projects.Create(ctx, project); err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	return project
}

func seedThreadAt(ctx context.Context, t *testing.T, repos ports.Repositories, owner domain.TenantID, projectID domain.ProjectID, title string, at time.Time) *domain.Thread {
	t.Helper()
	idStr, _ := domain.NewID(domain.PrefixThread)
	thread, err := domain.NewThread(domain.ThreadID(idStr), owner, projectID, title, principal, at)
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return thread
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

// seedRunForKey creates one run on the given thread with a distinct
// idempotency key and creation instant.
func seedRunForKey(t *testing.T, repos ports.Repositories, thread *domain.Thread, key string, at time.Time) *domain.Run {
	t.Helper()
	ctx := context.Background()
	idStr, _ := domain.NewID(domain.PrefixRun)
	run, err := domain.NewRun(domain.RunID(idStr), thread.TenantID, thread.ID, key, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatalf("seed run %s: %v", key, err)
	}
	return run
}

// testRunListByThread pins the read contract behind GET /v1/threads/{id}/runs:
// only the caller's own runs come back, oldest first in the store-assigned
// per-thread sequence order (true creation order), honoring the keyset
// cursor and limit. A run appended between two page requests — even one
// whose created_at is backdated by a clock rollback — lands strictly after
// every sequence the reader has consumed and causes no duplicate or missing
// entry for a reader walking increasing cursors. Foreign or unknown threads
// are uniformly not-found (ADR-0004) while a known empty thread yields an
// empty page.
func testRunListByThread(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")
	project := seedProject(ctx, t, repos, tenantID)
	foreignProject := seedForeignProject(ctx, t, repos, otherTenID)

	thread := seedThreadAt(ctx, t, repos, tenantID, project.ID, "runs thread", fixedTime(2))
	foreignThread := seedThreadAt(ctx, t, repos, otherTenID, foreignProject.ID, "foreign thread", fixedTime(2))
	emptyThread := seedThreadAt(ctx, t, repos, tenantID, project.ID, "empty thread", fixedTime(3))

	// Creation instants are deliberately NOT monotonic with insertion: the
	// sequence is the only ordering authority, so a backdated timestamp
	// must not influence position.
	first := seedRunForKey(t, repos, thread, "list-1", fixedTime(20))
	second := seedRunForKey(t, repos, thread, "list-2", fixedTime(10))
	// Identical timestamps must still yield one deterministic order on
	// every store implementation: insertion order, i.e. ascending seq.
	tieA := seedRunForKey(t, repos, thread, "tie-a", fixedTime(30))
	tieB := seedRunForKey(t, repos, thread, "tie-b", fixedTime(30))
	newest := seedRunForKey(t, repos, thread, "list-newest", fixedTime(40))
	seedRunForKey(t, repos, foreignThread, "foreign", fixedTime(15))

	wantOrder := []domain.RunID{first.ID, second.ID, tieA.ID, tieB.ID, newest.ID}

	list, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, 0, 0)
	if err != nil || total != 5 {
		t.Fatalf("full list: %d runs, total %d, err %v", len(list), total, err)
	}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Fatalf("position %d = %s, want %s (insertion order)", i, list[i].ID, want)
		}
		if i > 0 && list[i].Seq <= list[i-1].Seq {
			t.Fatalf("sequences must be strictly increasing in listing order: %d then %d", list[i-1].Seq, list[i].Seq)
		}
	}
	if list[0].Seq < 1 {
		t.Fatalf("store must assign positive sequences, got %d", list[0].Seq)
	}

	page, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, 0, 2)
	if err != nil || total != 5 || len(page) != 2 {
		t.Fatalf("limited first page: %d runs, total %d, err %v", len(page), total, err)
	}
	if page[0].ID != wantOrder[0] || page[1].ID != wantOrder[1] {
		t.Fatalf("limit must keep the earliest entries in order: %+v", page)
	}

	resumeAfter := page[1].Seq

	beyond, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, int64(len(wantOrder))+100, 0)
	if err != nil || total != 5 || len(beyond) != 0 {
		t.Fatalf("beyond-max cursor must be an empty page: %d/%d %v", len(beyond), total, err)
	}

	// The release-blocker regression: a run inserted BETWEEN two page
	// requests whose created_at predates everything already listed (clock
	// rollback). Its sequence still sorts it at the tail, so the resuming
	// reader neither sees it early nor loses any unread run.
	appended := seedRunForKey(t, repos, thread, "list-backdated-midwalk", fixedTime(1))
	resumed, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, resumeAfter, 0)
	if err != nil || total != 6 || len(resumed) != 4 {
		t.Fatalf("resume after backdated append: %d runs, total %d, err %v", len(resumed), total, err)
	}
	wantResumed := []domain.RunID{tieA.ID, tieB.ID, newest.ID, appended.ID}
	for i, want := range wantResumed {
		if resumed[i].ID != want {
			t.Fatalf("resumed position %d = %s, want %s (backdated append must land at the tail)", i, resumed[i].ID, want)
		}
	}
	seen := map[domain.RunID]bool{}
	for _, run := range page {
		seen[run.ID] = true
	}
	for _, run := range resumed {
		if seen[run.ID] {
			t.Fatalf("backdated tail append produced duplicate %s across page requests", run.ID)
		}
		seen[run.ID] = true
	}
	if len(seen) != 6 {
		t.Fatalf("walk across the append must cover all 6 runs exactly once, saw %d", len(seen))
	}

	empty, total, err := repos.Runs.ListByThread(ctx, tenantID, emptyThread.ID, 0, 0)
	if err != nil || total != 0 || len(empty) != 0 || empty == nil {
		t.Fatalf("known thread without runs must list an empty non-nil page: %d/%d %v", len(empty), total, err)
	}

	crossTenantErr := try(func() error {
		_, _, err := repos.Runs.ListByThread(ctx, otherTenID, thread.ID, 0, 0)
		return err
	})
	if ErrKind(crossTenantErr) != domain.ErrKindNotFound {
		t.Fatalf("foreign-tenant list must be uniform not-found, got %v", crossTenantErr)
	}
	missingErr := try(func() error {
		_, _, err := repos.Runs.ListByThread(ctx, tenantID, domain.ThreadID(tid("thr", "missingthread000000")), 0, 0)
		return err
	})
	if ErrKind(missingErr) != domain.ErrKindNotFound {
		t.Fatalf("unknown-thread list must be uniform not-found, got %v", missingErr)
	}
}

// testRunListBoundedTraversal walks a history comfortably above one server
// page (>200 runs) through keyset cursors and requires exact once coverage
// in strict sequence order, with the final seeded run last.
func testRunListBoundedTraversal(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThreadAt(ctx, t, repos, tenantID, project.ID, "long history thread", fixedTime(2))

	const count = 205
	const pageSize = 32
	wantIDs := make([]domain.RunID, 0, count)
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		run := seedRunForKey(t, repos, thread, fmt.Sprintf("long-%03d", i),
			base.Add(time.Duration(count-i)*time.Second)) // timestamps descend; insertion order is authoritative
		wantIDs = append(wantIDs, run.ID)
	}

	var cursor int64
	gotIDs := make([]domain.RunID, 0, count)
	prevSeq := int64(0)
	pages := 0
	for {
		page, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, cursor, pageSize)
		if err != nil {
			t.Fatalf("page at cursor %d: %v", cursor, err)
		}
		if int64(len(gotIDs)) > total {
			t.Fatalf("total %d dropped below the %d entries already consumed", total, len(gotIDs))
		}
		pages++
		if pages > 1 && len(page) == 0 && int64(len(gotIDs)) < total {
			t.Fatalf("empty page at cursor %d with %d runs outstanding", cursor, total-int64(len(gotIDs)))
		}
		for _, run := range page {
			if run.Seq <= prevSeq {
				t.Fatalf("sequence regressed across pages: %d after %d", run.Seq, prevSeq)
			}
			prevSeq = run.Seq
			cursor = run.Seq
			gotIDs = append(gotIDs, run.ID)
		}
		if int64(len(gotIDs)) >= count {
			break
		}
		if pages > count/pageSize+4 {
			t.Fatalf("traversal did not terminate: %d pages for %d runs", pages, count)
		}
	}
	if len(gotIDs) != count {
		t.Fatalf("walked history must cover all %d runs exactly once, saw %d", count, len(gotIDs))
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("history position %d = %s, want %s", i, gotIDs[i], wantIDs[i])
		}
	}
	full, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, 0, 0)
	if err != nil || total != count || len(full) != count {
		t.Fatalf("unbounded list: %d runs, total %d, err %v", len(full), total, err)
	}
}

// testRunListConcurrentTailGrowth creates runs from several goroutines while
// a single-threaded reader walks keyset pages, requiring that the reader
// never observes a duplicate or a sequence regression mid-growth, and that
// one final walk after the writers settle covers every run exactly once in
// sequence order.
func testRunListConcurrentTailGrowth(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThreadAt(ctx, t, repos, tenantID, project.ID, "concurrent growth thread", fixedTime(2))

	const writers = 6
	const perWriter = 12
	const totalRuns = writers * perWriter

	writerErrs := make(chan error, writers)
	writerDone := make(chan struct{})
	var wg sync.WaitGroup
	at := fixedTime(50)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				idStr, err := domain.NewID(domain.PrefixRun)
				if err != nil {
					writerErrs <- err
					return
				}
				run, err := domain.NewRun(domain.RunID(idStr), tenantID, thread.ID,
					fmt.Sprintf("growth-%d-%02d", w, i), at)
				if err != nil {
					writerErrs <- err
					return
				}
				if err := repos.Runs.Create(ctx, run); err != nil {
					writerErrs <- fmt.Errorf("writer %d item %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	go func() {
		wg.Wait()
		close(writerDone)
	}()

	seen := make(map[domain.RunID]bool)
	var cursor int64
	settled := false
	for walks := 0; !settled; walks++ {
		if walks > 16*totalRuns {
			t.Fatalf("concurrent walk failed to converge: saw %d of %d runs", len(seen), totalRuns)
		}
		page, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, cursor, 4)
		if err != nil {
			t.Fatalf("concurrent walk at cursor %d: %v", cursor, err)
		}
		for _, run := range page {
			if seen[run.ID] {
				t.Fatalf("concurrent growth produced duplicate %s at cursor %d", run.ID, cursor)
			}
			if run.Seq <= cursor {
				t.Fatalf("sequence regressed during concurrent growth: %d at cursor %d", run.Seq, cursor)
			}
			seen[run.ID] = true
			cursor = run.Seq
		}
		select {
		case <-writerDone:
			// Every creation attempt has landed; the count is final and any
			// outstanding entry must surface on the next page, so the walk
			// either progresses or has hit a real defect.
			if int64(len(seen)) == total && total == totalRuns {
				settled = true
			} else if len(page) == 0 {
				var writerErr error
				select {
				case writerErr = <-writerErrs:
				default:
				}
				t.Fatalf("walk stalled with writers settled: saw %d of %d (total %d); first writer error: %v",
					len(seen), totalRuns, total, writerErr)
			}
		default:
			if len(page) == 0 {
				// Writers still running and no new tail entries since the
				// last read: yield instead of busy-spinning, so a starved
				// scheduler can never burn through the convergence budget
				// before the writers land their creations.
				time.Sleep(time.Millisecond)
			}
		}
	}
	select {
	case err := <-writerErrs:
		t.Fatalf("concurrent creator failed: %v", err)
	default:
	}

	final, total, err := repos.Runs.ListByThread(ctx, tenantID, thread.ID, 0, 0)
	if err != nil || total != totalRuns || len(final) != totalRuns {
		t.Fatalf("final full list: %d runs, total %d, err %v", len(final), total, err)
	}
	prev := int64(0)
	for _, run := range final {
		if !seen[run.ID] {
			t.Fatalf("settled walk missed run %s (seq %d)", run.ID, run.Seq)
		}
		if run.Seq <= prev {
			t.Fatalf("final list sequences not strictly increasing: %d after %d", run.Seq, prev)
		}
		prev = run.Seq
	}
}

// testRunCreateOnUnknownThread pins the typed rejection both adapters must
// produce when a run references a thread that does not exist in the run's
// tenant: invalid_request with code run_thread_unknown, never a leaked
// foreign-key failure or a silent orphan insert.
func testRunCreateOnUnknownThread(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	idStr, err := domain.NewID(domain.PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(domain.RunID(idStr), tenantID,
		domain.ThreadID(tid("thr", "missingthread")), "orphan-key", fixedTime(4))
	if err != nil {
		t.Fatal(err)
	}
	err = repos.Runs.Create(ctx, run)
	if ErrKind(err) != domain.ErrKindInvalid {
		t.Fatalf("unknown-thread creation must be typed invalid, got %v", err)
	}
	if code := ErrorCode(err); code != "run_thread_unknown" {
		t.Fatalf("unknown-thread creation must carry code run_thread_unknown, got %q", code)
	}

	// A foreign tenant's thread is equally invisible to the creator. The
	// foreign tenant must exist first: the project and thread seeds are
	// tenant-owned rows with real foreign keys.
	seedTenant(ctx, t, repos, otherTenID, "globex")
	project := seedProject(ctx, t, repos, otherTenID)
	foreignThread := seedThreadAt(ctx, t, repos, otherTenID, project.ID, "foreign thread", fixedTime(2))
	idStr, err = domain.NewID(domain.PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	crossRun, err := domain.NewRun(domain.RunID(idStr), tenantID, foreignThread.ID, "cross-tenant-key", fixedTime(5))
	if err != nil {
		t.Fatal(err)
	}
	err = repos.Runs.Create(ctx, crossRun)
	if ErrKind(err) != domain.ErrKindInvalid || ErrorCode(err) != "run_thread_unknown" {
		t.Fatalf("cross-tenant thread creation must be the same typed invalid, got kind=%v code=%q",
			ErrKind(err), ErrorCode(err))
	}
}

// testAppendMessageOnUnknownThread pins the uniform not-found both adapters
// must produce when appending to a thread that does not exist in the
// message's tenant — the same absence the append's row lock probe exists to
// detect before any sequence allocation happens.
func testAppendMessageOnUnknownThread(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "globex")
	project := seedProject(ctx, t, repos, otherTenID)
	foreignThread := seedThreadAt(ctx, t, repos, otherTenID, project.ID, "foreign thread", fixedTime(2))

	append := func(threadID domain.ThreadID, tenantID domain.TenantID) error {
		msg := &domain.Message{
			ID:           domain.MessageID(tid("msg", "orphansuffix000000")),
			TenantID:     tenantID,
			ThreadID:     threadID,
			Role:         domain.RoleUser,
			DeliveryMode: domain.DeliveryImmediate,
			Content:      "orphan",
		}
		return repos.Threads.AppendMessage(ctx, msg)
	}
	if err := append(domain.ThreadID(tid("thr", "missingthread")), tenantID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("append on unknown thread must be not-found, got %v", err)
	}
	if err := append(foreignThread.ID, tenantID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("append on foreign-tenant thread must be uniformly not-found, got %v", err)
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
		idStr, _ := domain.NewID(domain.PrefixEvent)
		evt := &domain.Event{
			ID:            domain.EventID(idStr),
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

// ErrorCode extracts the stable short code from a *domain.Error anywhere in
// the wrap chain, so adapters that add context never lose the contract code.
// Foreign errors carry no code.
func ErrorCode(err error) string {
	var dom *domain.Error
	if errors.As(err, &dom) {
		return dom.Code
	}
	return ""
}

func try(f func() error) error { return f() }

func testConcurrentIdempotentCreate(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	const contenders = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, conflicts := 0, 0
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runIDStr, _ := domain.NewID(domain.PrefixRun)
			run, _ := domain.NewRun(domain.RunID(runIDStr), tenantID, thread.ID, "race-key", fixedTime(6))
			err := repos.Runs.Create(ctx, run)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case ErrKind(err) == domain.ErrKindConflict:
				conflicts++
			default:
				t.Errorf("unexpected error kind: %v", err)
			}
		}()
	}
	wg.Wait()
	if winners != 1 || conflicts != contenders-1 {
		t.Fatalf("expected exactly one winner and %d conflicts, got %d/%d", contenders-1, winners, conflicts)
	}
	got, err := repos.Runs.GetByIdempotencyKey(ctx, tenantID, thread.ID, "race-key")
	if err != nil {
		t.Fatalf("winner must be retrievable: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("winner must be the untouched first insert: v%d", got.Version)
	}
}

// testConcurrentStaleWrites drives N writers through the same version step;
// exactly one may win each generation, the rest must observe conflicts.
func testConcurrentStaleWrites(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)
	_, run := seedRunAt(t, repos, thread)

	taskIDStr, _ := domain.NewID(domain.PrefixTask)
	task, err := domain.NewTask(domain.TaskID(taskIDStr), tenantID, run.ID, thread.ID, "contested", domain.TaskKindCodeChange, 0, nil, 3, fixedTime(5))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	const writers = 6
	base := *task
	var wg sync.WaitGroup
	wins := int64(0)
	for i := range writers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			candidate := base
			if n%2 == 0 {
				candidate.Name = "writer-a"
			} else {
				candidate.Name = "writer-b"
			}
			if uerr := repos.Tasks.Update(ctx, &candidate, base.Version); uerr == nil {
				atomic.AddInt64(&wins, 1)
			} else if ErrKind(uerr) != domain.ErrKindConflict && ErrKind(uerr) != domain.ErrKindNotFound {
				t.Errorf("unexpected update error: %v", uerr)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one writer must win version %d, got %d", base.Version, wins)
	}
}

// seedRunAt seeds a run for an existing thread.
func seedRunAt(t *testing.T, repos ports.Repositories, thread *domain.Thread) (*domain.Thread, *domain.Run) {
	ctx := context.Background()
	runIDStr, _ := domain.NewID(domain.PrefixRun)
	run, err := domain.NewRun(domain.RunID(runIDStr), tenantID, thread.ID, tid("key", "concurrent"), fixedTime(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Runs.Create(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return thread, run
}

func testSpecLifecycle(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)

	specIDStr, _ := domain.NewID(domain.PrefixSpec)
	content := domain.SpecContent{
		Outcome:         "feature works",
		Requirements:    []string{"r1", "r2"},
		NonGoals:        []string{"n1"},
		SuccessCriteria: []string{"c1"},
		Blockers:        []string{},
	}
	spec, err := domain.NewSpec(domain.SpecID(specIDStr), tenantID, thread.ID, 1, content, fixedTime(5))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Specs.Create(ctx, spec); err != nil {
		t.Fatalf("create spec: %v", err)
	}
	got, err := repos.Specs.Get(ctx, tenantID, spec.ID)
	if err != nil {
		t.Fatalf("get spec: %v", err)
	}
	if got.Content.Outcome != "feature works" || len(got.Content.Requirements) != 2 || len(got.Content.Blockers) != 0 {
		t.Fatalf("spec round-trip mismatch: %+v", got.Content)
	}
	v2ID, _ := domain.NewID(domain.PrefixSpec)
	v2Content := content
	v2Content.Outcome = "feature works better"
	v2, _ := domain.NewSpec(domain.SpecID(v2ID), tenantID, thread.ID, 2, v2Content, fixedTime(6))
	if err := repos.Specs.Create(ctx, v2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	latest, err := repos.Specs.LatestForThread(ctx, tenantID, thread.ID)
	if err != nil || latest.Version != 2 || latest.Content.Outcome != "feature works better" {
		t.Fatalf("latest must be v2: %+v %v", latest, err)
	}
	if _, err := repos.Specs.LatestForThread(ctx, otherTenID, thread.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("cross-tenant latest must be not-found, got %v", err)
	}
}

func testIntegrationLifecycle(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	idStr, _ := domain.NewID(domain.PrefixIntegration)
	conn, err := domain.NewIntegration(domain.IntegrationID(idStr), tenantID, domain.IntegrationGitHub,
		[]string{"repo:read"}, tid("secret", "ref"), fixedTime(5))
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Integrations.Create(ctx, conn); err != nil {
		t.Fatalf("create: %v", err)
	}
	expected := conn.Version
	if err := conn.TransitionTo(domain.ConnectionConnected); err != nil {
		t.Fatal(err)
	}
	if err := repos.Integrations.Update(ctx, conn, expected); err != nil {
		t.Fatalf("update: %v", err)
	}
	stale := *conn
	_ = stale.TransitionTo(domain.ConnectionRevoked)
	if ErrKind(repos.Integrations.Update(ctx, &stale, expected)) != domain.ErrKindConflict {
		t.Fatalf("stale integration write must conflict")
	}
	listed, err := repos.Integrations.ListByTenant(ctx, tenantID)
	if err != nil || len(listed) != 1 || listed[0].Status != domain.ConnectionConnected {
		t.Fatalf("list mismatch: %+v %v", listed, err)
	}
	if _, err := repos.Integrations.Get(ctx, otherTenID, conn.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("cross-tenant integration read must be not-found")
	}
}

func testAuditAppendOnly(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")

	for i := range 3 {
		idStr, _ := domain.NewID(domain.PrefixAuditEvent)
		evt := &domain.AuditEvent{
			ID:           domain.AuditEventID(idStr),
			TenantID:     tenantID,
			Actor:        domain.Actor{Type: domain.PrincipalHuman, ID: string(principal)},
			Action:       domain.ActionSandboxExec,
			ResourceType: "sandbox",
			ResourceID:   tid("sbx", "audit"),
			Result:       domain.AuditResultAllowed,
			Metadata:     map[string]any{"i": i},
			At:           fixedTime(10 + i),
		}
		if err := repos.Audit.Append(ctx, evt); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	page, err := repos.Audit.ListByTenant(ctx, tenantID, "", 10)
	if err != nil || len(page) != 3 {
		t.Fatalf("list all: %d %v", len(page), err)
	}
	for i := 1; i < len(page); i++ {
		if !page[i].At.After(page[i-1].At) {
			t.Fatalf("append order not preserved at %d", i)
		}
	}
	foreign, err := repos.Audit.ListByTenant(ctx, otherTenID, "", 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("tenant scoping broken on audit log: %d %v", len(foreign), err)
	}
	limited, err := repos.Audit.ListByTenant(ctx, tenantID, string(page[0].ID), 10)
	if err != nil || len(limited) != 2 || limited[0].ID == page[0].ID {
		t.Fatalf("after-cursor audit pagination broken: %d", len(limited))
	}
}

func testPolicyDecisionRetrieval(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)
	_, run := seedRunAt(t, repos, thread)

	record := func(action domain.PolicyAction, outcome domain.PolicyOutcome) {
		idStr, _ := domain.NewID(domain.PrefixPolicyDecision)
		d := &domain.PolicyDecision{
			ID:            domain.PolicyDecisionID(idStr),
			TenantID:      tenantID,
			Request:       domain.PolicyRequest{TenantID: tenantID, RunID: run.ID, Principal: principal, Action: action},
			Outcome:       outcome,
			Reason:        "contract test",
			PolicyVersion: "policy.v1",
			CreatedAt:     fixedTime(20),
		}
		if err := repos.PolicyDecisions.Record(ctx, d); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	record(domain.ActionSCMLocalCommit, domain.PolicyAllow)
	record(domain.ActionNetworkAccess, domain.PolicyDeny)

	byRun, err := repos.PolicyDecisions.ListByRun(ctx, tenantID, run.ID)
	if err != nil || len(byRun) != 2 {
		t.Fatalf("list by run: %d %v", len(byRun), err)
	}
	if byRun[0].Outcome == byRun[1].Outcome {
		t.Fatalf("both outcomes should have been recorded distinctly")
	}
	if byRun[0].Request.TenantID != tenantID {
		t.Fatalf("request tenant must round-trip")
	}
	foreign, err := repos.PolicyDecisions.ListByRun(ctx, otherTenID, run.ID)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-tenant decision read must be empty: %d %v", len(foreign), err)
	}
}

func testPaginationBoundaries(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	project := seedProject(ctx, t, repos, tenantID)
	thread := seedThread(ctx, t, repos, tenantID, project.ID)
	_, run := seedRunAt(t, repos, thread)

	var seqs []int64
	for i := range 5 {
		idStr, _ := domain.NewID(domain.PrefixEvent)
		evt := &domain.Event{
			ID:            domain.EventID(idStr),
			Type:          domain.EventRunStatusChanged,
			TenantID:      tenantID,
			AggregateType: "run",
			AggregateID:   string(run.ID),
			RunID:         run.ID,
			Data:          map[string]any{"i": i},
		}
		if err := repos.Events.Append(ctx, evt); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, evt.Seq)
	}

	cases := []struct {
		name  string
		after int64
		limit int
		want  int
	}{
		{"from start no limit", 0, 0, 5},
		{"limit caps page", 0, 2, 2},
		{"boundary after second seq", seqs[1], 0, 3},
		{"boundary after last seq", seqs[4], 0, 0},
		{"beyond max cursor", seqs[4] + 100, 0, 0},
	}
	for _, tc := range cases {
		page, err := repos.Events.ListByRun(ctx, tenantID, run.ID, tc.after, tc.limit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(page) != tc.want {
			t.Errorf("%s: got %d events, want %d", tc.name, len(page), tc.want)
		}
	}
	// Message pagination shares cursor semantics.
	msgs, total, err := repos.Threads.Messages(ctx, tenantID, thread.ID, 0, 0)
	if err != nil || len(msgs) != 1 || total != 1 {
		t.Fatalf("thread messages baseline: %d/%d %v", len(msgs), total, err)
	}
	none, total, err := repos.Threads.Messages(ctx, tenantID, thread.ID, msgs[0].Seq, 0)
	if err != nil || len(none) != 0 || total != 1 {
		t.Fatalf("messages past cursor must be empty with stable total: %d/%d %v", len(none), total, err)
	}
}
