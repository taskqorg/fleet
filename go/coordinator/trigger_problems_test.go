package main

import (
	"strings"
	"testing"
	"time"
)

// Requirement: one trigger pass wakes the owner bot on several problems at once, one conversation each, whatever
// their order. Risk: HAL0's pass over its 48 unstarted problems failed every 15 s with "advisory lock order violation:
// cannot acquire session:<b> after session:<a>" and the watchdog raised HAL0 down (prod, 2026-11-26).
func TestTriggerWakesOwnerOnManyProblems(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	var saved struct {
		Bot struct {
			Id string `json:"id"`
		} `json:"bot"`
	}
	const bot = "11111111-2222-4333-8444-555555555555"
	e.opInto(t, "bot_settings_save", map[string]any{"botId": bot, "projectId": "specq", "baseRevision": 0,
		"settings": map[string]any{"name": "HAL0", "purpose": "ops", "profileId": "pm", "instructions": "x", "soul": "y",
			"duty": map[string]any{"problems": map[string]any{"system": true, "projects": []string{"specq"}}}}}, h, &saved)
	var trig map[string]any
	e.opInto(t, "trigger_save", map[string]any{"id": "22222222-3333-4444-8555-666666666666", "baseRevision": 0, "botId": bot, "presetId": "needs-you"}, h, &trig)
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		for i := range 6 {
			if err := problemRaise(q, "direct", Problem{Cause: "problem:test:" + string(rune('a'+i)), Text: "Something failed."}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := triggerPass(e.ctx, e.app, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("trigger pass: %v", err)
	}
	var n int
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM session WHERE bot_id = $1 AND subject LIKE 'problem:%'", bot).Scan(&n); err != nil || n != 6 {
		t.Fatalf("problem conversations %d, want 6 (%v)", n, err)
	}

	// A focus watch the bot's agent starts in one of those conversations (pm-watch) sees only the tasks it works: a
	// change of another task of the project wakes nobody there.
	var sid string
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT id FROM session WHERE bot_id = $1 AND subject LIKE 'problem:%' LIMIT 1", bot).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	e.opInto(t, "trigger_save", map[string]any{"id": "33333333-4444-4555-8666-777777777777", "baseRevision": 0, "sessionId": sid, "presetId": "focus-changes"}, h, &trig)
	if _, err := e.ownerStore.pool.Exec(e.ctx, "SET fleet.project_ids = '*'; INSERT INTO task(id, title, project_id) VALUES ('t-other', 'other work', 'specq'); UPDATE task SET state = 'in_progress' WHERE id = 't-other'"); err != nil {
		t.Fatal(err)
	}
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		row, err := q.db().TriggerGet(q.ctx(), "33333333-4444-4555-8666-777777777777")
		if err != nil {
			return err
		}
		row.ChangeCursor = 0
		target, err := triggerTargetOfRow(q, row)
		if err != nil {
			return err
		}
		parts, _, _, err := triggerChanges(q, row, target)
		for _, p := range parts {
			if strings.Contains(p.Text, "t-other") || strings.Contains(p.Text, "other work") {
				t.Errorf("a problem conversation sees another task: %+v", p)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
