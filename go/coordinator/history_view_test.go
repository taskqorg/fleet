package main

import (
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

	var all contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{}, e.ownerHeaders(), &all)
	got := map[string]contract.HistoryEntry{}
	for _, x := range all.Entries {
		got[x.Subject+" "+x.Verb] = x
	}
	if len(all.Entries) != 4 {
		t.Fatalf("entries %+v, want 4: the host change, the run failed and started, three host jobs as one", all.Entries)
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
	if all.Entries[0].Subject != "host:mac" {
		t.Fatalf("newest first: %s", all.Entries[0].Subject)
	}

	// One object: the session's entries only.
	var one contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"sessionId": "hs-s"}, e.ownerHeaders(), &one)
	if len(one.Entries) != 2 || one.NextCursor != nil {
		t.Fatalf("session scope: %+v", one)
	}
	// A page of one, then the rest from its cursor.
	var p1, p2 contract.HistoryViewReply
	e.opInto(t, "history_view", map[string]any{"section": "work", "limit": 1}, e.ownerHeaders(), &p1)
	if len(p1.Entries) != 1 || p1.Entries[0].Verb != "failed" || p1.NextCursor == nil {
		t.Fatalf("first page: %+v", p1)
	}
	e.opInto(t, "history_view", map[string]any{"section": "work", "limit": 1, "before": *p1.NextCursor}, e.ownerHeaders(), &p2)
	if len(p2.Entries) != 1 || p2.Entries[0].Verb != "started" {
		t.Fatalf("second page: %+v", p2)
	}
}
