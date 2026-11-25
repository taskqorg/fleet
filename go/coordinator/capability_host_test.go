package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: a steer is checked against the host running the turn, whoever sends it (composer, MCP or agent).
// Risk: 2026-11-25 review: a steer sent without a composer checked the "steer" capability of the default host
// (FLEET_HOST_ID, owner-mac on prod, where the coordinator does not run), so a turn on a host that can steer was queued
// instead of steered, and a turn on a host that cannot would have been sent a steer.
func TestSteerChecksTheRunningHost(t *testing.T) {
	env := openTestCoordinator(t, true)
	host := "steer-host"
	secret := env.registerHost(t, host)
	v := "1.261125.1"
	poll := func() []map[string]any {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
			HostId: host, Platform: contract.Platform("darwin"), Capabilities: []string{"codex", "steer"}, Stores: []contract.StoreReport{},
			Problems: []contract.EdgeProblem{}, ProjectRoots: []string{}, GitConnections: []string{}, ActiveExecutions: []string{},
			Load: &contract.HostLoad{Load1: ptr(1.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9)}, Version: &v,
		}, map[string]string{"Authorization": "Bearer " + secret})
		if code != 200 {
			t.Fatalf("poll = %d %v", code, body)
		}
		var out []map[string]any
		for _, raw := range anys(body["commands"]) {
			out = append(out, mapOf(raw))
		}
		return out
	}
	poll()
	sid, eid := dispatchTestCreateQueued(t, env, "req-steer-1", &host)
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	cmds := poll()
	if len(cmds) != 1 || cmds[0]["executionId"] != eid {
		t.Fatalf("start commands = %v", cmds)
	}
	if code, body := env.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{HostId: host, CommandId: astr(cmds[0]["id"]), ExecutionId: eid,
		Status: contract.ReceiptStatusStarted}, map[string]string{"Authorization": "Bearer " + secret}); code != 200 {
		t.Fatalf("receipt started = %d %v", code, body)
	}
	code, msg := env.callJSON(t, "POST", "/api/ops/message_send", doc{"sessionId": sid, "requestId": "req-steer-2", "text": "also check the logs", "delivery": "steer"}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("message_send steer = %d %v", code, msg)
	}
	if msg["state"] != "steering" {
		t.Fatalf("steer to a running turn on a steer-capable host = state %v, want steering: %v", msg["state"], msg)
	}
}
