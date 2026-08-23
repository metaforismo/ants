package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/metaforismo/ants/internal/correlation"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/orchestration"
)

// ---- tenants ----

type createTenantRequest struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	idStr, err := domain.NewID(domain.PrefixTenant)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	tenant, terr := domain.NewTenant(domain.TenantID(idStr), req.Slug, req.Name, domain.PlanFree, req.Region, s.now())
	if terr != nil {
		writeProblem(w, r, asDomainError(terr))
		return
	}
	// Tenant insert and its creation event commit as one unit (ADR-0010):
	// a tenant whose event cannot be persisted must not exist half-notified,
	// and the durable outbox delivery joins the same commit via Append.
	if terr := s.uow.Do(r.Context(), func(ctx context.Context) error {
		if cerr := s.repos.Tenants.Create(ctx, tenant); cerr != nil {
			return cerr
		}
		return s.emitTenantEvent(ctx, tenant)
	}); terr != nil {
		writeProblem(w, r, asDomainError(terr))
		return
	}
	writeJSON(w, http.StatusCreated, tenant)
}

// ---- projects ----

type createProjectRequest struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	SeedName      string `json:"seed_name"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	var req createProjectRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	idStr, err := domain.NewID(domain.PrefixProject)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	project, perr := domain.NewProject(domain.ProjectID(idStr), p.TenantID, req.Slug, req.Name, req.DefaultBranch, req.SeedName, s.now())
	if perr != nil {
		writeProblem(w, r, asDomainError(perr))
		return
	}
	if cerr := s.repos.Projects.Create(r.Context(), project); cerr != nil {
		writeProblem(w, r, asDomainError(cerr))
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	projects, err := s.repos.Projects.ListByTenant(r.Context(), p.TenantID)
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// ---- threads and messages ----

type createThreadRequest struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	var req createThreadRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	projectID, err := domain.ParseProjectID(req.ProjectID)
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	if _, err := s.repos.Projects.Get(r.Context(), p.TenantID, projectID); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	idStr, idErr := domain.NewID(domain.PrefixThread)
	if idErr != nil {
		writeInternal(w, r, idErr)
		return
	}
	thread, terr := domain.NewThread(domain.ThreadID(idStr), p.TenantID, projectID, req.Title, p.ID, s.now())
	if terr != nil {
		writeProblem(w, r, asDomainError(terr))
		return
	}
	if cerr := s.repos.Threads.Create(r.Context(), thread); cerr != nil {
		writeProblem(w, r, asDomainError(cerr))
		return
	}
	writeJSON(w, http.StatusCreated, thread)
}

// handleListThreads serves the tenant's most recently updated threads. The
// page is bounded server-side; the web client renders it as one list and
// re-fetches, so no cursor contract is exposed yet.
func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threads, err := s.repos.Threads.ListByTenant(r.Context(), p.TenantID, defaultPageLimit)
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threadID, err := domain.ParseThreadID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	thread, getErr := s.repos.Threads.Get(r.Context(), p.TenantID, threadID)
	if getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

type appendMessageRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleAppendMessage(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threadID, err := domain.ParseThreadID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	if _, err := s.repos.Threads.Get(r.Context(), p.TenantID, threadID); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	var req appendMessageRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	idStr, idErr := domain.NewID(domain.PrefixMessage)
	if idErr != nil {
		writeInternal(w, r, idErr)
		return
	}
	msg := &domain.Message{
		ID:           domain.MessageID(idStr),
		TenantID:     p.TenantID,
		ThreadID:     threadID,
		Role:         domain.RoleUser,
		DeliveryMode: domain.DeliveryImmediate,
		Content:      req.Content,
	}
	if verr := msg.Validate(); verr != nil {
		writeProblem(w, r, asDomainError(verr))
		return
	}
	if aerr := s.repos.Threads.AppendMessage(r.Context(), msg); aerr != nil {
		writeProblem(w, r, asDomainError(aerr))
		return
	}
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threadID, err := domain.ParseThreadID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	after, cerr := parseAfterCursor(r)
	if cerr != nil {
		writeProblem(w, r, cerr)
		return
	}
	messages, total, listErr := s.repos.Threads.Messages(r.Context(), p.TenantID, threadID, after, defaultPageLimit)
	if listErr != nil {
		writeProblem(w, r, asDomainError(listErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "total": total})
}

// handleListThreadRuns serves one page of the thread's run history in the
// store's stable oldest-first (created_at asc, id asc) order. Runs append
// only at the tail, so offsets never reshuffle; clients that need the true
// latest run walk pages up to the authoritative `total` (see the console's
// listAllThreadRuns) instead of relying on any single bounded page. The
// store distinguishes an unknown or foreign-tenant thread (uniform 404,
// ADR-0004) from a known thread whose page is simply empty, so no existence
// oracle leaks here.
func (s *Server) handleListThreadRuns(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threadID, err := domain.ParseThreadID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	after, cerr := parseAfterCursor(r)
	if cerr != nil {
		writeProblem(w, r, cerr)
		return
	}
	runs, total, listErr := s.repos.Runs.ListByThread(r.Context(), p.TenantID, threadID, after, defaultPageLimit)
	if listErr != nil {
		writeProblem(w, r, asDomainError(listErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "total": total})
}

// ---- runs ----

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	threadID, err := domain.ParseThreadID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(w, r, domain.Invalidf("idempotency_key_required", "the Idempotency-Key header is required to start a run"))
		return
	}
	if len(key) > domain.MaxIdempotencyKeyLen {
		writeProblem(w, r, domain.Invalidf("idempotency_key_too_long", "idempotency key exceeds %d characters", domain.MaxIdempotencyKeyLen))
		return
	}

	result, startErr := s.engine.StartRun(r.Context(), orchestration.StartInput{
		TenantID:       p.TenantID,
		ThreadID:       threadID,
		Principal:      p.ID,
		Actor:          domain.Actor{Type: domain.PrincipalHuman, ID: string(p.ID)},
		IdempotencyKey: key,
	})
	if startErr != nil {
		writeProblem(w, r, asDomainError(startErr))
		return
	}
	// StartRun only enqueues durable work (run + runnable claim in one unit
	// of work); execution is owned by the process-level run worker started
	// next to the outbox dispatcher (ADR-0012 part 2). Nothing here spawns
	// background work tied to this request's context.
	writeJSONStatus(w, http.StatusAccepted, result.Run)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	runID, err := domain.ParseRunID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	run, getErr := s.repos.Runs.Get(r.Context(), p.TenantID, runID)
	if getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	tasks, listErr := s.repos.Tasks.ListByRun(r.Context(), p.TenantID, runID)
	if listErr != nil {
		writeProblem(w, r, asDomainError(listErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "tasks": tasks})
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	runID, err := domain.ParseRunID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	if _, getErr := s.repos.Runs.Get(r.Context(), p.TenantID, runID); getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	after, cerr := parseAfterCursor(r)
	if cerr != nil {
		writeProblem(w, r, cerr)
		return
	}
	events, listErr := s.repos.Events.ListByRun(r.Context(), p.TenantID, runID, after, defaultPageLimit)
	if listErr != nil {
		writeProblem(w, r, asDomainError(listErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	runID, err := domain.ParseRunID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	if cancelErr := s.engine.Cancel(r.Context(), p.TenantID, runID); cancelErr != nil {
		writeProblem(w, r, asDomainError(cancelErr))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func (s *Server) handleRunReport(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	runID, err := domain.ParseRunID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	run, getErr := s.repos.Runs.Get(r.Context(), p.TenantID, runID)
	if getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	if !run.Status.Terminal() || run.Report == nil {
		writeProblem(w, r, domain.Conflictf("report_not_ready", "report becomes available once the run finishes (current state: %s)", run.Status))
		return
	}
	writeJSON(w, http.StatusOK, run.Report)
}

// ---- tasks and artifacts ----

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	taskID, err := domain.ParseTaskID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	task, getErr := s.repos.Tasks.Get(r.Context(), p.TenantID, taskID)
	if getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	artifactID, err := domain.ParseArtifactID(r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, asDomainError(err))
		return
	}
	artifact, getErr := s.repos.Artifacts.Get(r.Context(), p.TenantID, artifactID)
	if getErr != nil {
		writeProblem(w, r, asDomainError(getErr))
		return
	}
	contentType := "text/plain; charset=utf-8"
	switch artifact.Kind {
	case domain.ArtifactReport:
		contentType = "application/json; charset=utf-8"
	case domain.ArtifactDiff:
		contentType = "text/x-diff; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Ants-Digest", artifact.Digest)
	w.Header().Set("X-Ants-Retention", string(artifact.Retention))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(artifact.Content)
}

// ---- shared helpers ----

const defaultPageLimit = 200

// parseAfterCursor parses the shared `after` list cursor. The grammar is
// exactly the OpenAPI schema (integer, minimum 0, default 0): an omitted
// cursor means 0, and a present cursor must be a single value of decimal
// digits only. Anything else — repeated parameters, explicit empties,
// signs, whitespace or other padding, non-digits, values beyond int64 — is
// a typed invalid_cursor problem rather than a silent fallback to the
// default, so a client bug can never masquerade as "page from the start".
// Leading zeros are accepted: the numeric value, not its notation,
// identifies the offset.
func parseAfterCursor(r *http.Request) (int64, *domain.Error) {
	values := r.URL.Query()["after"]
	switch len(values) {
	case 0:
		return 0, nil
	case 1:
		// Fall through to value validation below.
	default:
		return 0, domain.Invalidf("invalid_cursor", "the after cursor must be given at most once")
	}
	raw := values[0]
	if raw == "" || !isASCIIDigits(raw) {
		return 0, domain.Invalidf("invalid_cursor", "the after cursor must be a non-negative integer")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// Digits-only input can only overflow int64 here.
		return 0, domain.Invalidf("invalid_cursor", "the after cursor exceeds the supported range")
	}
	return v, nil
}

// isASCIIDigits reports whether s is one or more ASCII digits — no sign,
// no whitespace, no padding characters — the canonical wire form of a
// non-negative integer.
func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// emitTenantEvent appends the tenant-created event; its durable outbox
// delivery joins whatever unit the caller has open (ADR-0011), so a failure
// rolls back together with the tenant insert. The trace_id slot carries the
// request's correlation identifier when one is being served (ADR-0018).
func (s *Server) emitTenantEvent(ctx context.Context, tenant *domain.Tenant) error {
	id, err := domain.NewID(domain.PrefixEvent)
	if err != nil {
		return fmt.Errorf("generate event id: %w", err)
	}
	evt := &domain.Event{
		ID:            domain.EventID(id),
		Type:          domain.EventTenantCreated,
		OccurredAt:    s.now(),
		TenantID:      tenant.ID,
		AggregateType: "tenant",
		AggregateID:   string(tenant.ID),
		TraceID:       correlation.TraceID(ctx, ""),
		Data: map[string]any{
			"slug": tenant.Slug,
			"plan": string(tenant.Plan),
		},
	}
	return s.repos.Events.Append(ctx, evt)
}

func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	writeProblem(w, r, domain.Internalf(err, "internal", "unexpected server error"))
}

func mustPrincipal(w http.ResponseWriter, r *http.Request) *Principal {
	p, err := principalFrom(r.Context())
	if err != nil {
		writeProblem(w, r, &domain.Error{Kind: domain.ErrKindUnauthorized, Code: "no_principal", Message: "authentication required"})
		return nil
	}
	return p
}

func asDomainError(err error) *domain.Error {
	var dom *domain.Error
	if errors.As(err, &dom) {
		return dom
	}
	return domain.Internalf(err, "internal", "%v", err)
}
