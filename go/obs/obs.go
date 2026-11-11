// Package obs records one span per unit of work (docs/designs/fleet-telemetry.md): the span row, W3C trace context, the
// HTTP handler and pass wrappers, and the sink that receives finished spans. Standard library only.
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Kinds and outcomes: the Enum8 values of the span table.
const (
	KindServer   = "server"
	KindClient   = "client"
	KindInternal = "internal"
	KindPass     = "pass"
	KindCheck    = "check"

	OK        = "ok"
	Error     = "error"
	Refused   = "refused"
	Cancelled = "cancelled"
)

// ErrorClasses: the closed list of error_class values. A new class is a code change.
var ErrorClasses = []string{"timeout", "auth", "http_4xx", "http_5xx", "pg_conflict", "pg", "network", "decode", "panic", "internal"}

// Span is one row of the ClickHouse span table; JSON names are its columns (JSONEachRow).
type Span struct {
	TraceID          string            `json:"trace_id"`
	SpanID           string            `json:"span_id"`
	ParentSpanID     string            `json:"parent_span_id"`
	Service          string            `json:"service"`
	HostID           string            `json:"host_id"`
	Version          string            `json:"version"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	Start            string            `json:"start"` // UTC "2006-01-02 15:04:05.000000"
	DurationNs       uint64            `json:"duration_ns"`
	Outcome          string            `json:"outcome"`
	ErrorClass       string            `json:"error_class"`
	Actor            string            `json:"actor"`
	ProjectID        *string           `json:"project_id"`
	TaskID           *string           `json:"task_id"`
	ExecutionID      *string           `json:"execution_id"`
	ExperimentID     *string           `json:"experiment_id"`
	AccountID        *string           `json:"account_id"`
	TokensIn         *uint64           `json:"tokens_in"`
	TokensOut        *uint64           `json:"tokens_out"`
	TokensCacheRead  *uint64           `json:"tokens_cache_read"`
	TokensCacheWrite *uint64           `json:"tokens_cache_write"`
	Units            *float64          `json:"units"`
	UnitKind         string            `json:"unit_kind"`
	Bytes            *uint64           `json:"bytes"`
	Items            *uint64           `json:"items"`
	Retries          *uint64           `json:"retries"`
	Load1            *float64          `json:"load1"`
	MemFree          *float64          `json:"mem_free"`
	DiskFree         *float64          `json:"disk_free"`
	StoreFree        *float64          `json:"store_free"`
	CPUSeconds       *float64          `json:"cpu_seconds"`
	RSSBytes         *uint64           `json:"rss_bytes"`
	PeakRSSBytes     *uint64           `json:"peak_rss_bytes"`
	OpenFiles        *uint32           `json:"open_files"`
	Attrs            map[string]string `json:"attrs"`

	begin time.Time
	ended bool
}

// TimeFormat is the span start as ClickHouse DateTime64(6) text.
const TimeFormat = "2006-01-02 15:04:05.000000"

// Run is the work identity copied onto every span of a request or command (never joined).
type Run struct {
	ProjectID, TaskID, ExecutionID, ExperimentID, AccountID, Actor string
}

// Process identity, set once by main.
var (
	Service, HostID, Version string
	sink                     atomic.Pointer[func(*Span)]
	// AttrMaxBytes caps one attribute value (setting telemetry.attrMaxBytes).
	AttrMaxBytes = 256
)

// SetSink sets where finished spans go; nil drops them. The sink must not block.
func SetSink(f func(*Span)) {
	if f == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&f)
}

type ctxKey int

const (
	spanKey ctxKey = iota
	runKey
	remoteKey
)

type remote struct{ trace, span string }

// WithRun returns ctx carrying the run identity for spans started under it.
func WithRun(ctx context.Context, r Run) context.Context { return context.WithValue(ctx, runKey, r) }

// RunOf is the run identity in ctx.
func RunOf(ctx context.Context) Run { r, _ := ctx.Value(runKey).(Run); return r }

// WithRemote returns ctx whose next span is a child of the W3C traceparent tp (ignored when invalid).
func WithRemote(ctx context.Context, tp string) context.Context {
	t, s, ok := ParseTraceparent(tp)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, remoteKey, remote{t, s})
}

// From is the span in ctx, or nil.
func From(ctx context.Context) *Span { s, _ := ctx.Value(spanKey).(*Span); return s }

// Traceparent is the W3C header for the span in ctx, or "".
func Traceparent(ctx context.Context) string {
	if s := From(ctx); s != nil {
		return "00-" + s.TraceID + "-" + s.SpanID + "-01"
	}
	return ""
}

var traceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

// ParseTraceparent reads a W3C traceparent (version 00); all-zero ids are invalid.
func ParseTraceparent(tp string) (trace, span string, ok bool) {
	m := traceparent.FindStringSubmatch(strings.TrimSpace(tp))
	if m == nil || m[1] == strings.Repeat("0", 32) || m[2] == strings.Repeat("0", 16) {
		return "", "", false
	}
	return m[1], m[2], true
}

func id(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Start begins a span named name under the span (or remote parent) in ctx; with neither it starts a new trace.
func Start(ctx context.Context, name, kind string) (context.Context, *Span) {
	s := &Span{SpanID: id(8), Service: Service, HostID: HostID, Version: Version, Name: name, Kind: kind, Outcome: OK, begin: time.Now()}
	if p := From(ctx); p != nil {
		s.TraceID, s.ParentSpanID = p.TraceID, p.SpanID
	} else if r, ok := ctx.Value(remoteKey).(remote); ok {
		s.TraceID, s.ParentSpanID = r.trace, r.span
	} else {
		s.TraceID = id(16)
	}
	r := RunOf(ctx)
	s.ProjectID, s.TaskID, s.ExecutionID, s.ExperimentID, s.AccountID = opt(r.ProjectID), opt(r.TaskID), opt(r.ExecutionID), opt(r.ExperimentID), opt(r.AccountID)
	s.Actor = r.Actor
	if s.Actor == "" {
		s.Actor = "fleet"
	}
	return context.WithValue(ctx, spanKey, s), s
}

// allowed: attribute key prefixes a span may carry; content never goes into spans.
var allowed = []string{"http.", "server.", "db.", "gen_ai.request.model", "fleet.", "missing."}

// Set stores one attribute. Keys outside the allow list are refused and counted in missing.attrs; long values are cut.
func (s *Span) Set(key, value string) {
	if s == nil {
		return
	}
	if s.Attrs == nil {
		s.Attrs = map[string]string{}
	}
	ok := false
	for _, p := range allowed {
		if strings.HasPrefix(key, p) {
			ok = true
			break
		}
	}
	if !ok {
		n, _ := strconv.Atoi(s.Attrs["missing.attrs"])
		s.Attrs["missing.attrs"] = strconv.Itoa(n + 1)
		return
	}
	if len(value) > AttrMaxBytes {
		value = value[:AttrMaxBytes]
	}
	s.Attrs[key] = value
}

// Fail marks the span an error of class (one of ErrorClasses).
func (s *Span) Fail(class string) {
	if s != nil {
		s.Outcome, s.ErrorClass = Error, class
	}
}

// End finishes the span once (by the goroutine that owns it): err (when the outcome is still ok) marks it an error, classed by Classify; the span goes to the sink.
func (s *Span) End(err error) {
	if s == nil || s.ended {
		return
	}
	s.ended = true
	if err != nil && s.Outcome == OK {
		if errors.Is(err, context.Canceled) {
			s.Outcome = Cancelled
		} else {
			s.Fail(Classify(err))
		}
	}
	s.Start = s.begin.UTC().Format(TimeFormat)
	s.DurationNs = uint64(time.Since(s.begin))
	if f := sink.Load(); f != nil {
		(*f)(s)
	}
}

var hex32, hex16 = regexp.MustCompile(`^[0-9a-f]{32}$`), regexp.MustCompile(`^([0-9a-f]{16})?$`)

// spanName: names are stable identifiers (route patterns, pass and call names), never free text.
var spanName = regexp.MustCompile(`^[A-Za-z0-9 _.:/{}@+-]{1,200}$`)

// Valid: a span row from outside (a host's batch) fits the span table: hex ids, known kind and outcome, a start in
// TimeFormat, a stable name; its attributes pass the same allow list and cap as Set.
func Valid(s *Span) bool {
	if !hex32.MatchString(s.TraceID) || !hex16.MatchString(s.SpanID) || s.SpanID == "" || !hex16.MatchString(s.ParentSpanID) || !spanName.MatchString(s.Name) {
		return false
	}
	if _, err := time.Parse(TimeFormat, s.Start); err != nil {
		return false
	}
	switch s.Kind {
	case KindServer, KindClient, KindInternal, KindPass, KindCheck:
	default:
		return false
	}
	switch s.Outcome {
	case OK, Error, Refused, Cancelled:
	default:
		return false
	}
	in := s.Attrs
	s.Attrs = nil
	for k, v := range in {
		s.Set(k, v)
	}
	return true
}

// Classify maps an error to its error_class.
func Classify(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.As(err, &ne):
		return "network"
	}
	return "internal"
}

// Pass runs one pass of a periodic loop as a pass span pass.<name> with its item count.
func Pass(ctx context.Context, name string, fn func(context.Context) (int, error)) {
	ctx, s := Start(ctx, "pass."+name, KindPass)
	n, err := 0, error(nil)
	defer func() {
		if r := recover(); r != nil {
			s.Fail("panic")
			s.End(nil)
			panic(r)
		}
		u := uint64(max(n, 0))
		s.Items = &u
		s.End(err)
	}()
	n, err = fn(ctx)
}

// HTTPOutcome maps a response status to outcome and error_class (fleet-telemetry.md §1). refused marks a 403 or 409
// that is a decision (limit, pause, grant).
func HTTPOutcome(status int, refused bool) (string, string) {
	switch {
	case status < 400:
		return OK, ""
	case status == 401 || status == 423 || status == 429 || refused:
		return Refused, ""
	case status < 500:
		return Error, "http_4xx"
	}
	return Error, "http_5xx"
}

// Recorder is the ResponseWriter the handler wrapper passes on; MarkRefused flags a refusal by design.
type Recorder struct {
	http.ResponseWriter
	Status  int
	refused bool
}

func (r *Recorder) WriteHeader(c int) {
	if r.Status == 0 {
		r.Status = c
	}
	r.ResponseWriter.WriteHeader(c)
}
func (r *Recorder) Write(b []byte) (int, error) {
	if r.Status == 0 {
		r.Status = 200
	}
	return r.ResponseWriter.Write(b)
}
func (r *Recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// MarkRefused marks the response a refusal by design, when w is a Recorder.
func MarkRefused(w http.ResponseWriter) {
	if r, ok := w.(*Recorder); ok {
		r.refused = true
	}
}

// Handler records one server span per request; name gives the span name from the request after next served it
// (the matched route pattern); "" drops the span (a long-lived stream).
func Handler(next http.Handler, name func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, s := Start(WithRemote(r.Context(), r.Header.Get("traceparent")), "", KindServer)
		rec := &Recorder{ResponseWriter: w}
		r2 := r.WithContext(ctx) // the mux records the matched pattern on the request it serves
		defer func() {
			p := recover()
			n := name(r2)
			if n == "" {
				if p != nil {
					panic(p)
				}
				return
			}
			s.Name = n
			s.Set("http.route", n)
			if p != nil {
				rec.Status = 500
			}
			if rec.Status == 0 {
				rec.Status = 200
			}
			s.Set("http.response.status_code", strconv.Itoa(rec.Status))
			s.Outcome, s.ErrorClass = HTTPOutcome(rec.Status, rec.refused)
			if p != nil {
				s.ErrorClass = "panic"
			} else if r.Context().Err() != nil && rec.Status < 400 {
				s.Outcome = Cancelled
			}
			s.End(nil)
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r2)
	})
}

// Transport records one client span named name per outbound call and sends traceparent.
func Transport(name string, rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return roundTripper{name, rt}
}

type roundTripper struct {
	name string
	rt   http.RoundTripper
}

func (t roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, s := Start(r.Context(), t.name, KindClient)
	s.Set("server.address", r.URL.Hostname())
	r = r.Clone(ctx)
	r.Header.Set("traceparent", Traceparent(ctx))
	resp, err := t.rt.RoundTrip(r)
	if err == nil {
		s.Set("http.response.status_code", strconv.Itoa(resp.StatusCode))
		s.Outcome, s.ErrorClass = HTTPOutcome(resp.StatusCode, false)
	}
	s.End(err)
	return resp, err
}
