package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: a tool command queued for a store host (artifact-verify, artifact-copy) wakes that host's held poll.
// Risk: the store host sees the command only when its 25 s poll times out, so every remote-store artifact, and the
// completed run waiting for it, waits up to one more poll period.
func TestQueuedToolCommandWakesHeldPoll(t *testing.T) {
	env := openTestCoordinator(t, false)
	sec := env.registerHost(t, "h-store")
	body, err := json.Marshal(contract.PollRequest{HostId: "h-store", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}})
	if err != nil {
		t.Fatal(err)
	}
	type answered struct {
		err      error
		code     int
		commands []any
	}
	got := make(chan answered, 1)
	go func() {
		req, err := http.NewRequestWithContext(env.ctx, "POST", env.server.URL+"/api/hosts/poll?wait=25s", bytes.NewReader(body))
		if err != nil {
			got <- answered{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+sec)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- answered{err: err}
			return
		}
		defer resp.Body.Close()
		var reply struct {
			Commands []any `json:"commands"`
		}
		err = json.NewDecoder(resp.Body).Decode(&reply)
		got <- answered{err: err, code: resp.StatusCode, commands: reply.Commands}
	}()
	// Hold: the poll has entered; the pause lets its first read find nothing pending, so only a wake (or the 25 s
	// timeout) can answer it. The helper callJSON is not used here: it calls t.Fatal, which a goroutine must not.
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		env.app.pollMu.Lock()
		hb := env.app.heartbeats["h-store"]
		entered := hb != nil && hb.activePolls > 0
		env.app.pollMu.Unlock()
		if entered {
			break
		}
		if time.Now().After(end) {
			t.Fatal("poll never entered")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if err := queueCommand(env.ctx, env.app, "h-store", "artifact-verify", "a1", doc{"artifactId": "a1", "storeId": "s1", "url": "http://store.invalid/a1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-got:
		if a.err != nil || a.code != 200 || len(a.commands) != 1 {
			t.Fatalf("held poll after queueCommand: err=%v status=%d commands=%v", a.err, a.code, a.commands)
		}
	case <-time.After(3 * time.Second): // without a wake the poll answers only at its 25 s deadline
		t.Fatal("queued tool command did not wake the held poll")
	}
}
