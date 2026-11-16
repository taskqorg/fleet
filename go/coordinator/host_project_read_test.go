package main

import (
	"errors"
	"fleet/contract"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Requirement: a native agent's read project operation (project_context) answers without the GLOBAL lock, like the ops
// and agent-tool paths; write operations keep GLOBAL.
// Risk: one slow project_context for a large project held GLOBAL past the executor's 35 s delivery deadline, so every host
// poll and receipt queued behind it and the executor's outbox retried the cancelled request forever (16 Nov 2026).
func TestHostReadProjectOperationSkipsGlobal(t *testing.T) {
	for name, want := range map[string]bool{"project_context": true, "project_list": true, "catalogue": true, "project_update": false, "account_list": false, "no_such_operation": false} {
		if got := readProjectOperation(name); got != want {
			t.Fatalf("readProjectOperation(%q) = %v, want %v", name, got, want)
		}
	}
	env := openTestCoordinator(t, true)
	hostID := "h-proj-read"
	secret := env.registerHost(t, hostID)
	auth := map[string]string{"Authorization": "Bearer " + secret}
	poll := contract.PollRequest{HostId: hostID, Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}
	if c, b := env.callJSON(t, "POST", "/api/hosts/poll", poll, auth); c != 200 {
		t.Fatalf("initial poll = %d %v", c, b)
	}
	c, sess := eCreateSession(t, env)
	if c != 200 {
		t.Fatalf("create session = %d %v", c, sess)
	}
	sid := sess["sessionId"].(string)
	c, msg := env.callJSON(t, "POST", "/api/sessions/"+sid+"/messages", doc{"requestId": "req-proj-read", "text": "go"}, env.ownerHeaders())
	if c != 200 {
		t.Fatalf("first message = %d %v", c, msg)
	}
	execID := msg["id"].(string)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	c, cmds := env.callJSON(t, "POST", "/api/hosts/poll", poll, auth)
	if c != 200 {
		t.Fatalf("poll for command = %d %v", c, cmds)
	}
	cmdID := ""
	list, _ := cmds["commands"].([]any)
	for _, item := range list {
		if m := item.(map[string]any); m["executionId"] == execID {
			cmdID = m["id"].(string)
		}
	}
	if cmdID == "" {
		t.Fatalf("no command for %s: %v", execID, cmds)
	}
	if err := runTx(env.ctx, env.app, nil, true, GLOBAL, func(q *Request) error {
		s, err := get(q, sid, "session")
		if err != nil {
			return err
		}
		s["projectId"] = "cs"
		return put(q, "session", s)
	}); err != nil {
		t.Fatal(err)
	}

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- env.appStore.Tx(env.ctx, nil, true, GLOBAL, func(tx pgx.Tx) error {
			close(held)
			select {
			case <-release:
				return nil
			case <-time.After(10 * time.Second):
				return errors.New("GLOBAL holder timed out")
			}
		})
	}()
	<-held
	type result struct {
		code int
		body map[string]any
	}
	answered := make(chan result, 1)
	go func() {
		code, body := env.callJSON(t, "POST", "/api/hosts/project-operations", doc{
			"hostId": hostID, "executionId": execID, "commandId": cmdID,
			"delegation": doc{"operation": "project_context", "projectId": "cs", "requestId": "project_context-read", "arguments": doc{}},
		}, auth)
		answered <- result{code, body}
	}()
	select {
	case r := <-answered:
		close(release)
		if r.code != 200 || r.body["id"] != "cs" {
			t.Fatalf("project_context under held GLOBAL = %d %v", r.code, r.body)
		}
	case <-time.After(4 * time.Second):
		close(release)
		t.Fatal("host project_context waited for the GLOBAL lock")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
