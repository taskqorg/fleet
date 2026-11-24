package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: pages/home.md "Session card": the now line is the first sentence of the latest agent message as plain
// text, else the task title, cut to one line. Risk: a card shows markdown syntax, a code block or a whole paragraph.
func TestNowLine(t *testing.T) {
	for _, c := range []struct{ md, task, want string }{
		{"I'll **rebase** on `main` first. Then push.", "", "I'll rebase on main first."},
		{"```go\nx := 1\n```\n\n## Plan\n- Read [the spec](https://x/y) and fix it", "", "Read the spec and fix it"},
		{"PR #812 passed v1.2.3 checks! Merged.", "", "PR #812 passed v1.2.3 checks!"},
		{"---\n| a | b |\n|---|---|", "Fix the build", "| a | b |"},
		{"", "  Fix   the build ", "Fix the build"},
		{"", "", ""},
		{strings.Repeat("word ", 60), "", strings.TrimSpace(strings.Repeat("word ", 31)) + "…"},
	} {
		got := ""
		if l := nowLine(c.md, c.task); l != nil {
			got = *l
		}
		if got != c.want {
			t.Errorf("nowLine(%q, %q) = %q, want %q", c.md, c.task, got, c.want)
		}
	}
}

// Requirement: FLT-46. person_visit keeps a person's last Home visit on the server: each visit answers the one before
// it, record false only reads, and the visit is the person's own. Risk: the quiet summary counts from the visit it just
// recorded (always nothing), or one person's visit moves another's.
func TestPersonVisit(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	visit := func(h map[string]string, body any, want int) map[string]any {
		t.Helper()
		c, r := e.callJSON(t, "POST", "/api/ops/person_visit", body, h)
		if c != want {
			t.Fatalf("person_visit %v = %d %v", body, c, r)
		}
		return r
	}
	first := visit(owner, doc{"page": "home"}, 200)
	if first["previousAt"] != nil || first["recordedAt"] == nil {
		t.Fatalf("first visit = %v", first)
	}
	second := visit(owner, doc{"page": "home"}, 200)
	if num(second["previousAt"]) != num(first["recordedAt"]) {
		t.Fatalf("second visit = %v, want previousAt %v", second, first["recordedAt"])
	}
	read := visit(owner, doc{"page": "home", "record": false}, 200)
	if num(read["previousAt"]) != num(second["recordedAt"]) || read["recordedAt"] != nil {
		t.Fatalf("read = %v", read)
	}
	if other := visit(member, doc{"page": "home"}, 200); other["previousAt"] != nil {
		t.Fatalf("member sees the owner's visit: %v", other)
	}
	visit(owner, doc{"page": "tasks"}, 400)
}

// Requirement: FLT-46 / FLT-30. session_context gives one session's plan with done marks (its own board, else its
// task's), its watch timer, its bot's automations and the other swarm members with their state; sessions_list rows
// carry the now line. Risk: the context panel shows another session's plan, lists the session among its own swarm,
// or misses a timer; a card has no now line although the agent spoke.
func TestSessionContext(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	call := func(op string, body any, want int) map[string]any {
		t.Helper()
		c, r := e.callJSON(t, "POST", "/api/ops/"+op, body, owner)
		if c != want {
			t.Fatalf("%s %v = %d %v", op, body, c, r)
		}
		return r
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO task(id,title,project_id,state) VALUES ('t-ctx','Ship the context panel','desiign','in_progress');
INSERT INTO session(id,project_id,task_id,payload) VALUES
 ('s-ctx','desiign','t-ctx','{}'),
 ('s-ctx-w','desiign','t-ctx','{}'),
 ('s-ctx-solo','desiign',NULL,'{}');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,data) VALUES
 ('e-ctx','agent_run','succeeded','desiign','t-ctx','s-ctx','{"id":"e-ctx","sessionId":"s-ctx"}');
INSERT INTO event(id,execution_id,session_id,project_id,type,payload) VALUES
 ('ev-ctx-1','e-ctx','s-ctx','desiign','item.completed','{"data":{"fleetNormalized":{"blocks":[{"kind":"text","role":"agent","markdown":"Older words."}]}}}'),
 ('ev-ctx-2','e-ctx','s-ctx','desiign','item.completed','{"data":{"fleetNormalized":{"blocks":[{"kind":"text","role":"agent","markdown":"Reading **the spec** now. Then code."},{"kind":"tool","name":"Read","state":"done"}]}}}'),
 ('ev-ctx-3','e-ctx','s-ctx','desiign','item.completed','{"data":{"fleetNormalized":{"blocks":[{"kind":"tool","name":"Edit","state":"done"}]}}}');
`)
	call("work_plan_set", doc{"projectId": "desiign", "scopeKind": "task", "scopeId": "t-ctx", "title": "Context panel", "threads": []any{
		doc{"id": "th", "title": "Build", "tasks": []any{
			doc{"id": "a", "title": "Read the spec", "status": "done", "ownerMember": "lead", "sessionId": "s-ctx"},
			doc{"id": "b", "title": "Write the read", "status": "in_progress", "ownerMember": "worker", "role": "worker", "sessionId": "s-ctx-w"},
			doc{"id": "c", "title": "Test it"},
		}},
	}}, 200)
	call("watch_set", doc{"sessionId": "s-ctx", "kinds": []string{"task"}, "timerMs": 60000}, 200)

	var ctx contract.SessionContextReply
	asJSON(t, call("session_context", doc{"sessionId": "s-ctx"}, 200), &ctx)
	p := ctx.Plan
	if p == nil || p.ScopeKind != contract.BoardScopeKindTask || p.Total != 3 || p.Done != 1 || !p.Steps[0].Done || p.Steps[2].Status != contract.WorkPlanTaskStatusPending {
		t.Fatalf("plan = %+v", p)
	}
	if len(ctx.Swarm) != 1 || ctx.Swarm[0].MemberName != "worker" || deref(ctx.Swarm[0].SessionId) != "s-ctx-w" || deref(ctx.Swarm[0].SessionCode) == "" || deref(ctx.Swarm[0].State) != "idle" {
		t.Fatalf("swarm = %s", jsonBytes(ctx.Swarm))
	}
	if len(ctx.Timers) != 1 || ctx.Timers[0].EveryMs != 60000 || ctx.Timers[0].NextAt == nil {
		t.Fatalf("timers = %+v", ctx.Timers)
	}
	var solo contract.SessionContextReply
	asJSON(t, call("session_context", doc{"sessionId": "s-ctx-solo"}, 200), &solo)
	if solo.Plan != nil || len(solo.Swarm) != 0 || len(solo.Timers) != 0 || len(solo.Automations) != 0 {
		t.Fatalf("solo session context = %s", jsonBytes(solo))
	}
	botID := newID()
	home := astr(mapOf(call("bot_settings_save", contract.BotSettingsSaveRequest{BotId: botID, ProjectId: "desiign", Settings: testBotSettings("pm")}, 200)["bot"])["homeSessionId"])
	call("bot_automation_save", contract.BotAutomationSaveRequest{BotId: botID, AutomationId: newID(), Settings: contract.BotAutomationSettings{
		Name: "Morning check", Instructions: "Read Needs you.", ProjectId: "desiign", Kind: "schedule", ScheduleTime: "08:00", ScheduleZone: "UTC", Event: "none", Enabled: true}}, 200)
	var bot contract.SessionContextReply
	asJSON(t, call("session_context", doc{"sessionId": home}, 200), &bot)
	if len(bot.Automations) != 1 || bot.Automations[0].Settings.Name != "Morning check" || bot.Automations[0].NextRunAt == nil {
		t.Fatalf("bot session automations = %s", jsonBytes(bot.Automations))
	}
	call("session_context", doc{"sessionId": "s-unknown"}, 404)

	var list contract.SessionsListReply
	asJSON(t, call("sessions_list", doc{"group": "none"}, 200), &list)
	now := map[string]string{}
	for _, r := range list.Sessions {
		now[r.Id] = deref(r.Now)
	}
	if now["s-ctx"] != "Reading the spec now." || now["s-ctx-w"] != "Ship the context panel" || now["s-ctx-solo"] != "" {
		t.Fatalf("now lines = %v", now)
	}
}

// asJSON: a decoded reply read again as its contract type.
func asJSON(t *testing.T, v any, out any) {
	t.Helper()
	if err := json.Unmarshal(jsonBytes(v), out); err != nil {
		t.Fatal(err)
	}
}

// Requirement: the now-line read probes event_agent_text, so its predicate must be the index's. Risk: the two drift
// and every sessions_list page scans all events of each listed session again.
func TestAgentTextIndexPredicate(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, "CREATE INDEX event_agent_text")
	if i < 0 || !strings.Contains(s[i:], "jsonb_path_exists(payload, '"+agentTextPath+"')") {
		t.Fatal("event_agent_text predicate differs from agentTextPath")
	}
}
