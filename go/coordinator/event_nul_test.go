package main

import (
	"encoding/json"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: a host event batch is stored whatever text its tool output carries.
// Risk: 2026-11-24 an event whose data held U+0000 failed EventsInsert (22P05) with 500, and its host retried the batch
// every few minutes; an escaped backslash followed by "u0000" is ordinary text and must stay as sent.
func TestHostEventWithNulIsStored(t *testing.T) {
	if got := string(jsonNulFree([]byte(`{"a":"x\u0000y","b":"\\u0000","c":"\\\u0000"}`))); got != `{"a":"x\ufffdy","b":"\\u0000","c":"\\\ufffd"}` {
		t.Fatalf("jsonNulFree = %s", got)
	}
	e := openTestCoordinator(t, false)
	sec := e.registerHost(t, "h-nul")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO session(id,host_id,payload) VALUES ('s-nul','h-nul','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}');
INSERT INTO execution(id,kind,state,observation,session_id,host_id,harness,model,started_at,data) VALUES
 ('e-nul','agent_run','running','confirmed','s-nul','h-nul','claude','sonnet',now(),'{"id":"e-nul","sessionId":"s-nul","hostId":"h-nul"}');
`)
	data, _ := json.Marshal(map[string]any{"type": "item.completed", "text": "<Skeleton />\x00 and a literal \\u0000"})
	code, body := e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{HostId: "h-nul", ExecutionId: "e-nul",
		Events: []contract.HostEvent{{Id: "nul-1", Seq: 1, Timestamp: nowMs(), Type: "item.completed", Data: data}}}, hostHdr)
	if code != 200 {
		t.Fatalf("events with U+0000 = %d %v", code, body)
	}
	out := runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `SELECT payload->'data'->>'text' FROM event WHERE execution_id='e-nul' AND type='item.completed'`)
	if want := "<Skeleton />\uFFFD and a literal \\u0000"; strings.TrimSpace(out) != want {
		t.Fatalf("stored text = %q, want %q", out, want)
	}
}
