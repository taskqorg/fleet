package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: lab_view (pages/laboratory.md) answers the Lab screen as its four reads would: the experiments, the
// registry, the open ratings and the last 30 days' leaderboard. Risk: a part drifts from its own operation.
func TestLabView(t *testing.T) {
	e := openTestCoordinator(t, false)
	var got contract.LabViewReply
	e.opInto(t, "lab_view", map[string]any{}, e.ownerHeaders(), &got)
	var reg contract.LabRegistryReply
	e.opInto(t, "lab_registry", map[string]any{}, e.ownerHeaders(), &reg)
	var lb contract.LabLeaderboardReply
	e.opInto(t, "lab_leaderboard", map[string]any{"days": 30}, e.ownerHeaders(), &lb)
	if len(got.UseCases) == 0 || len(got.UseCases) != len(reg.UseCases) {
		t.Fatalf("use cases: %d in the view, %d in lab_registry", len(got.UseCases), len(reg.UseCases))
	}
	if got.Experiments == nil || got.Ratings == nil || len(got.Leaderboard.Entries) != len(lb.Entries) || len(got.Leaderboard.Collections) != len(lb.Collections) {
		t.Fatalf("parts: %+v", got)
	}
}

// Requirement: lab_trial keeps each Lab trial's leaderboard facts equal to its run's data, so the leaderboard never
// reads the run's data (its snapshots). Risk: the facts drift after a run's data changes and the comparison shows an
// old duration or misses a use case.
func TestLabTrialFacts(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`SELECT set_config('fleet.project_ids', '*', false)`)
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id) VALUES ('lt-x', 'fleet', 'single', 'p')`)
	exec(`INSERT INTO execution (id, kind, state, experiment_id, variant, started_at, data, observation, config_ref)
	 VALUES ('lt-r', 'pipeline_run', 'running', 'lt-x', 'v1', now(), '{"cell":{"example":"ex-1"},"example":{"use_case_id":"uc-1"}}', 'confirmed', '{}')`)
	check := func(step, example, useCase string, ms float64) {
		t.Helper()
		var a, b string
		var d float64
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT cell_example, example_use_case_id, duration_ms FROM lab_trial WHERE execution_id = 'lt-r'`).Scan(&a, &b, &d); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if a != example || b != useCase || d != ms {
			t.Fatalf("%s: %q %q %v, want %q %q %v", step, a, b, d, example, useCase, ms)
		}
	}
	check("insert", "ex-1", "uc-1", -1)
	exec(`UPDATE execution SET state = 'succeeded', data = data || '{"durationMs": 1234.5}' WHERE id = 'lt-r'`)
	check("the run ends with its duration", "ex-1", "uc-1", 1234.5)
	exec(`UPDATE execution SET data = jsonb_set(data, '{cell,example}', '"ex-2"') WHERE id = 'lt-r'`)
	check("the cell's example changes", "ex-2", "uc-1", 1234.5)

	// The trial's state is a fact too, and its change moves the version the comparison's cache is kept by.
	version := func() string {
		t.Helper()
		var v string
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state||'|'||(SELECT count(*) FROM lab_trial)||'|'||(SELECT max(changed_at) FROM lab_trial)::text FROM lab_trial WHERE execution_id = 'lt-r'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version()
	exec(`UPDATE execution SET state = 'failed' WHERE id = 'lt-r'`)
	if after := version(); after == before || !strings.HasPrefix(after, "failed|") {
		t.Fatalf("a state change moves the trial's facts: %q then %q", before, after)
	}
}
