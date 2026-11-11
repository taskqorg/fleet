package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Requirement: slow command preparation preserves host liveness and completed outputs between commands.
// Risk: a batch of native starts hides exits and stops heartbeats for the entire batch; heartbeat polls replay work.
func TestExchangeServicesCompletionAndHeartbeatDuringPreparation(t *testing.T) {
	e, root := scriptExecutor(t)
	lastSignal = nil
	t.Cleanup(func() { lastSignal = nil })
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	gate, waiting := filepath.Join(root, "gate"), filepath.Join(root, "waiting")
	cli := filepath.Join(root, "codex")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\nwhile test ! -f %q; do sleep 0.01; done\nprintf '%%s\\n' '{\"id\":2,\"result\":{\"rateLimits\":{\"primary\":{\"usedPercent\":42,\"windowDurationMins\":300}}}}'\n", waiting, gate)
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(root, "accounts.json")
	if err := os.WriteFile(reg, []byte(enc(M{"work": M{"harness": "codex", "home": home, "nativeHome": home, "executable": cli, "readiness": "ready"}})), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_NATIVE_ACCOUNTS", reg)
	if err := e.run(M{"id": "first", "executionId": "first", "sessionId": "first", "kind": "script", "payload": M{"command": "printf saved > \"$FLEET_OUTPUT_DIR/result.txt\""}}); err != nil {
		t.Fatal(err)
	}
	<-e.live["first"].exited
	slow := M{"id": "slow", "executionId": "slow", "sessionId": "slow", "kind": "account-inspect", "payload": M{"accountId": "account", "harness": "codex", "binding": "work", "mode": "limits"}}
	late := M{"id": "late", "executionId": "late", "sessionId": "late", "kind": "readiness", "payload": M{"project": M{}}}
	var polls atomic.Int32
	observed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pollRoute || r.Header.Get("Authorization") != "Bearer fixture" {
			w.WriteHeader(403)
			return
		}
		n := polls.Add(1)
		commands := []any{slow}
		if n > 1 {
			commands = append(commands, late)
			if _, err := os.Stat(waiting); err == nil {
				status := lastReceipt(e, "first")
				select {
				case observed <- status:
				default:
				}
				os.WriteFile(gate, []byte("release"), 0600)
			}
		}
		json.NewEncoder(w).Encode(M{"commands": commands})
	}))
	defer server.Close()
	e.url, e.token = server.URL, "fixture"
	// Failure cleanup only: the old implementation cannot generate the poll that releases the fake vendor.
	timeout := time.AfterFunc(5*time.Second, func() { os.WriteFile(gate, []byte("timeout"), 0600) })
	defer timeout.Stop()
	if err := e.exchange(); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-observed:
		if status != "completed" {
			t.Fatalf("prior receipt during preparation = %q", status)
		}
	default:
		t.Fatal("no heartbeat during blocked preparation")
	}
	if lastReceipt(e, "slow") != "completed" {
		t.Fatal("slow command did not complete")
	}
	if _, ok := e.commandBody("late", false); ok {
		t.Fatal("heartbeat executed a command outside the ordered batch")
	}
	if err := e.exchange(); err != nil {
		t.Fatal(err)
	}
	if lastReceipt(e, "late") != "completed" {
		t.Fatal("next exchange lost the pending command")
	}
	var count int
	if err := e.db.QueryRow("SELECT count(*) FROM commands WHERE id='slow'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("slow command count %d, %v", count, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "sessions/first/output/result.txt")); err != nil || string(b) != "saved" {
		t.Fatalf("saved output %q, %v", b, err)
	}
}

// Requirement: typed poll replies keep the complete old command identity across executor updates.
// Risk: dropping coordinator extensions changes the journal body and admits or fails a terminal replay.
func TestTypedPollRetainsTerminalCommandIdentity(t *testing.T) {
	e, _ := scriptExecutor(t)
	original := decM(`{"id":"retained","executionId":"retained-x","sessionId":"retained-s","kind":"script","status":"pending","payload":{"command":"exit 99"},"sourceGuard":{"revision":7}}`)
	body := enc(original)
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
