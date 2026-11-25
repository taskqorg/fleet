package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type laneServer struct {
	mu      sync.Mutex
	got     []string // route and executionId of each answered request, in answer order
	events  map[string][]string
	release chan struct{}
	waiting chan struct{} // an events request is waiting for its release
}

// Event replies wait for the agent tool request. A watchdog fails the request if the tool never arrives.
// With no watchdog, events may finish on their own so a tool in the same lane can run.
func laneFixture(t *testing.T, watchdog <-chan struct{}) (*httptest.Server, *laneServer) {
	t.Helper()
	s := &laneServer{events: map[string][]string{}, release: make(chan struct{}), waiting: make(chan struct{}, 1000)}
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := decM(string(b))
		execution := jStr(body, "executionId")
		if r.URL.Path == "/api/hosts/events" {
			s.waiting <- struct{}{}
			var fallback <-chan time.Time
			if watchdog == nil {
				fallback = time.After(2 * time.Second)
			}
			select {
			case <-s.release:
			case <-fallback:
			case <-watchdog:
				http.Error(w, "agent tool did not release events", http.StatusGatewayTimeout)
				return
			}
		}
		s.mu.Lock()
		s.got = append(s.got, r.URL.Path+" "+execution)
		for _, e := range jArr(body, "events") {
			s.events[execution] = append(s.events[execution], jStr(e, "id"))
		}
		s.mu.Unlock()
		if r.URL.Path == "/api/hosts/agent-tools" { // recorded before the events it releases
			once.Do(func() { close(s.release) })
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":{"ok":true}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

// Requirement: an agent tool request behind many other executions' event rows goes out at once, and the interleaved
// event rows of each execution go in one request, in order. Risk (17 Nov 2026): agent tool calls of a host with 10-20
// executions waited minutes behind hundreds of event rows sent one request each, and answered pending.
func TestAgentToolRowOvertakesOtherExecutionsEvents(t *testing.T) {
	resetRetryRows(t)
	watchdog := make(chan struct{})
	srv, s := laneFixture(t, watchdog)
	db := journal(t)
	defer db.Close()
	for i := range 200 {
		x := fmt.Sprintf("busy-%d", i%20)
		if err := queue(db, "/api/hosts/events", M{"hostId": "h", "executionId": x, "events": []any{M{"id": fmt.Sprint(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	key := "cmd:agent:1"
	if _, err := db.Exec("INSERT INTO delegation_requests(id,body) VALUES(?,'{}')", key); err != nil {
		t.Fatal(err)
	}
	if err := queue(db, "/api/hosts/agent-tools", M{"hostId": "h", "commandId": "cmd", "executionId": "caller", "agentToolKey": key}); err != nil {
		t.Fatal(err)
	}
	// Bound a broken gate without making successful delivery depend on elapsed time.
	timer := time.AfterFunc(10*time.Second, func() { close(watchdog) })
	defer timer.Stop()
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) != 21 || s.got[0] != "/api/hosts/agent-tools caller" {
		t.Fatalf("requests %v, want the agent tool request answered first and one events request per execution", s.got)
	}
	for i := range 20 {
		x := fmt.Sprintf("busy-%d", i)
		want := []string{}
		for j := i; j < 200; j += 20 {
			want = append(want, fmt.Sprint(j))
		}
		if enc(s.events[x]) != enc(want) {
			t.Fatalf("%s events %v, want %v", x, s.events[x], want)
		}
	}
	var response string
	db.QueryRow("SELECT response FROM delegation_requests WHERE id=?", key).Scan(&response)
	if count(db, "outbox") != 0 || response != `{"result":{"ok":true}}` {
		t.Fatalf("pending=%d response=%q", count(db, "outbox"), response)
	}
}

// Requirement: a new row of an idle execution leaves while another execution's delivery is still in flight; the
// busy execution's new rows wait for its next run. Risk: one slow ingest holds every new agent tool request until it
// ends.
func TestNewRowLeavesWhileAnotherLaneDelivers(t *testing.T) {
	resetRetryRows(t)
	srv, s := laneFixture(t, nil)
	db := journal(t)
	defer db.Close()
	queue(db, "/api/hosts/events", M{"hostId": "h", "executionId": "slow", "events": []any{M{"id": "1"}}})
	wait, err := startFlush(db, srv.URL, "invented-token", "fixture-host")
	if err != nil {
		t.Fatal(err)
	}
	<-s.waiting // the slow lane's request is in flight
	queue(db, "/api/hosts/events", M{"hostId": "h", "executionId": "slow", "events": []any{M{"id": "2"}}})
	queue(db, "/api/hosts/agent-tools", M{"hostId": "h", "executionId": "caller", "commandId": "c", "agentToolKey": "c:agent:1"})
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := append([]string(nil), s.got...)
	s.mu.Unlock()
	if enc(got) != enc([]string{"/api/hosts/agent-tools caller", "/api/hosts/events slow"}) || count(db, "outbox") != 1 {
		t.Fatalf("requests %v pending %d, want the agent tool request first and the busy lane's new row kept", got, count(db, "outbox"))
	}
	if err := flush(db, srv.URL, "invented-token", "fixture-host"); err != nil || count(db, "outbox") != 0 {
		t.Fatalf("err=%v pending=%d, want the busy lane's new row delivered on its next run", err, count(db, "outbox"))
	}
}
