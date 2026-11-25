package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: library_view (pages/library.md) answers the tree's lists in one read, as definition_list, skills_list,
// bot_list, system_changes and recommendation_changes would, without the 30-day resource rollup. Risk: a list is
// missing from the tree, or the rollup creeps back and the read costs seconds again.
func TestLibraryView(t *testing.T) {
	e := openTestCoordinator(t, false)
	// Runs pinned to the baseline agent: two in the window (one not started yet), one 40 days old, one draft.
	now := time.Now().UTC()
	for _, x := range []struct {
		id      string
		started any
		updated time.Time
		pin     string
	}{
		{"lv-1", now.Add(-time.Hour), now, `[{"kind":"agent","id":"baseline","revision":"1"}]`},
		{"lv-2", nil, now, `[{"kind":"agent","id":"baseline","revision":"1"}]`},
		{"lv-old", now.Add(-40 * 24 * time.Hour), now.Add(-40 * 24 * time.Hour), `[{"kind":"agent","id":"baseline","revision":"1"}]`},
		{"lv-draft", now.Add(-time.Hour), now, `[{"kind":"agent","id":"baseline","revision":"1","draft":true}]`},
	} {
		if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, started_at, data, observation, config_ref, definitions)
		 VALUES ($1, 'agent_run', 'succeeded', $2, '{}', 'confirmed', '{}', $3::jsonb)`, x.id, x.started, x.pin); err != nil {
			t.Fatal(err)
		}
		for _, sql := range []string{`ALTER TABLE execution DISABLE TRIGGER execution_touch`, `UPDATE execution SET updated_at = $2 WHERE id = $1`, `ALTER TABLE execution ENABLE TRIGGER execution_touch`} {
			args := []any{}
			if sql[0] == 'U' {
				args = []any{x.id, x.updated}
			}
			if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	var got contract.LibraryViewReply
	e.opInto(t, "library_view", map[string]any{}, e.ownerHeaders(), &got)
	var agents contract.DefinitionListReply
	e.opInto(t, "definition_list", map[string]any{"kind": "agent"}, e.ownerHeaders(), &agents)
	if len(got.Agents) == 0 || len(got.Agents) != len(agents.Items) {
		t.Fatalf("agents: %d in the view, %d in definition_list", len(got.Agents), len(agents.Items))
	}
	runs := map[string]int64{}
	for _, a := range agents.Items {
		runs[a.Id] = a.Last30.Runs
	}
	for _, a := range got.Agents {
		if a.Last30.Runs != runs[a.Id] || a.Last30.Succeeded > a.Last30.Runs {
			t.Fatalf("%s: %d runs in the view, %d in definition_list", a.Id, a.Last30.Runs, runs[a.Id])
		}
		if a.Id == "baseline" && a.Last30.Runs != 2 {
			t.Fatalf("baseline: the started and the unstarted run of the window, not the old or the draft one: %d", a.Last30.Runs)
		}
		if r := a.Last30.Resources; r == nil || r.Tokens.Total != nil || len(r.Cost) != 0 {
			t.Fatalf("%s: last30 carries a resource rollup: %+v", a.Id, r)
		}
	}
	if got.Pipelines == nil || got.Methods == nil || got.Skills == nil || got.Bots == nil || got.Recommendations == nil || got.Changes.Versions.Hosts == nil {
		t.Fatalf("every list is present (empty, not null): %+v", got)
	}
}
