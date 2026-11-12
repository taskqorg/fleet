package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// R1, R3; risk: retransmission duplicates a committed mutation or returns a foreign receipt.
// Outside fixture supplies a real coordinator and proved host; this test drives the native handler and actual outbox.
// A lost committed response must remain uncertain, replay once, and permit explicit attachment only after acknowledgment.
func TestPMTransportAgainstCoordinator(t *testing.T) {
	base := os.Getenv("FLEET_PM_TEST_URL")
	if base == "" {
		t.Skip("outside coordinator fixture")
	}
	var ctx M
	if err := json.Unmarshal([]byte(os.Getenv("FLEET_PM_TEST_CONTEXT")), &ctx); err != nil {
		t.Fatal(err)
	}
	token := os.Getenv("FLEET_PM_TEST_TOKEN")
	db := journal(t)
	defer db.Close()
	call := func(name string, args M) M {
		t.Helper()
		r, err := delegation(M{"method": "tools/call", "params": M{"name": name, "arguments": args}}, ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return r.(M)
	}
	// The advertised legacy no-argument context call must bind to this execution's project.
	contextDone := make(chan M, 1)
	go func() { contextDone <- call("project_context", M{}) }()
	contextDeadline := time.Now().Add(5 * time.Second)
	for count(db, "outbox") == 0 && time.Now().Before(contextDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := flush(db, base, token, jStr(ctx, "hostId")); err != nil {
		t.Fatal(err)
	}
	if result := <-contextDone; jAt(result, "isError") == true {
		t.Fatal("bound project context rejected", result)
	}
	var mu sync.Mutex
	lost := false
	attempts := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, _ := http.NewRequest("POST", base+r.URL.Path, bytes.NewReader(body))
		req.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		mu.Lock()
		attempts++
		drop := !lost && bytes.Contains(body, []byte(`"name":"task_create"`))
		if drop {
			lost = true
		}
		mu.Unlock()
		if drop {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(response.StatusCode)
		w.Write(answer)
	}))
	defer proxy.Close()
	// Hold the first mutation's answer until the native timeout exposes its journal identity.
	done := make(chan M, 1)
	go func() {
		done <- call("task_create", M{"projectId": "desiign", "title": os.Getenv("FLEET_PM_TEST_TITLE"), "expectedOutcome": "Read-only receipt"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for count(db, "outbox") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := flush(db, proxy.URL, token, jStr(ctx, "hostId")); err == nil {
		t.Fatal("lost committed response did not stay queued")
	}
	pending := <-done
	p := decM(jStr(jArr(pending, "content")[0], "text"))
	key := jStr(p, "requestKey")
	if jStr(p, "status") != "pending" || key == "" || count(db, "outbox") != 1 {
		t.Fatal("missing uncertain request identity", pending)
	}
	still := call("agent_tool_result", M{"requestKey": key})
	if jStr(decM(jStr(jArr(still, "content")[0], "text")), "status") != "pending" {
		t.Fatal(still)
	}
	if count(db, "outbox") != 1 {
		t.Fatal("lookup repeated mutation")
	}
	// Concurrent retransmissions of the exact durable envelope must all return the original acknowledgment.
	var body string
	if err := db.QueryRow("SELECT body FROM delegation_requests WHERE id=?", key).Scan(&body); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req, _ := http.NewRequest("POST", base+"/api/hosts/agent-tools", bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- ""
				return
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			results <- string(b)
		}()
	}
	a, b := <-results, <-results
	if a == "" || a != b {
		t.Fatal("concurrent retries disagree")
	}
	if err := flush(db, proxy.URL, token, jStr(ctx, "hostId")); err != nil {
		t.Fatal(err)
	}
	ack := call("agent_tool_result", M{"requestKey": key})
	task := decM(jStr(jArr(ack, "content")[0], "text"))
	id := jStr(task, "id")
	if id == "" {
		t.Fatal(ack)
	}
	// The local receipt must not be readable under another command/execution context.
	foreign := decM(enc(ctx))
	foreign["executionId"] = "foreign"
	r, err := delegation(M{"method": "tools/call", "params": M{"name": "agent_tool_result", "arguments": M{"requestKey": key}}}, foreign, db)
	if err != nil || jAt(r, "isError") != true {
		t.Fatal("foreign receipt", r, err)
	}
	// Drive the explicit session-task linkage through the same native transport, not through a test-only core shortcut.
	done = make(chan M, 1)
	go func() { done <- call("session_task_attach", M{"taskId": id}) }()
	deadline = time.Now().Add(5 * time.Second)
	for count(db, "outbox") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := flush(db, proxy.URL, token, jStr(ctx, "hostId")); err != nil {
		t.Fatal(err)
	}
	if r := <-done; jAt(r, "isError") == true {
		t.Fatal(r)
	}
	if attempts != 3 {
		t.Fatalf("outbox requests %d, want lost create/replayed create/attach", attempts)
	}
}
