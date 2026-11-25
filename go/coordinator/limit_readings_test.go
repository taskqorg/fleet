package main

import (
	"testing"
)

// Requirement: subscription_limits, access_status and dispatch read the latest limit reading per subscription and
// period at any age (a 30-day window, a reading without a reset time), the newest event winning, delta events never,
// and row-level security leaving a person the latest reading of what they see; risk (2026-11-26): latestReadings read
// and sorted every reading ever written (1.2-7 s on prod), and a cut by age dropped 30-day windows from dispatch.
func TestLatestReadingsAnyAge(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, x := range [][2]string{{"lr-x1", "desiign"}, {"lr-x2", "hello-world"}} {
		exec(`INSERT INTO execution (id, kind, state, project_id, account_id, host_id, started_at, data, observation, config_ref)
		 VALUES ($1, 'agent_run', 'succeeded', $2, 'acct-1', 'h1', now(), '{}', 'confirmed', '{}')`, x[0], x[1])
	}
	ev := func(id, execution, project string, delta bool, readings string, ago string) {
		exec(`INSERT INTO event (id, execution_id, project_id, event_seq, type, at, payload, delta, updated_at)
		 VALUES ($1, $2, $3, 0, 'limit_reading', now(), jsonb_build_object('data', jsonb_build_object('readings', $4::jsonb)), $5, now() - $6::interval)`,
			id, execution, project, readings, delta, ago)
	}
	ev("lr-e1", "lr-x1", "desiign", false, `[{"period":"monthly","pct":40,"resetsAt":9999999999999},{"period":"weekly","pct":10}]`, "40 days")
	ev("lr-e2", "lr-x1", "desiign", false, `[{"period":"weekly","pct":60}]`, "1 day")
	ev("lr-e3", "lr-x1", "desiign", true, `[{"period":"weekly","pct":99}]`, "0 days") // a delta: never a reading
	ev("lr-e4", "lr-x2", "hello-world", false, `[{"period":"weekly","pct":75}]`, "0 days")
	// Two readings of one period in one statement: the later event wins, and the statement does not fail.
	exec(`INSERT INTO event (id, execution_id, project_id, event_seq, type, at, payload, delta) VALUES
	 ('lr-e5', 'lr-x1', 'desiign', 1, 'limit_reading', now(), '{"data":{"readings":[{"period":"5h","pct":20}]}}', false),
	 ('lr-e6', 'lr-x1', 'desiign', 2, 'limit_reading', now(), '{"data":{"readings":[{"period":"5h","pct":30}]}}', false)`)

	pct := func(rs map[string][]Reading, period string) float64 {
		for _, r := range rs["acct-1"] {
			if r.Period == period && r.Pct != nil {
				return *r.Pct
			}
		}
		return -1
	}
	all, err := latestReadings(e.ctx, e.app)
	if err != nil {
		t.Fatal(err)
	}
	if got := pct(all, "monthly"); got != 40 {
		t.Errorf("monthly (40 days old) = %v, want 40", got)
	}
	if got := pct(all, "weekly"); got != 75 {
		t.Errorf("weekly = %v, want 75 (the newest event, hello-world)", got)
	}
	if got := pct(all, "5h"); got != 30 {
		t.Errorf("5h = %v, want 30 (the later event of one statement)", got)
	}
	// A delta that turns final counts, in its own scope, ordered by its event's sequence as before.
	exec(`UPDATE event SET delta = false WHERE id = 'lr-e3'`)
	var final float64
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT (payload->>'pct')::float FROM limit_reading_latest WHERE account_id = 'acct-1' AND period = 'weekly' AND scope = 'desiign'`).Scan(&final); err != nil {
		t.Fatal(err)
	}
	if final != 99 {
		t.Errorf("desiign weekly after the delta turned final = %v, want 99", final)
	}

	// A person who sees only hello-world gets its reading, not desiign's newer one.
	tx, err := e.ownerStore.pool.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `SELECT set_config('fleet.project_ids', 'hello-world', true), set_config('fleet.person', 'member@trial.test', true)`); err != nil {
		t.Fatal(err)
	}
	var weekly float64
	if err := tx.QueryRow(e.ctx, `SELECT (payload->>'pct')::float FROM limit_reading_latest WHERE account_id = 'acct-1' AND period = 'weekly' ORDER BY event_seq DESC LIMIT 1`).Scan(&weekly); err != nil {
		t.Fatal(err)
	}
	if weekly != 75 {
		t.Errorf("hello-world member sees weekly %v, want 75", weekly)
	}
}
