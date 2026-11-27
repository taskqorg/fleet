package main

import (
	"testing"
	"time"
)

// Requirement: executions_reconcile reconciles a preparing/open pipeline_step whose parent pipeline_run already reached
// a terminal state, leaving no dangling step in preparing (2026-11-10 pipeline_step cleanup).
func TestReconcileTerminalParentPipelineStep(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	exec(`INSERT INTO execution (id, kind, state, observation, started_at, ended_at, updated_at, problem, data, config_ref)
	 VALUES ('rec-run', 'pipeline_run', 'failed', 'confirmed', $1, $1, $1, 'step:cancelled', '{}', '{}')`, old)
	exec(`INSERT INTO execution (id, kind, state, observation, parent_execution_id, updated_at, data, config_ref)
	 VALUES ('rec-run:step:3', 'pipeline_step', 'preparing', 'confirmed', 'rec-run', $1, '{"id":"check","index":3,"kind":"script","state":"pending"}', '{}')`, old)

	var rep struct {
		Reconciled []string `json:"reconciled"`
	}
	e.opInto(t, "executions_reconcile", map[string]any{}, e.ownerHeaders(), &rep)
	if len(rep.Reconciled) != 1 || rep.Reconciled[0] != "rec-run:step:3" {
		t.Fatalf("reconciled: %+v, want [rec-run:step:3]", rep.Reconciled)
	}

	var state, problem string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state::text, coalesce(problem, '') FROM execution WHERE id = 'rec-run:step:3'`).Scan(&state, &problem); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || problem != "unknown:abandoned" {
		t.Fatalf("step state=%q problem=%q, want failed / unknown:abandoned", state, problem)
	}
}
