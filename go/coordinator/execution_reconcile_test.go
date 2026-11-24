package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if code != 200 || len(anys(taskOK["openQuestions"])) != 0 || taskOK["completionReportMissing"] != true {
		t.Fatalf("t-tool-ok before worker_done expected 0 openQuestions and completionReportMissing=true, got %d %v", code, taskOK)
	}
	code, wdOK := e.callJSON(t, "POST", "/api/ops/worker_done", contract.WorkerDoneRequest{
		ExecutionId: "e-tool-ok",
		Outcome:     json.RawMessage(`"succeeded"`),
	}, owner)
	if code != 200 || wdOK["action"] != "accepted" {
		t.Fatalf("worker_done e-tool-ok = %d %v", code, wdOK)
	}
	code, taskOK = e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: "t-tool-ok"}, owner)
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

// Requirement: fleet.md Work management & Agent execution (T-FLT-35):
// 1) A clean process exit (exitCode 0, no remaining child processes, no worker_done report, no open question)
//    with diagnostic mcp-lifecycle-* files in Receipt.Artifacts must reach terminal succeeded immediately instead
//    of entering or stalling in waiting, and must promote queued follow-up messages.
// 2) Real output artifacts still wait until stored/settled, while pre-reserve upload failure (outbox.failed on
//    /api/hosts/artifacts/failed) or non-artifact waiting state past process.closed grace reaches terminal failed
//    without stalling in waiting, while a still-uploading control stays waiting.
// Risk: clean-exit runs enter waiting behind diagnostic mcp-lifecycle-* files or fail to settle out of waiting,
// leaving tasks/sessions stalled with no open question and no report.
func TestCleanExitWaitingReconciliation(t *testing.T) {
	t.Setenv("FLEET_PROCESS_CLOSED_GRACE_MS", "200")
	oldLocations, oldSecrets, oldEndpointHosts := defaultLocations, storeSecretsDir, storeEndpointHosts
	defaultLocations = []string{"h-wait"}
	storeSecretsDir = filepath.Join(t.TempDir(), "stores")
	storeEndpointHosts = []string{"127.0.0.1"}
	t.Cleanup(func() {
		defaultLocations, storeSecretsDir, storeEndpointHosts = oldLocations, oldSecrets, oldEndpointHosts
	})

	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut, http.MethodPost, http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer fakeS3.Close()

	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	sec := e.registerHost(t, "h-wait")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}

	code, body := e.callJSON(t, "POST", "/api/hosts/stores", doc{
		"store": doc{
			"id":              "wait-store",
			"bucket":          "fleet",
			"endpoint":        fakeS3.URL,
			"accessKeyId":     "k",
			"secretAccessKey": "s",
		},
	}, hostHeadersWith(hostHdr))
	if code != 200 {
		t.Fatalf("stores = %d %v", code, body)
	}

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','wait-proj','config/projects/wait-proj/project.json',1,'wait-proj');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','wait-proj',1,'{"settings":{"id":"wait-proj","name":"Wait Proj","contextFiles":[]},"contextFiles":[]}','w1','editor','person','t');
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-mcp-clean','Clean exit with diagnostic mcp logs','wait-proj','in_progress'),
 ('t-mcp-noreport','Clean exit with diagnostic mcp logs no queue','wait-proj','in_progress'),
 ('t-out-wait','Clean exit with output wait then stored','wait-proj','in_progress'),
 ('t-out-fail','Clean exit with pre-reserve upload fail','wait-proj','in_progress'),
 ('t-wait-lost','Unexpected waiting after process closed','wait-proj','in_progress'),
 ('t-wait-live','Still uploading output control','wait-proj','in_progress');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('s-mcp-clean','wait-proj','t-mcp-clean','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-mcp-noreport','wait-proj','t-mcp-noreport','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-out-wait','wait-proj','t-out-wait','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-out-fail','wait-proj','t-out-fail','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-wait-lost','wait-proj','t-wait-lost','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-wait-live','wait-proj','t-wait-live','h-wait','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,harness,model,started_at,last_seen_at,data) VALUES
 ('e-mcp-clean','agent_run','running','confirmed','wait-proj','t-mcp-clean','s-mcp-clean','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-mcp-clean","sessionId":"s-mcp-clean","hostId":"h-wait","taskId":"t-mcp-clean"}'),
 ('e-mcp-noreport','agent_run','running','confirmed','wait-proj','t-mcp-noreport','s-mcp-noreport','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-mcp-noreport","sessionId":"s-mcp-noreport","hostId":"h-wait","taskId":"t-mcp-noreport"}'),
 ('e-out-wait','agent_run','running','confirmed','wait-proj','t-out-wait','s-out-wait','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-out-wait","sessionId":"s-out-wait","hostId":"h-wait","taskId":"t-out-wait"}'),
 ('e-out-fail','agent_run','running','confirmed','wait-proj','t-out-fail','s-out-fail','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-out-fail","sessionId":"s-out-fail","hostId":"h-wait","taskId":"t-out-fail"}'),
 ('e-wait-lost','agent_run','waiting','confirmed','wait-proj','t-wait-lost','s-wait-lost','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-wait-lost","sessionId":"s-wait-lost","hostId":"h-wait","taskId":"t-wait-lost"}'),
 ('e-wait-live','agent_run','waiting','confirmed','wait-proj','t-wait-live','s-wait-live','h-wait','claude','sonnet',now()-interval '2 minutes',now()-interval '5 seconds','{"id":"e-wait-live","sessionId":"s-wait-live","hostId":"h-wait","taskId":"t-wait-live","waitReason":"artifacts","waitingSince":`+jsonMilliLiteral(time.Now())+`,"declaredArtifacts":["output/live.txt"],"pendingArtifacts":["output/live.txt"]}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,parent_execution_id,session_id,problem,started_at,ended_at,data) VALUES
 ('call:mcp-tool-err','tool_run','failed','confirmed','wait-proj','t-mcp-clean','e-mcp-clean','s-mcp-clean','step:bad-request',now()-interval '1 minute',now()-interval '50 seconds','{"connection":"youtrack","tool":"issue_get"}'),
 ('call:mcp-noreport-err','tool_run','failed','confirmed','wait-proj','t-mcp-noreport','e-mcp-noreport','s-mcp-noreport','step:bad-request',now()-interval '1 minute',now()-interval '50 seconds','{"connection":"youtrack","tool":"issue_get"}'),
 ('call:out-tool-err','tool_run','failed','confirmed','wait-proj','t-out-wait','e-out-wait','s-out-wait','step:bad-request',now()-interval '1 minute',now()-interval '50 seconds','{"connection":"youtrack","tool":"issue_get"}');
INSERT INTO command(id,execution_id,session_id,host_id,project_id,kind,status,handed_out,payload) VALUES
 ('cmd-mcp-clean','e-mcp-clean','s-mcp-clean','h-wait','wait-proj','start','started',true,'{}'),
 ('cmd-mcp-noreport','e-mcp-noreport','s-mcp-noreport','h-wait','wait-proj','start','started',true,'{}'),
 ('cmd-out-wait','e-out-wait','s-out-wait','h-wait','wait-proj','start','started',true,'{}'),
 ('cmd-out-fail','e-out-fail','s-out-fail','h-wait','wait-proj','start','started',true,'{}'),
 ('cmd-wait-lost','e-wait-lost','s-wait-lost','h-wait','wait-proj','start','completed',true,'{}'),
 ('cmd-wait-live','e-wait-live','s-wait-live','h-wait','wait-proj','start','completed',true,'{}');
INSERT INTO queued_message(id,session_id,project_id,task_id,request_id,text,state) VALUES
 ('qm-mcp-clean','s-mcp-clean','wait-proj','t-mcp-clean','req-mcp-clean','follow up after clean exit','queued'),
 ('qm-out-wait','s-out-wait','wait-proj','t-out-wait','req-out-wait','follow up after output upload','queued');
`)

	pcData, _ := json.Marshal(contract.ProcessClosed{ExitCode: ptr(int64(0)), StdoutClosed: true, ObservedOwnedProcessesRemaining: ptr(false)})

	// 1) Clean exit declaring diagnostic mcp-lifecycle-*.jsonl (no report, no open question) succeeds immediately
	// without entering waiting, and promotes the queued follow-up message.
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-wait", ExecutionId: "e-mcp-clean",
		Events: []contract.HostEvent{{Id: "pc-mcp", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("process.closed e-mcp-clean = %d %v", code, body)
	}
	code, body = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-wait", CommandId: "cmd-mcp-clean", ExecutionId: "e-mcp-clean",
		Status: contract.ReceiptStatusCompleted, Artifacts: []string{"mcp-lifecycle-trace.jsonl"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("completed receipt e-mcp-clean = %d %v", code, body)
	}
	code, st := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-mcp-clean"}, owner)
	if code != 200 || st["state"] != "succeeded" || st["observation"] != "confirmed" || st["next"] != "message" {
		t.Fatalf("e-mcp-clean status = %d %v, want succeeded/confirmed/message", code, st)
	}
	code, tk := e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: "t-mcp-clean"}, owner)
	if code != 200 || tk["state"] != "in_progress" || len(anys(tk["openQuestions"])) != 0 {
		t.Fatalf("t-mcp-clean = %d %v, want in_progress, 0 openQuestions", code, tk)
	}
	if qmState := queryScalarTest(t, e, "SELECT state::text FROM queued_message WHERE id='qm-mcp-clean'"); qmState != "started" {
		t.Fatalf("qm-mcp-clean state = %q, want started", qmState)
	}
	// Without a queued follow-up, clean exit with diagnostic mcp-lifecycle-* also succeeds immediately and flags completionReportMissing.
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-wait", ExecutionId: "e-mcp-noreport",
		Events: []contract.HostEvent{{Id: "pc-mcp-nr", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("process.closed e-mcp-noreport = %d %v", code, body)
	}
	code, body = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-wait", CommandId: "cmd-mcp-noreport", ExecutionId: "e-mcp-noreport",
		Status: contract.ReceiptStatusCompleted, Artifacts: []string{"mcp-lifecycle-trace.jsonl"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("completed receipt e-mcp-noreport = %d %v", code, body)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-mcp-noreport"}, owner)
	if code != 200 || st["state"] != "succeeded" || st["observation"] != "confirmed" || st["next"] != "message" {
		t.Fatalf("e-mcp-noreport status = %d %v, want succeeded/confirmed/message", code, st)
	}
	code, tk = e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: "t-mcp-noreport"}, owner)
	if code != 200 || tk["state"] != "in_progress" || len(anys(tk["openQuestions"])) != 0 || tk["completionReportMissing"] != true {
		t.Fatalf("t-mcp-noreport = %d %v, want in_progress, 0 openQuestions, completionReportMissing=true", code, tk)
	}

	// 2) Clean exit with real output deliverable enters waiting, then settles to succeeded once reserved/stored
	// (even with a failed child tool_run), promoting the queued follow-up message.
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-wait", ExecutionId: "e-out-wait",
		Events: []contract.HostEvent{{Id: "pc-out-wait", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("process.closed e-out-wait = %d %v", code, body)
	}
	code, body = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-wait", CommandId: "cmd-out-wait", ExecutionId: "e-out-wait",
		Status: contract.ReceiptStatusCompleted, Artifacts: []string{"output/result.txt"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("completed receipt e-out-wait = %d %v", code, body)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-out-wait"}, owner)
	if code != 200 || st["state"] != "waiting" {
		t.Fatalf("e-out-wait before reserve = %d %v, want waiting", code, st)
	}
	code, res := e.callJSON(t, "POST", "/api/hosts/artifacts/reserve", doc{
		"hostId":      "h-wait",
		"executionId": "e-out-wait",
		"name":        "output/result.txt",
		"mediaType":   "text/plain",
		"size":        0,
		"sha256":      emptySHA256,
	}, hostHdr)
	if code != 200 || res["state"] != "stored" {
		t.Fatalf("reserve e-out-wait = %d %v", code, res)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-out-wait"}, owner)
	if code != 200 || st["state"] != "succeeded" || st["next"] != "message" {
		t.Fatalf("e-out-wait after reserve = %d %v, want succeeded/message", code, st)
	}
	if qmState := queryScalarTest(t, e, "SELECT state::text FROM queued_message WHERE id='qm-out-wait'"); qmState != "started" {
		t.Fatalf("qm-out-wait state = %q, want started", qmState)
	}

	// 3) Clean exit entering waiting on output/missing.txt whose pre-reserve upload fails (outbox.failed on
	// /api/hosts/artifacts/failed) settles immediately to failed instead of stalling for 24h.
	code, body = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-wait", CommandId: "cmd-out-fail", ExecutionId: "e-out-fail",
		Status: contract.ReceiptStatusCompleted, Artifacts: []string{"output/missing.txt"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("completed receipt e-out-fail = %d %v", code, body)
	}
	afData, _ := json.Marshal(contract.OutboxFailed{Route: contract.RouteArtifactsFailed.Path, Status: 404, Reason: "coordinator 404"})
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-wait", ExecutionId: "e-out-fail",
		Events: []contract.HostEvent{
			{Id: "pc-out-fail", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData},
			{Id: "of-art-fail", Seq: 1_000_000_000_001, Timestamp: nowMs(), Type: string(contract.FleetEventTypeOutboxFailed), Data: afData},
		},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("events e-out-fail = %d %v", code, body)
	}
	if err := reconcileArtifactWaits(e.ctx, e.app); err != nil {
		t.Fatalf("reconcileArtifactWaits: %v", err)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-out-fail"}, owner)
	if code != 200 || st["state"] != "failed" || st["next"] != "message" {
		t.Fatalf("e-out-fail status = %d %v, want failed/message", code, st)
	}
	code, tk = e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: "t-out-fail"}, owner)
	if code != 200 || tk["state"] != "waiting" || len(anys(tk["openQuestions"])) != 1 {
		t.Fatalf("t-out-fail = %d %v, want waiting with operator_decision", code, tk)
	}

	// 4) Non-artifact waiting execution with process.closed past grace reconciles to failed/unconfirmed,
	// while still-uploading control remains waiting.
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-wait", ExecutionId: "e-wait-lost",
		Events: []contract.HostEvent{{Id: "pc-wait-lost", Seq: 1, Timestamp: nowMs(), Type: string(contract.FleetEventTypeProcessClosed), Data: pcData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("process.closed e-wait-lost = %d %v", code, body)
	}
	time.Sleep(300 * time.Millisecond)
	if err := reconcileLostExecutions(e.ctx, e.app); err != nil {
		t.Fatalf("reconcileLostExecutions: %v", err)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-wait-lost"}, owner)
	if code != 200 || st["state"] != "failed" || st["observation"] != "unconfirmed" || st["next"] != "message" {
		t.Fatalf("e-wait-lost status = %d %v, want failed/unconfirmed/message", code, st)
	}
	code, st = e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-wait-live"}, owner)
	if code != 200 || st["state"] != "waiting" {
		t.Fatalf("e-wait-live control = %d %v, want waiting", code, st)
	}
}

func hostHeadersWith(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

func queryScalarTest(t *testing.T, e *testCoordEnv, sql string) string {
	t.Helper()
	var out string
	err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		return q.tx.QueryRow(q.ctx(), sql).Scan(&out)
	})
	if err != nil {
		t.Fatalf("query %s: %v", sql, err)
	}
	return out
}

// Requirement: fleet.md Reliability, T-FLT-36 (executions_reconcile, DispLostCandidates, executionAbandon, host_events):
// 1) Historical executions stuck in preparing/running/waiting after missed or rejected completion acks
//    (non-artifact waiting + process.closed or outbox.failed, accepted start dropped from activeExecutions,
//    terminal command with open execution, teardown on open execution, or removed host) reconcile safely via
//    executions_reconcile (preview & apply) to failed/unconfirmed/unknown:abandoned with cleared wait_reason and
//    operator audit provenance, while preserving live runs, artifact waits, and queued preparing runs.
// 2) Non-owner callers receive 403 on executions_reconcile, and late teardown events do not overwrite
//    observation='unconfirmed' back to 'confirmed' on already-abandoned executions.
// Risk: historical stuck runs remain wedged forever or are misclassified as confirmed by late teardown events,
// or an unsafe bulk recovery op kills live executions or artifact waits without audit provenance.
func TestReconcileHistoricalStuckExecutions(t *testing.T) {
	t.Setenv("FLEET_PROCESS_CLOSED_GRACE_MS", "200")
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{
		"Authorization":  "Bearer " + e.browserSecret,
		"X-Fleet-Person": "member@trial.test",
	}
	sec := e.registerHost(t, "h-hist")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}
	_ = e.registerHost(t, "h-gone")

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','hist-proj','config/projects/hist-proj/project.json',1,'hist-proj');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','hist-proj',1,'{"settings":{"id":"hist-proj","name":"Hist Proj","contextFiles":[]},"contextFiles":[]}','h1','editor','person','t');
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-wait-pc','Waiting after process.closed','hist-proj','in_progress'),
 ('t-wait-ob','Waiting after outbox.failed','hist-proj','in_progress'),
 ('t-prep-acc','Preparing accepted dropped','hist-proj','in_progress'),
 ('t-term-cmd','Terminal command open exec','hist-proj','in_progress'),
 ('t-teardown','Teardown open exec','hist-proj','in_progress'),
 ('t-removed','Removed host open exec','hist-proj','in_progress'),
 ('t-live','Live control','hist-proj','in_progress'),
 ('t-wait-art','Artifact wait control','hist-proj','in_progress'),
 ('t-prep-q','Queued preparing control','hist-proj','queued');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('s-wait-pc','hist-proj','t-wait-pc','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-wait-ob','hist-proj','t-wait-ob','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-prep-acc','hist-proj','t-prep-acc','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-term-cmd','hist-proj','t-term-cmd','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-teardown','hist-proj','t-teardown','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-removed','hist-proj','t-removed','h-gone','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-live','hist-proj','t-live','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-wait-art','hist-proj','t-wait-art','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('s-prep-q','hist-proj','t-prep-q','h-hist','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,harness,model,wait_reason,started_at,last_seen_at,data) VALUES
 ('e-wait-pc','agent_run','waiting','confirmed','hist-proj','t-wait-pc','s-wait-pc','h-hist','claude','sonnet','question',now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-wait-pc","sessionId":"s-wait-pc","hostId":"h-hist","taskId":"t-wait-pc","waitReason":"question","waitDetail":"stale wait"}'),
 ('e-wait-ob','agent_run','waiting','confirmed','hist-proj','t-wait-ob','s-wait-ob','h-hist','claude','sonnet','question',now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-wait-ob","sessionId":"s-wait-ob","hostId":"h-hist","taskId":"t-wait-ob","waitReason":"question"}'),
 ('e-prep-acc','agent_run','preparing','confirmed','hist-proj','t-prep-acc','s-prep-acc','h-hist','claude','sonnet',NULL,now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-prep-acc","sessionId":"s-prep-acc","hostId":"h-hist","taskId":"t-prep-acc"}'),
 ('e-term-cmd','agent_run','running','confirmed','hist-proj','t-term-cmd','s-term-cmd','h-hist','claude','sonnet',NULL,now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-term-cmd","sessionId":"s-term-cmd","hostId":"h-hist","taskId":"t-term-cmd"}'),
 ('e-teardown','agent_run','running','confirmed','hist-proj','t-teardown','s-teardown','h-hist','claude','sonnet',NULL,now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-teardown","sessionId":"s-teardown","hostId":"h-hist","taskId":"t-teardown"}'),
 ('e-removed','agent_run','running','confirmed','hist-proj','t-removed','s-removed','h-gone','claude','sonnet',NULL,now()-interval '10 minutes',now()-interval '9 minutes','{"id":"e-removed","sessionId":"s-removed","hostId":"h-gone","taskId":"t-removed"}'),
 ('e-live','agent_run','running','confirmed','hist-proj','t-live','s-live','h-hist','claude','sonnet',NULL,now()-interval '2 minutes',now()-interval '10 seconds','{"id":"e-live","sessionId":"s-live","hostId":"h-hist","taskId":"t-live"}'),
 ('e-wait-art','agent_run','waiting','confirmed','hist-proj','t-wait-art','s-wait-art','h-hist','claude','sonnet',NULL,now()-interval '2 minutes',now()-interval '1 minute','{"id":"e-wait-art","sessionId":"s-wait-art","hostId":"h-hist","taskId":"t-wait-art","waitReason":"artifacts","waitingSince":`+jsonMilliLiteral(time.Now().Add(-1*time.Minute))+`,"pendingArtifacts":["out.txt"],"declaredArtifacts":["out.txt"]}'),
 ('e-prep-q','agent_run','preparing','confirmed','hist-proj','t-prep-q','s-prep-q','h-hist','claude','sonnet',NULL,NULL,NULL,'{"id":"e-prep-q","sessionId":"s-prep-q","hostId":"h-hist","taskId":"t-prep-q"}');
INSERT INTO command(id,execution_id,session_id,host_id,project_id,kind,status,handed_out,payload,updated_at) VALUES
 ('cmd-wait-pc','e-wait-pc','s-wait-pc','h-hist','hist-proj','start','started',true,'{}',now()-interval '9 minutes'),
 ('cmd-wait-ob','e-wait-ob','s-wait-ob','h-hist','hist-proj','start','started',true,'{}',now()-interval '9 minutes'),
 ('cmd-prep-acc','e-prep-acc','s-prep-acc','h-hist','hist-proj','start','accepted',true,'{}',now()-interval '9 minutes'),
 ('cmd-term-cmd','e-term-cmd','s-term-cmd','h-hist','hist-proj','start','completed',true,'{}',now()-interval '9 minutes'),
 ('cmd-teardown','e-teardown','s-teardown','h-hist','hist-proj','start','started',true,'{}',now()-interval '9 minutes'),
 ('cmd-removed','e-removed','s-removed','h-gone','hist-proj','start','started',true,'{}',now()-interval '9 minutes'),
 ('cmd-live','e-live','s-live','h-hist','hist-proj','start','started',true,'{}',now()-interval '10 seconds'),
 ('cmd-wait-art','e-wait-art','s-wait-art','h-hist','hist-proj','start','completed',true,'{}',now()-interval '1 minute'),
 ('cmd-prep-q','e-prep-q','s-prep-q','h-hist','hist-proj','start','pending',false,'{}',now()-interval '10 minutes');
INSERT INTO event(id,execution_id,session_id,project_id,event_seq,type,payload,updated_at) VALUES
 ('ev-wait-pc','e-wait-pc','s-wait-pc','hist-proj',1,'process.closed','{"type":"process.closed","data":{"exitCode":0}}',now()-interval '5 minutes'),
 ('ev-wait-ob','e-wait-ob','s-wait-ob','hist-proj',1,'outbox.failed','{"type":"outbox.failed","data":{"route":"/api/hosts/receipts","status":409}}',now()-interval '5 minutes'),
 ('ev-teardown','e-teardown','s-teardown','hist-proj',1,'teardown','{"type":"teardown","data":{"removed":true}}',now()-interval '5 minutes'),
 ('ev-wait-art-pc','e-wait-art','s-wait-art','hist-proj',1,'process.closed','{"type":"process.closed","data":{"exitCode":0}}',now()-interval '1 minute');
UPDATE host SET state='removed', updated_at=now()-interval '5 minutes' WHERE host_id='h-gone';
`)

	// Live host reports only e-live as active (plus e-prep-q is still pending pickup).
	time.Sleep(300 * time.Millisecond)
	code, body := e.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
		HostId:           "h-hist",
		Platform:         contract.Platform("darwin"),
		Capabilities:     []string{"claude"},
		Stores:           []contract.StoreReport{},
		Problems:         []contract.EdgeProblem{},
		ProjectRoots:     []string{},
		GitConnections:   []string{},
		ActiveExecutions: []string{"e-live"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("poll = %d %v", code, body)
	}

	// Non-owner must be refused.
	code, _ = e.callJSON(t, "POST", "/api/ops/executions_reconcile", doc{"preview": true}, member)
	if code != 403 {
		t.Fatalf("non-owner executions_reconcile = %d, want 403", code)
	}

	// Preview with explicit ids including both stuck runs and protected controls.
	allIDs := []string{
		"e-wait-pc", "e-wait-ob", "e-prep-acc", "e-term-cmd", "e-teardown", "e-removed",
		"e-live", "e-wait-art", "e-prep-q", "missing-id",
	}
	code, prev := e.callJSON(t, "POST", "/api/ops/executions_reconcile", doc{"preview": true, "ids": allIDs}, owner)
	if code != 200 || prev["preview"] != true || len(anys(prev["reconciled"])) != 0 {
		t.Fatalf("executions_reconcile preview = %d %v", code, prev)
	}
	wantCauses := map[string]string{
		"e-wait-pc":  "process_closed_no_receipt",
		"e-wait-ob":  "outbox_failed_receipt",
		"e-prep-acc": "missing_from_host_active_executions",
		"e-term-cmd": "terminal_command_open_execution",
		"e-teardown": "workspace_teardown",
		"e-removed":  "host_removed",
	}
	gotCandidates := map[string]string{}
	for _, raw := range anys(prev["candidates"]) {
		c := mapOf(raw)
		id, _ := c["id"].(string)
		cause, _ := c["cause"].(string)
		gotCandidates[id] = cause
	}
	for id, want := range wantCauses {
		if gotCandidates[id] != want {
			t.Fatalf("preview candidate %s cause = %q, want %q (all=%v)", id, gotCandidates[id], want, gotCandidates)
		}
	}
	if len(anys(prev["skipped"])) != 4 {
		t.Fatalf("preview skipped = %v, want 4 controls+missing", prev["skipped"])
	}
	// Verify preview mutated nothing.
	code, stBefore := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-wait-pc"}, owner)
	if code != 200 || stBefore["state"] != "waiting" {
		t.Fatalf("preview mutated e-wait-pc: %d %v", code, stBefore)
	}

	// Execute reconciliation with explicit audit reason (scanning without ids).
	opReason := "T-FLT-36 operator reconciliation: no live worker process"
	code, rec := e.callJSON(t, "POST", "/api/ops/executions_reconcile", doc{"preview": false, "reason": opReason}, owner)
	if code != 200 || rec["preview"] != false {
		t.Fatalf("executions_reconcile apply = %d %v", code, rec)
	}
	reconciledSet := map[string]bool{}
	for _, raw := range anys(rec["reconciled"]) {
		if id, ok := raw.(string); ok {
			reconciledSet[id] = true
		}
	}
	for id := range wantCauses {
		if !reconciledSet[id] {
			t.Fatalf("expected %s reconciled, got %v", id, rec["reconciled"])
		}
		code, st := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: id}, owner)
		if code != 200 || st["state"] != "failed" || st["observation"] != "unconfirmed" {
			t.Fatalf("reconciled %s status = %d %v, want failed/unconfirmed", id, code, st)
		}
		code, sc := e.callJSON(t, "POST", "/api/ops/setting_changes", doc{"target": "execution:" + id}, owner)
		if code != 200 || len(anys(sc["changes"])) != 1 {
			t.Fatalf("audit for %s = %d %v, want 1 row", id, code, sc)
		}
		ch := mapOf(anys(sc["changes"])[0])
		if ch["actor"] != testOwnerEmail || ch["value"] != "failed" || ch["reason"] != opReason {
			t.Fatalf("audit row for %s = %v", id, ch)
		}
	}
	// Verify waitReason and wait_reason were cleared on waiting runs.
	var waitCol string
	err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
		rows, err := q.db().DispExecStates(q.ctx(), []string{"e-wait-pc", "e-wait-ob"})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.WaitReason != "" {
				waitCol = r.ID + ":" + r.WaitReason
			}
		}
		return nil
	})
	if err != nil || waitCol != "" {
		t.Fatalf("expected cleared waitReason, got err=%v waitCol=%q", err, waitCol)
	}

	// Verify controls were NOT touched.
	for id, wantState := range map[string]string{
		"e-live":     "running",
		"e-wait-art": "waiting",
		"e-prep-q":   "preparing",
	} {
		code, st := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: id}, owner)
		if code != 200 || st["state"] != wantState {
			t.Fatalf("control %s state = %d %v, want %s", id, code, st, wantState)
		}
	}

	// Idempotent re-run with explicit ids skips already-reconciled runs and controls.
	code, rerun := e.callJSON(t, "POST", "/api/ops/executions_reconcile", doc{"preview": false, "ids": allIDs}, owner)
	if code != 200 || len(anys(rerun["reconciled"])) != 0 || len(anys(rerun["skipped"])) != len(allIDs) {
		t.Fatalf("idempotent rerun = %d %v", code, rerun)
	}

	// A late teardown event on an abandoned failed/unconfirmed execution MUST NOT mark observation='confirmed'.
	tdData, _ := json.Marshal(contract.Teardown{Removed: true})
	code, body = e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-hist", ExecutionId: "e-wait-pc",
		Events: []contract.HostEvent{{Id: "td-late", Seq: 99, Timestamp: nowMs(), Type: string(contract.FleetEventTypeTeardown), Data: tdData}},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("late teardown event = %d %v", code, body)
	}
	code, stAfterTd := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "e-wait-pc"}, owner)
	if code != 200 || stAfterTd["state"] != "failed" || stAfterTd["observation"] != "unconfirmed" {
		t.Fatalf("late teardown flipped observation: %d %v", code, stAfterTd)
	}
}

// Requirement: fleet.md Reliability, T-FLT-36 (reconcileLostExecutions background pass):
// The background lost pass reconciles historical non-artifact waiting runs, accepted preparing runs missing from
// activeExecutions, terminal-command open runs, teardown open runs, and removed-host open runs automatically with
// actor='fleet' audit rows, without touching live runs, artifact waits, or queued runs.
// Risk: background reconciliation skips historical waiting/preparing/teardown/removed-host runs unless manually invoked.
func TestReconcileLostPassHealsWaitingAndTeardown(t *testing.T) {
	t.Setenv("FLEET_PROCESS_CLOSED_GRACE_MS", "200")
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	sec := e.registerHost(t, "h-bg")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','bg-proj','config/projects/bg-proj/project.json',1,'bg-proj');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','bg-proj',1,'{"settings":{"id":"bg-proj","name":"BG Proj","contextFiles":[]},"contextFiles":[]}','b1','editor','person','t');
INSERT INTO task(id,title,project_id,state) VALUES
 ('tb-wait','Waiting stuck','bg-proj','in_progress'),
 ('tb-live','Live control','bg-proj','in_progress');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('sb-wait','bg-proj','tb-wait','h-bg','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}'),
 ('sb-live','bg-proj','tb-live','h-bg','{"agentConfig":{"id":"claude-worker","harness":"claude","model":"sonnet"}}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,harness,model,started_at,last_seen_at,data) VALUES
 ('eb-wait','agent_run','waiting','confirmed','bg-proj','tb-wait','sb-wait','h-bg','claude','sonnet',now()-interval '5 minutes',now()-interval '4 minutes','{"id":"eb-wait","sessionId":"sb-wait","hostId":"h-bg","taskId":"tb-wait","waitReason":"question"}'),
 ('eb-live','agent_run','running','confirmed','bg-proj','tb-live','sb-live','h-bg','claude','sonnet',now()-interval '2 minutes',now()-interval '10 seconds','{"id":"eb-live","sessionId":"sb-live","hostId":"h-bg","taskId":"tb-live"}');
INSERT INTO event(id,execution_id,session_id,project_id,event_seq,type,payload,updated_at) VALUES
 ('evb-pc','eb-wait','sb-wait','bg-proj',1,'process.closed','{"type":"process.closed","data":{"exitCode":0}}',now()-interval '2 minutes');
`)
	time.Sleep(300 * time.Millisecond)
	code, body := e.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
		HostId:           "h-bg",
		Platform:         contract.Platform("darwin"),
		Capabilities:     []string{"claude"},
		Stores:           []contract.StoreReport{},
		Problems:         []contract.EdgeProblem{},
		ProjectRoots:     []string{},
		GitConnections:   []string{},
		ActiveExecutions: []string{"eb-live"},
	}, hostHdr)
	if code != 200 {
		t.Fatalf("poll = %d %v", code, body)
	}
	if err := reconcileLostExecutions(e.ctx, e.app); err != nil {
		t.Fatalf("reconcileLostExecutions: %v", err)
	}
	code, stWait := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "eb-wait"}, owner)
	if code != 200 || stWait["state"] != "failed" || stWait["observation"] != "unconfirmed" {
		t.Fatalf("eb-wait = %d %v, want failed/unconfirmed", code, stWait)
	}
	code, sc := e.callJSON(t, "POST", "/api/ops/setting_changes", doc{"target": "execution:eb-wait"}, owner)
	if code != 200 || len(anys(sc["changes"])) != 1 || mapOf(anys(sc["changes"])[0])["actor"] != "fleet" {
		t.Fatalf("eb-wait audit = %d %v, want 1 row by fleet", code, sc)
	}
	code, stLive := e.callJSON(t, "POST", "/api/ops/execution_status", contract.ExecutionStatusRequest{ExecutionId: "eb-live"}, owner)
	if code != 200 || stLive["state"] != "running" || stLive["observation"] != "confirmed" {
		t.Fatalf("eb-live = %d %v, want running/confirmed", code, stLive)
	}
}
