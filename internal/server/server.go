// Package server exposes the versioned /v1 HTTP API for the tranche 1
// vertical slice. Handlers are thin adapters over domain stores and the
// orchestration engine; authentication is pluggable and dev-only mode is an
// explicit configuration decision.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/orchestration"
	"github.com/metaforismo/ants/internal/ports"
)

// Principal identifies the authenticated actor for one request.
type Principal struct {
	TenantID domain.TenantID
	Tenant   *domain.Tenant
	ID       domain.PrincipalID
}

type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, *domain.Error)
}

// DevHeaderAuthenticator implements local development auth via explicit
// headers. It must never be enabled outside development.
type DevHeaderAuthenticator struct {
	Tenants ports.TenantStore
}

const (
	headerDevTenant    = "X-Ants-Dev-Tenant"
	headerDevPrincipal = "X-Ants-Dev-Principal"
)

func (a *DevHeaderAuthenticator) Authenticate(r *http.Request) (*Principal, *domain.Error) {
	slug := r.Header.Get(headerDevTenant)
	if slug == "" {
		return nil, &domain.Error{
			Kind:    domain.ErrKindUnauthorized,
			Code:    "missing_tenant_header",
			Message: fmt.Sprintf("header %s is required", headerDevTenant),
		}
	}
	principal := r.Header.Get(headerDevPrincipal)
	if _, err := domain.ParsePrincipalID(principal); err != nil {
		return nil, &domain.Error{
			Kind:    domain.ErrKindUnauthorized,
			Code:    "invalid_principal",
			Message: fmt.Sprintf("header %s must contain a valid principal id", headerDevPrincipal),
		}
	}
	tenant, err := a.Tenants.GetBySlug(r.Context(), slug)
	if err != nil {
		return nil, &domain.Error{Kind: domain.ErrKindUnauthorized, Code: "unknown_tenant", Message: "tenant not recognized"}
	}
	return &Principal{TenantID: tenant.ID, Tenant: tenant, ID: domain.PrincipalID(principal)}, nil
}

// UnconfiguredAuthenticator refuses every request: production requires real
// OIDC, and silently serving unauthenticated traffic is not acceptable.
type UnconfiguredAuthenticator struct{}

func (UnconfiguredAuthenticator) Authenticate(_ *http.Request) (*Principal, *domain.Error) {
	return nil, &domain.Error{
		Kind:    domain.ErrKindUnauthorized,
		Code:    "authentication_not_configured",
		Message: "no authentication provider configured; enable dev_header_auth only for local development or deploy OIDC",
	}
}

type Server struct {
	cfg    config.Config
	repos  ports.Repositories
	auth   Authenticator
	engine *orchestration.Engine
	log    *slog.Logger
	clock  ports.Clock

	http *http.Server
}

type Deps struct {
	Config config.Config
	Repos  ports.Repositories
	Engine *orchestration.Engine
	Logger *slog.Logger
}

func New(deps Deps) (*Server, error) {
	if deps.Repos.Tenants == nil || deps.Engine == nil || deps.Logger == nil {
		return nil, fmt.Errorf("server: repos, engine and logger are required")
	}
	var auth Authenticator
	if deps.Config.Server.DevHeaderAuth {
		auth = &DevHeaderAuthenticator{Tenants: deps.Repos.Tenants}
	} else {
		auth = UnconfiguredAuthenticator{}
	}
	srv := &Server{
		cfg:    deps.Config,
		repos:  deps.Repos,
		auth:   auth,
		engine: deps.Engine,
		log:    deps.Logger,
		clock:  ports.SystemClock{},
	}
	mux := http.NewServeMux()
	srv.routes(mux)
	srv.http = &http.Server{
		Addr:         deps.Config.Server.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  deps.Config.Server.ReadTimeout.Duration,
		WriteTimeout: deps.Config.Server.WriteTimeout.Duration,
	}
	return srv, nil
}

// Route is one entry of the public API surface. The table below is the code
// side of the contract pinned by openapi/v1/openapi.yaml; the contract test
// fails when the two drift apart.
type Route struct {
	Method string
	Path   string // net/http mux pattern; placeholders use {id} syntax
	Auth   bool
}

// APIRoutes lists every /v1 endpoint plus health probes.
func APIRoutes() []Route {
	return []Route{
		{Method: http.MethodGet, Path: "/healthz"},
		{Method: http.MethodGet, Path: "/readyz"},
		{Method: http.MethodPost, Path: "/v1/tenants"},
		{Method: http.MethodGet, Path: "/v1/projects", Auth: true},
		{Method: http.MethodPost, Path: "/v1/projects", Auth: true},
		{Method: http.MethodPost, Path: "/v1/threads", Auth: true},
		{Method: http.MethodGet, Path: "/v1/threads/{id}", Auth: true},
		{Method: http.MethodPost, Path: "/v1/threads/{id}/messages", Auth: true},
		{Method: http.MethodGet, Path: "/v1/threads/{id}/messages", Auth: true},
		{Method: http.MethodPost, Path: "/v1/threads/{id}/runs", Auth: true},
		{Method: http.MethodGet, Path: "/v1/runs/{id}", Auth: true},
		{Method: http.MethodGet, Path: "/v1/runs/{id}/events", Auth: true},
		{Method: http.MethodPost, Path: "/v1/runs/{id}/cancel", Auth: true},
		{Method: http.MethodGet, Path: "/v1/runs/{id}/report", Auth: true},
		{Method: http.MethodGet, Path: "/v1/tasks/{id}", Auth: true},
		{Method: http.MethodGet, Path: "/v1/artifacts/{id}", Auth: true},
	}
}

func (s *Server) routes(mux *http.ServeMux) {
	for _, route := range APIRoutes() {
		route := route
		var handler http.HandlerFunc
		switch {
		case route.Path == "/healthz":
			handler = s.handleHealth
		case route.Path == "/readyz":
			handler = s.handleReady
		default:
			switch route.Path {
			case "/v1/tenants":
				handler = s.handleCreateTenant
			case "/v1/projects":
				if route.Method == http.MethodGet {
					handler = s.handleListProjects
				} else {
					handler = s.handleCreateProject
				}
			case "/v1/threads":
				handler = s.handleCreateThread
			case "/v1/threads/{id}":
				handler = s.handleGetThread
			case "/v1/threads/{id}/messages":
				if route.Method == http.MethodGet {
					handler = s.handleListMessages
				} else {
					handler = s.handleAppendMessage
				}
			case "/v1/threads/{id}/runs":
				handler = s.handleStartRun
			case "/v1/runs/{id}":
				handler = s.handleGetRun
			case "/v1/runs/{id}/events":
				handler = s.handleRunEvents
			case "/v1/runs/{id}/cancel":
				handler = s.handleCancelRun
			case "/v1/runs/{id}/report":
				handler = s.handleRunReport
			case "/v1/tasks/{id}":
				handler = s.handleGetTask
			case "/v1/artifacts/{id}":
				handler = s.handleGetArtifact
			default:
				panic(fmt.Sprintf("route %s %s has no handler mapping", route.Method, route.Path))
			}
		}
		mux.Handle(route.Method+" "+route.Path, s.wrap(handler, route.Auth))
	}
	// Catch-all: unknown paths get RFC 9457 problems instead of net/http's
	// plain-text default.
	mux.Handle("/", s.wrap(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, domain.NotFoundf("route", r.URL.Path))
	}, false))
}

// wrap applies the middleware chain; authenticated routes resolve their
// principal before any handler code runs.
func (s *Server) wrap(next http.HandlerFunc, requiresAuth bool) http.Handler {
	return s.recoverPanics(s.withRequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requiresAuth {
			principal, derr := s.auth.Authenticate(r)
			if derr != nil {
				writeProblem(w, r, derr)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		}
		next(w, r)
	})))
}

type principalKey struct{}

func principalFrom(ctx context.Context) (*Principal, error) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	if !ok || p == nil {
		return nil, fmt.Errorf("principal missing from request context")
	}
	return p, nil
}

func (s *Server) now() time.Time { return s.clock.Now().UTC() }

// Handler exposes the fully wrapped HTTP handler for external servers
// (httptest in tests, custom listeners later).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Start serves until Shutdown; listener errors are returned to the caller.
func (s *Server) Start() error {
	s.log.Info("ants api listening", "addr", s.cfg.Server.HTTPAddr, "dev_auth", s.cfg.Server.DevHeaderAuth)
	err := s.http.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown drains HTTP connections. Run execution is owned by the
// process-level worker (ADR-0012 part 2), so no background work remains tied
// to requests and nothing but the HTTP server itself needs draining here.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
