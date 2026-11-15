package main

import (
	"testing"
	"time"
)

// Requirement: typed step timing uses observed terminal ends, including retained
// pre-column history; reopened/unknown work must not acquire a fabricated end.
// Risk: succeeded steps display not-finished, stale ends survive a retry, or bad
// legacy JSON breaks task/execution reads and container totals double count.
func TestPipelineStepTerminalTimingProjection(t *testing.T) {
	env := openTestCoordinator(t, false)
	start := nowMs() - 3000
	finish := start + 1234
	cases := []struct {
		id, state string
		legacy    any
		column    bool
		want      bool
	}{
		{"legacy-ok", "succeeded", finish, false, true},
		{"legacy-failed", "failed", finish, false, true},
		{"legacy-cancelled", "cancelled", finish, false, true},
		{"legacy-missing", "succeeded", nil, false, false},
		{"legacy-string", "succeeded", "yesterday", false, false},
		{"legacy-huge", "succeeded", 1e300, false, false},
		{"legacy-negative", "succeeded", -1, false, false},
		{"legacy-fraction", "succeeded", float64(finish) + 0.5, false, false},
		{"reopened-stale", "running", finish, true, false},
		{"typed-column", "succeeded", nil, true, true},
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if _, e := insertExecution(q, doc{"id": "legacy-pipeline", "kind": "pipeline_run", "projectId": "hello-world", "data": doc{"pipelineId": "timing"}}); e != nil {
			return e
		}
		for _, c := range cases {
			var end any
			if c.column {
				end = finish
			}
			if _, e := insertExecution(q, doc{"id": c.id, "kind": "pipeline_step", "state": c.state, "projectId": "hello-world", "parentExecutionId": "legacy-pipeline", "startedAt": start, "endedAt": end, "data": doc{"finishedAt": c.legacy, "durationMs": 1234, "updatedAt": nowMs()}}); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, reply := env.callJSON(t, "POST", "/api/ops/executions_list", doc{"parentExecutionId": "legacy-pipeline"}, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, reply)
	}
	got := map[string]doc{}
	for _, v := range anys(reply["executions"]) {
		r := mapOf(v)
		got[astr(r["id"])] = r
	}
	for _, c := range cases {
		r := got[c.id]
		if r == nil {
			t.Fatal("missing row", c.id)
		}
		if !c.want {
			if r["endedAt"] != nil {
				t.Fatal("fabricated end", c.id, r["endedAt"])
			}
			continue
		}
		tm, e := time.Parse(time.RFC3339Nano, astr(r["endedAt"]))
		if e != nil || tm.UnixMilli() != finish {
			t.Fatal("wrong retained end", c.id, r["endedAt"], e)
		}
	}
	run := planFixture(t, "persisted-pipeline")
	run["startedAt"] = start
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if e := putRun(q, run); e != nil {
			return e
		}
		if e := selectPipelineStep(q, run, 0); e != nil {
			return e
		}
		selected := mapOf(anys(run["steps"])[0])
		selected["state"], selected["startedAt"], selected["finishedAt"] = "succeeded", start, finish
		if e := putRun(q, run); e != nil {
			return e
		}
		var end *time.Time
		if e := q.tx.QueryRow(q.ctx(), "SELECT ended_at FROM execution WHERE id=$1", stepRow(run, 0)).Scan(&end); e != nil {
			return e
		}
		if end == nil || end.UnixMilli() != finish {
			t.Fatal("terminal end not persisted", end)
		}
		step := mapOf(anys(run["steps"])[0])
		step["state"] = "running"
		step["startedAt"] = finish + 1 // retained old finish must not leak into this attempt
		if e := putRun(q, run); e != nil {
			return e
		}
		if e := q.tx.QueryRow(q.ctx(), "SELECT ended_at FROM execution WHERE id=$1", stepRow(run, 0)).Scan(&end); e != nil {
			return e
		}
		if end != nil {
			t.Fatal("reopen retained stale end", end)
		}
		if e := writeTotals(q, stepRow(run, 0)); e != nil {
			return e
		}
		var n int
		if e := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution_totals WHERE execution_id=$1", stepRow(run, 0)).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("pipeline container counted", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
