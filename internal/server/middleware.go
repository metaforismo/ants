package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "path", r.URL.Path, "panic", fmtValue(rec))
				writeProblem(w, r, &domain.Error{
					Kind:    domain.ErrKindInternal,
					Code:    "internal",
					Message: "unexpected server error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRequestLog(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			id, err := domain.NewID("req")
			if err != nil {
				requestID = "req_unknown"
			} else {
				requestID = id
			}
		}
		w.Header().Set("X-Request-ID", requestID)
		if s.metrics != nil {
			s.metrics.HTTPInFlightInc()
			defer s.metrics.HTTPInFlightDec()
		}
		next.ServeHTTP(rec, r)
		if s.metrics != nil {
			s.metrics.HTTPObserveRequest(r.Method, route, rec.status, time.Since(started))
		}
		s.log.Log(r.Context(), levelFor(rec.status), "http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", requestID,
		)
	})
}

func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func fmtValue(v any) string {
	const max = 256
	out := fmt.Sprintf("%v", v)
	if len(out) > max {
		out = out[:max]
	}
	return out
}
