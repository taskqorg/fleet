package main

import (
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
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
	// The header focus: experiments of the focus only, ignored projects left out.
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO experiment (id, project_id, kind, pipeline_id) VALUES ('lv-a', 'fleet', 'single', 'p'), ('lv-b', 'dam', 'single', 'p')`); err != nil {
		t.Fatal(err)
	}
	ids := func(r contract.LabViewReply) string {
		var out []string
		for _, x := range r.Experiments {
			out = append(out, x.Id)
		}
		return strings.Join(out, ",")
	}
	var dam, notDam contract.LabViewReply
	e.opInto(t, "lab_view", map[string]any{"projectIds": []string{"dam"}}, e.ownerHeaders(), &dam)
	e.opInto(t, "lab_view", map[string]any{"excludeProjectIds": []string{"dam"}}, e.ownerHeaders(), &notDam)
	// The leaderboard takes the focus too: a trial of dam counts under dam and not without it.
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, project_id, experiment_id, variant, started_at, ended_at, data, observation, config_ref)
	 VALUES ('lv-r', 'pipeline_run', 'succeeded', 'dam', 'lv-b', 'v1', now(), now(), '{}', 'confirmed', '{}')`); err != nil {
		t.Fatal(err)
	}
	trials := func(only, without []string) int {
		var rows []db.LabLeaderboardTrialsRow
		if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) {
			rows, err = q.db().LabLeaderboardTrials(q.ctx(), db.LabLeaderboardTrialsParams{ProjectIds: only, ExcludeProjectIds: without, Days: 30})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rows {
			if r.ID == "lv-r" {
				n++
			}
		}
		return n
	}
	if trials(nil, nil) != 1 || trials([]string{"dam", "fleet"}, nil) != 1 || trials([]string{"fleet"}, nil) != 0 || trials(nil, []string{"dam"}) != 0 {
		t.Fatal("leaderboard trials do not follow projectIds and excludeProjectIds")
	}
	if ids(dam) != "lv-b" || strings.Contains(ids(notDam), "lv-b") || !strings.Contains(ids(notDam), "lv-a") {
		t.Fatalf("focus: dam %q, without dam %q", ids(dam), ids(notDam))
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

// Requirement: a settled trial's lineage is kept, an unsettled one is read again, so the comparison's recompute reads
// only new trials. Risk: a running trial's leaves freeze, or a kept lineage is shared across scopes.
func TestLabLineageCache(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`SELECT set_config('fleet.project_ids', '*', false)`)
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id) VALUES ('ll-x', 'fleet', 'single', 'p')`)
	old := time.Now().Add(-time.Hour)
	for _, id := range []string{"ll-done", "ll-live"} {
		exec(`INSERT INTO execution (id, kind, state, experiment_id, variant, started_at, ended_at, data, observation, config_ref)
		 VALUES ($1, 'pipeline_run', 'succeeded', 'll-x', 'v', $2, $2, '{}', 'confirmed', '{}')`, id, old)
	}
	child := func(id, parent string) {
		exec(`INSERT INTO execution (id, kind, state, parent_execution_id, started_at, data, observation, config_ref) VALUES ($1, 'agent_run', 'succeeded', $2, now(), '{}', 'confirmed', '{}')`, id, parent)
	}
	child("ll-c1", "ll-done")
	child("ll-c2", "ll-live")
	read := func() map[string]int {
		t.Helper()
		var got map[string]int
		err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
			l, err := labTrialsLineage(q, "test", []string{"ll-done", "ll-live"}, map[string]bool{"ll-done": true})
			got = map[string]int{}
			for _, k := range l.ScopeKeys {
				got[k]++
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got["ll-done"] != 1 || got["ll-live"] != 1 {
		t.Fatalf("first read: %v", got)
	}
	child("ll-c3", "ll-done")
	child("ll-c4", "ll-live")
	if got := read(); got["ll-done"] != 1 || got["ll-live"] != 2 {
		t.Fatalf("the settled trial's lineage is kept, the other read again: %v", got)
	}
}
