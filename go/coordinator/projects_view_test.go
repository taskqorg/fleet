package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: projects_view (pages/projects.md) counts per project on the server: open questions, sessions working
// and tasks in flight for the catalogue (every project), the same over the focus for the summary, and the focus's
// first open questions. Risk: a count drifts from the lists' own (sessions_list and task_page groups) or the focus
// leaks into the catalogue.
func TestProjectsView(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	for _, s := range []struct{ id, project, state string }{{"pv-a1", "a", "running"}, {"pv-a2", "a", "succeeded"}, {"pv-b1", "b", "running"}} {
		exec(`INSERT INTO session (id, project_id, created_at) VALUES ($1, $2, $3)`, s.id, s.project, now)
		exec(`INSERT INTO execution (id, kind, state, session_id, project_id, harness, started_at, data, observation, config_ref)
		 VALUES ($1, 'agent_run', $2::execution_state, $3, $4, 'codex', $5, '{}', 'confirmed', '{}')`, s.id+"-r", s.state, s.id, s.project, now)
	}
	exec(`INSERT INTO task (id, code, project_id, title, state) VALUES ('pv-t1', 'T-PV-1', 'a', 'one', 'in_progress'), ('pv-t2', 'T-PV-2', 'a', 'two', 'completed')`)
	for i, p := range []string{"a", "b", "b", "b", "b"} {
		exec(`INSERT INTO question (id, kind, project_id, text, asked_at) VALUES ($1, 'operator_decision', $2, 'Pick?', $3)`, "pv-q"+itoa(i), p, now.Add(time.Duration(i)*time.Minute))
	}

	by := func(cs []contract.ProjectWorkCounts) map[string]contract.ProjectWorkCounts {
		m := map[string]contract.ProjectWorkCounts{}
		for _, c := range cs {
			m[deref(c.ProjectId)] = c
		}
		return m
	}
	var all contract.ProjectsViewReply
	e.opInto(t, "projects_view", map[string]any{}, e.ownerHeaders(), &all)
	c := by(all.Catalogue)
	if c["a"].Working != 1 || c["a"].InFlight != 1 || c["a"].Questions != 1 || c["b"].Working != 1 || c["b"].Questions != 4 || c["b"].InFlight != 0 {
		t.Fatalf("catalogue: %+v", all.Catalogue)
	}
	if all.WaitingTotal != 5 || len(all.Waiting) != 3 || all.Waiting[0].Id != "pv-q0" {
		t.Fatalf("waiting: the 3 oldest of 5: %d %+v", all.WaitingTotal, all.Waiting)
	}

	// The focus narrows the summary, never the catalogue.
	var a contract.ProjectsViewReply
	e.opInto(t, "projects_view", map[string]any{"projectIds": []string{"a"}}, e.ownerHeaders(), &a)
	if len(by(a.Catalogue)) != 2 || a.WaitingTotal != 1 || len(a.Lines) != 1 || deref(a.Lines[0].ProjectId) != "a" || a.Lines[0].Working != 1 || a.Lines[0].InFlight != 1 {
		t.Fatalf("focus a: %+v", a)
	}

	// A conversation can match through its project set and still belong to its checkout project's group.
	exec(`UPDATE session SET payload = '{"focus":{"projectIds":["a"]}}' WHERE id = 'pv-b1'`)
	e.opInto(t, "projects_view", map[string]any{"projectIds": []string{"a"}}, e.ownerHeaders(), &a)
	if got := by(a.Lines); len(got) != 2 || got["a"].Working != 1 || got["b"].Working != 1 || got["b"].InFlight != 0 {
		t.Fatalf("focus by conversation project set: %+v", a.Lines)
	}
}
