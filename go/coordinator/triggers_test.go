package main

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Requirement (docs/designs/fleet-triggers.md): a conversation trigger with a change condition checks without a model
// turn while nothing in its focus changed, counts the quiet checks, and fires once with the change list when something
// did; a trigger of the same session that covers the fired kinds does not report the same change again; a trigger whose
// message is still queued waits; saves and deletes use the revision; only who directs the session manages it.
// Risk: a timer wakes the model on every tick for nothing (the 400k-token wakes of C-FLT-592), or two triggers report
// one change twice.
func TestTriggers(t *testing.T) {
	e := openTestCoordinator(t, false)
	sid := newID()
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return sessionPut(q, &agentSession{ID: sid, ProjectID: "desiign", DirectedBy: testOwnerEmail})
	}); err != nil {
		t.Fatal(err)
	}
	owner := e.ownerHeaders()
	member := e.ownerHeaders()
	member["X-Fleet-Person"] = "member@trial.test"
	call := func(name string, req any, headers map[string]string, want int) map[string]any {
		t.Helper()
		status, reply := e.callJSON(t, "POST", "/api/ops/"+name, req, headers)
		if status != want {
			t.Fatalf("%s: got %d, want %d (%v)", name, status, want, reply)
		}
		return reply
	}
	psql := func(sql string) string {
		t.Helper()
		return strings.TrimSpace(runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", sql))
	}
	list := call("trigger_list", contract.TriggerListRequest{SessionId: &sid}, owner, 200)
	if len(anys(list["presets"])) < 4 || list["canManage"] != true || len(anys(list["triggers"])) != 0 || list["passMs"] != float64(triggerTickMs()) {
		t.Fatalf("trigger_list before any trigger = %v", list)
	}
	checkID, changesID := newID(), newID()
	every := int64(60_000)
	call("trigger_save", contract.TriggerSaveRequest{Id: checkID, SessionId: &sid, PresetId: ptr("conversation-check"),
		Settings: &contract.TriggerSettingsInput{EveryMs: &every}}, member, 403)
	check := mapOf(call("trigger_save", contract.TriggerSaveRequest{Id: checkID, SessionId: &sid, PresetId: ptr("conversation-check"),
		Settings: &contract.TriggerSettingsInput{EveryMs: &every}}, owner, 200)["trigger"])
	settings := mapOf(check["settings"])
	if settings["presetId"] != "conversation-check" || !strings.HasPrefix(astr(settings["text"]), "Fleet: conversation check") ||
		len(anys(settings["ifChanged"])) != 3 || check["revision"] != float64(1) {
		t.Fatalf("preset values were not copied: %v", check)
	}
	call("trigger_save", contract.TriggerSaveRequest{Id: changesID, SessionId: &sid, PresetId: ptr("focus-changes"),
		Settings: &contract.TriggerSettingsInput{IfChanged: []string{"question"}}}, owner, 200)

	pass := func(at time.Time) {
		t.Helper()
		if err := triggerPass(e.ctx, e.app, at); err != nil {
			t.Fatal(err)
		}
	}
	messages := func() string {
		return psql("SELECT count(*) FROM message WHERE session_id = '"+sid+"' AND payload->>'triggerId' IS NOT NULL;") +
			"+" + psql("SELECT count(*) FROM queued_message WHERE session_id = '"+sid+"' AND payload->>'triggerId' IS NOT NULL;")
	}
	now := time.Now()
	pass(now.Add(2 * time.Minute))
	if got := messages(); got != "0+0" {
		t.Fatalf("a check without changes sent a message: %s", got)
	}
	changesBefore := psql("SELECT count(*) FROM change WHERE kind = 'trigger' AND id = '" + checkID + "';")
	pass(now.Add(3 * time.Minute))
	if after := psql("SELECT count(*) FROM change WHERE kind = 'trigger' AND id = '" + checkID + "';"); after == changesBefore {
		t.Fatalf("a quiet check that moved the counter wrote no trigger change (%s)", after)
	}
	pass(now.Add(3 * time.Minute)) // not due again: no write, no change
	quiet := psql("SELECT quiet_checks FROM trigger WHERE id = '" + checkID + "';")
	if quiet != "2" {
		t.Fatalf("quiet checks = %q, want 2", quiet)
	}

	psql(`INSERT INTO question(id, kind, project_id, text, state) VALUES ('trig-q1', 'operator_decision', 'desiign', 'Ship it?', 'open');`)
	pass(now.Add(4 * time.Minute))
	if got := messages(); got != "1+0" {
		t.Fatalf("one change should send exactly one message (the first due trigger's), got %s", got)
	}
	text := psql("SELECT text FROM message WHERE session_id = '" + sid + "' AND payload->>'triggerId' IS NOT NULL;")
	if !strings.Contains(text, "question trig-q1") || psql("SELECT source FROM message WHERE session_id = '"+sid+"' AND payload->>'triggerId' IS NOT NULL;") != "trigger" {
		t.Fatalf("trigger message = %q", text)
	}
	fired := psql("SELECT id || ':' || fires || ':' || quiet_checks FROM trigger WHERE fires > 0;")
	if fired != checkID+":1:0" && fired != changesID+":1:0" {
		t.Fatalf("fired state = %q", fired)
	}

	// The session is busy with the first message: the next change waits in a queued message, then the trigger waits.
	psql(`INSERT INTO question(id, kind, project_id, text, state) VALUES ('trig-q2', 'operator_decision', 'desiign', 'And this?', 'open');`)
	pass(now.Add(6 * time.Minute))
	psql(`INSERT INTO question(id, kind, project_id, text, state) VALUES ('trig-q3', 'operator_decision', 'desiign', 'Third?', 'open');`)
	pass(now.Add(8 * time.Minute))
	if got := messages(); got != "1+1" {
		t.Fatalf("a queued trigger message should hold its trigger back, got %s", got)
	}

	// Revisions: a stale save or delete is refused; pause keeps the values; trigger_run fires a paused trigger now.
	call("trigger_save", contract.TriggerSaveRequest{Id: checkID, BaseRevision: 0, SessionId: &sid}, owner, 200) // a create retry
	call("trigger_save", contract.TriggerSaveRequest{Id: checkID, BaseRevision: 0, SessionId: &sid,
		Settings: &contract.TriggerSettingsInput{Name: ptr("Stale")}}, owner, 409)
	paused := mapOf(call("trigger_save", contract.TriggerSaveRequest{Id: checkID, BaseRevision: 1,
		Settings: &contract.TriggerSettingsInput{Enabled: ptr(false)}}, owner, 200)["trigger"])
	if mapOf(paused["settings"])["enabled"] != false || paused["nextAt"] != nil || mapOf(paused["settings"])["everyMs"] != float64(every) {
		t.Fatalf("paused trigger = %v", paused)
	}
	run := call("trigger_run", contract.TriggerRunRequest{Id: checkID}, owner, 200)
	if run["sent"] != true {
		t.Fatalf("trigger_run = %v", run)
	}
	call("trigger_delete", contract.TriggerDeleteRequest{Id: checkID, BaseRevision: 1}, owner, 409)
	call("trigger_delete", contract.TriggerDeleteRequest{Id: checkID, BaseRevision: 2}, member, 403)
	call("trigger_delete", contract.TriggerDeleteRequest{Id: checkID, BaseRevision: 2}, owner, 200)
	if n := len(anys(call("trigger_list", contract.TriggerListRequest{SessionId: &sid}, owner, 200)["triggers"])); n != 1 {
		t.Fatalf("triggers after delete = %d", n)
	}
	call("trigger_save", contract.TriggerSaveRequest{Id: newID(), SessionId: &sid, PresetId: ptr("needs-you")}, owner, 400)
	huge := int64(1e15) // past what a time.Duration holds: it would wrap and fire every pass
	call("trigger_save", contract.TriggerSaveRequest{Id: newID(), SessionId: &sid, PresetId: ptr("conversation-check"),
		Settings: &contract.TriggerSettingsInput{EveryMs: &huge}}, owner, 400)
	call("trigger_save", contract.TriggerSaveRequest{Id: newID(), SessionId: &sid, Settings: &contract.TriggerSettingsInput{
		Name: ptr("x"), When: ptr(contract.TriggerWhen("every"))}}, owner, 400)
}

// Requirement (docs/designs/fleet-triggers.md#migration): migration 106 turns watches, bot automations and the bot
// settings' daily wake and Needs you switch into triggers that keep their cursors, slots and pause.
// Risk: an upgrade drops a running watch or replays a whole change history into a bot.
func TestTriggerMigration(t *testing.T) {
	sockDir, pgPort, binDir := ensureTestPostgres(t)
	dbName := fmt.Sprintf("fleet_mig106_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	rolesRaw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "infrastructure", "fleet-postgres.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rolesSQL := strings.NewReplacer("DATABASE fleet", "DATABASE "+dbName, "datname = 'fleet'", "datname = '"+dbName+"'",
		"\\connect fleet", "\\connect "+dbName).Replace(string(rolesRaw))
	runTestPSQL(t, binDir, sockDir, pgPort, "postgres", "postgres", rolesSQL)
	t.Cleanup(func() {
		_, _ = osexec.Command(filepath.Join(binDir, "psql"), "-h", sockDir, "-p", fmt.Sprint(pgPort), "-U", "postgres", "-d", "postgres",
			"-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", dbName)).CombinedOutput()
	})
	ctx := context.Background()
	url := fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", dbName, sockDir, pgPort)
	at := strings.Index(schemaSQL, "-- migration 106")
	if at < 0 {
		t.Fatal("schema.sql missing -- migration 106")
	}
	if err := migrateSQL(ctx, url, schemaSQL[:at]); err != nil {
		t.Fatalf("pre-106 migrate: %v", err)
	}
	s, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO session(id, project_id, payload) VALUES ('s-watch', 'desiign', '{}'), ('s-paused', 'desiign', '{}');
		INSERT INTO watch(id, session_id, project_id, payload) VALUES
		 ('watch:s-watch', 's-watch', 'desiign', '{"kinds":["task","limits"],"cursor":41,"timerMs":300000,"nextWakeAt":1795585670222,"wakes":7,"lastWakeAt":1795585600000}'),
		 ('watch:s-paused', 's-paused', 'desiign', '{"kinds":[],"cursor":5,"pausedTimerMs":60000}');
		INSERT INTO bot(id, project_id, director, revision) VALUES ('b1', 'desiign', 'owner@trial.test', 1);
		INSERT INTO bot_revision(bot_id, revision, name, purpose, profile_id, instructions, soul, schedule_time, schedule_zone, wake_on_needs_you)
		 VALUES ('b1', 1, 'HAL0', '', 'pm', '', '', '09:00', 'Etc/UTC', true);
		INSERT INTO bot_wake_cursor(bot_id, project_id, needs_you_change_seq, last_schedule_key) VALUES ('b1', 'desiign', 77, '2026-11-24|09:00|Etc/UTC');
		INSERT INTO bot_automation(id, bot_id, project_id, revision, name, instructions, kind, schedule_time, schedule_zone, event, enabled, effective_since, event_cursor, last_schedule_date)
		 VALUES ('a-daily', 'b1', 'desiign', 7, 'Daily', 'Reconcile.', 'schedule', '10:00', 'UTC', 'none', true, now(), 0, '2026-11-24'),
		        ('a-ny', 'b1', 'desiign', 2, 'Questions', 'Read them.', 'needs_you', '', '', 'question', false, now(), 99, '');
		INSERT INTO bot_run(id, bot_id, project_id, bot_revision, cause, automation_id) VALUES ('r1', 'b1', 'desiign', 1, 'schedule', 'a-daily');
	`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, url); err != nil {
		t.Fatalf("migration 106: %v", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, coalesce(session_id, bot_id), when_kind, coalesce(every_ms, 0), daily_time, array_to_string(if_changed, ','),
		enabled, change_cursor, last_slot, text, revision FROM trigger ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, target, when, daily, kinds, slot, text string
		var every, cursor int64
		var revision int32
		var enabled bool
		if err := rows.Scan(&id, &target, &when, &every, &daily, &kinds, &enabled, &cursor, &slot, &text, &revision); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %s %d %s [%s] %v %d %s %q r%d", id, target, when, every, daily, kinds, enabled, cursor, slot, text, revision))
	}
	want := []string{
		`a-daily b1 daily 0 10:00 [] true 0 2026-11-24 "Reconcile." r7`,
		`a-ny b1 every 15000  [question] false 99  "Read them." r2`,
		`conversation-check:s-paused s-paused every 60000  [task,question,execution] false 5  "Fleet: conversation check; review your focus with task_page and questions_list." r1`,
		`conversation-check:s-watch s-watch every 300000  [task,question,execution] true 41  "Fleet: conversation check; review your focus with task_page and questions_list." r1`,
		`daily-reconciliation:b1 b1 daily 0 09:00 [] true 0 2026-11-24 "" r1`,
		`focus-changes:s-watch s-watch every 15000  [task,limits] true 41  "" r1`,
		`needs-you:b1 b1 every 15000  [question,execution] true 77  "" r1`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("migrated triggers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var runTrigger string
	if err := s.pool.QueryRow(ctx, "SELECT trigger_id FROM bot_run WHERE id = 'r1'").Scan(&runTrigger); err != nil || runTrigger != "a-daily" {
		t.Fatalf("bot_run trigger_id = %q (%v)", runTrigger, err)
	}
}

// Requirement (docs/designs/fleet-triggers.md, Pass): a trigger every pass checks on each pass. Risk: its next time
// falls a moment after the next pass, so it waits two passes (30 s instead of 15 s on prod).
func TestTriggerDueWithinHalfPass(t *testing.T) {
	now := time.Now()
	tick := time.Duration(triggerTickMs()) * time.Millisecond
	every := triggerTickMs()
	for _, c := range []struct {
		next time.Duration
		due  bool
	}{{0, true}, {tick/2 - time.Millisecond, true}, {tick/2 + time.Millisecond, false}, {tick, false}} {
		next := now.Add(c.next)
		if _, due := triggerDue(db.Trigger{Enabled: true, WhenKind: "every", EveryMs: &every, NextAt: &next}, now); due != c.due {
			t.Fatalf("next in %v: due %v, want %v", c.next, due, c.due)
		}
	}
}
