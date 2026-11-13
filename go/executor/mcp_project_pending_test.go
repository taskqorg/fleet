package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Fleet R3/R5: a delayed project query must remain recoverable, never look like a successful null or cross an execution boundary.
func TestProjectToolPendingRecovery(t *testing.T) {
	for _, name := range []string{"project_context", "account_list", "pipeline_run"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := journal(t)
			ctx := M{"hostId": "host", "commandId": "command", "executionId": "execution", "projectId": "fixture"}
			call := func(name string, args M, context M) any {
				t.Helper()
				r, err := delegation(M{"method": "tools/call", "params": M{"name": name, "arguments": args}}, context, db)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			args := M{}
			if name == "pipeline_run" {
				args = M{"requestId": "mutation", "query": "diagnostic"}
			}
			pending := call(name, args, ctx)
			body, err := dec(jStr(jArr(pending, "content")[0], "text"))
			if err != nil || jStr(body, "status") != "pending" || jAt(pending, "isError") != true {
				t.Fatal("timeout must be pending, not null", pending, err)
			}
			key := jStr(body, "requestKey")
			if !strings.HasPrefix(key, "command:") {
				t.Fatal(body)
			}
			again := call("agent_tool_result", M{"requestKey": key}, ctx)
			if !strings.Contains(enc(again), "pending") {
				t.Fatal(again)
			}
			// The retained response arrives after the native call stopped waiting. A result-named field is project data, not a JSON-RPC envelope.
			response := M{"revision": 7, "result": "project data", "accounts": []any{}}
			var effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				effects.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(enc(response)))
			}))
			defer server.Close()
			if err := flush(db, server.URL, "token", "host"); err != nil {
				t.Fatal(err)
			}

			recovered := call("agent_tool_result", M{"requestKey": key}, ctx)
			if jAt(recovered, "isError") != false || jStr(jArr(recovered, "content")[0], "text") != enc(response) {
				t.Fatal(recovered)
			}
			if name == "pipeline_run" {
				retried := call(name, args, ctx)
				if jStr(jArr(retried, "content")[0], "text") != enc(response) {
					t.Fatal(retried)
				}
			}
			call("agent_tool_result", M{"requestKey": key}, ctx)
			if err := flush(db, server.URL, "token", "host"); err != nil || effects.Load() != 1 {
				t.Fatal("recovery/retry duplicated effect", effects.Load(), err)
			}
			if _, err := db.Exec("UPDATE delegation_requests SET response=? WHERE id=?", enc(M{"error": "Project grant rejected"}), key); err != nil {
				t.Fatal(err)
			}
			refused := call("agent_tool_result", M{"requestKey": key}, ctx)
			if jAt(refused, "isError") != true || !strings.Contains(enc(refused), "Project grant rejected") {
				t.Fatal("late refusal hidden", refused)
			}
			for _, foreign := range []M{
				{"commandId": "other", "executionId": "execution"},
				{"commandId": "command", "executionId": "other"},
			} {
				denied := call("agent_tool_result", M{"requestKey": key}, foreign)
				if jAt(denied, "isError") != true || !strings.Contains(enc(denied), "belongs to another") {
					t.Fatal("foreign result escaped", denied)
				}
			}
			// A forged key prefix cannot override the identities in the stored request.
			if _, err := db.Exec("UPDATE delegation_requests SET body=? WHERE id=?", enc(M{"commandId": "other", "executionId": "execution"}), key); err != nil {
				t.Fatal(err)
			}
			denied := call("agent_tool_result", M{"requestKey": key}, ctx)
			if !strings.Contains(enc(denied), "another command") {
				t.Fatal(denied)
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM delegation_requests").Scan(&n); err != nil || n != 1 {
				t.Fatal("recovery repeated operation", n, err)
			}
		})
	}
}

// Fleet child submission is unchanged: timeout is submission uncertainty, not completion; a retry reuses its requestId.
func TestChildSubmissionTimeoutUnchanged(t *testing.T) {
	db := journal(t)
	ctx := M{"hostId": "host", "commandId": "command", "executionId": "execution", "projectId": "fixture", "profiles": []any{"reviewer"}}
	req := M{"method": "tools/call", "params": M{"name": "request_child", "arguments": M{"requestId": "child", "profileId": "reviewer", "input": "review"}}}
	pending, err := delegation(req, ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	body := decM(jStr(jArr(pending, "content")[0], "text"))
	if jStr(body, "status") != "pending-or-rejected" || jAt(body, "response") != nil || !strings.Contains(jStr(body, "instruction"), "Finish this turn") || jAt(pending, "isError") != false {
		t.Fatal(pending)
	}
	var effects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effects.Add(1)
		w.Write([]byte(`{"childExecutionId":"child-execution"}`))
	}))
	defer server.Close()
	if err := flush(db, server.URL, "token", "host"); err != nil {
		t.Fatal(err)
	}
	accepted, err := delegation(req, ctx, db)
	if err != nil || jStr(decM(jStr(jArr(accepted, "content")[0], "text")), "status") != "accepted" {
		t.Fatal(accepted, err)
	}
	if err := flush(db, server.URL, "token", "host"); err != nil || effects.Load() != 1 {
		t.Fatal("duplicate child submission", effects.Load(), err)
	}
}
