package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Requirement: an outbox row the coordinator needs longer than the heartbeat deadline to ingest is delivered by one
// request; the rows behind it (the terminal receipt) follow without waiting for repeated attempts.
// Risk: each attempt is cut at the heartbeat deadline, the coordinator rolls the cancelled ingest back, and the same
// work restarts from scratch, so a slow process.closed batch holds the whole outbox for several deadlines.
func TestSlowIngestDeliveredByOneRequest(t *testing.T) {
	if deliveryClient.Timeout <= outboxClient.Timeout {
		t.Fatalf("delivery deadline %v is not longer than the heartbeat deadline %v", deliveryClient.Timeout, outboxClient.Timeout)
	}
	// Scaled: ingest 150 ms, heartbeat deadline 40 ms, delivery deadline 1 s.
	oldHeartbeat, oldDelivery := outboxClient, deliveryClient
	outboxClient = &http.Client{Timeout: 40 * time.Millisecond, CheckRedirect: noRedirect}
	deliveryClient = &http.Client{Timeout: time.Second, CheckRedirect: noRedirect}
	defer func() { outboxClient, deliveryClient = oldHeartbeat, oldDelivery }()
	var mu sync.Mutex
	attempts, stored := map[string]int{}, []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the server notices a client cancel only after the body is read
		mu.Lock()
		attempts[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/api/hosts/events" {
			select { // a slow ingest: longer than the heartbeat deadline, committed only if the client still waits
			case <-time.After(150 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		mu.Lock()
		stored = append(stored, r.URL.Path)
		mu.Unlock()
		io.WriteString(w, "{}")
	}))
	defer server.Close()
	db := journal(t)
	defer db.Close()
	if err := queue(db, "/api/hosts/events", M{"executionId": "fixture", "events": []any{M{"id": "closed", "type": "process.closed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := queue(db, "/api/hosts/receipts", M{"executionId": "fixture", "commandId": "fixture-start", "status": "completed"}); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for i := 0; i < 5 && count(db, "outbox") > 0; i++ { // the outbox loop retries a failed flush
		flush(db, server.URL, "invented-token", "fixture-host")
	}
	mu.Lock()
	defer mu.Unlock()
	if count(db, "outbox") != 0 || attempts["/api/hosts/events"] != 1 || len(stored) != 2 || stored[1] != "/api/hosts/receipts" {
		t.Fatalf("pending=%d event attempts=%d stored=%v", count(db, "outbox"), attempts["/api/hosts/events"], stored)
	}
	t.Logf("slow event and receipt delivered in %v", time.Since(began).Round(time.Millisecond))
}
