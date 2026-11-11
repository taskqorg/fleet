package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"fleet/contract"
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
		r := connectionCall("web", tool, M{})
		if string(r.Outcome) != want[0] || code(r) != want[1] || r.CallId != "c1" {
			t.Fatal(tool, r)
		}
	}
	gw.Close()
	if r := connectionCall("web", "ok", M{}); r.Outcome != contract.CallOutcomeError || code(r) != "unreachable" {
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
		if _, err := delegation(M{"method": "tools/call", "params": bad}, ctx, nil); err == nil || !strings.Contains(err.Error(), "Invalid project request") {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

// R1; risk: an opaque rejection prevents correction of invalid model arguments.
// A coordinator validation refusal must survive the actual outbox instead of becoming an opaque grant error.
func TestAgentRejectionKeepsValidation(t *testing.T) {
	db := journal(t)
	defer db.Close()
	key := "command:agent:validation"
	body := M{"agentToolKey": key, "commandId": "command"}
	if err := journalOnce(db, key, enc(body), "/api/hosts/agent-tools"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"Expected id","rawPayload":"must not be retained"}`))
	}))
	defer server.Close()
	if err := flush(db, server.URL, "token", "host"); err != nil {
		t.Fatal(err)
	}
	var response string
	if err := db.QueryRow("SELECT response FROM delegation_requests WHERE id=?", key).Scan(&response); err != nil {
		t.Fatal(err)
	}
	r := decM(response)
	if jStr(r, "message") != "Expected id" || jAt(r, "statusCode") == nil || strings.Contains(response, "rawPayload") {
		t.Fatal(response)
	}
}
