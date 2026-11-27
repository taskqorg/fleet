package main

import (
	"strings"
	"testing"
	"time"
)

// Requirement: the runners' tool-call groups read closed hours from runner_call_hour (migration 115) and the rest
// live. Risk: a call is counted twice (in a summed hour and live) or not at all, or the pass drops a call that ended
// in a closed hour. The groups read before and after the pass must count the same calls.
func TestRunnerCallHoursMatchLive(t *testing.T) {
	e := openTestCoordinator(t, false)
	now := time.Now().UTC()
	hourAgo := now.Truncate(time.Hour).Add(-90 * time.Minute)
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, host_id, started_at, ended_at, data, observation, config_ref)
	 VALUES ('call-a', 'tool_run', 'succeeded', 'h1', $1, $1, '{"connection":"github"}', 'confirmed', '{}'),
	        ('call-b', 'tool_run', 'failed', 'h1', $1, $1, '{"connection":"github"}', 'confirmed', '{}'),
	        ('call-c', 'tool_run', 'succeeded', 'h1', $2, $2, '{"connection":"github"}', 'confirmed', '{}'),
	        ('call-d', 'tool_run', 'running', 'h1', $2, NULL, '{"connection":"github"}', 'confirmed', '{}')`, hourAgo, now); err != nil {
		t.Fatal(err)
	}
	count := func() (calls, failed int64, running bool) {
		t.Helper()
		if err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
			from, err := q.db().RunnerCallLiveFrom(q.ctx())
			if err != nil {
				return err
			}
			gs, err := q.db().RunnerCallGroups(q.ctx(), from)
			for _, g := range gs {
				if g.Host == "h1" && g.Caller == "conn:github" {
					calls, failed, running = g.Calls, g.Failed, g.Running
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	c0, f0, r0 := count()
	if c0 != 4 || f0 != 1 || !r0 {
		t.Fatalf("live: %d calls, %d failed, running %v; want 4, 1, true", c0, f0, r0)
	}
	if err := runnerCallHours(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if c, f, r := count(); c != c0 || f != f0 || r != r0 {
		t.Fatalf("after the pass: %d calls, %d failed, running %v; want %d, %d, %v", c, f, r, c0, f0, r0)
	}
	if err := runnerCallHours(e.ctx, e.app); err != nil { // summing again replaces the hours, never adds to them
		t.Fatal(err)
	}
	if c, _, _ := count(); c != c0 {
		t.Fatalf("after a second pass: %d calls, want %d", c, c0)
	}

	t.Run("bounded detail", func(t *testing.T) {
		if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, started_at, ended_at) VALUES
		 ('runner-current', 'agent_run', 'running', now() - interval '2 days', null),
		 ('runner-old', 'host_job', 'succeeded', now() - interval '2 days', now() - interval '2 days')`); err != nil {
			t.Fatal(err)
		}
		if err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
			rec := &recordingTx{Tx: q.tx, by: map[string]*recordedStatement{}}
			sub := *q
			sub.tx = rec
			runs, err := sub.db().RunnerRuns(sub.ctx())
			if err != nil {
				return err
			}
			if len(runs) != 1 || runs[0].ID != "runner-current" {
				t.Fatalf("recent and active runners: %+v", runs)
			}
			stmt := rec.by[rec.order[0]]
			plan, err := explainOne(q, stmt.sql, stmt.args)
			if err != nil {
				return err
			}
			if !strings.Contains(plan, "CTE pick") || !strings.Contains(plan, "Limit") || strings.Contains(plan, "HashAggregate") {
				t.Fatalf("runner detail must filter and limit its candidates without deduplicating a union:\n%s", plan)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
