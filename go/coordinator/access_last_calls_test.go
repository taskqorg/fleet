package main

import (
	"testing"
	"time"
)

// Requirement: access_status shows each connection's latest call and latest success (AccessLastCalls), chosen in
// execution_spend by connection so an index serves it under row level security. Risk: the walk returns an older call,
// another connection's call, a run that is not a tool call, or no success when an older one exists; or an open Access
// screen never learns of new calls (a gateway pass publishes one change per connection it saw a call of).
func TestAccessLastCalls(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	call := func(id, kind, state, conn string, ago time.Duration) {
		at := now.Add(-ago)
		exec(`INSERT INTO execution (id, kind, state, project_id, started_at, data, observation, config_ref)
		 VALUES ($1, $2::execution_kind, $3::execution_state, 'fleet', $4, jsonb_build_object('connection', $5::text, 'tool', 'search', 'startedAt', $6::bigint), 'confirmed', '{}')`,
			id, kind, state, at, conn, at.UnixMilli())
	}
	call("lc-a1", "tool_run", "succeeded", "tavily", 3*time.Minute)
	call("lc-a2", "tool_run", "failed", "tavily", 2*time.Minute)
	call("lc-a3", "agent_run", "succeeded", "tavily", time.Minute) // not a call of the connection
	call("lc-b1", "tool_run", "failed", "meshy", time.Minute)
	err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		rows, err := q.db().AccessLastCalls(q.ctx(), []string{"tavily", "meshy", "none"})
		if err != nil {
			return err
		}
		got := map[string][3]any{}
		for _, r := range rows {
			got[r.Connection] = [3]any{r.LastID, r.LastState, r.OkStartedAt}
		}
		want := map[string][3]any{
			"tavily": {"lc-a2", "failed", now.Add(-3 * time.Minute).UnixMilli()},
			"meshy":  {"lc-b1", "failed", int64(0)},
			"none":   {"", "", int64(0)},
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s: got %v, want %v", k, got[k], w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO api_connection (id, name, provider, holder) VALUES ('connection:tavily', 'tavily', 'tavily', $1)`, testOwnerEmail)
	count := func() (n int) {
		t.Helper()
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM change WHERE kind = 'api-connection' AND id = 'connection:tavily' AND holder = $1`, testOwnerEmail).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
		return q.db().ListSpendWakeGateway(q.ctx(), []string{"lc-a1", "lc-a2", "lc-b1"})
	}); err != nil {
		t.Fatal(err)
	}
	if n := count() - before; n != 1 {
		t.Fatalf("api-connection changes for tavily from a gateway pass: %d, want 1", n)
	}
}
