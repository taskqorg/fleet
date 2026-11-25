package main

// Server-Timing for operations: an operation marks its phases with phase(ctx, name), and POST /api/ops/{name} answers
// them in the Server-Timing header (names and milliseconds, nothing else), so a slow read is broken down from the
// browser's network panel or curl on the deployed site, without logs.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type phaseKey struct{}

type phases struct {
	mu    sync.Mutex
	start time.Time
	list  []string
}

// phase starts timing a named phase of the request in ctx; the returned func ends it. A no-op outside an operation. The
// header is written with the response's first byte: a phase that ends later is not in it.
func phase(ctx context.Context, name string) func() {
	p, _ := ctx.Value(phaseKey{}).(*phases)
	if p == nil {
		return func() {}
	}
	t := time.Now()
	return func() {
		ms := float64(time.Since(t).Microseconds()) / 1000
		p.mu.Lock()
		p.list = append(p.list, fmt.Sprintf("%s;dur=%.1f", name, ms))
		p.mu.Unlock()
	}
}

// withPhases: the request carries a phase list, and the response writes it as Server-Timing before its first byte.
func withPhases(w http.ResponseWriter, hr *http.Request) (http.ResponseWriter, *http.Request) {
	p := &phases{start: time.Now()}
	return &timingWriter{ResponseWriter: w, p: p}, hr.WithContext(context.WithValue(hr.Context(), phaseKey{}, p))
}

type timingWriter struct {
	http.ResponseWriter
	p     *phases
	wrote bool
}

func (w *timingWriter) timing() {
	if w.wrote {
		return
	}
	w.wrote = true
	w.p.mu.Lock()
	list := append(w.p.list, fmt.Sprintf("total;dur=%.1f", float64(time.Since(w.p.start).Microseconds())/1000))
	w.p.mu.Unlock()
	w.Header().Set("Server-Timing", strings.Join(list, ", "))
}

func (w *timingWriter) WriteHeader(code int) { w.timing(); w.ResponseWriter.WriteHeader(code) }

func (w *timingWriter) Write(b []byte) (int, error) { w.timing(); return w.ResponseWriter.Write(b) }

// Unwrap lets http.ResponseController and obs.MarkRefused reach the writer underneath.
func (w *timingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush writes the header first, then flushes the writer underneath.
func (w *timingWriter) Flush() { w.timing(); _ = http.NewResponseController(w.ResponseWriter).Flush() }
