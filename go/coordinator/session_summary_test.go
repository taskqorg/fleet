package main

import (
	"testing"
	"time"
)

// Requirement: session_summary (migration 116) holds a session's state, activity, run count and open questions as a
// recount would give them, after every write that changes them. Risk: a list shows a session as running after its run
// ended, misses new activity, or counts runs or questions wrong.
func TestSessionSummaryMatchesRecount(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	check := func(step string) {
		t.Helper()
		var got, want struct {
			state          string
			executions     int64
			openQuestions  int64
			activityLagsMs float64
		}
		err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT ss.state, ss.executions, ss.open_questions,
		  extract(epoch FROM greatest(d.updated_at,
		   (SELECT max(greatest(x.started_at, x.ended_at)) FROM execution x WHERE x.session_id = d.id),
		   (SELECT max(m.updated_at) FROM message m WHERE m.session_id = d.id),
		   (SELECT max(v.updated_at) FROM event v WHERE v.session_id = d.id AND NOT v.delta)) - greatest(d.updated_at, ss.activity_at)) * 1000
		 FROM session d JOIN session_summary ss ON ss.session_id = d.id WHERE d.id = 'sum-s'`).Scan(&got.state, &got.executions, &got.openQuestions, &got.activityLagsMs)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		err = e.ownerStore.pool.QueryRow(e.ctx, `SELECT (SELECT state FROM fleet_conversation_state('sum-s')),
		  (SELECT count(*) FROM execution WHERE session_id = 'sum-s'),
		  (SELECT count(*) FROM question WHERE session_id = 'sum-s' AND state = 'open')`).Scan(&want.state, &want.executions, &want.openQuestions)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got.state != want.state || got.executions != want.executions || got.openQuestions != want.openQuestions || got.activityLagsMs > 0 {
			t.Fatalf("%s: summary %+v, recount %+v", step, got, want)
		}
	}
	exec(`INSERT INTO session (id, created_at) VALUES ('sum-s', now())`)
	check("new session")
	at := time.Now().UTC()
	exec(`INSERT INTO execution (id, kind, state, session_id, harness, started_at, data, observation, config_ref) VALUES ('sum-r1', 'agent_run', 'running', 'sum-s', 'codex', $1, '{}', 'confirmed', '{}')`, at)
	check("a run starts")
	exec(`INSERT INTO message (id, session_id, execution_id, text) VALUES ('sum-m1', 'sum-s', 'sum-r1', 'hello')`)
	exec(`INSERT INTO event (id, execution_id, session_id, event_seq, type, at, payload, delta) VALUES ('sum-e1', 'sum-r1', 'sum-s', 0, 'item.completed', now(), '{}', false)`)
	check("a message and an event")
	exec(`INSERT INTO question (id, kind, session_id, execution_id, text) VALUES ('sum-q1', 'agent_question', 'sum-s', 'sum-r1', 'ok?')`)
	check("a question opens")
	exec(`UPDATE question SET state = 'answered' WHERE id = 'sum-q1'`)
	exec(`UPDATE execution SET state = 'succeeded', ended_at = $1 WHERE id = 'sum-r1'`, at.Add(time.Minute))
	check("the run ends and the question is answered")
	exec(`INSERT INTO execution (id, kind, state, session_id, started_at, data, observation, config_ref) VALUES ('sum-t1', 'tool_run', 'succeeded', 'sum-s', $1, '{}', 'confirmed', '{}')`, at.Add(2*time.Minute))
	check("a tool call")
	exec(`INSERT INTO execution (id, kind, state, session_id, started_at, data, observation, config_ref) VALUES ('sum-r2', 'agent_run', 'failed', 'sum-s', $1, '{}', 'confirmed', '{}')`, at.Add(3*time.Minute))
	check("a second run fails")
	exec(`DELETE FROM execution WHERE id = 'sum-r2'`)
	check("the failed run is deleted")
}
