package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"fleet/contract"
	"fleet/obs"
)

// Fleet R3/R5: a delayed project query must remain recoverable, never look like a successful null or cross an execution boundary.
func TestProjectToolPendingRecovery(t *testing.T) {
	for _, name := range []string{"project_context", "account_list", "pipeline_run"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := journal(t)
			const traceparent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
			cmd := contract.StartCommand{Id: "command", ExecutionId: "execution", Kind: "start", Traceparent: optStr(traceparent)}
			if _, err := db.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cmd.Id, enc(cmd)); err != nil {
				t.Fatal(err)
			}
			spool := filepath.Join(journalDir(db), "spans")
			if err := os.Mkdir(spool, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx := M{"hostId": "host", "commandId": "command", "executionId": "execution", "projectId": "fixture"}
			call := func(name string, args M, context M) any {
				t.Helper()
				r, err := delegationCall(name, args, context, db)
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
			call("agent_tool_result", M{"requestKey": key}, ctx)
			files, err := filepath.Glob(filepath.Join(spool, "pending-*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("expected one original pending span after repeated lookups, got %v (%v)", files, err)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var span obs.Span
			if err := json.Unmarshal(raw, &span); err != nil || !obs.Valid(&span) || span.TraceID != strings.Split(traceparent, "-")[1] ||
				span.ExecutionID == nil || *span.ExecutionID != "execution" || span.Name != "fleet.tool.wait" ||
				span.Attrs["fleet.wait"] != "pending" || span.Attrs["fleet.operation"] != name || span.Attrs["fleet.request"] != wakeName(key) ||
				strings.Contains(string(raw), key) || strings.Contains(string(raw), "diagnostic") {
				t.Fatalf("bad pending evidence %s (%v)", raw, err)
			}
			// The retained response arrives after the native call stopped waiting. A result-named field is project data, not a JSON-RPC envelope.
			response := M{"revision": 7, "result": "project data", "accounts": []any{}}
			var effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				effects.Add(1)
				trace, _, ok := obs.ParseTraceparent(r.Header.Get("traceparent"))
				if !ok || trace != span.TraceID || r.URL.Path != contract.RouteProjectOperations.Path {
					t.Errorf("project route did not carry the execution trace: %s, %s", r.URL.Path, trace)
				}
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
