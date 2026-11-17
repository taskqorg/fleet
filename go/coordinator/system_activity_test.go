package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: docs/designs/fleet-analytics.md, System activity: a prepared project summary leaves Fleet's own calls
// out like the live reads. Risk: prepared project resources keep counting the coordinator's reads.
func TestSystemActivityNotInPreparedContribution(t *testing.T) {
	for leaf, want := range map[string]bool{
		`{"project_id":"p1","kind":"tool_run","state":"succeeded","has_totals":true}`:                              true,
		`{"project_id":"p1","kind":"tool_run","state":"succeeded","has_totals":true,"system_actor":"coordinator"}`: false,
	} {
		c, _, err := resourceContribution("call:x", resourceSnapshot{Leaf: json.RawMessage(leaf)})
		if err != nil || c.Included != want {
			t.Fatalf("%s: included %v, want %v (%v)", leaf, c.Included, want, err)
		}
	}
}

// Requirement: docs/designs/fleet-analytics.md, System activity: the coordinator's own reads (a read token, its calls
// marked systemActor coordinator) stay under the project whose grant allowed them, out of its analytics, execution
// list and resources by default, and reachable with system include or only. Risk: Fleet's background reads fill a
// project's Executions count, failure chips and execution lists, or vanish from every view.
func TestSystemActivityLeftOutOfProjectWork(t *testing.T) {
	e := openTestCoordinator(t, false)
	ctx := e.ctx
	if _, err := e.ownerStore.pool.Exec(ctx, `SET fleet.project_ids='*';
	 INSERT INTO document(kind,id,data) VALUES
	  ('run-token','run-token:sys','{"id":"run-token:sys","sha256":"sys","executionId":"github-read:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","projectId":"p1","step":true,"systemActor":"coordinator"}'),
	  ('run-token','run-token:agent','{"id":"run-token:agent","sha256":"agent","executionId":"a-run","projectId":"p1"}');
	 INSERT INTO execution(id,kind,state,project_id,started_at,ended_at) VALUES ('a-run','agent_run','succeeded','p1',now()-interval '1 minute',now())`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, l := range []contract.CallLine{
		{CallId: "sys-1", TokenSha256: strPtr("sys"), ExecutionId: strPtr("github-read:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"), Tool: "pulls_open", Connection: "github", Outcome: contract.CallOutcomeOk, Status: 200, StartedAt: now - 2000, Ms: 10},
		{CallId: "sys-2", TokenSha256: strPtr("sys"), ExecutionId: strPtr("github-read:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"), Tool: "releases", Connection: "github", Outcome: contract.CallOutcomeError, ErrorCode: strPtr("http-404"), Status: 404, StartedAt: now - 1000, Ms: 10},
		{CallId: "agent-1", TokenSha256: strPtr("agent"), ExecutionId: strPtr("a-run"), Tool: "pulls_open", Connection: "github", Outcome: contract.CallOutcomeOk, Status: 200, StartedAt: now - 1500, Ms: 10},
	} {
		if err := runTx(ctx, e.app, nil, true, "", func(q *Request) error { _, err := connRecord(q, l); return err }); err != nil {
			t.Fatal(err)
		}
	}
	var marked []string
	if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
		rs, err := q.tx.Query(q.ctx(), "SELECT id FROM execution WHERE system_actor='coordinator' AND project_id='p1' ORDER BY id")
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var id string
			if err := rs.Scan(&id); err != nil {
				return err
			}
			marked = append(marked, id)
		}
		return rs.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(marked, []string{"call:sys-1", "call:sys-2"}) {
		t.Fatalf("marked system calls: %v", marked)
	}

	p1 := "p1"
	list := func(s *contract.SystemActivity) []string {
		var ids []string
		if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
			rows, err := executionsList(q, contract.ExecutionsListRequest{ProjectId: &p1, System: s})
			for _, r := range rows {
				ids = append(ids, r.Id)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		return ids
	}
	sys := func(v contract.SystemActivity) *contract.SystemActivity { return &v }
	if got := list(nil); !slices.Equal(got, []string{"a-run", "call:agent-1"}) {
		t.Fatalf("default list: %v", got)
	}
	if got := list(sys(contract.SystemActivityOnly)); !slices.Equal(got, []string{"call:sys-1", "call:sys-2"}) {
		t.Fatalf("system only: %v", got)
	}
	if got := list(sys(contract.SystemActivityInclude)); len(got) != 4 {
		t.Fatalf("system include: %v", got)
	}
	readToken := "github-read:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
	if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
		rows, err := executionsList(q, contract.ExecutionsListRequest{ParentExecutionId: &readToken})
		if err == nil && len(rows) != 2 {
			err = fmt.Errorf("a read token's calls by parent: %d rows, want 2", len(rows))
		}
		if err != nil {
			return err
		}
		rep, err := analyticsQuery(q, &contract.AnalyticsQueryRequest{Filters: &contract.AnalyticsFilter{Parent: []string{readToken}},
			Window:   contract.AnalyticsWindow{From: json.RawMessage(fmt.Sprint(now - 3600e3)), To: json.RawMessage(fmt.Sprint(now + 3600e3))},
			Measures: []contract.AnalyticsMeasure{contract.AnalyticsMeasureExecutions}})
		if m := rep.Totals.Measures["executions"]; err == nil && (m.Value == nil || *m.Value != 2) {
			err = fmt.Errorf("analytics of a read token's calls by parent: %v", m.Value)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	count := func(s *contract.SystemActivity, dims ...contract.AnalyticsDimension) (int64, int64) {
		var rep contract.AnalyticsQueryReply
		if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
			var err error
			rep, err = analyticsQuery(q, &contract.AnalyticsQueryRequest{ProjectId: &p1, System: s, Dimensions: dims,
				Window:   contract.AnalyticsWindow{From: json.RawMessage(fmt.Sprint(now - 3600e3)), To: json.RawMessage(fmt.Sprint(now + 3600e3))},
				Measures: []contract.AnalyticsMeasure{contract.AnalyticsMeasureExecutions, contract.AnalyticsMeasureFailed}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		v := func(k string) int64 {
			if m, ok := rep.Totals.Measures[k]; ok && m.Value != nil {
				return int64(*m.Value)
			}
			return 0
		}
		return v("executions"), v("failed")
	}
	if n, f := count(nil); n != 2 || f != 0 {
		t.Fatalf("default analytics: %d executions, %d failed", n, f)
	}
	if n, f := count(sys(contract.SystemActivityOnly)); n != 2 || f != 1 {
		t.Fatalf("system only analytics: %d executions, %d failed", n, f)
	}
	if n, _ := count(sys(contract.SystemActivityInclude)); n != 4 {
		t.Fatalf("system include analytics: %d executions", n)
	}
	// A grouping dimension never changes which rows count.
	if n, f := count(nil, contract.AnalyticsDimensionConnection); n != 2 || f != 0 {
		t.Fatalf("connection dimension analytics: %d executions, %d failed", n, f)
	}
	if n, f := count(sys(contract.SystemActivityInclude), contract.AnalyticsDimensionConnection); n != 4 || f != 1 {
		t.Fatalf("connection dimension with system include: %d executions, %d failed", n, f)
	}
	if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
		v, _, err := spend(q, map[string]any{"scope": "project", "scope_id": p1, "unit": "wall_ms"})
		if err == nil && v != 10.0 {
			err = fmt.Errorf("project wall_ms spend %v, want 10 (the agent's call, not the coordinator's reads)", v)
		}
		if err != nil {
			return err
		}
		calls, _, err := spend(q, map[string]any{"scope": "connection", "scope_id": "github", "unit": "calls"})
		if err == nil && calls != 3.0 {
			err = fmt.Errorf("github connection calls %v, want 3 (every call on it)", calls)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := runTx(ctx, e.app, nil, false, "", func(q *Request) error {
		s, err := projectResources(q, p1)
		if err == nil && s.Executions != 2 {
			err = fmt.Errorf("project resources count %d executions, want 2", s.Executions)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Requirement: migration 69 marks the earlier rows of the coordinator's own reads and every read token (revoked ones
// too), and systemReadsCleanup deletes those rows (owner decision) with their totals once no spool line names them.
// Risk: legacy reads keep inflating project analytics, the deletion reaches an agent's or a person's call, a deleted
// call is ingested again from the spool with its key side effects (a refused key disabled again), or later calls go
// unmarked.
func TestMigration69RemovesLegacySystemReads(t *testing.T) {
	e := openTestCoordinator(t, false)
	ownerURL := fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", e.dbName, e.sockDir, e.pgPort)
	if _, err := e.ownerStore.pool.Exec(e.ctx, `SET fleet.project_ids='*';
	 INSERT INTO document(kind,id,data) VALUES
	  ('run-token','run-token:open','{"id":"run-token:open","sha256":"open","executionId":"bugsink-read:0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","projectId":"p1","step":true}'),
	  ('run-token','run-token:revoked','{"id":"run-token:revoked","sha256":"revoked","executionId":"github-read:11111111-2222-3333-4444-555555555555","projectId":"p1","step":true,"revokedAt":1}'),
	  ('run-token','run-token:agent','{"id":"run-token:agent","sha256":"agent","executionId":"a-run","projectId":"p1"}');
	 INSERT INTO execution(id,kind,state,project_id,parent_execution_id,started_at,data) VALUES
	  ('a-run','agent_run','succeeded','p1',NULL,now(),'{}'),
	  ('call:old-1','tool_run','succeeded','p1','github-read:11111111-2222-3333-4444-555555555555',now(),'{}'),
	  ('call:old-2','tool_run','failed','p2','github-amoenum-read:11111111-2222-3333-4444-666666666666',NULL,'{}'),
	  ('call:old-3','tool_run','succeeded','p1',NULL,now(),'{"tokenName":"github-read"}'),
	  ('call:spooled','tool_run','failed','p1','github-read:11111111-2222-3333-4444-555555555555',now(),'{}'),
	  ('call:personal','tool_run','succeeded','p1',NULL,now(),'{"tokenName":"owner-laptop"}'),
	  ('call:own-github-read','tool_run','succeeded',NULL,NULL,now(),'{"tokenName":"github-read"}'),
	  ('call:agent','tool_run','succeeded','p1','a-run',now(),'{}');
	 INSERT INTO execution_totals(execution_id,project_id,wall_ms) VALUES ('call:old-1','p1',5),('call:agent','p1',7);
	 DELETE FROM schema_migration WHERE version=69`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(e.ctx, ownerURL); err != nil {
		t.Fatal(err)
	}
	state := func() (left, system []string, totals int, marked, unmarked bool, work int) {
		if err := runTx(e.ctx, e.app, nil, false, "", func(q *Request) error {
			return q.tx.QueryRow(q.ctx(), `SELECT (SELECT array_agg(id ORDER BY id) FROM execution),
			  (SELECT array_agg(id ORDER BY id) FROM execution WHERE system_actor='coordinator'),
			  (SELECT count(*) FROM execution_totals),
			  (SELECT bool_and(data->>'systemActor'='coordinator') FROM document WHERE id IN ('run-token:open','run-token:revoked')),
			  (SELECT NOT data ? 'systemActor' FROM document WHERE id='run-token:agent'),
			  (SELECT count(DISTINCT execution_id) FROM resource_work WHERE execution_id LIKE 'call:old-%')`).Scan(&left, &system, &totals, &marked, &unmarked, &work)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	legacy := []string{"call:old-1", "call:old-2", "call:old-3", "call:spooled"}
	left, system, totals, marked, unmarked, work := state()
	if len(left) != 8 || !slices.Equal(system, legacy) || totals != 2 || !marked || !unmarked || work != 3 {
		t.Fatalf("after migration 69: executions %v, system %v, totals %d, read tokens marked %v, agent token unmarked %v, resource work %d", left, system, totals, marked, unmarked, work)
	}
	// The spool unseen (no gateway directory): any row may be named by a line a later start ingests, so none goes.
	t.Setenv("FLEET_GATEWAY_DIR", "")
	if n, err := systemReadsCleanup(e.ctx, e.app); err != nil || n != 0 {
		t.Fatalf("cleanup without a spool deleted %d rows, err %v", n, err)
	}
	// One spool line still names a legacy call: that row stays, so ingest never takes the line as new.
	dir := t.TempDir()
	t.Setenv("FLEET_GATEWAY_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "state", "spool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "spool", "2026-11-16.jsonl"), []byte(`{"callId":"spooled","tool":"pulls_open","connection":"github","outcome":"error","disabled":{"key":"k","status":401}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := systemReadsCleanup(e.ctx, e.app)
	if err != nil || n != 3 {
		t.Fatalf("cleanup deleted %d rows, err %v", n, err)
	}
	left, system, totals, _, _, _ = state()
	if !slices.Equal(left, []string{"a-run", "call:agent", "call:own-github-read", "call:personal", "call:spooled"}) || !slices.Equal(system, []string{"call:spooled"}) || totals != 1 {
		t.Fatalf("after cleanup: executions %v, system %v, totals %d", left, system, totals)
	}
}
