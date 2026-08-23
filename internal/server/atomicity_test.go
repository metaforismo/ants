package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
	"github.com/metaforismo/ants/internal/server"
)

// flakyEvents fails event appends on demand so tests can script persistence
// outages at the store boundary without touching real infrastructure.
type flakyEvents struct {
	ports.EventLog
	mu   sync.Mutex
	fail bool
}

func (f *flakyEvents) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *flakyEvents) Append(ctx context.Context, evt *domain.Event) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return domain.NewError(domain.ErrKindTransient, "event_store_unavailable", "event store unavailable")
	}
	return f.EventLog.Append(ctx, evt)
}

// flakyTasks fails task listings on demand for the same purpose.
type flakyTasks struct {
	ports.TaskStore
	mu   sync.Mutex
	fail bool
}

func (f *flakyTasks) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *flakyTasks) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID) ([]*domain.Task, error) {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return nil, domain.NewError(domain.ErrKindTransient, "task_store_unavailable", "task store unavailable")
	}
	return f.TaskStore.ListByRun(ctx, tenantID, runID)
}

// auditServer builds an httptest server over the standard composition root
// with selected repositories wrapped by failure-scriptable proxies. It is
// the only place these proxies exist; production wiring is untouched.
func auditServer(t *testing.T, mutate func(r *ports.Repositories)) (*httptest.Server, *app.App) {
	t.Helper()
	cfg := config.Defaults()
	application := buildApp(t, cfg)
	repos := application.Repos
	if mutate != nil {
		mutate(&repos)
	}
	srv, err := server.New(server.Deps{
		Config:  cfg,
		Repos:   repos,
		Auth:    &fakeAuthenticator{tenants: application.Repos.Tenants},
		Uow:     application.Uow,
		Engine:  application.Engine,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:   func(context.Context) error { return nil },
		Metrics: application.Metrics,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, application
}

// TestTenantCreationIsAtomicWithItsEvent pins the ADR-0010 API contract: the
// tenant insert and its creation event commit as one unit of work, so a
// failing event append leaves no tenant row and no outbox delivery — and
// creation succeeds once the outage clears.
func TestTenantCreationIsAtomicWithItsEvent(t *testing.T) {
	events := &flakyEvents{}
	ts, application := auditServer(t, func(r *ports.Repositories) {
		events.EventLog = r.Events
		r.Events = events
	})
	ctx := context.Background()

	slug := "atomic-" + uniqueSuffix()
	events.setFail(true)
	e := &env{t: t, baseURL: ts.URL}
	_, raw := e.doJSON(t, http.MethodPost, "/v1/tenants", "",
		map[string]string{"slug": slug, "name": "Audit"}, nil, http.StatusServiceUnavailable)
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "event_store_unavailable" {
		t.Fatalf("failing event append must surface as its typed problem: %s (%v)", raw, err)
	}
	if _, err := application.Repos.Tenants.GetBySlug(ctx, slug); domain.ErrKindOf(err) != domain.ErrKindNotFound {
		t.Fatalf("rolled-back unit must leave no tenant row, got %v", err)
	}
	stats, err := application.Repos.Outbox.Stats(ctx)
	if err != nil || stats.Pending+stats.Leased+stats.Delivered+stats.Dead != 0 {
		t.Fatalf("rolled-back unit must enqueue no outbox delivery: %+v %v", stats, err)
	}

	events.setFail(false)
	e.doJSON(t, http.MethodPost, "/v1/tenants", "",
		map[string]string{"slug": slug, "name": "Audit"}, nil, http.StatusCreated)
	tenant, err := application.Repos.Tenants.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("committed unit must leave the tenant row: %v", err)
	}
	eventList, err := application.Repos.Events.ListByTenant(ctx, tenant.ID, 0, 10)
	if err != nil || len(eventList) != 1 || eventList[0].Type != domain.EventTenantCreated {
		t.Fatalf("committed unit must carry exactly one tenant-created event: %+v %v", eventList, err)
	}
}

// TestGetRunSurfacesTaskListFailure pins that run reads never silently
// degrade: an unavailable task store is a typed transient problem, not a
// 200 with an empty task list.
func TestGetRunSurfacesTaskListFailure(t *testing.T) {
	tasks := &flakyTasks{}
	ts, _ := auditServer(t, func(r *ports.Repositories) {
		tasks.TaskStore = r.Tasks
		r.Tasks = tasks
	})

	slug := "taskfail-" + uniqueSuffix()
	e := &env{t: t, baseURL: ts.URL, tenantAS: slug}
	var tenant map[string]any
	e.doJSON(t, http.MethodPost, "/v1/tenants", "", map[string]string{"slug": slug, "name": "TaskFail"}, &tenant, http.StatusCreated)

	_, threadID := e.seedProjectThread(slug)
	runID := e.startRun(t, threadID)

	tasks.setFail(true)
	status, _, raw := e.do(http.MethodGet, "/v1/runs/"+runID, e.headers(slug), "")
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "task_store_unavailable" {
		t.Fatalf("task-store outage must surface as its typed problem, got %d %s (%v)", status, raw, err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("task-store outage must be a transient failure, got %d", status)
	}

	tasks.setFail(false)
	status, _, raw = e.do(http.MethodGet, "/v1/runs/"+runID, e.headers(slug), "")
	if status != http.StatusOK {
		t.Fatalf("recovered store must serve the run again, got %d %s", status, raw)
	}
	var view struct {
		Tasks []any `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("run view must decode: %v", err)
	}
}
