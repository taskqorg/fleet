package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetRetryRows(t *testing.T) {
	t.Helper()
	clear := func() {
		retryMu.Lock()
		retryRows = map[int64]*edgeState{}
		retryMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func shortDelivery(t *testing.T) {
	t.Helper()
	old := deliveryClient
	deliveryClient = &http.Client{Timeout: 40 * time.Millisecond, CheckRedirect: noRedirect}
	t.Cleanup(func() { deliveryClient = old })
}

// Requirement: a row that times out or gets a 5xx holds only its own execution; other executions' rows keep flowing,
// on its route too. Risk (16 Nov 2026): one cancelled /api/hosts/project-operations request at the head of the outbox
// stopped every later row, including other executions' terminal receipts.
func TestRetryingRowHoldsOnlyItsExecution(t *testing.T) {
	resetRetryRows(t)
	shortDelivery(t)
	var mu sync.Mutex
	delivered := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/api/hosts/project-operations", strings.Contains(string(b), `"slow-event`):
			<-r.Context().Done() // slower than the delivery deadline
			return
		case r.URL.Path == "/api/hosts/delegations":
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "{}")
			return
		}
		mu.Lock()
		delivered[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer server.Close()
	db := journal(t)
	defer db.Close()
	rows := []struct {
		route string
		value M
	}{
		{"/api/hosts/project-operations", M{"executionId": "slow-op", "commandId": "slow-cmd", "delegation": M{"requestId": "r1"}}},
		{"/api/hosts/delegations", M{"executionId": "bad-gateway", "commandId": "bg-cmd", "delegation": M{"requestId": "r2"}}},
		{"/api/hosts/events", M{"executionId": "slow-op", "events": []any{M{"id": "held-behind-op"}}}},
		{"/api/hosts/events", M{"executionId": "slow-ingest", "events": []any{M{"id": "slow-event-1"}}}},
		{"/api/hosts/events", M{"executionId": "slow-ingest", "events": []any{M{"id": "slow-event-2"}}}},
		{"/api/hosts/events", M{"executionId": "other", "events": []any{M{"id": "other-1"}}}},
		{"/api/hosts/receipts", M{"executionId": "other", "commandId": "other-cmd", "status": "completed"}},
	}
	for _, r := range rows {
		if err := queue(db, r.route, r.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := flush(db, server.URL, "invented-token", "fixture-host"); err == nil {
		t.Fatal("flush reported success with rows still retrying")
	}
	mu.Lock()
	defer mu.Unlock()
	if delivered["/api/hosts/events"] != 1 || delivered["/api/hosts/receipts"] != 1 {
		t.Fatalf("other execution not delivered past the retrying rows: %v", delivered)
	}
	if n := count(db, "outbox"); n != 5 || count(db, "outbox_failed") != 0 {
		t.Fatalf("pending=%d failed=%d, want the two slow executions' rows and the 502 row queued", n, count(db, "outbox_failed"))
	}
}

// Requirement: a retrying row waits out its own backoff instead of costing a full delivery deadline on every pass.
func TestRetryingRowBacksOff(t *testing.T) {
	resetRetryRows(t)
	shortDelivery(t)
	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		attempts++
		mu.Unlock()
		<-r.Context().Done()
	}))
	defer server.Close()
	db := journal(t)
	defer db.Close()
	if err := queue(db, "/api/hosts/project-operations", M{"executionId": "slow", "commandId": "c", "delegation": M{"requestId": "r"}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = flush(db, server.URL, "invented-token", "fixture-host")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 || count(db, "outbox") != 1 {
		t.Fatalf("attempts=%d pending=%d, want one attempt inside the backoff and the row kept", attempts, count(db, "outbox"))
	}
}

// Requirement: an unreachable coordinator (connection refused, not a timeout) stops the pass at once and keeps every row.
func TestUnreachableCoordinatorStopsFlush(t *testing.T) {
	resetRetryRows(t)
	shortDelivery(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	ln.Close()
	db := journal(t)
	defer db.Close()
	for _, route := range []string{"/api/hosts/receipts", "/api/hosts/events", "/api/hosts/agent-tools"} {
		if err := queue(db, route, M{"executionId": route, "commandId": "c"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := flush(db, url, "invented-token", "fixture-host"); err == nil {
		t.Fatal("flush reported success against an unreachable coordinator")
	}
	if count(db, "outbox") != 3 || !heldOnly.Load() {
		t.Fatalf("pending=%d heldOnly=%v, want every row kept and the long interval", count(db, "outbox"), heldOnly.Load())
	}
	retryMu.Lock()
	defer retryMu.Unlock()
	if len(retryRows) != 0 {
		t.Fatalf("an unreachable coordinator put rows into backoff: %v", retryRows)
	}
}
