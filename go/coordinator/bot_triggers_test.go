package main

import (
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// FLT-26: one bot owns independent, durable triggers (the bot page's automations, trigger_save and trigger_list).
// Cover conflicting edits, cross-project event leakage, restart replay, and pause leaving an execution live.
func TestBotAutomations(t *testing.T) {
	e := openTestCoordinator(t, false)
	const project = "desiign"
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	owner := e.ownerHeaders()
	call := func(name string, req any, headers map[string]string, want int) map[string]any {
		t.Helper()
		status, reply := e.callJSON(t, "POST", "/api/ops/"+name, req, headers)
		if status != want {
			t.Fatalf("%s: got %d, want %d (%v)", name, status, want, reply)
		}
		return reply
	}
	botID := newID()
	call("bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: project, Settings: testBotSettings("pm"),
	}, member, 200)

	dailyID, eventID := newID(), newID()
	// automation: a bot trigger's settings as the bot page edits them.
	type automation struct {
		Name, Text, When, Time, Zone string
		EveryMs                      int64
		Kinds                        []string
		Enabled                      bool
	}
	daily := automation{Name: "HAL0 tariff refresh", Text: "Compare official provider tariffs in dry-run mode.",
		When: "daily", Time: "08:00", Zone: "Asia/Tokyo", Kinds: []string{}, Enabled: true}
	event := automation{Name: "Review operator decisions", Text: "Read the new question and summarize it.",
		When: "every", EveryMs: triggerTickMs(), Kinds: []string{"question"}, Enabled: true}
	save := func(id string, revision int64, a automation, headers map[string]string, status int) map[string]any {
		t.Helper()
		when := contract.TriggerWhen(a.When)
		in := &contract.TriggerSettingsInput{Name: &a.Name, Text: &a.Text, When: &when, IfChanged: a.Kinds, Enabled: &a.Enabled}
		if a.When == "daily" {
			in.DailyTime, in.DailyZone = &a.Time, &a.Zone
		} else {
			in.EveryMs = &a.EveryMs
		}
		rep := call("trigger_save", contract.TriggerSaveRequest{Id: id, BaseRevision: revision, BotId: &botID, Settings: in}, headers, status)
		return mapOf(rep["trigger"])
	}
	day := save(dailyID, 0, daily, member, 200)
	save(eventID, 0, event, member, 200)
	due, err := time.Parse(time.RFC3339Nano, astr(day["nextAt"]))
	if err != nil || due.UTC().Hour() != 23 {
		t.Fatalf("08:00 Asia/Tokyo should be 23:00 UTC on the preceding date: %v (%v)", day, err)
	}
	read := func(id string) []any {
		t.Helper()
		rep := call("bot_runs", contract.BotRunsRequest{BotId: &botID, AutomationId: &id}, owner, 200)
		return anys(rep["runs"])
	}
	pass := func(app *App, at time.Time) {
		t.Helper()
		if err := triggerPass(e.ctx, app, at); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(execID string) {
		t.Helper()
		if execID == "" {
			t.Fatal("run is missing a Fleet execution")
		}
		if err := e.appStore.Write(e.ctx, nil, []string{"execution:" + execID}, func(q *Request) error {
			_, err := executionTransition(q, execID, executionEvent{To: "succeeded"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	pass(e.app, due.Add(-time.Nanosecond))
	if got := read(dailyID); len(got) != 0 {
		t.Fatalf("daily automation ran before its local wake time: %v", got)
	}
	pass(e.app, due.Add(time.Minute))
	scheduled := read(dailyID)
	if len(scheduled) != 1 || mapOf(scheduled[0])["cause"] != "schedule" {
		t.Fatalf("expected a single daily wake, got %v", scheduled)
	}
	scheduledRun := mapOf(scheduled[0])
	execID := astr(scheduledRun["executionId"])
	executionText := runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres",
		"SELECT text FROM message WHERE execution_id = '"+execID+"';")
	if !strings.Contains(executionText, daily.Text) {
		t.Fatalf("schedule did not send the saved automation instructions: %q", executionText)
	}

	daily.Enabled = false
	save(dailyID, 1, daily, member, 200)
	save(dailyID, 1, daily, member, 200) // replay of the same pause
	changed := daily
	changed.Name = "Stale edit"
	save(dailyID, 1, changed, member, 409)
	list := call("trigger_list", contract.TriggerListRequest{BotId: &botID}, member, 200)
	automations := anys(list["triggers"])
	if len(automations) != 2 {
		t.Fatalf("expected two triggers, got %v", list)
	}
	for _, entry := range automations {
		a := mapOf(entry)
		s := mapOf(a["settings"])
		if a["id"] == dailyID && (s["enabled"] != false || a["nextAt"] != nil || mapOf(a["lastRun"])["state"] != "queued") {
			t.Fatalf("paused automation lost its active run or still has a next run: %v", a)
		}
		if a["id"] == eventID && s["enabled"] != true {
			t.Fatalf("pausing daily disabled the event automation: %v", a)
		}
	}

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO question(id, kind, project_id, text, state) VALUES
		  ('automation-q', 'operator_decision', 'desiign', 'Approve the review?', 'open'),
		  ('foreign-automation-q', 'operator_decision', 'specq', 'Foreign question', 'open');
		INSERT INTO execution(id, kind, state, project_id, problem)
		VALUES ('automation-problem', 'agent_run', 'failed', 'desiign', 'Filtered execution problem');
	`)
	pass(e.app, due.Add(2*time.Minute))
	if runs := read(eventID); len(runs) != 0 {
		t.Fatalf("expected the event to wait while the paused schedule still has a live run: %v", runs)
	}

	finish(execID)
	restarted := &App{store: e.appStore, browserSecret: e.browserSecret, hostID: "test-coord"}
	restarted.ensureRuntime()
	restarted.people.Store(e.app.people.Load())
	pass(restarted, due.Add(3*time.Minute))
	eventRuns := read(eventID)
	head := mapOf(eventRuns[0])
	eventText := runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres",
		"SELECT text FROM message WHERE execution_id = '"+astr(head["executionId"])+"';")
	if len(eventRuns) != 1 || head["cause"] != "needs_you" || !strings.Contains(eventText, "question automation-q") ||
		strings.Contains(eventText, "foreign-automation-q") || strings.Contains(eventText, "automation-problem") {
		t.Fatalf("event filter or overlap recovery failed: %v %q", eventRuns, eventText)
	}
	finish(astr(head["executionId"]))

	pass(restarted, due.Add(4*time.Minute))
	if runs := read(eventID); len(runs) != 1 {
		t.Fatalf("event replay duplicated a wake after restart: %v", runs)
	}

	manualReq := contract.BotRunRequest{BotId: botID, AutomationId: &dailyID, RequestId: ptr("run-tariff")}
	rep := call("bot_run", manualReq, member, 200)
	manual := mapOf(rep["run"])
	if rep["admitted"] != true || mapOf(manual["automation"])["name"] != daily.Name || astr(manual["executionId"]) == "" {
		t.Fatalf("manual wake missing automation revision or execution attribution: %v", rep)
	}
	daily.Name = "Edited tariff refresh"
	save(dailyID, 2, daily, member, 200)
	replayed := mapOf(call("bot_run", manualReq, member, 200)["run"])
	if replayed["id"] != manual["id"] || mapOf(replayed["automation"])["name"] != "HAL0 tariff refresh" {
		t.Fatalf("retry after edit replaced historical automation data: %v", replayed)
	}
	finish(astr(manual["executionId"]))
	daily.Enabled = true
	save(dailyID, 3, daily, member, 200)
	pass(restarted, due.Add(5*time.Minute))
	if runs := read(dailyID); len(runs) != 2 {
		t.Fatalf("same-day reenable duplicated scheduled wake: %v", runs)
	}

	syncTestPersonGrant(t, e, "member@trial.test", project, false)
	call("trigger_list", contract.TriggerListRequest{BotId: &botID}, member, 404)
	manualReq.RequestId = ptr("run-after-revocation")
	refused := mapOf(call("bot_run", manualReq, owner, 200)["run"])
	if refused["state"] != "failed" || refused["executionId"] != nil {
		t.Fatalf("manual wake bypassed revoked director: %v", refused)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		INSERT INTO question(id, kind, project_id, text, state)
		VALUES ('automation-revoked', 'operator_decision', 'desiign', 'Post-revoke question', 'open');
	`)
	pass(restarted, due.AddDate(0, 0, 1))
	for _, id := range []string{dailyID, eventID} {
		runs := read(id)
		last := mapOf(runs[0])
		if last["state"] != "failed" || last["executionId"] != nil || !strings.Contains(astr(last["reason"]), "no longer has access") {
			t.Fatalf("automatic wake bypassed revoked director: %v", runs)
		}
	}
}

func TestBotAutomationScheduleClockChanges(t *testing.T) {
	// FLT-26: repeated local times must not repeat a wake and nonexistent local
	// times must not silently move to a different clock time.
	a := db.Trigger{WhenKind: "daily", Enabled: true, DailyTime: "02:30", DailyZone: "Europe/Paris"}
	spring := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)
	if _, due := dueDaily(a, spring); due {
		t.Fatal("woke on a nonexistent local slot")
	}
	next := nextDaily(a, spring)
	if next == nil || !next.Equal(time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected next valid local date, got %v", next)
	}
	fall := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	date, due := dueDaily(a, fall)
	if !due {
		t.Fatal("no wake after repeated local hour")
	}
	a.LastSlot = date
	if _, due := dueDaily(a, fall.Add(time.Minute)); due {
		t.Fatal("repeated local date woke twice")
	}
}
