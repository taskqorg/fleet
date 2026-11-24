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

func syncTestPersonGrant(t *testing.T, e *testCoordEnv, email, projectID string, grant bool) {
	t.Helper()
	if grant {
		runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
			INSERT INTO person_grant(email,object_kind,object_id) VALUES ('`+email+`','project','`+projectID+`')
			ON CONFLICT DO NOTHING;
		`)
	} else {
		runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
			DELETE FROM person_grant WHERE email = '`+email+`' AND object_id = '`+projectID+`';
		`)
	}
	snap := e.app.people.Load()
	newByEmail := map[string]*Person{}
	for k, p := range snap.byEmail {
		cp := *p
		if cp.Email == email {
			var next []personGrant
			for _, g := range cp.Grants {
				if g.Kind == "project" && g.ID == projectID {
					continue
				}
				next = append(next, g)
			}
			if grant {
				next = append(next, personGrant{Kind: "project", ID: projectID})
			}
			cp.Grants = next
		}
		newByEmail[k] = &cp
	}
	e.app.people.Store(&peopleSnapshot{byEmail: newByEmail})
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return nil }); err != nil {
		t.Fatalf("invalidate auth cache: %v", err)
	}
}

// Requirement: Persistent bots continue the native session only when project, workspace, host, profile, and account match,
// and otherwise clear continuation and create a retained handoff scoped strictly to the execution project (FLT-20 PR 3).
// Risk: Workspace, account, profile-harness, or project changes reuse a stale native session or leak cross-project transcripts.
func TestBotScopedContinuityAndHandoffIsolation(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	outsider := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "outsider@trial.test"}
	secH := testRegisterHostFor(t, e, "h-scope", "member@trial.test")
	hostHdr := map[string]string{"Authorization": "Bearer " + secH}

	// Seed a second project proj-b and grant member access to it as well.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO definition(kind,id,path,head,project_id) VALUES
		 ('project','proj-b','config/projects/proj-b/project.json',1,'proj-b');
		INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
		 ('project','proj-b',1,'{"settings":{"id":"proj-b","name":"Project B","contextFiles":[]},"contextFiles":[]}','pb1','editor','person','t');
	`)
	syncTestPersonGrant(t, e, "member@trial.test", "proj-b", true)

	settings := testBotSettings("pm")
	botID := newID()
	code, createResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: settings,
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save create = %d %v", code, createResp)
	}
	homeSID := astr(mapOf(createResp["bot"])["homeSessionId"])

	// Helper to drive one manual bot run with a native session receipt and custom account/workingDir assertion.
	runTurn := func(reqID, text, nsSet string) (string, string, *contract.StartPayload) {
		t.Helper()
		c, resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
			BotId: botID, RequestId: &reqID, Text: &text,
		}, member)
		if c != 200 || resp["admitted"] != true {
			t.Fatalf("bot_run %s = %d %v", reqID, c, resp)
		}
		poll := testPollForStart(t, e, "h-scope", secH)
		if len(poll.Commands) != 1 || poll.Commands[0].Start == nil || poll.Commands[0].Start.Payload == nil {
			t.Fatalf("expected start command for %s, got %+v", reqID, poll.Commands)
		}
		st := poll.Commands[0].Start
		if c, r := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
			HostId: "h-scope", CommandId: st.Id, ExecutionId: st.ExecutionId, Status: contract.ReceiptStatusStarted,
		}, hostHdr); c != 200 {
			t.Fatalf("started receipt %s = %d %v", reqID, c, r)
		}
		comp := contract.Receipt{
			HostId: "h-scope", CommandId: st.Id, ExecutionId: st.ExecutionId, Status: contract.ReceiptStatusCompleted,
		}
		if nsSet != "" {
			comp.NativeSessionId = &nsSet
		}
		if c, r := e.callJSON(t, "POST", "/api/hosts/receipts", comp, hostHdr); c != 200 {
			t.Fatalf("completed receipt %s = %d %v", reqID, c, r)
		}
		return st.ExecutionId, st.Id, st.Payload
	}

	// 1. Initial run in desiign establishes native session ns-alpha-1.
	_, _, p1 := runTurn("s-1", "SECRET_ALPHA_MARKER check health", "ns-alpha-1")
	if deref(p1.NativeSessionId) != "" {
		t.Fatalf("first run should not have nativeSessionId, got %q", deref(p1.NativeSessionId))
	}

	// 2. Same project, workspace, host, compatible profile (pm -> coding-worker), and account -> continues ns-alpha-1.
	settings.ProfileId = "coding-worker"
	if code, _ := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 1, Settings: settings,
	}, member); code != 200 {
		t.Fatalf("bot_settings_save compatible profile failed")
	}
	_, _, p2 := runTurn("s-2", "Continue in desiign", "ns-alpha-1")
	if deref(p2.NativeSessionId) != "ns-alpha-1" {
		t.Fatalf("expected compatible run to preserve ns-alpha-1, got %q", deref(p2.NativeSessionId))
	}

	// 3. Workspace change (workingDirectory updated via session_profile) -> clears nativeSessionId and attaches desiign handoff.
	if code, resp := e.callJSON(t, "POST", "/api/ops/session_profile", doc{
		"sessionId":        homeSID,
		"profileId":        "coding-worker",
		"workingDirectory": "/tmp/bot-alt-workspace",
	}, member); code != 200 {
		t.Fatalf("session_profile workingDirectory = %d %v", code, resp)
	}
	_, _, p3 := runTurn("s-3", "Work in new checkout", "ns-alpha-2")
	if deref(p3.NativeSessionId) != "" || !strings.Contains(deref(p3.Text), "SECRET_ALPHA_MARKER") {
		t.Fatalf("expected workspace change to reset nativeSessionId and carry desiign handoff, got ns=%q text=%q", deref(p3.NativeSessionId), deref(p3.Text))
	}

	// 4. Account change on the same host (named binding acct-a -> default "") -> resets nativeSessionId + handoff.
	// Mark last execution on homeSID as using account binding acct-a while next dispatch uses default (""), checking checkBotDispatchCompatibility.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		UPDATE execution SET data = jsonb_set(data, '{effectiveAccount}', '{"binding":"acct-a"}'::jsonb, true)
		WHERE id = (SELECT id FROM execution WHERE session_id = '`+homeSID+`' AND host_id IS NOT NULL ORDER BY seq DESC LIMIT 1);
	`)
	_, _, p4 := runTurn("s-4", "Run on default account", "ns-alpha-3")
	if deref(p4.NativeSessionId) != "" || !strings.Contains(deref(p4.Text), "SECRET_ALPHA_MARKER") {
		t.Fatalf("expected account change (acct-a -> default) to clear nativeSessionId and emit scoped handoff, got ns=%q text=%q", deref(p4.NativeSessionId), deref(p4.Text))
	}

	// 5. Cross-harness profile change (coding-worker/codex -> claude-reviewer/claude) -> resets nativeSessionId + emits scoped handoff.
	// Provision a Claude account + ready host observation on h-scope so Claude dispatch succeeds.
	cAcct, acctResp := e.callJSON(t, "POST", "/api/ops/account_create", doc{"name": "member-claude", "harness": "claude"}, member)
	if cAcct != 200 {
		t.Fatalf("account_create claude = %d %v", cAcct, acctResp)
	}
	claudeAcctID := astr(acctResp["id"])
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		_, err := accountReport(q, &contract.AccountObservation{
			AccountId:           claudeAcctID,
			Binding:             "claude-b",
			NativeLoginObserved: ptr(true),
			Platform:            contract.Platform("darwin"),
			Quota:               "ok",
			Readiness:           "ready",
		}, "h-scope")
		return err
	}); err != nil {
		t.Fatalf("accountReport claude = %v", err)
	}
	settings.ProfileId = "claude-reviewer"
	if code, resp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 2, Settings: settings,
	}, member); code != 200 {
		t.Fatalf("bot_settings_save cross-harness = %d %v", code, resp)
	}
	_, _, p5 := runTurn("s-5", "Run on claude profile", "ns-claude-1")
	if deref(p5.NativeSessionId) != "" || !strings.Contains(deref(p5.Text), "SECRET_ALPHA_MARKER") {
		t.Fatalf("expected cross-harness profile change to clear nativeSessionId and emit scoped handoff, got ns=%q text=%q", deref(p5.NativeSessionId), deref(p5.Text))
	}

	// 6. Switch Home session project to proj-b via session_transition -> must clear nativeSessionId and NEVER leak SECRET_ALPHA_MARKER into proj-b.
	if code, resp := e.callJSON(t, "POST", "/api/ops/session_transition", doc{
		"sessionId": homeSID,
		"projectId": "proj-b",
	}, owner); code != 200 {
		t.Fatalf("session_transition proj-b = %d %v", code, resp)
	}
	code, shResp := e.callJSON(t, "POST", "/api/ops/session_handoffs", doc{"sessionId": homeSID}, member)
	if code != 200 {
		t.Fatalf("session_handoffs = %d %v", code, shResp)
	}
	for _, hRaw := range anys(shResp["handoffs"]) {
		hm := mapOf(hRaw)
		if astr(hm["projectId"]) == "proj-b" && strings.Contains(astr(hm["text"]), "SECRET_ALPHA_MARKER") {
			t.Fatalf("proj-b handoff leaked SECRET_ALPHA_MARKER: %v", hm)
		}
	}

	// Multi-project session_get must filter nativeSessions & workspaceSnapshots by caller's project grants!
	// Revoke member's grant to desiign (keeping proj-b) and verify member cannot see desiign snapshots/nativeSessions or desiign handoffs.
	syncTestPersonGrant(t, e, "member@trial.test", "desiign", false)
	code, sgMember := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": homeSID}, member)
	if code != 200 {
		t.Fatalf("session_get member (proj-b access only) = %d %v", code, sgMember)
	}
	sessMap := mapOf(sgMember["session"])
	for k := range mapOf(sessMap["nativeSessions"]) {
		if strings.Contains(k, ":desiign:") {
			t.Fatalf("session_get leaked desiign nativeSessions key %q to member", k)
		}
	}
	for k := range mapOf(sessMap["workspaceSnapshots"]) {
		if strings.Contains(k, ":desiign:") {
			t.Fatalf("session_get leaked desiign workspaceSnapshots key %q to member", k)
		}
	}
	code, shFiltered := e.callJSON(t, "POST", "/api/ops/session_handoffs", doc{"sessionId": homeSID}, member)
	if code != 200 {
		t.Fatalf("session_handoffs filtered = %d %v", code, shFiltered)
	}
	for _, hRaw := range anys(shFiltered["handoffs"]) {
		if astr(mapOf(hRaw)["projectId"]) == "desiign" {
			t.Fatalf("session_handoffs leaked desiign handoff to revoked member: %v", hRaw)
		}
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": homeSID}, outsider); code != 404 {
		t.Fatalf("outsider expected 404 on session_get, got %d", code)
	}
}

// Requirement: Queued bot executions and live bot tool calls recheck director authority, project access, and profile trust
// before dispatch launch and tool execution (FLT-20 PR 3).
// Risk: A queued bot_run queued before revocation launches once a host polls, or an active bot execution executes tools
// after director project access is revoked in the database.
func TestBotQueuedWorkAuthorityRecheck(t *testing.T) {
	e := openTestCoordinator(t, false)
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	owner := e.ownerHeaders()

	botID := newID()
	code, createResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, createResp)
	}

	// Queue a bot run while NO host is registered so the execution remains queued in dispatch.
	code, qResp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("q-auth-1"), Text: ptr("Queued before revocation"),
	}, member)
	if code != 200 || qResp["admitted"] != true {
		t.Fatalf("bot_run queued = %d %v", code, qResp)
	}
	runID := astr(mapOf(qResp["run"])["id"])
	execID := astr(mapOf(qResp["run"])["executionId"])
	if execID == "" {
		t.Fatalf("expected queued executionId on run %v", qResp["run"])
	}

	// Now register a host, THEN revoke member@trial.test's desiign project grant in DB AND snapshot before dispatchPass runs!
	secH := testRegisterHostFor(t, e, "h-qauth", "owner@trial.test")
	syncTestPersonGrant(t, e, "member@trial.test", "desiign", false)

	poll := testPollForStart(t, e, "h-qauth", secH)
	if len(poll.Commands) != 0 {
		t.Fatalf("expected queued bot run NOT to launch after director revocation, got commands: %+v", poll.Commands)
	}
	code, runsAfter := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{BotId: &botID}, owner)
	if code != 200 || len(anys(runsAfter["runs"])) != 1 {
		t.Fatalf("bot_runs after queue refusal = %d %v", code, runsAfter)
	}
	r := mapOf(anys(runsAfter["runs"])[0])
	if astr(r["id"]) != runID || astr(r["state"]) != "cancelled" {
		t.Fatalf("expected queued bot run to cancel on authority recheck, got %v", r)
	}

	// Also verify agent-tools refuses a running bot execution when its director's grant is revoked in DB (even without snapshot reload).
	if code, _ := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: newID(), ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, owner); code != 200 {
		t.Fatalf("create owner bot failed")
	}
	// Restore member grant, launch bot directed by member, then revoke in SQL only.
	syncTestPersonGrant(t, e, "member@trial.test", "desiign", true)
	secM := testRegisterHostFor(t, e, "h-tool-auth", "member@trial.test")
	code, rLive := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("q-auth-2"), Text: ptr("Live run for tool auth"),
	}, member)
	if code != 200 || rLive["admitted"] != true {
		t.Fatalf("bot_run live = %d %v", code, rLive)
	}
	pollLive := testPollForStart(t, e, "h-tool-auth", secM)
	if len(pollLive.Commands) != 1 || pollLive.Commands[0].Start == nil {
		t.Fatalf("expected start command for live run, got %+v", pollLive.Commands)
	}
	stLive := pollLive.Commands[0].Start
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tool-auth", CommandId: stLive.Id, ExecutionId: stLive.ExecutionId, Status: contract.ReceiptStatusStarted,
	}, map[string]string{"Authorization": "Bearer " + secM}); c != 200 {
		t.Fatalf("started receipt = %d", c)
	}

	// Revoke member's DB person_grant directly without invalidating in-memory snapshot -> agent-tools MUST fail 403.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		DELETE FROM person_grant WHERE email = 'member@trial.test' AND object_id = 'desiign';
	`)
	code, toolErr := e.callJSON(t, "POST", "/api/hosts/agent-tools", contract.AgentToolsRequest{
		HostId:      "h-tool-auth",
		CommandId:   stLive.Id,
		ExecutionId: &stLive.ExecutionId,
		Request:     json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"task_list","arguments":{}}}`),
	}, map[string]string{"Authorization": "Bearer " + secM})
	if code != 403 {
		t.Fatalf("expected agent-tools after DB grant revocation to return 403, got %d %v", code, toolErr)
	}
}

// Requirement: Historical bot context (bot_get, bot_runs, bot_tasks, session_get, session_handoffs, execution events)
// and usage/spend metrics (analytics_query, spend_summary) recheck live database authority on every read (FLT-20 PR 3).
// Risk: Cached auth or HTTP read handlers return historical bot transcripts, events, or spend data after project access is revoked.
func TestBotHistoricalContextAndUsageAuthorityRecheck(t *testing.T) {
	e := openTestCoordinator(t, false)
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	secH := testRegisterHostFor(t, e, "h-hist", "member@trial.test")
	hostHdr := map[string]string{"Authorization": "Bearer " + secH}

	botID := newID()
	code, createResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, createResp)
	}
	homeSID := astr(mapOf(createResp["bot"])["homeSessionId"])

	code, runResp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("hist-1"), Text: ptr("Record secret historical context"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_run = %d %v", code, runResp)
	}
	poll := testPollForStart(t, e, "h-hist", secH)
	if len(poll.Commands) != 1 || poll.Commands[0].Start == nil {
		t.Fatalf("expected start command, got %+v", poll.Commands)
	}
	st := poll.Commands[0].Start
	execID := st.ExecutionId
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-hist", CommandId: st.Id, ExecutionId: execID, Status: contract.ReceiptStatusStarted,
	}, hostHdr); c != 200 {
		t.Fatalf("started receipt = %d", c)
	}
	pcData, _ := json.Marshal(contract.ProcessClosed{ExitCode: ptr(int64(0)), StdoutClosed: true})
	if c, evErr := e.callJSON(t, "POST", "/api/hosts/events", contract.HostEvents{
		HostId: "h-hist", ExecutionId: execID,
		Events: []contract.HostEvent{{
			Id:        "ev-hist-1",
			Seq:       1,
			Timestamp: nowMs(),
			Type:      string(contract.FleetEventTypeProcessClosed),
			Data:      pcData,
		}},
	}, hostHdr); c != 200 {
		t.Fatalf("hosts/events = %d %v", c, evErr)
	}
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-hist", CommandId: st.Id, ExecutionId: execID, Status: contract.ReceiptStatusCompleted,
	}, hostHdr); c != 200 {
		t.Fatalf("completed receipt = %d", c)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO execution_totals(execution_id,project_id,model,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,wall_ms,wall_ms_basis)
		VALUES ('`+execID+`','desiign','gpt-6.2-sol',1200,'measured',300,'measured',5000,'measured')
		ON CONFLICT (execution_id) DO UPDATE SET
			model = excluded.model,
			tokens_input = excluded.tokens_input,
			tokens_input_basis = excluded.tokens_input_basis,
			tokens_output = excluded.tokens_output,
			tokens_output_basis = excluded.tokens_output_basis,
			wall_ms = excluded.wall_ms,
			wall_ms_basis = excluded.wall_ms_basis;
		UPDATE execution SET model = 'gpt-6.2-sol' WHERE id = '`+execID+`';
	`)

	// Before revocation: member can read bot, runs, session, events, analytics_query, and spend_summary.
	for _, check := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/ops/bot_get", contract.BotGetRequest{BotId: botID}},
		{"POST", "/api/ops/bot_runs", contract.BotRunsRequest{BotId: &botID}},
		{"POST", "/api/ops/bot_tasks", contract.BotTasksRequest{BotId: botID}},
		{"POST", "/api/ops/session_get", doc{"sessionId": homeSID}},
		{"POST", "/api/ops/session_handoffs", doc{"sessionId": homeSID}},
		{"GET", "/api/executions/" + execID + "/events", nil},
	} {
		c, raw := e.callJSON(t, check.method, check.path, check.body, member)
		if c != 200 {
			t.Fatalf("pre-revoke %s = %d %v", check.path, c, raw)
		}
	}
	histWin := doc{"from": "2020-01-01T00:00:00Z", "to": "2030-01-01T00:00:00Z"}
	if c, aq := e.callJSON(t, "POST", "/api/ops/analytics_query", doc{
		"window":  histWin,
		"filters": doc{"projectId": []string{"desiign"}},
	}, member); c != 200 || len(anys(aq["rows"])) == 0 {
		t.Fatalf("pre-revoke analytics_query = %d %v", c, aq)
	}

	// Revoke member's project grant in DB ONLY (without clearing auth cache or people snapshot).
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		DELETE FROM person_grant WHERE email = 'member@trial.test' AND object_id = 'desiign';
	`)

	// All historical context reads MUST immediately deny (404).
	for _, path := range []struct {
		method, url string
		body        any
	}{
		{"POST", "/api/ops/bot_get", contract.BotGetRequest{BotId: botID}},
		{"POST", "/api/ops/bot_tasks", contract.BotTasksRequest{BotId: botID}},
		{"POST", "/api/ops/session_get", doc{"sessionId": homeSID}},
		{"POST", "/api/ops/session_handoffs", doc{"sessionId": homeSID}},
		{"GET", "/api/executions/" + execID + "/events", nil},
	} {
		c, raw := e.callJSON(t, path.method, path.url, path.body, member)
		if c != 404 {
			t.Fatalf("post-revoke %s expected 404, got %d %v", path.url, c, raw)
		}
	}
	if c, _ := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{BotId: &botID}, member); c != 404 {
		t.Fatalf("post-revoke bot_runs expected 404, got %d", c)
	}
	if c, aq := e.callJSON(t, "POST", "/api/ops/analytics_query", doc{
		"window":  histWin,
		"filters": doc{"sessionId": []string{homeSID}},
	}, member); c != 200 || len(anys(aq["rows"])) != 0 {
		t.Fatalf("post-revoke analytics_query leaked rows: %d %v", c, aq)
	}
	if c, ss := e.callJSON(t, "POST", "/api/ops/spend_summary", doc{
		"by":     "project",
		"window": histWin,
	}, member); c != 200 {
		t.Fatalf("post-revoke spend_summary = %d %v", c, ss)
	} else {
		tot := mapOf(ss["total"])
		if num(tot["tokens"]) != 0 {
			t.Fatalf("post-revoke spend_summary leaked desiign tokens: %v", ss)
		}
	}
}

// Requirement: Managed Fleet tasks created, continued, or explicitly linked from a bot keep independent task histories
// and accept/complete through Fleet's task lifecycle without blocking on or tearing down the Home session (FLT-20 PR 3).
// Risk: Bot-linked tasks merge transcripts with the Home session or block acceptance while the Home session has active work.
func TestBotManagedTaskLinkageAndIndependentHistory(t *testing.T) {
	e := openTestCoordinator(t, false)
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	owner := e.ownerHeaders()
	secH := testRegisterHostFor(t, e, "h-tasks", "member@trial.test")
	hostHdr := map[string]string{"Authorization": "Bearer " + secH}

	// Seed proj-b for cross-project linkage refusal test.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO definition(kind,id,path,head,project_id) VALUES
		 ('project','proj-b','config/projects/proj-b/project.json',1,'proj-b');
		INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
		 ('project','proj-b',1,'{"settings":{"id":"proj-b","name":"Project B","contextFiles":[]},"contextFiles":[]}','pb1','editor','person','t');
	`)
	syncTestPersonGrant(t, e, "member@trial.test", "proj-b", true)

	botID := newID()
	code, createResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_settings_save = %d %v", code, createResp)
	}
	homeSID := astr(mapOf(createResp["bot"])["homeSessionId"])

	// Start Home run 1.
	code, run1Resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("tl-1"), Text: ptr("HOME_ONLY_CONVERSATION_MARKER inspect issue"),
	}, member)
	if code != 200 {
		t.Fatalf("bot_run 1 = %d %v", code, run1Resp)
	}
	run1ID := astr(mapOf(run1Resp["run"])["id"])
	poll1 := testPollForStart(t, e, "h-tasks", secH)
	if len(poll1.Commands) != 1 || poll1.Commands[0].Start == nil {
		t.Fatalf("expected start command 1, got %+v", poll1.Commands)
	}
	st1 := poll1.Commands[0].Start
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tasks", CommandId: st1.Id, ExecutionId: st1.ExecutionId, Status: contract.ReceiptStatusStarted,
	}, hostHdr); c != 200 {
		t.Fatalf("started receipt 1 = %d", c)
	}

	// From inside Home run 1, bot agent creates task1 and starts a dedicated task session!
	callAgent := func(tool string, args doc) (int, map[string]any) {
		t.Helper()
		rpcBody := doc{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": doc{"name": tool, "arguments": args}}
		rawReq, _ := json.Marshal(rpcBody)
		return e.callJSON(t, "POST", "/api/hosts/agent-tools", contract.AgentToolsRequest{
			HostId: "h-tasks", CommandId: st1.Id, ExecutionId: &st1.ExecutionId, Request: rawReq,
		}, hostHdr)
	}
	cTool, tcOut := callAgent("task_create", doc{
		"title":           "Managed fix from bot",
		"expectedOutcome": "Fix verified",
		"held":            false,
	})
	if cTool != 200 {
		t.Fatalf("agent task_create = %d %v", cTool, tcOut)
	}
	tcResult := mapOf(tcOut["result"])
	var tcData doc
	if sc := mapOf(tcResult["structuredContent"]); len(sc) > 0 {
		tcData = sc
	} else {
		for _, item := range anys(tcResult["content"]) {
			_ = json.Unmarshal([]byte(astr(mapOf(item)["text"])), &tcData)
		}
	}
	task1ID := astr(tcData["id"])
	if task1ID == "" {
		t.Fatalf("missing task1 id from agent task_create: %v", tcOut)
	}
	if link := mapOf(tcData["botLink"]); astr(link["botId"]) != botID || astr(link["linkKind"]) != "created" || astr(link["botRunId"]) != run1ID {
		t.Fatalf("expected auto-linked botLink on task_create, got %v", link)
	}
	if astr(tcData["state"]) != "paused" || astr(tcData["pauseReason"]) != "held" {
		t.Fatalf("expected agent-created task to start held, got %v", tcData)
	}
	if cRel, relOut := e.callJSON(t, "POST", "/api/ops/task_update", doc{"taskId": task1ID, "action": "resume"}, member); cRel != 200 {
		t.Fatalf("task_update resume = %d %v", cRel, relOut)
	}

	// Agent starts an independent task session on task1 -> links/retains independent task history!
	cTool, ssOut := callAgent("session_start", doc{
		"projectId": "desiign",
		"taskId":    task1ID,
		"profileId": "codex-designer",
		"requestId": "start-t1-worker",
		"text":      "TASK1_ONLY_WORKER_MARKER",
	})
	if cTool != 200 {
		t.Fatalf("agent session_start on task1 = %d %v", cTool, ssOut)
	}
	ssResult := mapOf(ssOut["result"])
	var ssData doc
	if sc := mapOf(ssResult["structuredContent"]); len(sc) > 0 {
		ssData = sc
	} else {
		for _, item := range anys(ssResult["content"]) {
			_ = json.Unmarshal([]byte(astr(mapOf(item)["text"])), &ssData)
		}
	}
	task1SID := astr(ssData["sessionId"])
	task1ExecID := astr(ssData["executionId"])
	if task1SID == "" || task1SID == homeSID {
		t.Fatalf("expected task1 to get its own session separate from Home session %q, got %q", homeSID, task1SID)
	}

	// Complete Home run 1 so we can also start a second active Home run 2 while task1 moves through acceptance!
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tasks", CommandId: st1.Id, ExecutionId: st1.ExecutionId, Status: contract.ReceiptStatusCompleted,
	}, hostHdr); c != 200 {
		t.Fatalf("completed receipt 1 = %d", c)
	}

	// Poll and start+complete task1's worker execution, leaving a task thread message & delivery readiness.
	pollT := testPollForStart(t, e, "h-tasks", secH)
	if len(pollT.Commands) != 1 || pollT.Commands[0].Start == nil {
		t.Fatalf("expected task1 start command, got %+v", pollT.Commands)
	}
	stT := pollT.Commands[0].Start
	if task1ExecID == "" {
		task1ExecID = stT.ExecutionId
	}
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tasks", CommandId: stT.Id, ExecutionId: stT.ExecutionId, Status: contract.ReceiptStatusStarted,
	}, hostHdr); c != 200 {
		t.Fatalf("task1 started receipt = %d", c)
	}
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tasks", CommandId: stT.Id, ExecutionId: stT.ExecutionId, Status: contract.ReceiptStatusCompleted,
	}, hostHdr); c != 200 {
		t.Fatalf("task1 completed receipt = %d", c)
	}

	// Also test manual task creation + bot_task_link ("managed") + cross-project link refusal.
	c, t2Resp := e.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId":       "desiign",
		"title":           "Manually linked task",
		"expectedOutcome": "Done",
	}, member)
	if c != 200 {
		t.Fatalf("task_create t2 = %d %v", c, t2Resp)
	}
	task2ID := astr(t2Resp["id"])
	c, linkResp := e.callJSON(t, "POST", "/api/ops/bot_task_link", contract.BotTaskLinkRequest{
		BotId:  botID,
		TaskId: task2ID,
	}, member)
	if c != 200 || astr(mapOf(linkResp["link"])["linkKind"]) != "managed" {
		t.Fatalf("bot_task_link managed = %d %v", c, linkResp)
	}

	c, tForeign := e.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId":       "proj-b",
		"title":           "Foreign project task",
		"expectedOutcome": "Done",
	}, owner)
	if c != 200 {
		t.Fatalf("foreign task_create = %d %v", c, tForeign)
	}
	if c, _ := e.callJSON(t, "POST", "/api/ops/bot_task_link", contract.BotTaskLinkRequest{
		BotId:  botID,
		TaskId: astr(tForeign["id"]),
	}, owner); c != 400 {
		t.Fatalf("expected cross-project bot_task_link to return 400, got %d", c)
	}

	// Verify bot_runs linkedTaskIds on run 1 and bot_tasks list and task_get botLink.
	c, runsOut := e.callJSON(t, "POST", "/api/ops/bot_runs", contract.BotRunsRequest{
		BotId: &botID, Cause: ptr(contract.BotRunCauseManual),
	}, member)
	if c != 200 || len(anys(runsOut["runs"])) != 1 {
		t.Fatalf("bot_runs = %d %v", c, runsOut)
	}
	linkedIDs := anys(mapOf(anys(runsOut["runs"])[0])["linkedTaskIds"])
	if len(linkedIDs) != 1 || astr(linkedIDs[0]) != task1ID {
		t.Fatalf("expected run1 linkedTaskIds=[%s], got %v", task1ID, linkedIDs)
	}
	c, btOut := e.callJSON(t, "POST", "/api/ops/bot_tasks", contract.BotTasksRequest{BotId: botID}, member)
	if c != 200 || len(anys(btOut["tasks"])) != 2 {
		t.Fatalf("bot_tasks = %d %v", c, btOut)
	}
	c, tg1 := e.callJSON(t, "POST", "/api/ops/task_get", doc{"taskId": task1ID}, member)
	if c != 200 || astr(mapOf(tg1["botLink"])["botId"]) != botID || astr(mapOf(tg1["botLink"])["linkKind"]) != "created" {
		t.Fatalf("task_get botLink = %d %v", c, tg1)
	}

	// Verify independent conversation histories: thread entries on Home vs task1 never cross over.
	c, sgHome := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": homeSID}, member)
	if c != 200 {
		t.Fatalf("session_get home = %d %v", c, sgHome)
	}
	homeJSON, _ := json.Marshal(sgHome)
	if !strings.Contains(string(homeJSON), "HOME_ONLY_CONVERSATION_MARKER") || strings.Contains(string(homeJSON), "TASK1_ONLY_WORKER_MARKER") {
		t.Fatalf("home session history leaked task1 history: %s", string(homeJSON))
	}
	c, sgT1 := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": task1SID}, member)
	if c != 200 {
		t.Fatalf("session_get task1 = %d %v", c, sgT1)
	}
	t1JSON, _ := json.Marshal(sgT1)
	if !strings.Contains(string(t1JSON), "TASK1_ONLY_WORKER_MARKER") || strings.Contains(string(t1JSON), "HOME_ONLY_CONVERSATION_MARKER") {
		t.Fatalf("task1 session history leaked home history: %s", string(t1JSON))
	}

	// Start Home run 2 (using the queued wake run if already admitted, else manual bot_run) and concurrently accept task1 -> must succeed without touching Home run 2.
	poll2 := testPollForStart(t, e, "h-tasks", secH)
	if len(poll2.Commands) == 0 {
		c, run2Resp := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
			BotId: botID, RequestId: ptr("tl-2"), Text: ptr("Active Home run during task1 acceptance"),
		}, member)
		if c != 200 || run2Resp["admitted"] != true {
			t.Fatalf("bot_run 2 = %d %v", c, run2Resp)
		}
		poll2 = testPollForStart(t, e, "h-tasks", secH)
	}
	if len(poll2.Commands) != 1 || poll2.Commands[0].Start == nil {
		t.Fatalf("expected start command 2, got %+v", poll2.Commands)
	}
	st2 := poll2.Commands[0].Start
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-tasks", CommandId: st2.Id, ExecutionId: st2.ExecutionId, Status: contract.ReceiptStatusStarted,
	}, hostHdr); c != 200 {
		t.Fatalf("started receipt 2 = %d", c)
	}

	// Accept task1 via task_request_acceptance + questions_answer("accepted").
	c, accReq := e.callJSON(t, "POST", "/api/ops/task_request_acceptance", doc{
		"taskId": task1ID, "summary": "Completed by worker",
	}, member)
	if c != 200 {
		t.Fatalf("task_request_acceptance = %d %v", c, accReq)
	}
	qID := astr(accReq["id"])
	c, ansResp := e.callJSON(t, "POST", "/api/ops/questions_answer", doc{
		"questionId": qID, "answer": "accepted",
	}, member)
	if c != 200 {
		t.Fatalf("questions_answer accepted = %d %v", c, ansResp)
	}
	c, tgFinal := e.callJSON(t, "POST", "/api/ops/task_get", doc{"taskId": task1ID}, member)
	if c != 200 || astr(tgFinal["state"]) != "completed" {
		t.Fatalf("expected task1 to complete on acceptance while Home run 2 is active, got %d %v", c, tgFinal)
	}
	// Verify Home session active run 2 is still running (NOT torn down by task1 delivery/acceptance)!
	c, botNow := e.callJSON(t, "POST", "/api/ops/bot_get", contract.BotGetRequest{BotId: botID}, member)
	if c != 200 || astr(mapOf(botNow["bot"])["activeExecutionId"]) != st2.ExecutionId {
		t.Fatalf("expected Home run 2 to stay active after task1 completion, got %d %v", c, botNow)
	}
}
