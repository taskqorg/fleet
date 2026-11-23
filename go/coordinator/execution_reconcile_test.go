package main

import (
	"encoding/json"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: fleet.md Reliability, TaskAcceptanceExecution & DispLostIDs:
// 1) A completed agent_run whose child tool_run failed commits its terminal receipt cleanly without 409 poisoning.
// 2) A finished or dropped agent_run (outbox.failed on /api/hosts/receipts, process.closed without receipt past grace,
//    or missing from activeExecutions on a live polling host past grace) reconciles promptly to failed/unconfirmed
//    with status, runners, hosts_list, session_get, and task state in agreement, while preserving still-running
//    and unconfirmed controls.
// Risk: failed tool_run rows with higher seq poison terminal host receipts with 409, or finished executor runs stay
// reported running/confirmed for hours across execution_status, hosts_list, sessions, and runners.
func TestReconcileFinishedRunsAndToolRunAcceptance(t *testing.T) {
	t.Setenv("FLEET_PROCESS_CLOSED_GRACE_MS", "200")
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	sec := e.registerHost(t, "h-rec")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','rec-proj','config/projects/rec-proj/project.json',1,'rec-proj');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','rec-proj',1,'{"settings":{"id":"rec-proj","name":"Reconcile Proj","contextFiles":[]},"contextFiles":[]}','rec1','editor','person','t');
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-tool-ok','Task with failed tool_run','rec-proj','in_progress'),
 ('t-outbox-fail','Task with outbox.failed receipt','rec-proj','in_progress'),
 ('t-pc-grace','Task with process.closed past grace','rec-proj','in_progress'),
 ('t-missing-active','Task missing from activeExecutions','rec-proj','in_progress'),
 ('t-still-live','Task still running on host','rec-proj','in_progress'),
 ('t-unconf-ctrl','Task unconfirmed control','rec-proj','in_progress');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('s-tool-ok','rec-proj','t-tool-ok','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-outbox-fail','rec-proj','t-outbox-fail','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-pc-grace','rec-proj','t-pc-grace','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-missing-active','rec-proj','t-missing-active','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-still-live','rec-proj','t-still-live','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-unconf-ctrl','rec-proj','t-unconf-ctrl','h-rec','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,harness,model,started_at,last_seen_at,data) VALUES
 ('e-tool-ok','agent_run','running','confirmed','rec-proj','t-tool-ok','s-tool-ok','h-rec','claude','sonnet',now()-interval '2 minutes',now()-interval '10 seconds','{"id":"e-tool-ok","sessionId":"s-tool-ok","hostId":"h-rec","taskId":"t-tool-ok"}'),
 ('e-outbox-fail','agent_run','running','confirmed','rec-proj','t-outbox-fail','s-outbox-fail','h-rec','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-outbox-fail","sessionId":"s-outbox-fail","hostId":"h-rec","taskId":"t-outbox-fail"}'),
 ('e-pc-grace','agent_run','running','confirmed','rec-proj','t-pc-grace','s-pc-grace','h-rec','claude','sonnet',now()-interval '2 minutes',now()-interval '10 seconds','{"id":"e-pc-grace","sessionId":"s-pc-grace","hostId":"h-rec","taskId":"t-pc-grace"}'),
 ('e-missing-active','agent_run','running','confirmed','rec-proj','t-missing-active','s-missing-active','h-rec','claude','sonnet',now()-interval '2 minutes',now()-interval '10 seconds','{"id":"e-missing-active","sessionId":"s-missing-active","hostId":"h-rec","taskId":"t-missing-active"}'),
 ('e-still-live','agent_run','running','confirmed','rec-proj','t-still-live','s-still-live','h-rec','claude','sonnet',now()-interval '2 minutes',now()-interval '10 seconds','{"id":"e-still-live","sessionId":"s-still-live","hostId":"h-rec","taskId":"t-still-live"}'),
 ('e-unconf-ctrl','agent_run','running','unconfirmed','rec-proj','t-unconf-ctrl','s-unconf-ctrl','h-rec','claude','sonnet',now()-interval '30 seconds',now()-interval '10 seconds','{"id":"e-unconf-ctrl","sessionId":"s-unconf-ctrl","hostId":"h-rec","taskId":"t-unconf-ctrl","updatedAt":`+jsonMilliLiteral(time.Now().Add(-10*time.Second))+`}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,parent_execution_id,session_id,problem,started_at,ended_at,data) VALUES
 ('call:failed-tool','tool_run','failed','confirmed','rec-proj','t-tool-ok','e-tool-ok','s-tool-ok','step:provider-404',now()-interval '1 minute',now()-interval '50 seconds','{"connection":"youtrack","tool":"issue_get"}');
INSERT INTO command(id,execution_id,session_id,host_id,project_id,kind,status,handed_out,payload) VALUES
 ('cmd-tool-ok','e-tool-ok','s-tool-ok','h-rec','rec-proj','start','started',true,'{}'),
 ('cmd-outbox-fail','e-outbox-fail','s-outbox-fail','h-rec','rec-proj','start','started',true,'{}'),
 ('cmd-pc-grace','e-pc-grace','s-pc-grace','h-rec','rec-proj','start','started',true,'{}'),
 ('cmd-missing-active','e-missing-active','s-missing-active','h-rec','rec-proj','start','started',true,'{}'),
 ('cmd-still-live','e-still-live','s-still-live','h-rec','rec-proj','start','started',true,'{}'),
 ('cmd-unconf-ctrl','e-unconf-ctrl','s-unconf-ctrl','h-rec','rec-proj','start','unknown',true,'{}');
`)

	// 1) Failed child tool_run with higher seq must NOT cause 409 on completed receipt for e-tool-ok.
	pcData, _ := json.Marshal(contract.ProcessClosed{ExitCode: ptr(int64(0)), StdoutClosed: true})
	code, body := e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-rec", ExecutionId: "e-tool-ok",
		Events: []contract.HostEvent{{Id: "pc-1", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("process.closed e-tool-ok = %d %v", code, body)
	}
	code, body = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-rec", CommandId: "cmd-tool-ok", ExecutionId: "e-tool-ok", Status: contract.ReceiptStatusCompleted,
	}, hostHdr)
	if code != 200 {
		t.Fatalf("completed receipt for e-tool-ok = %d %v", code, body)
	}
	code, statusOK := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-tool-ok"}, owner)
	if code != 200 || statusOK["state"] != "succeeded" || statusOK["observation"] != "confirmed" || statusOK["next"] != "message" {
		t.Fatalf("e-tool-ok status = %d %v", code, statusOK)
	}
	code, taskOK := e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: "t-tool-ok"}, owner)
	if code != 200 || len(anys(taskOK["openQuestions"])) != 1 {
		t.Fatalf("t-tool-ok expected 1 acceptance question, got %d %v", code, taskOK)
	}

	// 2) Send process.closed + outbox.failed(/api/hosts/receipts) for e-outbox-fail, and process.closed for e-pc-grace.
	ofData, _ := json.Marshal(contract.OutboxFailed{Route: contract.RouteReceipts.Path, Status: 409, Reason: "coordinator 409"})
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-rec", ExecutionId: "e-outbox-fail",
		Events: []contract.HostEvent{
			{Id: "pc-2", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData},
			{Id: "of-2", Seq: 1_000_000_000_002, Timestamp: nowMs(), Type: string(contract.FleetEventTypeOutboxFailed), Data: ofData},
		},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("events e-outbox-fail = %d %v", code, body)
	}
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-rec", ExecutionId: "e-pc-grace",
		Events: []contract.HostEvent{{Id: "pc-3", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("events e-pc-grace = %d %v", code, body)
	}

	// Wait past closed grace (200ms) and poll with activeExecutions keeping ONLY e-still-live and e-unconf-ctrl.
	time.Sleep(300 * time.Millisecond)
	code, body = e.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
		HostId:           "h-rec",
		Platform:         contract.Platform("darwin"),
		Capabilities:     []string{"claude"},
		Stores:           []contract.StoreReport{},
		Problems:         []contract.EdgeProblem{},
		ProjectRoots:     []string{},
		GitConnections:   []string{},
		ActiveExecutions: []string{"e-still-live", "e-unconf-ctrl"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("poll = %d %v", code, body)
	}

	if err := reconcileLostExecutions(e.ctx, e.app); err != nil {
		t.Fatalf("reconcileLostExecutions: %v", err)
	}

	// Verify reconciled executions are failed/unconfirmed with next=message and problem=unknown:abandoned.
	for _, eid := range []string{"e-outbox-fail", "e-pc-grace", "e-missing-active"} {
		code, st := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: eid}, owner)
		if code != 200 || st["state"] != "failed" || st["observation"] != "unconfirmed" || st["next"] != "message" {
			t.Fatalf("%s status = %d %v, want failed/unconfirmed/message", eid, code, st)
		}
	}
	// Verify still-running and unconfirmed controls were NOT prematurely ended.
	code, liveSt := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-still-live"}, owner)
	if code != 200 || liveSt["state"] != "running" || liveSt["observation"] != "confirmed" || liveSt["next"] != "wait_or_steer" {
		t.Fatalf("e-still-live control = %d %v, want running/confirmed/wait_or_steer", code, liveSt)
	}
	code, unconfSt := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-unconf-ctrl"}, owner)
	if code != 200 || unconfSt["state"] != "running" || unconfSt["observation"] != "unconfirmed" || unconfSt["next"] != "session_fork" {
		t.Fatalf("e-unconf-ctrl control = %d %v, want running/unconfirmed/session_fork", code, unconfSt)
	}

	// Verify UI projections agree: hosts_list counts only the 2 still-running runs on h-rec, session_get sees terminal+unconfirmed,
	// and executions_runners marks reconciled runs failed with null/unavailable wallMs.
	code, hl := e.callJSON(t, "POST", "/api/ops/hosts_list", contract.HostsListRequest{}, owner)
	if code != 200 {
		t.Fatalf("hosts_list = %d %v", code, hl)
	}
	foundHost := false
	for _, rawH := range anys(hl["hosts"]) {
		h := mapOf(rawH)
		if h["id"] == "h-rec" {
			foundHost = true
			if int(num(h["running"])) != 2 {
				t.Fatalf("h-rec running = %v, want 2 (e-still-live + e-unconf-ctrl)", h["running"])
			}
		}
	}
	if !foundHost {
		t.Fatalf("h-rec missing from hosts_list")
	}

	code, sg := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": "s-outbox-fail"}, owner)
	if code != 200 {
		t.Fatalf("session_get = %d %v", code, sg)
	}
	exRows := anys(sg["executions"])
	if len(exRows) != 1 || mapOf(exRows[0])["state"] != "failed" || mapOf(exRows[0])["observation"] != "unconfirmed" {
		t.Fatalf("session_get s-outbox-fail executions = %v", exRows)
	}

	code, rn := e.callJSON(t, "POST", "/api/ops/executions_runners", doc{}, owner)
	if code != 200 {
		t.Fatalf("executions_runners = %d %v", code, rn)
	}
	seenRunner := map[string]map[string]any{}
	for _, rawR := range anys(rn["runs"]) {
		r := mapOf(rawR)
		if id, ok := r["id"].(string); ok {
			seenRunner[id] = r
		}
	}
	for _, eid := range []string{"e-outbox-fail", "e-pc-grace", "e-missing-active"} {
		r := seenRunner[eid]
		if r == nil || r["state"] != "failed" || r["problem"] != "unknown:abandoned" || r["wallMs"] != nil {
			t.Fatalf("runner row %s = %v, want failed/unknown:abandoned/wallMs=nil", eid, r)
		}
	}
	if r := seenRunner["e-still-live"]; r == nil || r["state"] != "running" {
		t.Fatalf("runner row e-still-live = %v, want running", r)
	}
}

func jsonMilliLiteral(t time.Time) string {
	b, _ := json.Marshal(t.UnixMilli())
	return string(b)
}
