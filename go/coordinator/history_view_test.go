package main

import (
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: history (docs/designs/fleet-history.md) records runs, tasks and audit changes as they are written, folds
// repeats of one subject into one entry with n, narrows to a section or an object with the same read, pages with
// before, and leaves a person's own preferences out. Risk: a write records nothing, a repeat floods the feed, or a
// scope leaks entries of other objects.
func TestHistoryView(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	exec(`INSERT INTO session (id, created_at, updated_at) VALUES ('hs-s', $1, $1)`, now)
	// Three host jobs on one host finish: one entry of three. A running agent run starts, then fails.
	for i := range 3 {
		exec(`INSERT INTO execution (id, kind, state, host_id, started_at, ended_at, data, observation, config_ref)
		 VALUES ($1, 'host_job', 'succeeded', 'mac', $2, $2, '{}', 'confirmed', '{}')`, "hs-job-"+string(rune('a'+i)), now.Add(time.Duration(i)*time.Second))
	}
	exec(`INSERT INTO execution (id, kind, state, session_id, harness, started_at, data, observation, config_ref)
	 VALUES ('hs-run', 'agent_run', 'running', 'hs-s', 'codex', $1, '{}', 'confirmed', '{}')`, now.Add(5*time.Second))
	exec(`UPDATE execution SET state = 'failed', ended_at = $1 WHERE id = 'hs-run'`, now.Add(6*time.Second))
	// The owner's own pins are not history; a change to a host is.
	exec(`INSERT INTO audit_log (id, target, field, value, actor) VALUES ('hs-a1', $1, 'pins', '["live"]', $2)`, "person:"+testOwnerEmail, testOwnerEmail)
	exec(`INSERT INTO audit_log (id, target, field, value, actor) VALUES ('hs-a2', 'host:mac', 'paused', 'true', $1)`, testOwnerEmail)

	// A question asked in the session and answered: two entries of one subject with different verbs.
	exec(`INSERT INTO question (id, kind, session_id, text, asked_at) VALUES ('hs-q', 'agent_question', 'hs-s', 'Merge it?', $1)`, now.Add(7*time.Second))
	exec(`UPDATE question SET state = 'answered', answer = 'yes', answered_by = $1, answered_at = $2 WHERE id = 'hs-q'`, testOwnerEmail, now.Add(8*time.Second))

	var all contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{}, e.ownerHeaders(), &all)
	got := map[string]contract.HistoryEntry{}
	for _, x := range all.Entries {
		got[x.Subject+" "+x.Verb] = x
	}
	if len(all.Entries) != 6 {
		t.Fatalf("entries %+v, want 6: the host change, the question answered and asked, the run failed and started, three host jobs as one", all.Entries)
	}
	if a := got["question:hs-q answered"]; a.Outcome != "ok" || string(a.Detail["answer"]) != `"yes"` || a.Actor == nil || *a.Actor != testOwnerEmail {
		t.Fatalf("answer: %+v", a)
	}
	if j := got["execution:hs-job-c finished"]; j.N != 3 || j.Section != "system" || j.FirstAt >= j.At {
		t.Fatalf("host jobs: %+v", j)
	}
	if r := got["execution:hs-run failed"]; r.Outcome != "failed" || r.SessionId == nil || *r.SessionId != "hs-s" {
		t.Fatalf("failed run: %+v", r)
	}
	if _, ok := got["execution:hs-run started"]; !ok {
		t.Fatal("the run's start is missing")
	}
	if a, ok := got["host:mac changed"]; !ok || a.Kind != "settings" || string(a.Detail["value"]) != "true" {
		t.Fatalf("host change: %+v", a)
	}
	if all.Entries[0].Subject != "question:hs-q" || all.Entries[2].Subject != "host:mac" {
		t.Fatalf("newest first: %s", all.Entries[0].Subject)
	}

	// One object: the session's entries only.
	var one contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"sessionId": "hs-s"}, e.ownerHeaders(), &one)
	if one.ScopeTitle == nil || *one.ScopeTitle == "" {
		t.Fatal("an untitled session is named by its code")
	}
	if len(one.Entries) != 4 || one.NextCursor != nil {
		t.Fatalf("session scope: %+v", one)
	}
	// A page of one, then the rest from its cursor.
	var p1, p2 contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"section": "work", "limit": 1}, e.ownerHeaders(), &p1)
	if len(p1.Entries) != 1 || p1.Entries[0].Verb != "answered" || p1.NextCursor == nil {
		t.Fatalf("first page: %+v", p1)
	}
	e.opInto(t, "history_view", map[string]any{"section": "work", "limit": 1, "before": *p1.NextCursor}, e.ownerHeaders(), &p2)
	if len(p2.Entries) != 1 || p2.Entries[0].Verb != "asked" {
		t.Fatalf("second page: %+v", p2)
	}

	// Two trials of one experiment start: one entry of two, titled by the experiment.
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id, examples, variants, evaluators, primary_evaluator, lab_group) VALUES ('hs-x', 'fleet', 'ab', 'p', '[]', '[]', '[]', 'e', 'code-mode')`)
	for _, id := range []string{"hs-t1", "hs-t2"} {
		exec(`INSERT INTO execution (id, kind, state, project_id, experiment_id, started_at, data, observation, config_ref) VALUES ($1, 'agent_run', 'running', 'fleet', 'hs-x', $2, '{}', 'confirmed', '{}')`, id, now.Add(9*time.Second))
	}
	// One trial ends: its start and end are one trial, still one entry for the experiment.
	exec(`UPDATE execution SET state = 'succeeded', ended_at = $1 WHERE id = 'hs-t1'`, now.Add(10*time.Second))
	var lab contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"section": "lab", "kinds": []string{"run"}}, e.ownerHeaders(), &lab)
	if len(lab.Entries) != 1 || lab.Entries[0].N != 2 || lab.Entries[0].Title != "code-mode trial" {
		t.Fatalf("trials: %+v", lab.Entries)
	}
	if d := lab.Entries[0].Detail; string(d["finished"]) != "1" || string(d["running"]) != "1" || lab.Entries[0].Verb != "trials" {
		t.Fatalf("trial counts: %+v", lab.Entries[0])
	}
	// Leaving the project out leaves its trials out.
	var out contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"section": "lab", "excludeProjectIds": []string{"fleet"}}, e.ownerHeaders(), &out)
	if len(out.Entries) != 0 {
		t.Fatalf("excluded project: %+v", out.Entries)
	}
	var x contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"experimentId": "hs-x"}, e.ownerHeaders(), &x)
	if x.ScopeTitle == nil || *x.ScopeTitle != "code-mode" {
		t.Fatalf("experiment scope title: %v", x.ScopeTitle)
	}

	// Two stuck-run problem questions (cause in data at insert, as problems.go asks): system, one entry of two, out of work.
	for _, id := range []string{"r1", "r2"} {
		exec(`INSERT INTO question (id, kind, project_id, text, data) VALUES ($1, 'operator_decision', 'fleet', 'Execution has shown no activity.', $2)`,
			"hs-p-"+id, `{"cause":"problem:execution:`+id+`:stuck","rule":"stuck"}`)
	}
	var sys, work contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"section": "system", "kinds": []string{"question"}}, e.ownerHeaders(), &sys)
	if len(sys.Entries) != 1 || sys.Entries[0].N != 2 {
		t.Fatalf("problem questions: %+v", sys.Entries)
	}
	e.opInto(t, "history_view", map[string]any{"section": "work", "kinds": []string{"question"}}, e.ownerHeaders(), &work)
	for _, x := range work.Entries {
		if strings.HasPrefix(x.Subject, "question:hs-p-") {
			t.Fatalf("a problem question in work: %+v", x)
		}
	}
}
