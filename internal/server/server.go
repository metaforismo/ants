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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/metrics"
	"github.com/metaforismo/ants/internal/orchestration"
	"github.com/metaforismo/ants/internal/ports"
)

// Principal identifies the authenticated actor for one request.
type Principal struct {
	TenantID domain.TenantID
	Tenant   *domain.Tenant
	ID       domain.PrincipalID
}

// Authenticator is the authentication seam (ADR-0004). Production wires the
// OIDC resource-server verifier (internal/authn); with no provider
// configured, UnconfiguredAuthenticator refuses every request. There is no
// development bypass anywhere in the composition surface (ADR-0019).
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, *domain.Error)
}

// UnconfiguredAuthenticator refuses every request: serving unauthenticated
// traffic silently is never acceptable.
type UnconfiguredAuthenticator struct{}

func (UnconfiguredAuthenticator) Authenticate(_ *http.Request) (*Principal, *domain.Error) {
	return nil, &domain.Error{
		Kind:    domain.ErrKindUnauthorized,
		Code:    "authentication_not_configured",
		Message: "no identity provider configured; set auth.oidc.issuer_url and auth.oidc.audience to enable authentication",
	}
}

type Server struct {
	cfg    config.Config
	repos  ports.Repositories
	uow    ports.Transactor
	auth   Authenticator
	engine *orchestration.Engine
	// ready reports whether backing dependencies can serve traffic. It is
	// injected by the composition root per store mode: a bounded PostgreSQL
	// ping there, an always-ready check for the memory store — never a
	// sentinel query guessing at persistence health from inside the server.
	ready func(ctx context.Context) error
	log   *slog.Logger
	clock ports.Clock
	// metricsHandler serves the Prometheus exposition when metrics are
	// enabled; nil means the route is not registered at all (ADR-0014).
	metrics *metrics.Metrics

	http *http.Server
}

type Deps struct {
	Config config.Config
	Repos  ports.Repositories
	// Auth authenticates every route marked Auth in the route table.
	// Required: a server must be told explicitly whether it verifies OIDC
	// tokens or refuses everything — never neither (ADR-0019).
	Auth Authenticator
	// Uow delimits units of work so a resource and its creation event commit
	// together (ADR-0010). Required: an API write that emits an event must
	// never be able to commit the state and lose the notification.
	Uow    ports.Transactor
	Engine *orchestration.Engine
	Logger *slog.Logger
	// Ready performs the dependency checks behind /readyz. Required: a
	// server that cannot state its readiness must not pretend to be ready.
	Ready func(ctx context.Context) error
	// Metrics is the Prometheus collector behind /metrics. Required when the
	// configuration enables metrics: a server that promises an exposition
	// must not silently serve none.
	Metrics *metrics.Metrics
}

func New(deps Deps) (*Server, error) {
	if deps.Repos.Tenants == nil || deps.Uow == nil || deps.Engine == nil || deps.Logger == nil {
		return nil, fmt.Errorf("server: repos, uow, engine and logger are required")
	}
	if deps.Ready == nil {
		return nil, fmt.Errorf("server: a readiness check is required")
	}
	if deps.Config.Metrics.Enabled && deps.Metrics == nil {
		return nil, fmt.Errorf("server: metrics are enabled but no collector was provided")
	}
	// Authentication is injected, never chosen inside the server: the
	// composition root decides between OIDC verification and explicit
	// refusal (ADR-0019), and tests wire deterministic fakes.
	if deps.Auth == nil {
		return nil, fmt.Errorf("server: an authenticator is required; wire the OIDC verifier or UnconfiguredAuthenticator{}")
	}
	srv := &Server{
		cfg:     deps.Config,
		repos:   deps.Repos,
		uow:     deps.Uow,
		auth:    deps.Auth,
		engine:  deps.Engine,
		ready:   deps.Ready,
		log:     deps.Logger,
		clock:   ports.SystemClock{},
		metrics: deps.Metrics,
	}
	mux := http.NewServeMux()
	srv.routes(mux)
	srv.http = &http.Server{
		Addr:         deps.Config.Server.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  deps.Config.Server.ReadTimeout.Duration,
		WriteTimeout: deps.Config.Server.WriteTimeout.Duration,
		IdleTimeout:  deps.Config.Server.IdleTimeout.Duration,
		// Explicit stdlib default (1 MiB): header size is bounded so a
		// hostile client cannot balloon server memory with header lines.
		MaxHeaderBytes: http.DefaultMaxHeaderBytes,
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
		{Method: http.MethodGet, Path: "/metrics"},
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
		if route.Path == "/metrics" && s.metrics == nil {
			// Disabled by configuration: no stub route, requests fall to the
			// catch-all and get the uniform not-found problem (ADR-0014).
			continue
		}
		var handler http.HandlerFunc
		switch {
		case route.Path == "/healthz":
			handler = s.handleHealth
		case route.Path == "/readyz":
			handler = s.handleReady
		case route.Path == "/metrics":
			handler = promhttp.HandlerFor(s.metrics.Registry(), promhttp.HandlerOpts{}).ServeHTTP
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
		mux.Handle(route.Method+" "+route.Path, s.withRequestLog(route.Path, route.Auth, handler))
	}
	// Catch-all: unknown paths get RFC 9457 problems instead of net/http's
	// plain-text default.
	mux.Handle("/", s.withRequestLog(metrics.RouteUnmatched, false, func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, domain.NotFoundf("route", r.URL.Path))
	}))
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
	mode := "unconfigured"
	if s.cfg.Auth.OIDC.Configured() {
		mode = "oidc"
	}
	s.log.Info("ants api listening", "addr", s.cfg.Server.HTTPAddr, "auth", mode)
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
