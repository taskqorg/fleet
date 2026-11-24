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
