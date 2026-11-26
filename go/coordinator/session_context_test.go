package main

import (
	"fmt"
	"maps"
	"slices"
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
		{"#812 merged. Next.", "", "#812 merged."},
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

// Requirement: the now-line read probes event_agent_text, so its predicate must be the index's. Risk: the two drift
// and every sessions_list page scans all events of each listed session again.
func TestAgentTextIndexPredicate(t *testing.T) {
	raw := []byte(migrationsText())
	s := string(raw)
	i := strings.Index(s, "CREATE INDEX event_agent_text")
	if i < 0 || !strings.Contains(s[i:], "jsonb_path_exists(payload, '"+agentTextPath+"')") {
		t.Fatal("event_agent_text predicate differs from agentTextPath")
	}
}

// Requirement: docs/designs/fleet-conversations.md#entities: pipeline and Lab agent-step conversations are hidden; the
// Conversations list, its counts and the nav counter leave them out. Risk: Lab trials fill the list (61% of its rows on
// 2026-11-26) and hide the conversations people work in.
func TestSessionListHidesStepConversations(t *testing.T) {
	c, err := sessionWhere(rowFilter{}, &[]any{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c, "x.pipeline_run_id IS NULL") {
		t.Fatalf("conditions %v", c)
	}
	if !strings.Contains(sqlSessionBase, "d.pipeline_run_id") || !strings.Contains(sqlSessionCountBase, "d.pipeline_run_id") {
		t.Fatal("both session bases carry pipeline_run_id")
	}
}

// Requirement: fleet-conversations.md#entities: a conversation's origin and listed follow from its columns, and
// sessions_list filters by origin, listed, room, subject and the project set. Risk: a hidden step conversation shows
// in the list, or a conversation of a project set is missing from that project's list.
func TestSessionListConversationFields(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id, state, variants) VALUES ('cf-x', 'desiign', 'single', 'search-basic', 'draft', '[]')`)
	exec(`INSERT INTO execution (id, kind, state, project_id, started_at, data, observation, config_ref) VALUES ('cf-run', 'pipeline_run', 'running', 'desiign', now(), '{}', 'confirmed', '{}')`)
	exec(`INSERT INTO execution (id, kind, state, project_id, experiment_id, started_at, data, observation, config_ref) VALUES ('cf-trial', 'pipeline_run', 'running', 'desiign', 'cf-x', now(), '{}', 'confirmed', '{}')`)
	exec(`INSERT INTO telegram_room (id, chat_id, thread_id, kind, title, chat_title) VALUES ('42:0', 42, 0, 'private', '', '')`)
	for _, s := range []struct{ id, task, run, subject, payload string }{
		{"cf-person", "", "", "", `{}`},
		{"cf-task", "T-1", "", "", `{"startRequest": "desiign:autostart:T-1"}`},
		{"cf-tg", "", "", "", `{"startRequest": "desiign:tg:5"}`},
		{"cf-fork", "", "", "", `{"forkOf": {"sessionId": "cf-person"}}`},
		{"cf-set", "", "", "pr:fleet#1", `{"focus": {"projectIds": ["hello-world"]}}`},
		{"cf-step", "", "cf-run", "", `{}`},
		{"cf-lab", "", "cf-trial", "", `{}`},
	} {
		exec(`INSERT INTO session (id, project_id, task_id, pipeline_run_id, subject, payload, created_at) VALUES ($1, 'desiign', nullif($2,''), nullif($3,''), nullif($4,''), $5, clock_timestamp())`,
			s.id, s.task, s.run, s.subject, s.payload)
	}
	exec(`INSERT INTO telegram_work (id, room_id, session_id, title, created_by) VALUES ('w-cf', '42:0', 'cf-tg', '', 'ann@example.com')`)
	list := func(filter doc) map[string]string {
		t.Helper()
		var out contract.SessionsListReply
		e.opInto(t, "sessions_list", doc{"filter": filter, "group": "none"}, e.ownerHeaders(), &out)
		got := map[string]string{}
		for _, r := range out.Sessions {
			if strings.HasPrefix(r.Id, "cf-") {
				got[r.Id] = fmt.Sprintf("%s/%v", *r.Origin, *r.Listed)
			}
		}
		return got
	}
	for _, c := range []struct {
		filter doc
		want   map[string]string
	}{
		{doc{}, map[string]string{"cf-person": "person/true", "cf-task": "task/true", "cf-tg": "telegram/true", "cf-fork": "fork/true", "cf-set": "person/true"}},
		{doc{"listed": false}, map[string]string{"cf-step": "pipeline/false", "cf-lab": "lab/false"}},
		{doc{"origin": []string{"telegram", "fork"}}, map[string]string{"cf-tg": "telegram/true", "cf-fork": "fork/true"}},
		{doc{"room": []string{"42:0"}}, map[string]string{"cf-tg": "telegram/true"}},
		{doc{"subject": []string{"pr:fleet#1"}}, map[string]string{"cf-set": "person/true"}},
		{doc{"projectId": []string{"hello-world"}}, map[string]string{"cf-set": "person/true"}},
	} {
		if got := list(c.filter); !maps.Equal(got, c.want) {
			t.Errorf("filter %v: %v, want %v", c.filter, got, c.want)
		}
	}
	// The first page counts the Started by chips (the origin filter left out) and the Steps toggle (hidden of the filter).
	var facets contract.SessionsListReply
	e.opInto(t, "sessions_list", doc{"filter": doc{"origin": []string{"telegram"}}, "group": "none"}, e.ownerHeaders(), &facets)
	if facets.Origins["telegram"] < 1 || facets.Origins["fork"] < 1 || facets.HiddenCount == nil || *facets.HiddenCount < 2 {
		t.Errorf("origins %v hidden %v: the origin filter is left out of both", facets.Origins, facets.HiddenCount)
	}
	e.opInto(t, "sessions_list", doc{"filter": doc{}, "group": "none"}, e.ownerHeaders(), &facets)
	if facets.HiddenCount == nil || *facets.HiddenCount < 2 {
		t.Errorf("hidden %v, want the pipeline and Lab steps", facets.HiddenCount)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/sessions_list", doc{"filter": doc{"origin": []string{"nope"}}}, e.ownerHeaders()); code != 400 {
		t.Errorf("an unknown origin answers %d, want 400", code)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/task_page", doc{"filter": doc{"botId": []string{"b"}}}, e.ownerHeaders()); code != 400 {
		t.Errorf("tasks take a session filter field: %d, want 400", code)
	}
}

// Requirement: session_update changes a conversation's project set by the rights of session_rename: its director or
// the owner, within the caller's grants; an empty set is the checkout project alone. Risk: a person changes another
// person's conversation, or puts it in a project they cannot see.
func TestSessionUpdateProjectSet(t *testing.T) {
	e := openTestCoordinator(t, false)
	for _, s := range [][2]string{{"su-member", "member@trial.test"}, {"su-other", "agents@trial.test"}} {
		if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO session (id, project_id, directed_by, created_at) VALUES ($1, 'desiign', $2, now())`, s[0], s[1]); err != nil {
			t.Fatal(err)
		}
	}
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	update := func(h map[string]string, body doc) (int, []string) {
		t.Helper()
		code, out := e.callJSON(t, "POST", "/api/ops/session_update", body, h)
		var ids []string
		xs, _ := out["projectIds"].([]any)
		for _, x := range xs {
			ids = append(ids, fmt.Sprint(x))
		}
		return code, ids
	}
	for _, c := range []struct {
		name    string
		h       map[string]string
		body    doc
		code    int
		project []string
	}{
		{"no field", member, doc{"sessionId": "su-member"}, 400, nil},
		{"another's conversation", member, doc{"sessionId": "su-other", "projectIds": []string{"desiign"}}, 403, nil},
		{"outside the grants", member, doc{"sessionId": "su-member", "projectIds": []string{"hello-world"}}, 404, nil},
		{"own", member, doc{"sessionId": "su-member", "projectIds": []string{"desiign"}}, 200, []string{"desiign"}},
		{"owner", e.ownerHeaders(), doc{"sessionId": "su-other", "projectIds": []string{"hello-world", "desiign"}}, 200, []string{"hello-world", "desiign"}},
		{"empty", e.ownerHeaders(), doc{"sessionId": "su-other", "projectIds": []string{}}, 200, []string{"desiign"}},
	} {
		if code, ids := update(c.h, c.body); code != c.code || (c.project != nil && !slices.Equal(ids, c.project)) {
			t.Errorf("%s: %d %v, want %d %v", c.name, code, ids, c.code, c.project)
		}
	}
}
