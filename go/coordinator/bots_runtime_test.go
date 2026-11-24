package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

func testBotSettings(profileID string) contract.BotSettings {
	return contract.BotSettings{
		Name:           "HAL0",
		Purpose:        "Watch project health and Needs you",
		ProfileId:      profileID,
		Instructions:   "Inspect open questions and failed runs first.",
		Soul:           "Report only confirmed evidence.",
		ScheduleTime:   "",
		ScheduleZone:   "",
		WakeOnNeedsYou: false,
	}
}

func testRegisterHostFor(t *testing.T, e *testCoordEnv, hostID, holder string) string {
	t.Helper()
	sec := e.registerHost(t, hostID)
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		UPDATE host SET holder = '`+holder+`' WHERE id = 'host:`+hostID+`';
	`)
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return nil }); err != nil {
		t.Fatalf("invalidate auth cache: %v", err)
	}
	return sec
}

func testPollForStart(t *testing.T, e *testCoordEnv, hostID, hostSecret string) contract.PollReply {
	t.Helper()
	pollBody := contract.PollRequest{
		HostId:       hostID,
		Platform:     contract.Platform("darwin"),
		Capabilities: []string{"codex", "claude", string(contract.CapabilityStartAccessV1), string(contract.CapabilityWorkspaceKeyV1)},
		Stores:       []contract.StoreReport{},
		Problems:     []contract.EdgeProblem{},
	}
	hdr := map[string]string{"Authorization": "Bearer " + hostSecret}
	// First poll ensures the host observation is online so dispatchPass ranks it as a candidate.
	code, raw := e.callJSON(t, "POST", "/api/hosts/poll", pollBody, hdr)
	if code != 200 {
		t.Fatalf("hosts/poll warmup = %d %v", code, raw)
	}
	b, _ := json.Marshal(raw)
	var rep contract.PollReply
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("decode poll reply: %v", err)
	}
	if len(rep.Commands) > 0 {
		return rep
	}
	if err := e.app.dispatchPass(e.ctx, true); err != nil {
		t.Fatalf("dispatchPass: %v", err)
	}
	code, raw = e.callJSON(t, "POST", "/api/hosts/poll", pollBody, hdr)
	if code != 200 {
		t.Fatalf("hosts/poll = %d %v", code, raw)
	}
	b, _ = json.Marshal(raw)
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("decode poll reply: %v", err)
	}
	return rep
}

// Requirement: fleet.md Persistent bots: one stable Home conversation in Conversations (unattached to a single task)
// with standard input queue and sender provenance, single active bot run admission, pinned settings/memory/project per
// turn, native session continuation when compatible vs retained handoff when incompatible, and quiet wake detection.
// Risk: concurrent wakes launch duplicate active runs, attaching Home to a task breaks multi-task continuity, profile
// or host changes reuse incompatible native sessions without handoff, or quiet wakes are misclassified.
func TestBotRuntimeHomeConversationAndAdmission(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	outsider := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "outsider@trial.test"}
	secA := testRegisterHostFor(t, e, "h-bot-a", "member@trial.test")
	secB := testRegisterHostFor(t, e, "h-bot-b", "member@trial.test")
	hostHdrA := map[string]string{"Authorization": "Bearer " + secA}
	hostHdrB := map[string]string{"Authorization": "Bearer " + secB}

	// Register a task in desiign to test task-attach refusal and later mutation tracking.
	code, taskResp := e.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId":       "desiign",
		"title":           "Existing project task",
		"expectedOutcome": "Done",
	}, member)
	if code != 200 {
		t.Fatalf("task_create = %d %v", code, taskResp)
	}
	taskID := astr(taskResp["id"])

	botID := newID()
	settings := testBotSettings("pm")
	code, saveResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId:        botID,
		ProjectId:    "desiign",
		BaseRevision: 0,
		Settings:     settings,
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save create = %d %v", code, saveResp)
	}
	botMap := mapOf(saveResp["bot"])
	homeSID := astr(botMap["homeSessionId"])
	if homeSID == "" {
		t.Fatalf("expected stable homeSessionId on bot create, got %v", botMap)
	}

	// Home conversation appears in Conversations (sessions_list), has no taskId, and refuses session_task_attach.
	code, sessList := e.callJSON(t, "POST", "/api/ops/sessions_list", doc{"filter": doc{"projectId": []string{"desiign"}}}, member)
	if code != 200 {
		t.Fatalf("sessions_list = %d %v", code, sessList)
	}
	foundHome := false
	for _, item := range anys(sessList["sessions"]) {
		if astr(mapOf(item)["id"]) == homeSID {
			foundHome = true
		}
	}
	if !foundHome {
		t.Fatalf("Home conversation %s missing from sessions_list: %v", homeSID, sessList["sessions"])
	}
	code, attachErr := e.callJSON(t, "POST", "/api/ops/session_task_attach", doc{"sessionId": homeSID, "taskId": taskID}, member)
	if code != 409 {
		t.Fatalf("session_task_attach on Home session = %d %v, want 409", code, attachErr)
	}

	// Save project-scoped memory revision 1.
	code, memResp := e.callJSON(t, "POST", "/api/ops/bot_memory_save", contract.BotMemorySaveRequest{
		BotId: botID, BaseRevision: 0, Memory: "Remember: check release notes.",
	}, member)
	if code != 200 {
		t.Fatalf("bot_memory_save = %d %v", code, memResp)
	}

	// 1. Trigger manual bot_run -> admitted as queued; second concurrent bot_run is skipped (single active run admission).
	code, run1Resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-1"), Text: ptr("Check morning status"),
	}, member)
	if code != 200 || run1Resp["admitted"] != true {
		t.Fatalf("bot_run 1 = %d %v", code, run1Resp)
	}
	run1 := mapOf(run1Resp["run"])
	exec1ID := astr(run1["executionId"])
	if astr(run1["state"]) != "queued" || num(run1["botRevision"]) != 1 || num(run1["memoryRevision"]) != 1 || exec1ID == "" {
		t.Fatalf("unexpected run1 metadata: %v", run1)
	}

	// Idempotent replay with same requestId returns same run; different text returns 409.
	code, replayResp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-1"), Text: ptr("Check morning status"),
	}, member)
	if code != 200 || replayResp["admitted"] != false || astr(mapOf(replayResp["run"])["id"]) != astr(run1["id"]) {
		t.Fatalf("bot_run replay = %d %v", code, replayResp)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-1"), Text: ptr("Conflicting text"),
	}, member); code != 409 {
		t.Fatalf("bot_run conflict = %d, want 409", code)
	}

	// Second distinct bot_run while run1 is active must NOT start another active run (recorded as skipped).
	code, runSkipResp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-2"), Text: ptr("Second concurrent wake"),
	}, member)
	if code != 200 || runSkipResp["admitted"] != false {
		t.Fatalf("bot_run 2 while active = %d %v", code, runSkipResp)
	}
	if astr(mapOf(runSkipResp["run"])["state"]) != "skipped" || astr(mapOf(runSkipResp["run"])["reason"]) != "active_run" {
		t.Fatalf("expected skipped active_run, got %v", runSkipResp["run"])
	}

	// Direct human message_send into Home conversation queues in standard input queue with sender provenance.
	code, msgResp := e.callJSON(t, "POST", "/api/ops/message_send", doc{
		"sessionId": homeSID,
		"requestId": "human-msg-1",
		"text":      "Please open a follow-up question",
		"sentBy":    "spoofed@evil.test",
		"source":    "spoofed",
	}, owner)
	if code != 200 || msgResp["queued"] != true || num(msgResp["position"]) != 1 {
		t.Fatalf("queued message_send = %d %v", code, msgResp)
	}
	if astr(msgResp["sentBy"]) != testOwnerEmail {
		t.Fatalf("expected server-derived sentBy %s, got %v", testOwnerEmail, msgResp["sentBy"])
	}

	// Outsider cannot inspect bot_runs.
	if code, _ := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{BotId: &botID}, outsider); code != 404 {
		t.Fatalf("outsider bot_runs = %d, want 404", code)
	}

	// Poll host A -> picks up exec1ID with pinned bot instructions, SOUL, and project memory.
	poll1 := testPollForStart(t, e, "h-bot-a", secA)
	if len(poll1.Commands) != 1 || poll1.Commands[0].Start == nil || poll1.Commands[0].Start.ExecutionId != exec1ID {
		t.Fatalf("expected start command for %s, got %+v", exec1ID, poll1.Commands)
	}
	start1 := poll1.Commands[0].Start.Payload
	if start1 == nil || !strings.Contains(deref(start1.Instructions), "Inspect open questions") ||
		!strings.Contains(deref(start1.Instructions), "Report only confirmed evidence") ||
		!strings.Contains(deref(start1.Instructions), "Remember: check release notes") {
		t.Fatalf("pinned bot instructions/soul/memory missing from StartPayload: %+v", start1)
	}

	// Host reports started -> completed with nativeSessionId "ns-home-1", and no mutations -> quiet wake!
	cmd1ID := poll1.Commands[0].Start.Id
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd1ID, ExecutionId: exec1ID, Status: contract.ReceiptStatusStarted, NativeSessionId: ptr("ns-home-1"),
	}, hostHdrA)
	if code != 200 {
		t.Fatalf("started receipt = %d", code)
	}
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd1ID, ExecutionId: exec1ID, Status: contract.ReceiptStatusCompleted, NativeSessionId: ptr("ns-home-1"),
	}, hostHdrA)
	if code != 200 {
		t.Fatalf("completed receipt = %d", code)
	}

	// Run 1 should now be succeeded & quiet=true; queued human message should have been promoted!
	code, runsAfter1 := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{BotId: &botID, Quiet: ptr(true)}, member)
	if code != 200 || len(anys(runsAfter1["runs"])) != 1 || astr(mapOf(anys(runsAfter1["runs"])[0])["id"]) != astr(run1["id"]) {
		t.Fatalf("expected run1 as quiet wake, got %d %v", code, runsAfter1)
	}

	// Promoted human message runs on host A and continues nativeSessionId "ns-home-1" because project/host/profile match!
	poll2 := testPollForStart(t, e, "h-bot-a", secA)
	if len(poll2.Commands) != 1 || poll2.Commands[0].Start == nil || poll2.Commands[0].Start.Payload == nil || deref(poll2.Commands[0].Start.Payload.NativeSessionId) != "ns-home-1" {
		t.Fatalf("expected promoted run on compatible host to continue ns-home-1, got %+v", poll2.Commands)
	}
	exec2ID := poll2.Commands[0].Start.ExecutionId
	cmd2ID := poll2.Commands[0].Start.Id
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd2ID, ExecutionId: exec2ID, Status: contract.ReceiptStatusStarted,
	}, hostHdrA)
	if code != 200 {
		t.Fatalf("started receipt 2 = %d", code)
	}
	// Simulate execution creating a question (non-quiet mutation) before completing.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO question(id,kind,project_id,execution_id,session_id,text,options,asked_by,state)
		VALUES ('q-by-bot','agent_question','desiign','`+exec2ID+`','`+homeSID+`','Confirm release?','["yes","no"]','execution:`+exec2ID+`','open');
	`)
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd2ID, ExecutionId: exec2ID, Status: contract.ReceiptStatusCompleted,
	}, hostHdrA)
	if code != 200 {
		t.Fatalf("completed receipt 2 = %d", code)
	}

	// Verify run2 has quiet=false and sentBy=owner@trial.test.
	code, runsMsg := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseMessage),
	}, member)
	if code != 200 || len(anys(runsMsg["runs"])) != 1 {
		t.Fatalf("message bot_runs = %d %v", code, runsMsg)
	}
	run2 := mapOf(anys(runsMsg["runs"])[0])
	if run2["quiet"] != false || astr(run2["sentBy"]) != testOwnerEmail || astr(run2["state"]) != "succeeded" {
		t.Fatalf("unexpected run2: %v", run2)
	}

	// Change profileId (pm -> coding-worker, same harness): restart_and_resume preserves ns-home-1 while updating profile.
	settings.ProfileId = "coding-worker"
	code, _ = e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 1, Settings: settings,
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save profile change = %d", code)
	}
	code, run3Resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-3"), Text: ptr("Run with new profile"),
	}, member)
	if code != 200 || run3Resp["admitted"] != true {
		t.Fatalf("bot_run 3 = %d %v", code, run3Resp)
	}
	poll3 := testPollForStart(t, e, "h-bot-a", secA)
	if len(poll3.Commands) != 1 || poll3.Commands[0].Start == nil || poll3.Commands[0].Start.Payload == nil {
		t.Fatalf("expected start command 3, got %+v", poll3.Commands)
	}
	start3 := poll3.Commands[0].Start.Payload
	if deref(start3.NativeSessionId) != "ns-home-1" {
		t.Fatalf("expected same-harness profile change to keep nativeSessionId ns-home-1, got %q", deref(start3.NativeSessionId))
	}
	// Complete run 3 on host A with native session ns-home-2, then dispatch run 4 to host B -> host mismatch triggers handoff!
	exec3ID := poll3.Commands[0].Start.ExecutionId
	cmd3ID := poll3.Commands[0].Start.Id
	if code, resp := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd3ID, ExecutionId: exec3ID, Status: contract.ReceiptStatusStarted,
	}, hostHdrA); code != 200 {
		t.Fatalf("started receipt 3 = %d %v", code, resp)
	}
	if code, resp := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-a", CommandId: cmd3ID, ExecutionId: exec3ID, Status: contract.ReceiptStatusCompleted, NativeSessionId: ptr("ns-home-1"),
	}, hostHdrA); code != 200 {
		t.Fatalf("completed receipt 3 = %d %v", code, resp)
	}
	// Pause host A so run 4 dispatches to host B!
	if code, resp := e.callJSON(t, "POST", "/api/ops/hosts_update", doc{"id": "h-bot-a", "intake": "paused"}, owner); code != 200 {
		t.Fatalf("hosts_update = %d %v", code, resp)
	}
	code, run4Resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("req-4"), Text: ptr("Run on host B"),
	}, member)
	if code != 200 || run4Resp["admitted"] != true {
		t.Fatalf("bot_run 4 = %d %v", code, run4Resp)
	}
	poll4 := testPollForStart(t, e, "h-bot-b", secB)
	if len(poll4.Commands) != 1 || poll4.Commands[0].Start == nil || poll4.Commands[0].Start.Payload == nil {
		t.Fatalf("expected start command 4 on host B, got %+v", poll4.Commands)
	}
	if deref(poll4.Commands[0].Start.Payload.NativeSessionId) != "" || !strings.Contains(deref(poll4.Commands[0].Start.Payload.Text), "Fleet context handoff") {
		t.Fatalf("expected host change to clear nativeSessionId and attach retained handoff, got %+v", poll4.Commands[0].Start.Payload)
	}
	_, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-b", CommandId: poll4.Commands[0].Start.Id, ExecutionId: poll4.Commands[0].Start.ExecutionId, Status: contract.ReceiptStatusCompleted,
	}, hostHdrB)
}

// Requirement: fleet.md Persistent bots: daily scheduled wakes (schedule_time, schedule_zone) and wake_on_needs_you
// triggers persist wake state & deduplication across coordinator restarts and exclude self-originated items.
// Risk: restart replays duplicate schedule/Needs-you wakes, self-originated questions/problems trigger infinite loops,
// or other projects' Needs-you items wake a scoped bot.
func TestBotRuntimeScheduleNeedsYouDedupAndSelfExclusion(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	_ = e.registerHost(t, "h-sched")

	now := time.Now().UTC().Truncate(time.Minute)
	botID := newID()
	settings := testBotSettings("pm")
	settings.ScheduleTime = now.Format("15:04")
	settings.ScheduleZone = "Etc/UTC"
	settings.WakeOnNeedsYou = true

	code, saveResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: settings,
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, saveResp)
	}
	homeSID := astr(mapOf(saveResp["bot"])["homeSessionId"])

	// 1. Trigger scheduled daily wake at `now`.
	if err := botTriggerPass(e.ctx, e.app, now); err != nil {
		t.Fatalf("botTriggerPass schedule: %v", err)
	}
	code, runs := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseSchedule),
	}, member)
	if code != 200 || len(anys(runs["runs"])) != 1 {
		t.Fatalf("expected 1 scheduled run, got %d %v", code, runs)
	}
	schedRun := mapOf(anys(runs["runs"])[0])
	schedExecID := astr(schedRun["executionId"])

	// Simulate coordinator restart (fresh App on same DB) + re-running trigger pass -> must NOT duplicate scheduled wake.
	restartedApp := &App{store: e.appStore, browserSecret: e.browserSecret, hostID: "test-coord"}
	restartedApp.ensureRuntime()
	restartedApp.people.Store(e.app.people.Load())
	if err := botTriggerPass(e.ctx, restartedApp, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("botTriggerPass after restart: %v", err)
	}
	code, runsAfterRestart := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseSchedule),
	}, member)
	if code != 200 || len(anys(runsAfterRestart["runs"])) != 1 {
		t.Fatalf("expected still 1 scheduled run after restart, got %d %v", code, runsAfterRestart)
	}

	// Finish the scheduled execution so bot is idle for Needs-you tests.
	if err := e.app.store.Write(e.ctx, nil, []string{"execution:" + schedExecID}, func(q *Request) error {
		_, err := executionTransition(q, schedExecID, executionEvent{To: "succeeded"})
		return err
	}); err != nil {
		t.Fatalf("finish schedExec: %v", err)
	}

	// 2. Insert self-originated question & execution problem, foreign project question, and foreign in-project Needs-you items.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO session(id,project_id,created_by_session_id,payload) VALUES
		 ('s-bot-child','desiign','`+homeSID+`','{}'),
		 ('s-foreign-proj','specq',NULL,'{}'),
		 ('s-foreign-same','desiign',NULL,'{}');
		INSERT INTO execution(id,kind,state,project_id,session_id,problem,data) VALUES
		 ('e-self-prob','agent_run','failed','desiign','`+homeSID+`','self-origin failure','{"id":"e-self-prob"}'),
		 ('e-child-prob','agent_run','failed','desiign','s-bot-child','child failure','{"id":"e-child-prob"}'),
		 ('e-foreign-prob','agent_run','failed','desiign','s-foreign-same','foreign worker crashed','{"id":"e-foreign-prob"}');
		INSERT INTO question(id,kind,project_id,execution_id,session_id,text,options,asked_by,state) VALUES
		 ('q-self-ask','agent_question','desiign','`+schedExecID+`','`+homeSID+`','Self question?','["ok"]','execution:`+schedExecID+`','open'),
		 ('q-other-proj','agent_question','specq',NULL,'s-foreign-proj','Other project question?','["ok"]','owner@trial.test','open'),
		 ('q-foreign-ny','operator_decision','desiign',NULL,'s-foreign-same','Approve foreign deploy?','["yes","no"]','owner@trial.test','open');
	`)

	if err := botTriggerPass(e.ctx, restartedApp, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("botTriggerPass needs_you: %v", err)
	}
	code, nyRuns := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseNeedsYou),
	}, owner)
	if code != 200 || len(anys(nyRuns["runs"])) != 1 {
		t.Fatalf("expected exactly 1 needs_you run, got %d %v", code, nyRuns)
	}
	nyRun := mapOf(anys(nyRuns["runs"])[0])
	triggers := anys(nyRun["triggers"])
	if len(triggers) != 2 {
		t.Fatalf("expected 2 foreign in-project triggers (q-foreign-ny and e-foreign-prob), got %v", triggers)
	}
	for _, tr := range triggers {
		id := astr(mapOf(tr)["id"])
		if id == "q-self-ask" || id == "e-self-prob" || id == "e-child-prob" || id == "q-other-proj" {
			t.Fatalf("self-origin or out-of-project item leaked into Needs you wake: %v", tr)
		}
	}

	// Even if change cursor is rewound (e.g. cursor loss / resync after restart), dedup_key prevents duplicate wake.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		UPDATE bot_wake_cursor SET needs_you_change_seq = 0 WHERE bot_id = '`+botID+`';
	`)
	nyExecID := astr(nyRun["executionId"])
	if err := e.app.store.Write(e.ctx, nil, []string{"execution:" + nyExecID}, func(q *Request) error {
		_, err := executionTransition(q, nyExecID, executionEvent{To: "succeeded"})
		return err
	}); err != nil {
		t.Fatalf("finish nyExec: %v", err)
	}
	if err := botTriggerPass(e.ctx, restartedApp, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("botTriggerPass after cursor rewind: %v", err)
	}
	code, nyRunsAfter := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseNeedsYou),
	}, owner)
	if code != 200 || len(anys(nyRunsAfter["runs"])) != 1 {
		t.Fatalf("expected dedup to prevent duplicate needs_you wake after cursor rewind, got %d %v", code, nyRunsAfter)
	}
}

// Requirement: fleet.md Persistent bots: pause blocks future wakes, stop requests cancellation through execution
// lifecycle and reports observed outcome (proven vs unconfirmed), authority is rechecked before launching queued work,
// and bot_runs keeps quiet, skipped, queued, failed and unconfirmed work inspectable.
// Risk: paused bots wake on schedules or Needs you, bot_stop reports proven stop without host receipt, revoked
// director executes queued work, or unconfirmed/failed/skipped runs are hidden from Runs.
func TestBotRuntimePauseStopAuthorityAndRunsObservability(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	sec := testRegisterHostFor(t, e, "h-ctrl", "member@trial.test")
	hostHdr := map[string]string{"Authorization": "Bearer " + sec}

	now := time.Now().UTC().Truncate(time.Minute)
	botID := newID()
	settings := testBotSettings("pm")
	settings.ScheduleTime = now.Format("15:04")
	settings.ScheduleZone = "Etc/UTC"
	settings.WakeOnNeedsYou = true

	code, saveResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: settings,
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, saveResp)
	}
	homeSID := astr(mapOf(saveResp["bot"])["homeSessionId"])

	// 1. Pause blocks future scheduled and Needs-you wakes (recorded as skipped reason=paused).
	code, pauseResp := e.callJSON(t, "POST", "/api/ops/bot_pause", contract.BotPauseRequest{
		BotId: botID, Paused: true, Reason: ptr("maintenance"),
	}, member)
	if code != 200 || mapOf(pauseResp["bot"])["paused"] != true {
		t.Fatalf("bot_pause = %d %v", code, pauseResp)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO question(id,kind,project_id,text,options,asked_by,state)
		VALUES ('q-while-paused','operator_decision','desiign','Paused wake check?','["ok"]','owner@trial.test','open');
	`)
	if err := botTriggerPass(e.ctx, e.app, now); err != nil {
		t.Fatalf("botTriggerPass while paused: %v", err)
	}
	code, skippedRuns := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, State: ptr(contract.BotRunStateSkipped),
	}, member)
	if code != 200 || len(anys(skippedRuns["runs"])) != 2 {
		t.Fatalf("expected 2 skipped runs (schedule + needs_you) while paused, got %d %v", code, skippedRuns)
	}
	for _, r := range anys(skippedRuns["runs"]) {
		if astr(mapOf(r)["reason"]) != "paused" {
			t.Fatalf("expected reason paused, got %v", r)
		}
	}

	// Unpause, then start a manual run, dispatch it to host, and stop it -> unknown/unconfirmed until host confirms!
	code, _ = e.callJSON(t, "POST", "/api/ops/bot_pause", contract.BotPauseRequest{BotId: botID, Paused: false}, member)
	if code != 200 {
		t.Fatalf("bot_pause resume = %d", code)
	}
	code, runResp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("stop-test-1"), Text: ptr("Long running bot work"),
	}, member)
	if code != 200 || runResp["admitted"] != true {
		t.Fatalf("bot_run = %d %v", code, runResp)
	}
	execID := astr(mapOf(runResp["run"])["executionId"])
	poll := testPollForStart(t, e, "h-ctrl", sec)
	if len(poll.Commands) != 1 || poll.Commands[0].Start == nil || poll.Commands[0].Start.ExecutionId != execID {
		t.Fatalf("expected start command for %s, got %+v", execID, poll.Commands)
	}
	cmdStartID := poll.Commands[0].Start.Id
	_, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-ctrl", CommandId: cmdStartID, ExecutionId: execID, Status: contract.ReceiptStatusStarted,
	}, hostHdr)

	// Queue a human message behind the running execution for authority revocation testing below.
	code, qMsgResp := e.callJSON(t, "POST", "/api/ops/message_send", doc{
		"sessionId": homeSID,
		"requestId": "queued-before-revoke",
		"text":      "Should fail when director loses project grant",
	}, member)
	if code != 200 || qMsgResp["queued"] != true {
		t.Fatalf("queued message_send = %d %v", code, qMsgResp)
	}

	// Call bot_stop -> requests stop command; outcome is unknown/unconfirmed before host receipt.
	code, stopResp := e.callJSON(t, "POST", "/api/ops/bot_stop", contract.BotStopRequest{BotId: botID}, member)
	if code != 200 || stopResp["stopped"] != true {
		t.Fatalf("bot_stop = %d %v", code, stopResp)
	}
	stopOutcome := mapOf(stopResp["stop"])
	if astr(stopOutcome["outcome"]) != "unknown" || astr(stopOutcome["detail"]) != "unconfirmed" {
		t.Fatalf("expected unknown/unconfirmed stop outcome before host receipt, got %v", stopOutcome)
	}

	// Host reports stop command status 'unknown' -> bot_runs shows observation == "unconfirmed".
	stopCmdID := astr(mapOf(stopOutcome["stopCommand"])["id"])
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-ctrl", CommandId: stopCmdID, ExecutionId: execID, Status: contract.ReceiptStatusUnknown,
	}, hostHdr)
	if code != 200 {
		t.Fatalf("unknown stop receipt = %d", code)
	}
	code, unconfRuns := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Observation: ptr(contract.BotRunObservationUnconfirmed),
	}, member)
	if code != 200 || len(anys(unconfRuns["runs"])) != 1 {
		t.Fatalf("expected 1 unconfirmed bot run, got %d %v", code, unconfRuns)
	}

	// Revoke member@trial.test's project grant BEFORE host completes stop (which triggers agentPromote on queued msg)!
	snap := e.app.people.Load()
	newByEmail := map[string]*Person{}
	for k, p := range snap.byEmail {
		cp := *p
		if cp.Email == "member@trial.test" {
			cp.Grants = nil
		}
		newByEmail[k] = &cp
	}
	e.app.people.Store(&peopleSnapshot{byEmail: newByEmail})

	// Host confirms execution stopped -> bot_run moves to cancelled, and agentPromote fails the queued message on authority recheck!
	code, _ = e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-ctrl", CommandId: stopCmdID, ExecutionId: execID, Status: contract.ReceiptStatusStopped,
	}, hostHdr)
	if code != 200 {
		t.Fatalf("stopped receipt = %d", code)
	}
	code, cancelledRuns := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, State: ptr(contract.BotRunStateCancelled),
	}, owner)
	if code != 200 || len(anys(cancelledRuns["runs"])) != 1 {
		t.Fatalf("expected 1 cancelled run, got %d %v", code, cancelledRuns)
	}
	code, failedRuns := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, State: ptr(contract.BotRunStateFailed),
	}, owner)
	if code != 200 || len(anys(failedRuns["runs"])) != 1 {
		t.Fatalf("expected 1 failed run for revoked-authority queued message, got %d %v", code, failedRuns)
	}

	// Verify pagination on bot_runs (owner sees all runs across states: skipped, cancelled, failed).
	code, page1 := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Limit: ptr(int64(2)),
	}, owner)
	if code != 200 || len(anys(page1["runs"])) != 2 || page1["hasMore"] != true {
		t.Fatalf("bot_runs page 1 = %d %v", code, page1)
	}
	after := astr(page1["nextAfter"])
	code, page2 := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Limit: ptr(int64(10)), After: &after,
	}, owner)
	if code != 200 || len(anys(page2["runs"])) != 2 || page2["hasMore"] != false {
		t.Fatalf("bot_runs page 2 = %d %v", code, page2)
	}
}
