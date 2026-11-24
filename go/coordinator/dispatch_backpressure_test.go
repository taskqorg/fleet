package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"fleet/contract"
	"fleet/obs"
)

func dispatchTestStatus(t *testing.T, env *testCoordEnv, execID string) map[string]any {
	t.Helper()
	code, body := env.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: execID}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("execution_status(%s) = %d %v", execID, code, body)
	}
	return body
}

func dispatchTestExecution(t *testing.T, env *testCoordEnv, sessionID, execID string) map[string]any {
	t.Helper()
	code, body := env.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": sessionID}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("session_get(%s) = %d %v", sessionID, code, body)
	}
	for _, raw := range anys(body["executions"]) {
		ex := mapOf(raw)
		if ex["id"] == execID {
			return ex
		}
	}
	t.Fatalf("execution %s not found in session %s: %v", execID, sessionID, body)
	return nil
}

func dispatchTestRouteDecisions(t *testing.T, env *testCoordEnv, execID string) []map[string]any {
	t.Helper()
	code, body := env.callJSON(t, "POST", "/api/ops/route_decisions", contract.RouteDecisionsRequest{ExecutionId: execID}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("route_decisions(%s) = %d %v", execID, code, body)
	}
	var out []map[string]any
	for _, raw := range anys(body["decisions"]) {
		out = append(out, mapOf(raw))
	}
	return out
}

func dispatchTestPoll(t *testing.T, env *testCoordEnv, hostID, secret string, load *contract.HostLoad) []map[string]any {
	t.Helper()
	req := contract.PollRequest{
		HostId:           hostID,
		Platform:         contract.Platform("darwin"),
		Capabilities:     []string{"codex"},
		Stores:           []contract.StoreReport{},
		Problems:         []contract.EdgeProblem{},
		ProjectRoots:     []string{},
		GitConnections:   []string{},
		ActiveExecutions: []string{},
		Load:             load,
	}
	code, body := env.callJSON(t, "POST", "/api/hosts/poll", req, map[string]string{"Authorization": "Bearer " + secret})
	if code != 200 {
		t.Fatalf("poll(%s) = %d %v", hostID, code, body)
	}
	var out []map[string]any
	for _, raw := range anys(body["commands"]) {
		out = append(out, mapOf(raw))
	}
	return out
}

func dispatchTestCreateQueued(t *testing.T, env *testCoordEnv, reqID string, hostID *string) (sessionID, execID string) {
	t.Helper()
	if hostID != nil {
		// Pin the execution to a specific host via session_start when testing single-host recovery.
		c, started := env.callJSON(t, "POST", "/api/ops/session_start", doc{
			"profileId": "pm",
			"requestId": reqID,
			"hostId":    *hostID,
			"text":      "backpressure test",
		}, env.ownerHeaders())
		if c != 200 {
			t.Fatalf("session_start = %d %v", c, started)
		}
		return astr(started["sessionId"]), astr(started["executionId"])
	}
	c, sess := eCreateSession(t, env)
	if c != 200 {
		t.Fatalf("session_create = %d %v", c, sess)
	}
	sid := astr(sess["sessionId"])
	c, msg := env.callJSON(t, "POST", "/api/ops/message_send", doc{"sessionId": sid, "requestId": reqID, "text": "backpressure test"}, env.ownerHeaders())
	if c != 200 {
		t.Fatalf("message_send = %d %v", c, msg)
	}
	return sid, astr(msg["id"])
}

// Requirement: docs/work-allocation.md & rules.go host-saturation: overloaded hosts (load1 > cpus or memFree < 5% memTotal)
// stop accepting new launches with waitReason="placement", spill work to healthy hosts, and resume once load recovers.
// Risk: an overloaded host (e.g. load average 33.3 on 8 cores) continues receiving launches until unresponsive, or stranded
// queued executions fail to launch after load drops.
func TestDispatchHostLoadBackpressureAndRecovery(t *testing.T) {
	env := openTestCoordinator(t, true)
	mac := "bp-mac"
	macSecret := env.registerHost(t, mac)

	// 1. Report severe CPU overload (load1 = 33.3 on 8 cores) on bp-mac.
	dispatchTestPoll(t, env, mac, macSecret, &contract.HostLoad{
		Load1: ptr(33.3), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9),
	})

	sid1, eid1 := dispatchTestCreateQueued(t, env, "req-bp-1", nil)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())

	cmds := dispatchTestPoll(t, env, mac, macSecret, &contract.HostLoad{
		Load1: ptr(33.3), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9),
	})
	if len(cmds) != 0 {
		t.Fatalf("overloaded host received commands: %v", cmds)
	}
	ex1 := dispatchTestExecution(t, env, sid1, eid1)
	if ex1["state"] != "preparing" || ex1["hostId"] != nil || ex1["waitReason"] != "placement" {
		t.Fatalf("expected preparing/unplaced/placement wait, got state=%v hostId=%v waitReason=%v", ex1["state"], ex1["hostId"], ex1["waitReason"])
	}
	detail, _ := ex1["waitDetail"].(string)
	if detail == "" || !(len(detail) > 0 && (ex1["waitDetail"] == "bp-mac is overloaded (load 33.30 on 8 cores)")) {
		t.Fatalf("unexpected waitDetail = %q", detail)
	}
	decisions := dispatchTestRouteDecisions(t, env, eid1)
	if len(decisions) == 0 || decisions[len(decisions)-1]["winner"] != nil {
		t.Fatalf("expected route_decision without winner while overloaded, got %v", decisions)
	}
	// Transient load backpressure should not open a Needs-You stall question.
	code, qBody := env.callJSON(t, "POST", "/api/ops/questions_list", doc{"state": "open"}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("questions_list = %d %v", code, qBody)
	}
	if len(anys(qBody["questions"])) != 0 {
		t.Fatalf("expected 0 open stall questions for transient backpressure, got %v", qBody["questions"])
	}

	// 2. Register a second healthy host: dispatch skips overloaded bp-mac and places queued work onto bp-pds.
	pds := "bp-pds"
	pdsSecret := env.registerHost(t, pds)
	dispatchTestPoll(t, env, pds, pdsSecret, &contract.HostLoad{
		Load1: ptr(2.0), Cpus: ptr(int64(24)), MemFree: ptr(20e9), MemTotal: ptr(32e9),
	})
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	pdsCmds := dispatchTestPoll(t, env, pds, pdsSecret, &contract.HostLoad{
		Load1: ptr(2.0), Cpus: ptr(int64(24)), MemFree: ptr(20e9), MemTotal: ptr(32e9),
	})
	if len(pdsCmds) != 1 || pdsCmds[0]["executionId"] != eid1 {
		t.Fatalf("expected healthy host %s to claim %s, got %v", pds, eid1, pdsCmds)
	}
	// Complete eid1 cleanly.
	code, rBody := env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: pds, CommandId: astr(pdsCmds[0]["id"]), ExecutionId: eid1, Status: contract.ReceiptStatusCompleted,
	}, map[string]string{"Authorization": "Bearer " + pdsSecret})
	if code != 200 {
		t.Fatalf("receipt completed = %d %v", code, rBody)
	}

	// 3. Pin a new execution to bp-mac while it is overloaded -> waits; then recover load -> launches immediately.
	sid2, eid2 := dispatchTestCreateQueued(t, env, "req-bp-2", &mac)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	if ex2 := dispatchTestExecution(t, env, sid2, eid2); ex2["hostId"] != nil || ex2["waitReason"] != "placement" {
		t.Fatalf("pinned overloaded execution should wait, got %v", ex2)
	}
	recoveredCmds := dispatchTestPoll(t, env, mac, macSecret, &contract.HostLoad{
		Load1: ptr(4.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9),
	})
	if len(recoveredCmds) != 1 || recoveredCmds[0]["executionId"] != eid2 {
		t.Fatalf("expected %s to launch %s after load recovery, got %v", mac, eid2, recoveredCmds)
	}
	code, _ = env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: mac, CommandId: astr(recoveredCmds[0]["id"]), ExecutionId: eid2, Status: contract.ReceiptStatusCompleted,
	}, map[string]string{"Authorization": "Bearer " + macSecret})
	if code != 200 {
		t.Fatalf("receipt eid2 = %d", code)
	}

	// 4. Memory pressure (< 5% free) via host.heartbeat span also blocks launches until memory recovers.
	hbSpan, _ := json.Marshal(obs.Span{
		TraceID:   "0123456789abcdef0123456789abcdef",
		SpanID:    "0123456789abcdef",
		Service:   "executor",
		HostID:    mac,
		Name:      "host.heartbeat",
		Kind:      obs.KindCheck,
		Start:     time.Now().UTC().Format(obs.TimeFormat),
		Outcome:   obs.OK,
		Load1:     ptr(1.0),
		MemFree:   ptr(100.0),
		Attrs:     map[string]string{"fleet.cpus": "8", "fleet.mem_total": "10000"},
	})
	code, spanBody := env.callJSON(t, "POST", "/api/hosts/spans", contract.SpanBatchRequest{
		BatchId: "bp-mem-span-1",
		Spans:   []json.RawMessage{hbSpan},
	}, map[string]string{"Authorization": "Bearer " + macSecret})
	if code != 200 {
		t.Fatalf("POST /api/hosts/spans = %d %v", code, spanBody)
	}
	sid3, eid3 := dispatchTestCreateQueued(t, env, "req-bp-3", &mac)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	ex3 := dispatchTestExecution(t, env, sid3, eid3)
	if ex3["hostId"] != nil || ex3["waitReason"] != "placement" {
		t.Fatalf("expected memory-pressured host to wait, got %v", ex3)
	}
	_ = dispatchTestStatus(t, env, eid3)
	// Recover via poll load.
	memRecovered := dispatchTestPoll(t, env, mac, macSecret, &contract.HostLoad{
		Load1: ptr(1.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9),
	})
	if len(memRecovered) != 1 || memRecovered[0]["executionId"] != eid3 {
		t.Fatalf("expected %s to launch %s after memory recovery, got %v", mac, eid3, memRecovered)
	}
}

// Requirement: docs/work-allocation.md & HostRecord.maxConcurrent: hosts enforce configured maxConcurrent (surfaced as
// hosts_list.slots) and observed CPU core count limits across a dispatch burst, releasing slots as executions end.
// Risk: burst queueing launches unbounded concurrent WIP onto a host before 1-minute load average reacts, or hosts_update
// ignores/cannot clear maxConcurrent.
func TestDispatchHostConcurrencyLimit(t *testing.T) {
	env := openTestCoordinator(t, true)
	h := "conc-host"
	code, reg := env.callJSON(t, "POST", "/api/ops/hosts_register", doc{"id": h, "maxConcurrent": 2}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("hosts_register = %d %v", code, reg)
	}
	secret := astr(reg["secret"])
	if mapOf(reg["host"])["maxConcurrent"] != float64(2) {
		t.Fatalf("hosts_register maxConcurrent = %v", reg["host"])
	}

	// Verify hosts_list exposes slots = 2.
	code, listBody := env.callJSON(t, "POST", "/api/ops/hosts_list", doc{}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("hosts_list = %d %v", code, listBody)
	}
	var slots float64
	for _, raw := range anys(listBody["hosts"]) {
		row := mapOf(raw)
		if row["id"] == h {
			slots, _ = row["slots"].(float64)
		}
	}
	if slots != 2 {
		t.Fatalf("hosts_list slots = %v, want 2", slots)
	}

	dispatchTestPoll(t, env, h, secret, &contract.HostLoad{
		Load1: ptr(0.5), Cpus: ptr(int64(8)), MemFree: ptr(16e9), MemTotal: ptr(24e9),
	})

	// Enqueue 3 executions in a burst: only 2 may be claimed while 1 waits at concurrency limit.
	sids := make([]string, 3)
	eids := make([]string, 3)
	for i := range 3 {
		sids[i], eids[i] = dispatchTestCreateQueued(t, env, fmt.Sprintf("req-conc-%d", i), &h)
	}
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())

	cmds := dispatchTestPoll(t, env, h, secret, &contract.HostLoad{
		Load1: ptr(0.5), Cpus: ptr(int64(8)), MemFree: ptr(16e9), MemTotal: ptr(24e9),
	})
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands claimed under maxConcurrent=2, got %d (%v)", len(cmds), cmds)
	}
	for _, cmd := range cmds {
		code, rBody := env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
			HostId: h, CommandId: astr(cmd["id"]), ExecutionId: astr(cmd["executionId"]), Status: contract.ReceiptStatusStarted,
		}, map[string]string{"Authorization": "Bearer " + secret})
		if code != 200 {
			t.Fatalf("receipt started = %d %v", code, rBody)
		}
	}
	ex3 := dispatchTestExecution(t, env, sids[2], eids[2])
	if ex3["hostId"] != nil || ex3["waitReason"] != "placement" || ex3["waitDetail"] != "conc-host is at concurrency limit (2/2 active)" {
		t.Fatalf("expected 3rd execution waiting at concurrency limit, got %v", ex3)
	}

	// Finish command 0 -> slot frees up and 3rd execution claims immediately.
	code, rBody := env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: h, CommandId: astr(cmds[0]["id"]), ExecutionId: astr(cmds[0]["executionId"]), Status: contract.ReceiptStatusCompleted,
	}, map[string]string{"Authorization": "Bearer " + secret})
	if code != 200 {
		t.Fatalf("receipt completed = %d %v", code, rBody)
	}
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	nextCmds := dispatchTestPoll(t, env, h, secret, &contract.HostLoad{
		Load1: ptr(0.5), Cpus: ptr(int64(8)), MemFree: ptr(16e9), MemTotal: ptr(24e9),
	})
	if len(nextCmds) != 1 || nextCmds[0]["executionId"] != eids[2] {
		t.Fatalf("expected 3rd execution %s after slot freed, got %v", eids[2], nextCmds)
	}
	code, _ = env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: h, CommandId: astr(nextCmds[0]["id"]), ExecutionId: astr(nextCmds[0]["executionId"]), Status: contract.ReceiptStatusStarted,
	}, map[string]string{"Authorization": "Bearer " + secret})
	if code != 200 {
		t.Fatalf("receipt started 3rd = %d", code)
	}

	// Clear maxConcurrent via hosts_update(maxConcurrent: null) -> slots becomes null, and observed cpus=2 caps at 2.
	code, upd := env.callJSON(t, "POST", "/api/ops/hosts_update", doc{"id": h, "maxConcurrent": nil}, env.ownerHeaders())
	if code != 200 || mapOf(upd["host"])["maxConcurrent"] != nil {
		t.Fatalf("hosts_update clear maxConcurrent = %d %v", code, upd)
	}
	dispatchTestPoll(t, env, h, secret, &contract.HostLoad{
		Load1: ptr(0.5), Cpus: ptr(int64(2)), MemFree: ptr(16e9), MemTotal: ptr(24e9),
	})
	s4, e4 := dispatchTestCreateQueued(t, env, "req-conc-3", &h)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	ex4 := dispatchTestExecution(t, env, s4, e4)
	if ex4["hostId"] != nil || ex4["waitReason"] != "placement" || ex4["waitDetail"] != "conc-host is at concurrency limit (2/2 active)" {
		t.Fatalf("expected cpus=2 default cap to hold 3rd active execution, got %v", ex4)
	}
}
