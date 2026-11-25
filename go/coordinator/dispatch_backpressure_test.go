package main

import (
	"encoding/json"
	"fmt"
	"strings"
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
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "0123456789abcdef",
		Service: "executor",
		HostID:  mac,
		Name:    "host.heartbeat",
		Kind:    obs.KindCheck,
		Start:   time.Now().UTC().Format(obs.TimeFormat),
		Outcome: obs.OK,
		Load1:   ptr(1.0),
		MemFree: ptr(100.0),
		Attrs:   map[string]string{"fleet.cpus": "8", "fleet.mem_total": "10000"},
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

// Requirement: fleet.md task states (a missing executor keeps work queued with the prerequisite visible) and
// executor_launch.go (an executor waiting to install a newer release admits no start).
// Risk: 2026-11-24 a PM conversation was placed on owner-mac while its executor drained for 1.261124.43; the start was
// never admitted and the conversation stayed preparing while another host was idle.
func TestDispatchSkipsHostDrainingForUpdate(t *testing.T) {
	env := openTestCoordinator(t, true)
	mac, pds := "upd-mac", "upd-pds"
	macSecret, pdsSecret := env.registerHost(t, mac), env.registerHost(t, pds)
	healthy := &contract.HostLoad{Load1: ptr(1.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9)}
	poll := func(host, secret, version string, update *contract.UpdateResult) []map[string]any {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
			HostId: host, Platform: contract.Platform("darwin"), Capabilities: []string{"codex"}, Stores: []contract.StoreReport{},
			Problems: []contract.EdgeProblem{}, ProjectRoots: []string{}, GitConnections: []string{}, ActiveExecutions: []string{},
			Load: healthy, Version: &version, Update: update,
		}, map[string]string{"Authorization": "Bearer " + secret})
		if code != 200 {
			t.Fatalf("poll(%s) = %d %v", host, code, body)
		}
		var out []map[string]any
		for _, raw := range anys(body["commands"]) {
			out = append(out, mapOf(raw))
		}
		return out
	}
	draining := &contract.UpdateResult{At: "2026-11-24T18:42:05Z", State: contract.UpdateStatePending, Version: "1.261124.43", Error: ptr("executor work is draining")}
	poll(mac, macSecret, "1.261124.40", draining)
	poll(pds, pdsSecret, "1.261124.43", &contract.UpdateResult{At: "2026-11-24T18:41:37Z", State: contract.UpdateStateOk, Version: "1.261124.43"})

	// Unpinned work goes to the host that admits it.
	sid1, eid1 := dispatchTestCreateQueued(t, env, "req-upd-1", nil)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	if ex := dispatchTestExecution(t, env, sid1, eid1); ex["hostId"] != pds {
		t.Fatalf("unpinned run placed on %v, want %s", ex["hostId"], pds)
	}

	// Work pinned to the draining host waits with the reason, then starts once the update lands.
	sid2, eid2 := dispatchTestCreateQueued(t, env, "req-upd-2", &mac)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	ex2 := dispatchTestExecution(t, env, sid2, eid2)
	if ex2["hostId"] != nil || ex2["waitReason"] != "placement" || ex2["waitDetail"] != "upd-mac is updating to 1.261124.43 and starts no new work until then" {
		t.Fatalf("pinned run on a draining host = hostId %v waitReason %v waitDetail %v", ex2["hostId"], ex2["waitReason"], ex2["waitDetail"])
	}
	// An update drain is transient, like load backpressure: no Needs you stall question.
	if code, qs := env.callJSON(t, "POST", "/api/ops/questions_list", doc{"state": "open"}, env.ownerHeaders()); code != 200 || len(anys(qs["questions"])) != 0 {
		t.Fatalf("open questions while a host updates = %d %v", code, qs["questions"])
	}
	cmds := poll(mac, macSecret, "1.261124.43", &contract.UpdateResult{At: "2026-11-24T18:51:00Z", State: contract.UpdateStateOk, Version: "1.261124.43"})
	if len(cmds) != 1 || cmds[0]["executionId"] != eid2 {
		t.Fatalf("updated host commands = %v, want start of %s", cmds, eid2)
	}

	// A continuation of a session that ran on the host waits, unplaced, for that host's update: its native session and
	// worktree live there. Past continuationHostWait it moves, with no native id and the earlier turns handed over.
	macHdr := map[string]string{"Authorization": "Bearer " + macSecret}
	native := "01a20ebe-42f4-7f01-ac08-a0dc70233565"
	for _, st := range []contract.ReceiptStatus{contract.ReceiptStatusStarted, contract.ReceiptStatusCompleted} {
		if code, body := env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{HostId: mac, CommandId: astr(cmds[0]["id"]), ExecutionId: eid2, Status: st, NativeSessionId: &native}, macHdr); code != 200 {
			t.Fatalf("receipt %s = %d %v", st, code, body)
		}
	}
	poll(mac, macSecret, "1.261124.43", &contract.UpdateResult{At: "2026-11-24T19:30:00Z", State: contract.UpdateStatePending, Version: "1.261124.53", Error: ptr("executor work is draining")})
	poll(pds, pdsSecret, "1.261124.53", &contract.UpdateResult{At: "2026-11-24T19:30:00Z", State: contract.UpdateStateOk, Version: "1.261124.53"})
	// The session was started pinned to upd-mac; unpinned, only the wait keeps its continuation there.
	runTestPSQL(t, env.binDir, env.sockDir, env.pgPort, env.dbName, "postgres", `UPDATE session SET host_id=NULL WHERE id='`+sid2+`'`)
	code, msg := env.callJSON(t, "POST", "/api/ops/message_send", doc{"sessionId": sid2, "requestId": "req-upd-3", "text": "continue"}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("message_send = %d %v", code, msg)
	}
	eid3 := astr(msg["id"])
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	if ex := dispatchTestExecution(t, env, sid2, eid3); ex["hostId"] != nil || !strings.Contains(astr(ex["waitDetail"]), "upd-mac") {
		t.Fatalf("continuation = hostId %v waitDetail %v, want unplaced waiting for upd-mac", ex["hostId"], ex["waitDetail"])
	}
	if out := runTestPSQL(t, env.binDir, env.sockDir, env.pgPort, env.dbName, "postgres", `SELECT data ? 'holdSince' FROM execution WHERE id='`+eid3+`'`); strings.TrimSpace(out) != "t" {
		t.Fatalf("the hold did not record when it began: %q", out)
	}
	runTestPSQL(t, env.binDir, env.sockDir, env.pgPort, env.dbName, "postgres",
		`UPDATE execution SET data=jsonb_set(data,'{holdSince}',to_jsonb((extract(epoch from now())*1000 - 16*60*1000)::bigint)) WHERE id='`+eid3+`'`)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	moved := poll(pds, pdsSecret, "1.261124.53", &contract.UpdateResult{At: "2026-11-24T19:46:00Z", State: contract.UpdateStateOk, Version: "1.261124.53"})
	var payload map[string]any
	for _, c := range moved {
		if c["executionId"] == eid3 {
			payload = mapOf(c["payload"])
		}
	}
	if payload == nil {
		t.Fatalf("after the wait the continuation did not move to %s: %v", pds, moved)
	}
	if n, _ := payload["nativeSessionId"].(string); n != "" {
		t.Fatalf("moved run resumes native %q from another host", n)
	}
	if s := fmt.Sprint(payload); !strings.Contains(s, "Fleet context handoff (host_move)") || !strings.Contains(s, "local commits are not here") {
		t.Fatalf("moved run has no context handoff: %v", payload)
	}
}

// Requirement: a run on another host than the session's last run starts a fresh native session.
// Risk: the executor refuses to resume a native session that lives on another host, on every run.
func TestNativeForOtherHost(t *testing.T) {
	native, mac := ptr("01a20ebe"), ptr("owner-mac")
	if got := nativeFor(native, mac, "owner-mac"); got != native {
		t.Fatalf("same host: %v", got)
	}
	if got := nativeFor(native, mac, "pds18-native"); got == nil || *got != "" {
		t.Fatalf("other host: %v", got)
	}
	if got := nativeFor(native, nil, "pds18-native"); got != native {
		t.Fatalf("no last host: %v", got)
	}
}

// Requirement: a host_move handoff stays within its budget however long the conversation is.
// Risk: a PM session moved between hosts again and again starts each run with its whole history.
func TestHostMoveHandoffIsBounded(t *testing.T) {
	turns := make([]agentTurn, 40)
	for i := range turns {
		turns[i] = agentTurn{harness: "codex", state: "succeeded", user: strings.Repeat("x", 3000), assistant: []string{"ok"}}
	}
	out := renderHandoff(turns, "host_move", "session s on owner-mac")
	if n := len([]rune(out)); n > hostMoveRunes || !strings.Contains(out, "local commits are not here") || !strings.Contains(out, "Only the last 12 of 40 turns") {
		t.Fatalf("host_move handoff: %d runes", n)
	}
}

// Requirement: fleet-people-access.md §7: a host with intake spare takes work only when no other host can.
// Risk: 2026-11-25 the owner's Mac, which the owner also works on, kept winning queued work by host creation order while
// Windows and PDS18 stood idle.
func TestDispatchSpareHostIsLast(t *testing.T) {
	env := openTestCoordinator(t, true)
	mac, pds := "spare-mac", "spare-pds"
	macSecret, pdsSecret := env.registerHost(t, mac), env.registerHost(t, pds)
	healthy := &contract.HostLoad{Load1: ptr(1.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9)}
	poll := func(host, secret string) {
		t.Helper()
		v := "1.261125.1"
		if code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
			HostId: host, Platform: contract.Platform("darwin"), Capabilities: []string{"codex"}, Stores: []contract.StoreReport{},
			Problems: []contract.EdgeProblem{}, ProjectRoots: []string{}, GitConnections: []string{}, ActiveExecutions: []string{},
			Load: healthy, Version: &v,
		}, map[string]string{"Authorization": "Bearer " + secret}); code != 200 {
			t.Fatalf("poll(%s) = %d %v", host, code, body)
		}
	}
	intake := func(host, v string) {
		t.Helper()
		if code, body := env.callJSON(t, "POST", "/api/ops/hosts_update", doc{"id": host, "intake": v}, env.ownerHeaders()); code != 200 {
			t.Fatalf("hosts_update %s intake %s = %d %v", host, v, code, body)
		}
	}
	poll(mac, macSecret)
	poll(pds, pdsSecret)

	// Created first, the Mac would win by host order; spare, it yields to the other host.
	intake(mac, "spare")
	sid1, eid1 := dispatchTestCreateQueued(t, env, "req-spare-1", nil)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	if ex := dispatchTestExecution(t, env, sid1, eid1); ex["hostId"] != pds {
		t.Fatalf("run with a spare and an open host placed on %v, want %s", ex["hostId"], pds)
	}

	// When no other host can take the work, the spare host does.
	intake(pds, "paused")
	sid2, eid2 := dispatchTestCreateQueued(t, env, "req-spare-2", nil)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	if ex := dispatchTestExecution(t, env, sid2, eid2); ex["hostId"] != mac {
		t.Fatalf("run with only a spare host open placed on %v, want %s", ex["hostId"], mac)
	}
	if code, _ := env.callJSON(t, "POST", "/api/ops/hosts_update", doc{"id": mac, "intake": "later"}, env.ownerHeaders()); code != 400 {
		t.Fatalf("hosts_update intake later = %d, want 400", code)
	}
}
