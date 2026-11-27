package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: host_diagnose asks the host through its poll and answers the report the next poll carries, with the
// last poll and the queued runs; risk: diagnosing a host needed the owner's Mac and a blind restart (2026-11-26).
func TestHostDiagnoseThroughPoll(t *testing.T) {
	e := openTestCoordinator(t, false)
	secret := e.registerHost(t, "h-diag")
	host := map[string]string{"Authorization": "Bearer " + secret}
	poll := func(extra doc) map[string]any {
		body := doc{"hostId": "h-diag", "platform": "linux", "capabilities": []string{}, "stores": []any{}, "problems": []any{}, "projectRoots": []string{},
			"gitConnections": []string{}, "activeExecutions": []string{}}
		for k, v := range extra {
			body[k] = v
		}
		code, reply := e.callJSON(t, "POST", "/api/hosts/poll", body, host)
		if code != 200 {
			t.Fatalf("poll %d %v", code, reply)
		}
		return reply
	}
	poll(nil)
	var out map[string]any
	e.opInto(t, "host_diagnose", doc{"host": "h-diag", "wait": 1}, e.ownerHeaders(), &out)
	if out["fresh"] != false || out["online"] != true {
		t.Fatalf("before a report: %v", out)
	}
	if r := poll(nil); r["diagnose"] != true {
		t.Fatalf("poll did not ask for a report: %v", r)
	}
	const session = "00000000-0000-4000-8000-000000000001" // invented session ID
	code, inventory := e.callJSON(t, "POST", "/api/hosts/inventory", contract.HostInventoryRequest{
		Sessions: []contract.HostInventorySession{{Id: session, Bytes: 1, ChangedAt: 1}},
	}, host)
	at, ok := inventory["inventoryAt"].(float64)
	if code != 200 || !ok || at <= 0 {
		t.Fatalf("inventory response: %d %v", code, inventory)
	}
	var savedAt int64
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT (payload #>> '{inventory,at}')::bigint FROM host_observation WHERE id='h-diag'`).Scan(&savedAt); err != nil || at != float64(savedAt) {
		t.Fatalf("inventory response timestamp %v and saved timestamp %v: %v", at, savedAt, err)
	}
	report := doc{"at": 1, "logTail": []string{"2026-11-26T07:39:00.000Z executor started"}, "outbox": doc{"rows": 0, "routes": []any{}},
		"outboxFailed": []any{doc{"route": "/api/hosts/artifacts/failed", "status": 404, "reason": "Unknown artifact", "at": 1}}, "transfersFailed": []any{}, "commands": []any{}}
	report["inventory"] = doc{"startedAt": 1, "inventoryAt": inventory["inventoryAt"], "response": "received",
		"candidates": []doc{{"sessionId": session, "root": "effective", "layout": "direct", "outcome": "uncommitted"}}}
	if r := poll(doc{"diagnosis": report}); r["diagnose"] == true {
		t.Fatalf("still asking after the report: %v", r)
	}
	e.opInto(t, "host_diagnose", doc{"host": "h-diag", "wait": 0}, e.ownerHeaders(), &out)
	rep, _ := out["report"].(map[string]any)
	if rep == nil || len(rep["logTail"].([]any)) != 1 || len(rep["outboxFailed"].([]any)) != 1 || out["reportedAt"] == nil {
		t.Fatalf("report %v", out)
	}
	var cached map[string]any
	e.opInto(t, "host_diagnosis", doc{"host": "h-diag"}, e.ownerHeaders(), &cached)
	if cached["report"] == nil || cached["fresh"] != false {
		t.Fatalf("host_diagnosis %v", cached)
	}
	diagnosis := cached["report"].(map[string]any)["inventory"].(map[string]any)
	candidate := diagnosis["candidates"].([]any)[0].(map[string]any)
	if diagnosis["inventoryAt"] != inventory["inventoryAt"] || candidate["sessionId"] != session || candidate["outcome"] != "uncommitted" {
		t.Fatalf("uncorrelated cleanup diagnosis: %v", diagnosis)
	}
	var changes int
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM change WHERE kind='host' AND id='h-diag' AND host_id='h-diag'`).Scan(&changes); err != nil || changes == 0 {
		t.Fatalf("a stored report emits no host change (%d, %v)", changes, err)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/host_diagnose", doc{"host": "nope", "wait": 0}, e.ownerHeaders()); code != 404 {
		t.Fatalf("unknown host %d", code)
	}
}
