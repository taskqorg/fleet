package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Requirement: after the coordinator asks to slow down (503), lanes hold, and once the wait ends one request probes it
// before every lane is asked again. Risk: an overloaded coordinator gets one request per execution every 2 s.
func TestStopProbesWithOneRequest(t *testing.T) {
	resetRetryRows(t)
	var mu sync.Mutex
	asked, down := 0, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		asked++
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer srv.Close()
	db := journal(t)
	defer db.Close()
	for i := range 5 {
		queue(db, "/api/hosts/receipts", M{"hostId": "h", "executionId": fmt.Sprint("x", i), "commandId": "c", "status": "running"})
	}
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err == nil || count(db, "outbox") != 5 {
		t.Fatalf("err=%v pending=%d, want the 503 to stop the pass with every row kept", err, count(db, "outbox"))
	}
	reset := func() {
		mu.Lock()
		asked = 0
		mu.Unlock()
	}
	expire := func() {
		stopMu.Lock()
		stops[db].until = nowMs() - 1
		stopMu.Unlock()
	}
	got := func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
	reset()
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err != nil || got() != 0 {
		t.Fatalf("err=%v asked=%d, want every lane held inside the wait", err, got())
	}
	expire()
	flush(db, srv.URL, "invented-token", "fixture-host")
	if got() != 1 || count(db, "outbox") != 5 {
		t.Fatalf("asked=%d pending=%d, want one probing request after the wait", got(), count(db, "outbox"))
	}
	expire()
	mu.Lock()
	down = false
	mu.Unlock()
	flush(db, srv.URL, "invented-token", "fixture-host") // the probe is answered; lanes that held go on the next pass
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err != nil || count(db, "outbox") != 0 {
		t.Fatalf("err=%v pending=%d, want every row delivered once the coordinator answers", err, count(db, "outbox"))
	}
}
