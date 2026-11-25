package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Requirement: typed poll replies keep the complete old command identity across executor updates.
// Risk: dropping coordinator extensions changes the journal body and admits or fails a terminal replay.
func TestTypedPollRetainsTerminalCommandIdentity(t *testing.T) {
	e, _ := scriptExecutor(t)
	original := decM(`{"id":"retained","executionId":"retained-x","sessionId":"retained-s","kind":"script","status":"pending","payload":{"command":"exit 99"},"sourceGuard":{"revision":9007199254740993,"ratio":1.0,"scale":1e3}}`)
	body := enc(original)
	reply, err := decodePoll(json.RawMessage(enc(M{"commands": []any{original}})))
	if err != nil || len(reply.bodies) != 1 || enc(reply.bodies[0]) != body {
		t.Fatalf("decoded command identity changed: %v", err)
	}
	if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", "retained", body); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"commands": []any{original}, "target": "1.2.3", "autoUpdate": "off"})
	}))
	defer server.Close()
	e.url = server.URL
	lastSignal = nil
	t.Cleanup(func() { lastSignal = nil })
	if err := e.exchange(); err != nil {
		t.Fatal(err)
	}
	if got, ok := e.commandBody("retained", false); !ok || got != body {
		t.Fatal("retained command identity changed")
	}
	if len(e.live) != 0 || count(e.db, "launches") != 0 || count(e.db, "outbox") != 0 {
		t.Fatal("terminal replay caused work or a new receipt")
	}
	if lastSignal == nil || lastSignal.Target == nil || *lastSignal.Target != "1.2.3" || lastSignal.AutoUpdate != "off" {
		t.Fatal("typed poll lost update fields")
	}
}

// Requirement: malformed coordinator-owned reply fields cannot acknowledge or execute journaled work.
// Risk: a 2xx JSON response with wrong field types silently becomes a healthy empty poll.
func TestTypedPollMismatchRetainsPendingWork(t *testing.T) {
	e, _ := scriptExecutor(t)
	body := enc(M{"id": "pending", "kind": "script", "executionId": "pending-x"})
	if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,0)", "pending", body); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"commands":[{"id":17,"kind":"script","executionId":"x"}]}`)
	}))
	defer server.Close()
	e.url = server.URL
	lastSignal = nil
	t.Cleanup(func() { lastSignal = nil })
	if err := e.exchange(); err == nil {
		t.Fatal("malformed typed reply accepted")
	}
	if got, ok := e.commandBody("pending", false); !ok || got != body {
		t.Fatal("pending work changed after decode failure")
	}
	if len(e.live) != 0 || count(e.db, "outbox") != 0 || lastSignal == nil || lastSignal.PollOk {
		t.Fatal("decode failure executed or acknowledged work")
	}
}
