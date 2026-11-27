package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// T-FLT-141: after an unbound host move, dispatch must hand off the retained
// context. The new thread binds to its own run and fences subsequent receipts.
func TestHostMoveNativeReceipt(t *testing.T) {
	e := openTestCoordinator(t, false)
	const host, session = "windows", "moved"
	headers := map[string]string{"Authorization": "Bearer " + e.registerHost(t, host)}
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO session (id,payload) VALUES
	 ('moved','{"nativeSessionId":"prior-thread","nativeSessions":{"codex::::":{"nativeSessionId":"prior-thread","executionId":"prior-run","seen":1}}}');
	 INSERT INTO execution (id,kind,state,observation,session_id,host_id,harness,started_at,data)
	 VALUES ('prior-run','agent_run','succeeded','confirmed','moved','pds18-native','codex',now(),'{"startedAt":1}'),
	        ('abandoned-run','agent_run','failed','unconfirmed','moved','windows','codex',now(),'{"startedAt":2}')`); err != nil {
		t.Fatal(err)
	}
	start := func(text, wantNative string) dispatchStartCommand {
		t.Helper()
		var command dispatchStartCommand
		err := e.app.store.Write(e.ctx, nil, []string{GLOBAL, DISPATCH}, func(q *Request) error {
			s, err := sessionByID(q, session)
			if err != nil {
				return err
			}
			turn, err := agentEnqueue(q, &s, text, newID(), nil, nil, host, nil)
			if err != nil {
				return err
			}
			run, err := executionGetAs[dispatchExecution](q, turn.ID)
			if err != nil {
				return err
			}
			command, err = e.app.startCommand(q, &run, host, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		payload := agentView[contract.StartPayload](command.Payload)
		if got := deref(payload.NativeSessionId); got != wantNative {
			t.Fatalf("start native ID = %q, want %q", got, wantNative)
		}
		if wantNative == "" && !strings.Contains(deref(payload.Text), "Current request:\n"+text) {
			t.Fatal("fresh start omitted the handoff")
		}
		return command
	}
	receipt := func(c dispatchStartCommand, status contract.ReceiptStatus, native string, problem *string, want int) {
		t.Helper()
		r := contract.Receipt{HostId: host, CommandId: c.ID, ExecutionId: c.ExecutionID, Status: status, Artifacts: []string{}, NativeSessionId: strPtr(native), Error: problem}
		if code, body := e.callJSON(t, "POST", "/api/hosts/receipts", r, headers); code != want {
			t.Fatalf("%s %q: HTTP %d %v, want %d", status, native, code, body, want)
		}
	}
	moved := start("continue on Windows", "")
	receipt(moved, contract.ReceiptStatusStarted, "", nil, 200)
	receipt(moved, contract.ReceiptStatusStarted, "windows-thread", nil, 200)
	receipt(moved, contract.ReceiptStatusCompleted, "wrong-thread", nil, 409)
	// A fresh run started before the fix has only the session capture.
	if _, err := e.ownerStore.pool.Exec(e.ctx, `UPDATE execution SET data=data-'nativeSessionId' WHERE id=$1`, moved.ExecutionID); err != nil {
		t.Fatal(err)
	}
	receipt(moved, contract.ReceiptStatusCompleted, "wrong-thread", nil, 409)
	receipt(moved, contract.ReceiptStatusCompleted, "windows-thread", nil, 200)
	var state, native string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state::text,s.payload->>'nativeSessionId'
	 FROM execution e JOIN session s ON s.id=e.session_id WHERE e.id=$1`, moved.ExecutionID).Scan(&state, &native); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || native != "windows-thread" {
		t.Fatalf("got %s / %s, want succeeded / windows-thread", state, native)
	}

	sameHost := start("resume the Windows thread", "windows-thread")
	receipt(sameHost, contract.ReceiptStatusStarted, "wrong-thread", nil, 409)
	reason := "thread/resume rejected by Codex"
	receipt(sameHost, contract.ReceiptStatusFailed, "windows-thread", &reason, 200)
	var observation, problem string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state::text,observation::text,problem
	 FROM execution WHERE id=$1`, sameHost.ExecutionID).Scan(&state, &observation, &problem); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || observation != "confirmed" || problem != reason {
		t.Fatalf("got %s / %s / %s, want confirmed resume failure", state, observation, problem)
	}
}
