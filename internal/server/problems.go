package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/metaforismo/ants/internal/domain"
)

// problem is the RFC 9457 Problem Details body. The code extension field
// gives clients a stable machine-readable identifier per failure mode.
type problem struct {
	Type     string         `json:"type"`
	Code     string         `json:"code"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail,omitempty"`
	Instance string         `json:"instance,omitempty"`
	Extras   map[string]any `json:"extras,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, derr *domain.Error) {
	status := derr.Kind.HTTPStatus()
	body := problem{
		Type:     derr.Kind.ProblemType(),
		Code:     derr.Code,
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   derr.Message,
		Instance: r.URL.Path,
	}
	if len(derr.Details) > 0 {
		body.Extras = derr.Details
	}
	writeJSONStatus(w, status, body)
}

// decodeStrict parses a JSON body rejecting unknown fields so client typos
// surface as 400s instead of silently ignored configuration. Bodies are
// capped at 1 MiB; the ResponseWriter is passed through so an oversized body
// also flags the connection for teardown instead of draining it.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.Invalidf("invalid_body", "request body is not valid for this endpoint: %v", err)
	}
	return nil
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	contentType := "application/json; charset=utf-8"
	if _, isProblem := v.(problem); isProblem {
		contentType = "application/problem+json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeJSONStatus(w, status, v)
}

// ---- health ----

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady answers 200 only while every injected dependency check passes.
// A failing check is a transient problem: the process is alive but cannot
// serve, so orchestrators should stop routing traffic to it.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Server.ReadinessTimeout.Duration)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		s.log.Error("readiness probe failed", "error", safeLogValue(err))
		writeProblem(w, r, &domain.Error{
			Kind:    domain.ErrKindTransient,
			Code:    "store_unavailable",
			Message: "persistence layer not reachable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// safeLogValue bounds error text in logs so probe failures cannot flood the
// structured log with driver output.
func safeLogValue(err error) string {
	if err == nil {
		return "<nil>"
	}
	const max = 512
	msg := err.Error()
	if len(msg) > max {
		return msg[:max]
	}
	return msg
}
