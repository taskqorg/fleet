package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Requirement: terminal receipts follow acknowledged events; risk: an oversized slow batch retries forever and leaves a physically closed execution running.
func TestEventBatchTimeoutFallsBackWithoutLosingTerminal(t *testing.T) {
	old := outboxClient
	outboxClient = &http.Client{Timeout: 40 * time.Millisecond, CheckRedirect: noRedirect}
	defer func() { outboxClient = old }()
	var mu sync.Mutex
	seen := map[string]bool{}
	terminal := false
	batches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v := decM(string(b))
		events := jArr(v, "events")
		mu.Lock()
		if len(events) > 1 {
			batches++
			seen[jStr(events[0], "id")] = true
			mu.Unlock()
			<-r.Context().Done()
			return
		}
		for _, ev := range events {
			seen[jStr(ev, "id")] = true
		}
		if r.URL.Path == "/api/hosts/receipts" {
			terminal = len(seen) == 2
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}))
	defer server.Close()
	db := journal(t)
	defer db.Close()
	for _, id := range []string{"fixture-1", "fixture-2"} {
		if err := queue(db, "/api/hosts/events", M{"executionId": "fixture", "events": []any{M{"id": id}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue(db, "/api/hosts/receipts", M{"executionId": "fixture", "commandId": "fixture-start", "status": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := flush(db, server.URL, "invented-token", "fixture-host"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if batches != 1 || len(seen) != 2 || !terminal || count(db, "outbox") != 0 || count(db, "outbox_failed") != 0 {
		t.Fatalf("batches=%d events=%d terminal=%v pending=%d", batches, len(seen), terminal, count(db, "outbox"))
	}
}
