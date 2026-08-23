package server

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// The request log field contract is ADR-0017: fixed low-cardinality values
// only — method, route pattern, status, duration, correlation id and its
// source, remote class. Raw URLs, query strings, headers, bodies, resource
// identifiers, secrets, and client addresses have no code path into these
// records.
const (
	headerCorrelation = "X-Request-ID"

	correlationSourceHeader    = "header"
	correlationSourceGenerated = "generated"

	remoteLoopback = "loopback"
	remotePrivate  = "private"
	remotePublic   = "public"
	remoteUnknown  = "unknown"
)

// correlation is the request's trace identifier (ADR-0017). Source states
// where it came from so logs never imply a client supplied an identifier
// that was actually generated here.
type correlation struct {
	id     string
	source string
}

// maxCorrelationIDLen bounds accepted header values; the acceptance grammar
// admits external identifiers (UUIDs, foreign trace ids) while refusing
// control characters, whitespace, quotes, and oversized input.
const maxCorrelationIDLen = 128

func validCorrelationID(v string) bool {
	if v == "" || len(v) > maxCorrelationIDLen {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '~' || r == ':' || r == '@' || r == '-':
		default:
			return false
		}
	}
	return true
}

// resolveCorrelation honors a well-formed inbound X-Request-ID verbatim —
// cross-system correlation — and generates a req_-prefixed identifier from
// the same primitive events use for IDs otherwise.
func resolveCorrelation(log *slog.Logger, r *http.Request) correlation {
	if v := r.Header.Get(headerCorrelation); validCorrelationID(v) {
		return correlation{id: v, source: correlationSourceHeader}
	}
	id, err := domain.NewID(domain.PrefixRequest)
	if err != nil {
		// Entropy exhaustion is a process-wide fault: name it in the log and
		// mark every identifier issued while it persists, instead of
		// fabricating randomness or failing requests over telemetry.
		log.Error("correlation id generation failed", "error", safeLogValue(err))
		id = domain.PrefixRequest + "_generation_failed"
	}
	return correlation{id: id, source: correlationSourceGenerated}
}

// remoteClass reduces the peer address to a bounded operational signal
// without retaining the address itself (ADR-0017): IPs are personal data,
// loopback/private/public is what operators actually act on. netip parsing
// keeps zone-qualified link-local v6 (the normal form on multi-interface
// hosts) classifiable instead of discarding it as unknown.
func remoteClass(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return remoteUnknown
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return remoteUnknown
	}
	switch {
	case addr.IsLoopback():
		return remoteLoopback
	case addr.IsPrivate() || addr.IsLinkLocalUnicast():
		return remotePrivate
	default:
		return remotePublic
	}
}

// requestRecorder captures the response status while leaving handler-visible
// writer semantics intact. Optional interfaces are preserved by capability:
// wrapWriter returns a wrapper that forwards Flush/Hijack only when the
// underlying writer truly supports them, so a handler's type assertion never
// succeeds against a silent no-op (ADR-0017). Unwrap lets
// http.ResponseController reach through to the real writer either way.
type requestRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *requestRecorder) WriteHeader(status int) {
	// net/http sends the first header write and ignores later ones (they are
	// logged as superfluous), so the first call is the only truthful
	// observation of the status the client actually receives. Every call
	// still forwards so the server's own superfluous-write detection and
	// handler semantics stay untouched.
	if !r.wroteHeader {
		r.wroteHeader = true
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *requestRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type flushRecorder struct{ *requestRecorder }

func (r *flushRecorder) Flush() {
	r.requestRecorder.ResponseWriter.(http.Flusher).Flush()
}

type hijackRecorder struct{ *requestRecorder }

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.requestRecorder.ResponseWriter.(http.Hijacker).Hijack()
}

type flushHijackRecorder struct{ *requestRecorder }

func (r *flushHijackRecorder) Flush() {
	r.requestRecorder.ResponseWriter.(http.Flusher).Flush()
}

func (r *flushHijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.requestRecorder.ResponseWriter.(http.Hijacker).Hijack()
}

// wrapWriter probes the underlying writer once per request and pairs the
// status-capturing core with the most specific truthful forwarding wrapper.
func wrapWriter(w http.ResponseWriter) (*requestRecorder, http.ResponseWriter) {
	base := &requestRecorder{ResponseWriter: w, status: http.StatusOK}
	_, canFlush := w.(http.Flusher)
	_, canHijack := w.(http.Hijacker)
	var out http.ResponseWriter = base
	switch {
	case canFlush && canHijack:
		out = &flushHijackRecorder{base}
	case canFlush:
		out = &flushRecorder{base}
	case canHijack:
		out = &hijackRecorder{base}
	}
	return base, out
}

// withRequestLog is the observability chain around every served route:
// panic recovery, metrics observation, and the redacted structured request
// log (ADR-0017). Recovery registers outermost so the deferred log observes
// the final status a recovered panic produced.
func (s *Server) withRequestLog(route string, requiresAuth bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec, writer := wrapWriter(w)
		corr := resolveCorrelation(s.log, r)
		writer.Header().Set(headerCorrelation, corr.id)

		defer func() {
			if s.metrics != nil {
				s.metrics.HTTPInFlightDec()
			}
		}()
		defer func() {
			if s.metrics != nil {
				s.metrics.HTTPObserveRequest(r.Method, route, rec.status, time.Since(started))
			}
			s.log.Log(r.Context(), levelFor(rec.status), "http_request",
				"method", r.Method,
				"route", route,
				"status", rec.status,
				"duration_ms", time.Since(started).Milliseconds(),
				"request_id", corr.id,
				"correlation_source", corr.source,
				"remote_class", remoteClass(r),
			)
		}()
		defer func() {
			recov := recover()
			if recov == nil {
				return
			}
			s.log.Error("panic recovered",
				"route", route,
				"request_id", corr.id,
				"panic", fmtValue(recov),
			)
			// Only a response that has not started can truthfully become a
			// 500 problem; past that point the truncated response stands.
			if !rec.wroteHeader {
				writeProblem(writer, r, &domain.Error{
					Kind:    domain.ErrKindInternal,
					Code:    "internal",
					Message: "unexpected server error",
				})
			}
		}()

		if s.metrics != nil {
			s.metrics.HTTPInFlightInc()
		}
		if requiresAuth {
			principal, derr := s.auth.Authenticate(r)
			if derr != nil {
				writeProblem(writer, r, derr)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		}
		next(writer, r)
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
