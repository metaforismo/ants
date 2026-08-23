package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- fake writers probing the recorder's capability forwarding ----

type baseWriter struct {
	header       http.Header
	wrote        []byte
	statuses     []int
	writeHeaderN int
}

func newBaseWriter() *baseWriter { return &baseWriter{header: http.Header{}} }

func (w *baseWriter) Header() http.Header { return w.header }
func (w *baseWriter) Write(p []byte) (int, error) {
	w.wrote = append(w.wrote, p...)
	return len(p), nil
}
func (w *baseWriter) WriteHeader(s int) { w.writeHeaderN++; w.statuses = append(w.statuses, s) }

type flushOnlyWriter struct {
	*baseWriter
	flushed int
}

func (w *flushOnlyWriter) Flush() { w.flushed++ }

type hijackOnlyWriter struct {
	*baseWriter
	hijacked bool
}

func (w *hijackOnlyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, errors.New("no real connection in unit harness")
}

type flushHijackWriter struct {
	*flushOnlyWriter
	hijacked bool
}

func (w *flushHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, errors.New("no real connection in unit harness")
}

func TestWrapWriterPreservesCapabilities(t *testing.T) {
	t.Run("plain writer gains no lying optional interfaces", func(t *testing.T) {
		base := newBaseWriter()
		core, wrapped := wrapWriter(base)
		if _, ok := wrapped.(http.Flusher); ok {
			t.Error("plain wrapper must not claim Flusher")
		}
		if _, ok := wrapped.(http.Hijacker); ok {
			t.Error("plain wrapper must not claim Hijacker")
		}
		if len(wrapped.Header()) != 0 {
			t.Error("fresh wrapper must forward the underlying header map")
		}
		core.WriteHeader(http.StatusTeapot)
		if len(base.statuses) != 1 || base.statuses[0] != http.StatusTeapot {
			t.Errorf("WriteHeader must forward exactly once, got %v", base.statuses)
		}
		if !core.wroteHeader || core.status != http.StatusTeapot {
			t.Errorf("core must capture status/wroteHeader, got %d %v", core.status, core.wroteHeader)
		}
	})

	t.Run("flusher is forwarded", func(t *testing.T) {
		under := &flushOnlyWriter{baseWriter: newBaseWriter()}
		core, wrapped := wrapWriter(under)
		f, ok := wrapped.(http.Flusher)
		if !ok {
			t.Fatal("wrapper must expose Flusher when underlying supports it")
		}
		f.Flush()
		if under.flushed != 1 {
			t.Fatalf("flush must reach the underlying writer, flushed=%d", under.flushed)
		}
		if _, ok := wrapped.(http.Hijacker); ok {
			t.Error("flush-only writer must not gain Hijacker")
		}
		if u := wrapped.(interface{ Unwrap() http.ResponseWriter }).Unwrap(); u != under {
			t.Error("Unwrap must return the underlying writer")
		}
		_ = core
	})

	t.Run("hijacker is forwarded", func(t *testing.T) {
		under := &hijackOnlyWriter{baseWriter: newBaseWriter()}
		_, wrapped := wrapWriter(under)
		h, ok := wrapped.(http.Hijacker)
		if !ok {
			t.Fatal("wrapper must expose Hijacker when underlying supports it")
		}
		if _, _, err := h.Hijack(); err == nil {
			t.Fatal("expected harness hijack error to prove forwarding")
		}
		if !under.hijacked {
			t.Error("Hijack must reach the underlying writer")
		}
		if _, ok := wrapped.(http.Flusher); ok {
			t.Error("hijack-only writer must not gain Flusher")
		}
	})

	t.Run("both capabilities are forwarded", func(t *testing.T) {
		under := &flushHijackWriter{flushOnlyWriter: &flushOnlyWriter{baseWriter: newBaseWriter()}}
		_, wrapped := wrapWriter(under)
		f, ok := wrapped.(http.Flusher)
		if !ok {
			t.Fatal("Flusher missing")
		}
		h, ok := wrapped.(http.Hijacker)
		if !ok {
			t.Fatal("Hijacker missing")
		}
		f.Flush()
		_, _, _ = h.Hijack()
		if under.flushed != 1 || !under.hijacked {
			t.Errorf("capabilities must forward: flushed=%d hijacked=%v", under.flushed, under.hijacked)
		}
	})
}

func TestRecorderImplicitStatusIsOK(t *testing.T) {
	// net/http answers an implicit 200 when a handler writes a body without
	// WriteHeader; the log must report that truthfully.
	base := newBaseWriter()
	rec, _ := wrapWriter(base)
	rec.Write([]byte("ok"))
	if rec.status != http.StatusOK || rec.wroteHeader {
		t.Errorf("implicit 200 must be captured without claiming explicit write, got %d wrote=%v", rec.status, rec.wroteHeader)
	}
}

func TestRecorderMultipleWriteHeaderRecordsWhatWasSent(t *testing.T) {
	// net/http sends only the first WriteHeader code and treats later calls
	// as superfluous no-ops. The recorder must report the first (the status
	// actually on the wire) while forwarding every call verbatim.
	base := newBaseWriter()
	rec, wrapped := wrapWriter(base)
	wrapped.WriteHeader(http.StatusTeapot)
	wrapped.WriteHeader(http.StatusBadGateway)

	if len(base.statuses) != 2 || base.statuses[0] != http.StatusTeapot || base.statuses[1] != http.StatusBadGateway {
		t.Errorf("every WriteHeader must forward verbatim, got %v", base.statuses)
	}
	if rec.status != http.StatusTeapot || !rec.wroteHeader {
		t.Errorf("recorder must capture the first sent status (%d), got %d wrote=%v", http.StatusTeapot, rec.status, rec.wroteHeader)
	}
}

// ---- panic handling through the real middleware chain ----

func middlewareServer(log *slog.Logger) *Server {
	return &Server{log: log}
}

func serveThrough(t *testing.T, s *Server, handler http.HandlerFunc) (*httptest.ResponseRecorder, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	s.log = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	req := httptest.NewRequest(http.MethodGet, "/v1/threads/thr_unitprobe00000000000001", nil)
	res := httptest.NewRecorder()
	s.withRequestLog("/v1/threads/{id}", false, handler).ServeHTTP(res, req)
	return res, buf
}

func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestPanicBeforeResponseBecomesTypedProblem(t *testing.T) {
	s := middlewareServer(nil)
	res, buf := serveThrough(t, s, func(w http.ResponseWriter, r *http.Request) {
		panic(errors.New("handler exploded badly"))
	})

	if res.Code != http.StatusInternalServerError {
		t.Errorf("recovered panic must answer 500, got %d", res.Code)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &problem); err != nil || problem.Code != "internal" {
		t.Errorf("recovered panic must be an internal problem, got %q (%v)", res.Body.String(), err)
	}

	records := decodeRecords(t, buf)
	var panicRec, reqRec map[string]any
	for _, m := range records {
		switch m["msg"] {
		case "panic recovered":
			panicRec = m
		case "http_request":
			reqRec = m
		}
	}
	if panicRec == nil || reqRec == nil {
		t.Fatalf("panic and request records required, got %v", records)
	}
	if panicRec["route"] != "/v1/threads/{id}" {
		t.Errorf("panic record must use normalized route, got %v", panicRec["route"])
	}
	if !strings.Contains(fmt.Sprint(panicRec["panic"]), "handler exploded") {
		t.Errorf("panic value should be bounded but present, got %v", panicRec["panic"])
	}
	if panicRec["request_id"] != reqRec["request_id"] {
		t.Errorf("panic and request records must share the correlation id: %v vs %v", panicRec["request_id"], reqRec["request_id"])
	}
	if reqRec["status"] != float64(http.StatusInternalServerError) {
		t.Errorf("request record must observe the final 500, got %v", reqRec["status"])
	}
}

func TestPanicAfterResponseStartDoesNotDoubleWrite(t *testing.T) {
	base := newBaseWriter()
	s := middlewareServer(nil)
	s.log = slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("mid-stream failure")
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	core, wrapped := wrapWriter(base)
	s.withRequestLog("/x", false, handler).ServeHTTP(wrapped, req)

	if base.writeHeaderN != 1 {
		t.Errorf("a response that already started must not get a second WriteHeader, got %d", base.writeHeaderN)
	}
	if core.status != http.StatusOK {
		t.Errorf("status stays what was actually sent, got %d", core.status)
	}
}

func TestRequestLogReportsFirstSentStatusOnDoubleWrite(t *testing.T) {
	// A buggy handler that writes headers twice gets exactly one response
	// from net/http — the first code. Metrics and logs observe that one.
	s := middlewareServer(nil)
	buf := &bytes.Buffer{}
	s.log = slog.New(slog.NewJSONHandler(buf, nil))
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.WriteHeader(http.StatusInternalServerError)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	base := newBaseWriter()
	_, wrapped := wrapWriter(base)
	s.withRequestLog("/x", false, handler).ServeHTTP(wrapped, req)

	var reqRec map[string]any
	for _, m := range decodeRecords(t, buf) {
		if m["msg"] == "http_request" {
			reqRec = m
		}
	}
	if reqRec == nil {
		t.Fatal("request record required")
	}
	if reqRec["status"] != float64(http.StatusTeapot) {
		t.Errorf("log must report the first sent status %d, got %v", http.StatusTeapot, reqRec["status"])
	}
}

func TestPanicValueIsBoundedInLogs(t *testing.T) {
	s := middlewareServer(nil)
	huge := strings.Repeat("x", 5000)
	_, buf := serveThrough(t, s, func(http.ResponseWriter, *http.Request) { panic(huge) })
	for _, m := range decodeRecords(t, buf) {
		if m["msg"] != "panic recovered" {
			continue
		}
		p, ok := m["panic"].(string)
		if !ok || len(p) > 256 {
			t.Fatalf("panic value must be bounded at 256 chars, got %d", len(p))
		}
	}
}

// ---- correlation + remote class grammars ----

func TestValidCorrelationID(t *testing.T) {
	cases := map[string]bool{
		"req_abc123":                           true,
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301": true,
		"a.b_c~d:e@f-g":                        true,
		"":                                     false,
		strings.Repeat("a", 128):               true,
		strings.Repeat("a", 129):               false,
		"has space":                            false,
		"new\nline":                            false,
		"tab\there":                            false,
		"sl/ash":                               false,
		"unicodeé":                             false,
	}
	for input, want := range cases {
		if got := validCorrelationID(input); got != want {
			t.Errorf("validCorrelationID(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestResolveCorrelationTruthfulSources(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	gen := resolveCorrelation(slog.New(slog.NewTextHandler(io.Discard, nil)), req)
	if gen.source != correlationSourceGenerated || !strings.HasPrefix(gen.id, "req_") {
		t.Errorf("absent header must generate req_ id with generated source, got %+v", gen)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headerCorrelation, "external-trace.42")
	got := resolveCorrelation(slog.New(slog.NewTextHandler(io.Discard, nil)), req)
	if got.id != "external-trace.42" || got.source != correlationSourceHeader {
		t.Errorf("valid inbound id must pass through as header-sourced, got %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headerCorrelation, "bad value\r\ninject")
	got = resolveCorrelation(slog.New(slog.NewTextHandler(io.Discard, nil)), req)
	if got.source != correlationSourceGenerated || strings.Contains(got.id, "bad") {
		t.Errorf("malformed inbound id must be fully replaced, got %+v", got)
	}
}

func TestRemoteClassVocabulary(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:12345":    "loopback",
		"[::1]:8080":         "loopback",
		"192.168.1.10:5000":  "private",
		"10.0.0.3:9999":      "private",
		"172.16.5.5:80":      "private",
		"[fe80::1%en0]:443":  "private",
		"93.184.216.34:443":  "public",
		"2606:2800:220:1::1": "", // no port → SplitHostPort fails below; handled by dedicated case
		"not-an-address":     "unknown",
		"":                   "unknown",
	}
	for addr, want := range cases {
		r := &http.Request{RemoteAddr: addr}
		if got := remoteClass(r); got != want && want != "" {
			t.Errorf("remoteClass(%q) = %q, want %q", addr, got, want)
		}
	}
	public := &http.Request{RemoteAddr: "[2001:db8::1]:443"}
	if got := remoteClass(public); got != "public" {
		t.Errorf("global unicast v6 must be public, got %q", got)
	}
	noPort := &http.Request{RemoteAddr: "127.0.0.1"}
	if got := remoteClass(noPort); got != "unknown" {
		t.Errorf("unparseable address must classify unknown, got %q", got)
	}
}
