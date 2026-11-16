package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Requirement: a row that times out or gets a 5xx holds only its own route and execution for this flush; other
// executions' events and receipts keep flowing. Risk (16 Nov 2026): one cancelled /api/hosts/project-operations
// request at the head of the outbox stopped every later row, including other executions' terminal receipts, and the
// host update waited on the drain forever.
func TestRetryingRowHoldsOnlyItsRouteAndExecution(t *testing.T) {
	old := deliveryClient
	deliveryClient = &http.Client{Timeout: 40 * time.Millisecond, CheckRedirect: noRedirect}
	defer func() { deliveryClient = old }()
	var mu sync.Mutex
	delivered := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/hosts/project-operations":
			<-r.Context().Done() // slower than the delivery deadline
			return
		case "/api/hosts/delegations":
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
		{"/api/hosts/project-operations", M{"executionId": "slow", "commandId": "slow-cmd", "delegation": M{"requestId": "r1"}}},
		{"/api/hosts/delegations", M{"executionId": "bad-gateway", "commandId": "bg-cmd", "delegation": M{"requestId": "r2"}}},
		{"/api/hosts/events", M{"executionId": "slow", "events": []any{M{"id": "slow-1"}}}},
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
	if n := count(db, "outbox"); n != 3 || count(db, "outbox_failed") != 0 {
		t.Fatalf("pending=%d failed=%d, want the slow, bad-gateway and held slow-execution rows queued", n, count(db, "outbox_failed"))
	}
}

// Requirement: an unreachable coordinator still stops the flush after the second network error, instead of waiting
// out one deadline per queued row.
func TestUnreachableCoordinatorStopsFlush(t *testing.T) {
	old := deliveryClient
	deliveryClient = &http.Client{Timeout: 40 * time.Millisecond, CheckRedirect: noRedirect}
	defer func() { deliveryClient = old }()
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
	for _, route := range []string{"/api/hosts/receipts", "/api/hosts/events", "/api/hosts/agent-tools", "/api/hosts/delegations"} {
		if err := queue(db, route, M{"executionId": route, "commandId": "c"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := flush(db, server.URL, "invented-token", "fixture-host"); err == nil {
		t.Fatal("flush reported success against an unreachable coordinator")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 || count(db, "outbox") != 4 {
		t.Fatalf("attempts=%d pending=%d, want 2 attempts and every row kept", attempts, count(db, "outbox"))
	}
}
