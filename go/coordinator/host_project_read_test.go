package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fleet/contract"
	"io"
	"net/http"
	"testing"
	"time"
)

// Requirement: a native agent's read project operation (project_context) answers without the GLOBAL lock, like the ops
// and agent-tool paths; write operations keep GLOBAL.
// Risk: one slow project_context for a large project held GLOBAL past the executor's 35 s delivery deadline, so every host
// poll and receipt queued behind it and the executor's outbox retried the cancelled request forever (16 Nov 2026).
func TestHostReadProjectOperationSkipsGlobal(t *testing.T) {
	reads := map[string]bool{}
	for _, v := range contract.ProjectOperationValues {
		if op, ok := projectOperation(string(v)); ok && op.read {
			reads[string(v)] = true
		}
	}
	for _, name := range []string{"project_context", "catalogue", "pipeline_get", "pipeline_history", "question_history"} {
		if !reads[name] {
			t.Fatalf("%s is not a registered read: %v", name, reads)
		}
	}
	for _, name := range []string{"pipeline_run", "direct_search", "question_ask", "account_list", "connections_key_request"} {
		if reads[name] {
			t.Fatalf("%s must keep the GLOBAL write transaction", name)
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
	c, msg := env.callJSON(t, "POST", "/api/ops/message_send", doc{"sessionId": sid, "requestId": "req-proj-read", "text": "go"}, env.ownerHeaders())
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
	if err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		s, err := sessionByID(q, sid)
		if err != nil {
			return err
		}
		s.ProjectID = "cs"
		return sessionPut(q, &s)
	}); err != nil {
		t.Fatal(err)
	}

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- env.appStore.Write(env.ctx, nil, []string{GLOBAL}, func(_ *Request) error {
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
	send := func(operation string, arguments doc) <-chan int {
		answered := make(chan int, 1)
		buf, _ := json.Marshal(doc{
			"hostId": hostID, "executionId": execID, "commandId": cmdID,
			"delegation": doc{"operation": operation, "projectId": "cs", "requestId": operation + "-read-test", "arguments": arguments},
		})
		go func() {
			req, _ := http.NewRequestWithContext(env.ctx, "POST", env.server.URL+"/api/hosts/project-operations", bytes.NewReader(buf))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				answered <- -1
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			answered <- resp.StatusCode
		}()
		return answered
	}
	write := send("question_ask", doc{"requestId": "q-read-test", "text": "still serialized?"})
	for _, op := range []string{"project_context", "catalogue", "pipeline_history", "question_history"} {
		select {
		case code := <-send(op, doc{}):
			if code != 200 {
				close(release)
				t.Fatalf("%s under held GLOBAL = %d", op, code)
			}
		case <-time.After(4 * time.Second):
			close(release)
			t.Fatalf("host %s waited for the GLOBAL lock", op)
		}
	}
	select {
	case code := <-write:
		close(release)
		t.Fatalf("question_ask answered %d while GLOBAL was held", code)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case code := <-write:
		if code != 200 {
			t.Fatalf("question_ask after GLOBAL release = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("question_ask did not finish after GLOBAL release")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// requirement: T-FLT-58 retains the initiating person and lineage when managed workers call pipeline_run.
// risk: project-tool receipts lose the initiating person, borrow the holder's grants, or return before the run can commit.
func TestHostPipelineRunInitiatingActor(t *testing.T) {
	env := openTestCoordinator(t, false)
	const hostID = "h-pipeline"
	const member = "member@trial.test"
	const project = "desiign"
	auth := map[string]string{"Authorization": "Bearer " + env.registerHost(t, hostID)}

	for _, tc := range []struct {
		name, executionDirector, sessionDirector, takenFrom, wantActor string
		wantStatus                                                     int
	}{
		{"execution director", member, testOwnerEmail, "", member, 200},
		{"session director", "", member, "", member, 200},
		{"taken work", testOwnerEmail, testOwnerEmail, member, member, 200},
		{"missing initiator", "", "", "", "", 403},
		{"initiator outside project", testOwnerEmail, testOwnerEmail, "outsider@trial.test", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sid, eid, cid, rid := newID(), newID(), newID(), newID()
			err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
				if err := sessionPut(q, &agentSession{ID: sid, ProjectID: project, DirectedBy: tc.sessionDirector}); err != nil {
					return err
				}
				if err := exec(q, `INSERT INTO execution(id,kind,state,project_id,session_id,host_id,directed_by,data)
					VALUES ($1,'agent_run','running',$2,$3,$4,$5,$6)`,
					eid, project, sid, hostID, strPtr(tc.executionDirector), jsonBytes(doc{"takenFrom": tc.takenFrom})); err != nil {
					return err
				}
				return commandPut(q, &agentCommand{ID: cid, Kind: "start", ExecutionID: eid, SessionID: sid, HostID: hostID, ProjectID: project})
			})
			if err != nil {
				t.Fatal(err)
			}

			body := doc{
				"hostId": hostID, "executionId": eid, "commandId": cid,
				"delegation": doc{"operation": "pipeline_run", "projectId": project, "requestId": rid,
					"arguments": doc{"requestId": rid, "query": "fixture"}},
			}
			code, reply := env.callJSON(t, "POST", "/api/hosts/project-operations", body, auth)
			if code != tc.wantStatus {
				t.Fatalf("pipeline_run = %d %v, want %d", code, reply, tc.wantStatus)
			}
			err = env.app.store.Read(env.ctx, nil, func(q *Request) error {
				if tc.wantActor == "" {
					var count int
					if err := q.tx.QueryRow(q.ctx(), `SELECT count(*) FROM execution WHERE kind='pipeline_run' AND data->>'requestId'=$1`, rid).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Fatalf("rejected call left %d pipeline runs", count)
					}
					return nil
				}
				id := astr(reply["id"])
				run, err := getRun(q, id)
				if err != nil {
					return err
				}
				if run == nil || run.ParentExecutionID != eid || run.SessionID != sid ||
					run.DirectedBy != tc.wantActor || run.director() != tc.wantActor || run.actor() != tc.wantActor {
					t.Fatalf("pipeline_run did not retain initiating actor %q and caller lineage: %+v", tc.wantActor, run)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantActor != "" {
				code, retry := env.callJSON(t, "POST", "/api/hosts/project-operations", body, auth)
				if code != 200 || retry["id"] != reply["id"] {
					t.Fatalf("pipeline_run retry = %d %v, want run %v", code, retry, reply["id"])
				}
			}
		})
	}
}
