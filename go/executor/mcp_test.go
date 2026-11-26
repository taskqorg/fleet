package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fleet/contract"
	"fleet/obs"
)

// Risk: a provider charge is reported as a plain error (or the reverse), so an agent repeats a paid call
// (fleet-connections.md §5). Fake gateway; one case per outcome rule.
func TestConnectionCallOutcomes(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("x-fleet-call-id", "c1")
		switch r.URL.Path {
		case "/v1/call/web/ok":
			w.Write([]byte(`{"usage":{"credits":1},"r":1}`))
		case "/v1/call/web/garbled":
			w.Write([]byte(`not json`))
		case "/v1/call/web/provider":
			w.Header().Set("x-fleet-error", "provider-502")
			w.WriteHeader(502)
			w.Write([]byte(`{}`))
		case "/v1/call/web/timeout":
			w.Header().Set("x-fleet-error", "provider-timeout")
			w.WriteHeader(504)
		}
	}))
	defer gw.Close()
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	t.Setenv("FLEET_RUN_TOKEN", "tok")
	code := func(r contract.CallLine) string {
		if r.ErrorCode == nil {
			return ""
		}
		return *r.ErrorCode
	}
	for tool, want := range map[string][2]string{"ok": {"ok", ""}, "garbled": {"unknown", "invalid-response"}, "provider": {"error", "http-502"}, "timeout": {"unknown", "provider-timeout"}} {
		r := connectionCall("web", M{"name": tool}, M{})
		if string(r.Outcome) != want[0] || code(r) != want[1] || r.CallId != "c1" {
			t.Fatal(tool, r)
		}
	}
	gw.Close()
	if r := connectionCall("web", M{"name": "ok"}, M{}); r.Outcome != contract.CallOutcomeError || code(r) != "unreachable" {
		t.Fatal("refused connection", r)
	}
}

// Risk: an operation the coordinator offers a profile (fleetOperations) never reaches the model, or a name outside the
// list is forwarded as an agent call.
func TestFleetOperationsListed(t *testing.T) {
	ctx := M{"operations": []any{M{"name": "task_create", "description": "d"}, M{"name": "ask", "description": "dup"}}}
	names := []string{}
	for _, x := range mcpTools(ctx)["tools"].([]any) {
		names = append(names, jStr(jAt(x, "name")))
	}
	n := 0
	for _, x := range names {
		if x == "task_create" {
			n++
		}
		if x == "ask" {
			n += 10
		}
	}
	if n != 11 {
		t.Fatalf("task_create once and ask once expected, got %v", names)
	}
	if len(operationsOf(M{})) != 0 {
		t.Fatal("no operations without the list")
	}
}

// Requirement: each project tool tells the model its own arguments (review C5, C6): the project tools in mcp-tools.json
// are exactly the contract's ProjectOperation list, each with a closed schema, and a call outside its schema is refused
// here, before it reaches the coordinator. Risk: the model gets one generic schema and learns a missing or wrong argument
// only from a runtime error after a coordinator round trip.
func TestProjectToolSchemas(t *testing.T) {
	ctx := M{"projectId": "p1", "hostId": "h", "commandId": "c", "executionId": "e"}
	listed := []string{}
	for _, x := range mcpTools(ctx)["tools"].([]any) {
		if slices.Contains(projectTools, jStr(x, "name")) {
			listed = append(listed, jStr(x, "name"))
			if jAt(x, "inputSchema", "additionalProperties") != false {
				t.Errorf("%s: open schema", jStr(x, "name"))
			}
		}
	}
	want := []string{}
	for _, o := range contract.ProjectOperationValues {
		want = append(want, string(o))
	}
	slices.Sort(listed)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Fatalf("project tools %v, contract %v", listed, want)
	}
	for _, bad := range []M{
		{"name": "pipeline_get", "arguments": M{"text": "x", "runId": "r"}}, // not an argument of pipeline_get
		{"name": "pipeline_get", "arguments": M{}},                          // runId missing
		{"name": "question_ask", "arguments": M{"requestId": "r"}},          // text missing
	} {
		if _, err := delegationCall(jStr(bad, "name"), bad["arguments"], ctx, nil); err == nil || !strings.Contains(err.Error(), "Invalid project request") {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

// R1; risk: an opaque rejection prevents correction of invalid model arguments.
// A coordinator validation refusal must survive the actual outbox instead of becoming an opaque grant error.
func TestAgentRejectionKeepsValidation(t *testing.T) {
	resetRetryRows(t)
	db := journal(t)
	defer db.Close()
	const traceparent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	cmd := contract.StartCommand{Id: "command", ExecutionId: "execution", Kind: "start", Traceparent: optStr(traceparent)}
	if _, err := db.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cmd.Id, enc(cmd)); err != nil {
		t.Fatal(err)
	}
	// An earlier row in the caller's execution must still leave before its tool request.
	if err := queue(db, contract.RouteEvents.Path, M{"executionId": "execution", "events": []any{M{"id": "earlier"}}}); err != nil {
		t.Fatal(err)
	}
	var spans []*obs.Span
	obs.SetSink(func(s *obs.Span) { spans = append(spans, s) })
	defer obs.SetSink(nil)
	var attempts atomic.Int32
	attemptKeys := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == contract.RouteEvents.Path {
			io.WriteString(w, "{}")
			return
		}
		b, _ := io.ReadAll(r.Body)
		attemptKeys <- jStr(decM(string(b)), "agentToolKey")
		trace, parent, ok := obs.ParseTraceparent(r.Header.Get("traceparent"))
		if !ok || trace != strings.Split(traceparent, "-")[1] || parent == strings.Split(traceparent, "-")[2] {
			t.Errorf("HTTP did not carry a child of the execution trace: %q", r.Header.Get("traceparent"))
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "{}")
			return
		}
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"Expected id","rawPayload":"must not be retained"}`))
	}))
	defer server.Close()
	type result struct {
		value any
		err   error
	}
	caller := make(chan result, 1)
	go func() {
		v, err := delegationCall("message_send", M{"text": "invented private argument"}, M{"hostId": "host", "commandId": cmd.Id, "executionId": cmd.ExecutionId}, db)
		caller <- result{v, err}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticks := time.NewTicker(5 * time.Millisecond)
	defer ticks.Stop()
	var row int64
	for row == 0 {
		select {
		case <-deadline.C:
			t.Fatal("agent tool did not enter the outbox")
		case <-ticks.C:
			db.QueryRow("SELECT rowid FROM outbox WHERE route=?", contract.RouteAgentTools.Path).Scan(&row)
		}
	}
	if err := flush(db, server.URL, "token", "host"); err == nil || attempts.Load() != 1 {
		t.Fatalf("expected retryable response from first attempt: %v", err)
	}
	key := <-attemptKeys
	var receipt string
	if err := db.QueryRow("SELECT coalesce(response,'') FROM delegation_requests WHERE id=?", key).Scan(&receipt); err != nil || receipt != "" {
		t.Fatalf("retry settled the request: %q, %v", receipt, err)
	}
	retryDone(row) // exercise the next attempt without waiting out backoff, covered by TestRetryingRowBacksOff
	if err := flush(db, server.URL, "token", "host"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deadline.C:
		t.Fatal("agent did not receive the definitive rejection")
	case r := <-caller:
		if r.err != nil || jAt(r.value, "isError") != true || !strings.Contains(enc(r.value), "Expected id") {
			t.Fatalf("bad direct tool reply: %+v", r)
		}
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected two attempts, got %d", attempts.Load())
	}
	if retryKey := <-attemptKeys; key == "" || key != retryKey {
		t.Fatalf("retry changed the request identity: %q, %q", key, retryKey)
	}
	if len(spans) != 2 || spans[0].Attrs["fleet.transport"] != "retry" || spans[0].Attrs["fleet.local_receipt"] != "" ||
		spans[1].Attrs["fleet.transport"] != "rejected" || spans[1].Attrs["fleet.local_receipt"] != "written" {
		t.Fatalf("expected a retry then rejection with local receipt, got %+v", spans)
	}
	for _, s := range spans {
		if s.Name != "fleet.tool.delivery" || s.ExecutionID == nil || *s.ExecutionID != cmd.ExecutionId ||
			s.Attrs["fleet.operation"] != "message_send" || s.Attrs["fleet.request"] != wakeName(key) ||
			s.Attrs["fleet.queue_ms"] == "" || s.Attrs["fleet.queued_behind_execution_row"] != "true" ||
			strings.Contains(enc(s), "invented private argument") || strings.Contains(enc(s), "Expected id") {
			t.Fatalf("incorrect or unsafe transport evidence: %+v", s)
		}
	}
	if err := db.QueryRow("SELECT response FROM delegation_requests WHERE id=?", key).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	r := decM(receipt)
	if jStr(r, "message") != "Expected id" || jAt(r, "statusCode") == nil || strings.Contains(receipt, "rawPayload") {
		t.Fatal(receipt)
	}
}
