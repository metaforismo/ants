package orchestration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/policy"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/review"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
	memory "github.com/metaforismo/ants/internal/store/memory"
)

const (
	testTenantID  = domain.TenantID("ten_testtenant00000000000")
	testProjectID = domain.ProjectID("prj_testproject000000000")
)

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

// recordingSleeper verifies backoff without waiting.
type recordingSleeper struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (s *recordingSleeper) Sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durations = append(s.durations, d)
	return nil
}

type harness struct {
	engine  *Engine
	repos   *memory.Repos
	fake    *sandbox.FakeDriver
	memSCM  *scm.Memory
	sleeper *recordingSleeper
	clock   *fixedClock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repos := memory.NewRepos()
	clock := &fixedClock{t: time.Now().UTC()}
	pol := policy.NewEngine(true, clock, ports.RandomIDs{}, repos.PolicyDecisions, repos.Audit)
	h := &harness{
		repos:   repos,
		fake:    sandbox.NewFakeDriver(),
		memSCM:  scm.NewMemory(),
		sleeper: &recordingSleeper{},
		clock:   clock,
	}
	seedDemo := func(ctx context.Context, name string) (scm.Seed, error) {
		if name != fixtures.DemoName {
			return scm.Seed{}, domain.NotFoundf("fixture", name)
		}
		return fixtures.DemoSeed(), nil
	}
	engine, err := New(Deps{
		Threads:    repos.Threads,
		Projects:   repos.Projects,
		Specs:      repos.Specs,
		Tasks:      repos.Tasks,
		Runs:       repos.Runs,
		Workspaces: repos.Workspaces,
		Artifacts:  repos.Artifacts,
		Events:     repos.Events,
		Uow:        repos.NewTransactor(),
		Policy:     pol,
		Sandbox:    h.fake,
		SCM:        h.memSCM,
		Planner:    planner.NewDeterministic(),
		Reviewer:   review.NewDeterministic(2000),
		Seeder:     seederFunc(seedDemo),
		Clock:      clock,
		IDs:        ports.RandomIDs{},
		Sleeper:    h.sleeper,
	}, Config{
		MaxParallelTasks: 4,
		TaskTimeout:      5 * time.Second,
		StageTimeout:     15 * time.Second,
		MaxAttempts:      3,
		RetryBackoff:     time.Millisecond,
		MaxTasksPerRun:   8,
		MaxExecOpsPerRun: 64,
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	h.engine = engine
	return h
}

type seederFunc func(ctx context.Context, name string) (scm.Seed, error)

func (f seederFunc) Seed(ctx context.Context, name string) (scm.Seed, error) { return f(ctx, name) }

func (h *harness) seedWorld(t *testing.T) (*domain.Tenant, *domain.Thread) {
	t.Helper()
	ctx := context.Background()
	tenant, err := domain.NewTenant(testTenantID, "acme", "Acme Inc", domain.PlanFree, "local", h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repos.Tenants.Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	project, err := domain.NewProject(testProjectID, testTenantID, "calc", "Calculator", "main", fixtures.DemoName, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repos.Projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	threadID, _ := domain.NewThreadID()
	thread, err := domain.NewThread(domain.ThreadID(threadID), testTenantID, testProjectID, "implement arithmetic", domain.PrincipalID("prn_testprincipal0000000"), h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repos.Threads.Create(ctx, thread); err != nil {
		t.Fatal(err)
	}
	msg := &domain.Message{
		ID:           domain.MessageID("msg_testmessage00000000"),
		TenantID:     testTenantID,
		ThreadID:     thread.ID,
		Role:         domain.RoleUser,
		DeliveryMode: domain.DeliveryImmediate,
		Content:      "please implement add and multiply for the calculator",
	}
	if err := h.repos.Threads.AppendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	return tenant, thread
}

// scriptHappyPath registers the deterministic outputs every verify command in
// the fixture expects once the corresponding feature exists.
func scriptHappyPath(h *harness) {
	h.fake.Script("test -f lib_add.sh", exit0())
	h.fake.Script("sh calc.sh add 20 22", stdout("42"))
	h.fake.Script("test -f lib_mul.sh", exit0())
	h.fake.Script("sh calc.sh multiply 6 7", stdout("42"))
	h.fake.Script("bash tests/calc_test.sh", stdout("all tests passed"))
}

func exit0() sandbox.ExecResult { return sandbox.ExecResult{ExitCode: 0} }
func stdout(s string) sandbox.ExecResult {
	return sandbox.ExecResult{ExitCode: 0, Stdout: []byte(s + "\n")}
}

func startAndExecute(t *testing.T, h *harness, thread *domain.Thread) (*domain.Run, error) {
	t.Helper()
	result, err := h.engine.StartRun(context.Background(), StartInput{
		TenantID:       testTenantID,
		ThreadID:       thread.ID,
		Principal:      domain.PrincipalID("prn_testprincipal0000000"),
		Actor:          domain.Actor{Type: domain.PrincipalHuman, ID: "prn_testprincipal0000000"},
		IdempotencyKey: "idem-key-1",
	})
	if err != nil {
		return nil, err
	}
	if result.Replayed {
		t.Fatalf("first start must not be a replay")
	}
	err = h.engine.Execute(context.Background(), testTenantID, result.Run.ID)
	run, getErr := h.repos.Runs.Get(context.Background(), testTenantID, result.Run.ID)
	if getErr != nil {
		t.Fatalf("reload run: %v", getErr)
	}
	return run, err
}
