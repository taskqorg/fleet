package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
)

// Requirement: exact nullable facts, complete tools and frozen pricing; risk: rounded money,
// lost zero/reasoning/API measurements, truncated tools, or changing historical methods.
func TestResourceContributionExact(t *testing.T) {
	leaf := `{"kind":"agent_run","state":"running","project_id":"A","has_totals":true,"model":"m","provider":"p","tokens_input":0,"tokens_input_basis":"measured","tokens_output":2,"tokens_output_basis":"measured","tokens_reasoning":7,"tokens_reasoning_basis":"measured","api_ms":0,"api_ms_basis":"measured","connection_cost":{"credits":"0.1234567890123456789"},"connection_cost_basis":"measured","credits":0.1234567890123456789,"direct_currency":"USD","direct_amount":0.1234567890123456789,"tool_calls_basis":"measured","tool_calls":{"byTool":{`
	parts := []string{}
	for i := 0; i < 55; i++ {
		parts = append(parts, fmt.Sprintf(`"tool%02d":{"calls":1,"errors":0}`, i))
	}
	leaf += strings.Join(parts, ",") + `},"other":{"calls":2,"errors":1}}}`
	c, m, err := resourceContribution("e", resourceSnapshot{Leaf: json.RawMessage(leaf)})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Included || len(c.Tools) != 55 || c.OtherCalls != 2 || c.OtherErrors != 1 {
		t.Fatalf("lost tool facts: %+v", c)
	}
	for k, want := range map[string]string{"input": "0", "reasoning": "7", "apiMs": "0", "credits": "0.1234567890123456789", "toolCalls": "57", "toolErrors": "1"} {
		if c.Metrics[k].Value == nil || *c.Metrics[k].Value != want {
			t.Errorf("%s=%+v want %s", k, c.Metrics[k], want)
		}
	}
	if c.Metrics["cpuMs"].Value != nil || c.ObservedAt != nil {
		t.Fatal("invented observation")
	}
	if len(c.Costs) != 1 || deref(c.Costs[0].Amount) != "0.1234567890123456789" {
		t.Fatalf("rounded cost %+v", c.Costs)
	}
	if len(m.Components) != 0 {
		t.Fatal("unexpected rates")
	}
	got, err := decimalArithmetic("0.000000000000000001", "9007199254740993", true)
	if err != nil || got != "0.009007199254740993" {
		t.Fatalf("decimal %q %v", got, err)
	}
}

// Requirement: confirmed turns are retained for every leaf while only agent/tool actors
// contribute tool-call coverage; risk: non-agent turns disappear or unrelated tool JSON changes basis.
func TestResourceNonAgentTurns(t *testing.T) {
	c, _, err := resourceContribution("job", resourceSnapshot{Leaf: json.RawMessage(`{"kind":"host_job","state":"succeeded","project_id":"A","has_totals":true,"turn_count":7,"turn_count_basis":"measured","tool_calls":{"byTool":{"ignored":{"calls":99,"errors":1}}},"tool_calls_basis":"estimated"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if deref(c.Metrics["turns"].Value) != "7" || c.Metrics["toolCalls"].Value != nil || len(c.Tools) != 0 {
		t.Fatalf("non-agent facts lost or invented: %+v", c)
	}
}

// Requirement: legacy resource reads include reported reasoning/API; risk: mapped SQL observations
// disappear in the reducer or reasoning is counted twice in total tokens.
func TestResourceLegacyReasoningAPI(t *testing.T) {
	got := summarizeResourceLeaves([]resourceLeafRow{{Kind: "agent_run", TokensInput: ptr(int64(5)), TokensOutput: ptr(int64(9)), TokensReasoning: ptr(int64(4)), ApiMs: ptr(int64(0)), TokensInputBasis: contract.ResourceBasisMeasured, TokensOutputBasis: contract.ResourceBasisMeasured}})
	if got.Tokens.Reasoning == nil || *got.Tokens.Reasoning != 4 || got.Durations.ApiMs == nil || *got.Durations.ApiMs != 0 || got.Tokens.Total == nil || *got.Tokens.Total != 14 {
		t.Fatalf("bad legacy mapping %+v", got)
	}
}

// Requirement: immutable work, replacement, builds/rebuilds and metadata survive real SQL races;
// risk: double counting, dropped work/checkpoints, stale owners or repricing on reconstruction.
// Invoked by tests/go/project-resources.test.ts on its isolated PostgreSQL fixture.
func TestResourcePostgres(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL fixture provided by tests/go/project-resources.test.ts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	tx := func(fn func(*Request) error) {
		t.Helper()
		if err := runTx(ctx, a, nil, true, NOLOCK, fn); err != nil {
			t.Fatal(err)
		}
	}
	sql := func(s string, args ...any) { t.Helper(); tx(func(q *Request) error { return exec(q, s, args...) }) }
	scalar := func(s string, args ...any) string {
		t.Helper()
		var v string
		if err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(ctx, s, args...).Scan(&v) }); err != nil {
			t.Fatal(err)
		}
		return v
	}
	equal := func(s, want string, args ...any) {
		t.Helper()
		if got := scalar(s, args...); got != want {
			t.Fatalf("%s = %q want %q", s, got, want)
		}
	}
	replace := func(id string) {
		t.Helper()
		if err := resourceReplace(ctx, a, id); err != nil {
			t.Fatal(err)
		}
	}
	header := func(p string) contract.PreparedHeaderV1 {
		t.Helper()
		var h contract.PreparedHeaderV1
		tx(func(q *Request) error { var e error; h, e = resourcePreparedHeader(q, p); return e })
		return h
	}
	num := func(p, k string) string {
		t.Helper()
		return scalar(`SELECT coalesce(sum(sum_value),0)::text FROM resource_metric WHERE project_id=$1 AND metric=$2`, p, k)
	}
	build := func(p string) {
		t.Helper()
		for i := 0; i < 20; i++ {
			if err := resourceBuildPage(ctx, a, p); err != nil {
				t.Fatal(err)
			}
			var ids []string
			tx(func(q *Request) error {
				rs, e := q.tx.Query(ctx, `SELECT DISTINCT w.execution_id FROM resource_work w JOIN resource_work_scope s USING(work_id) WHERE s.project_id=$1`, p)
				if e != nil {
					return e
				}
				ids, e = pgx.CollectRows(rs, pgx.RowTo[string])
				return e
			})
			for _, id := range ids {
				replace(id)
			}
			if scalar(`SELECT phase FROM resource_build WHERE project_id=$1`, p) == "ready" {
				return
			}
		}
		t.Fatal("build did not drain")
	}
	sql(`INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('fixture-old','m','input_tokens',0.01,'USD','2020-01-01','2020-01-01','owner')`)
	sql(`INSERT INTO execution(id,kind,state,project_id,model,started_at,ended_at) VALUES('p-e','agent_run','succeeded','A','m','2024-01-01','2024-01-02');
 INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,tokens_reasoning,tokens_reasoning_basis,api_ms,api_ms_basis,wall_ms,wall_ms_basis,cpu_ms,cpu_ms_basis,peak_memory_bytes,peak_memory_bytes_basis,tool_calls,tool_calls_basis)
 VALUES('p-e','A',10,'measured',0,'measured',3,'measured',4,'measured',100,'measured',20,'measured',512,'measured','{"byTool":{"a":{"calls":2,"errors":1}},"other":{"calls":1,"errors":1}}','measured')`)
	replace("p-e")
	build("A")
	if got := num("A", "input"); got != "10" {
		t.Fatal(got)
	}
	h := header("A")
	if h.Executions != 1 || h.Tokens.Reasoning == nil || *h.Tokens.Reasoning != 3 || h.Durations.ApiMs == nil || *h.Durations.ApiMs != 4 || h.Tools.TotalCalls != 3 || h.Tools.FailedCalls != 2 {
		t.Fatalf("initial header %+v", h)
	}
	equal(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='USD'`, "0.1")
	rev := scalar(`SELECT revision::text FROM resource_project_summary WHERE project_id='A'`)
	sql(`INSERT INTO resource_work(execution_id) VALUES('p-e')`)
	replace("p-e")
	equal(`SELECT revision::text FROM resource_project_summary WHERE project_id='A'`, rev)
	// Scoped prepared reads must retain pending/error evidence despite maintenance-only work RLS.
	read := func(member bool) resourcePreparedState {
		t.Helper()
		var state resourcePreparedState
		sc := (*Scope)(nil)
		if member {
			sc = &Scope{ProjectIDs: "A", Person: "member@trial.test"}
		}
		e := st.Tx(ctx, sc, false, NOLOCK, func(tx pgx.Tx) error {
			q := backgroundRequest(ctx, a, tx)
			if member {
				q.person = &Person{Email: "member@trial.test"}
			}
			var e error
			state, e = resourceReadPrepared(q, "A")
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		return state
	}
	if v := read(true); !v.Ready || v.Pending || v.Revision != rev {
		t.Fatalf("ready read %+v", v)
	}
	sql(`INSERT INTO resource_work(execution_id) VALUES('p-e')`)
	tx(func(q *Request) error {
		return exec(q, `INSERT INTO resource_work_scope SELECT work_id,'A' FROM resource_work WHERE execution_id='p-e' ON CONFLICT DO NOTHING`)
	})
	if v := read(true); !v.Ready || !v.Pending || v.OldestPendingAt == nil || v.Revision != rev {
		t.Fatalf("pending read %+v", v)
	}
	replace("p-e")

	sql(`UPDATE execution_totals SET computed_at=clock_timestamp() WHERE execution_id='p-e'`)
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='p-e'`, "0")
	// Updates made while a source writer owns its row must not block replacement.
	conn, e := st.pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	writer, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	sql(`INSERT INTO resource_work(execution_id) VALUES('p-e')`)
	if _, e = writer.Exec(ctx, `UPDATE execution_totals SET tokens_input=20 WHERE execution_id='p-e'`); e != nil {
		t.Fatal(e)
	}
	replace("p-e")
	if got := num("A", "input"); got != "10" {
		t.Fatal("uncommitted facts read", got)
	}
	if e = writer.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	replace("p-e")
	if got := num("A", "input"); got != "20" {
		t.Fatal(got)
	}
	// A source commit AFTER claim but DURING the fact statement leaves an unclaimed identity.
	sql(`INSERT INTO resource_work(execution_id) VALUES('p-e')`)
	gate, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('fixture:resource-read',0))`); e != nil {
		t.Fatal(e)
	}
	originalSQL := resourceSourceSQL
	resourceSourceSQL = strings.Replace(originalSQL, "WITH leaf AS", `WITH gate AS MATERIALIZED(SELECT pg_advisory_xact_lock(hashtextextended('fixture:resource-read',0))), leaf AS`, 1) + ` FROM gate`
	result := make(chan error, 1)
	go func() { result <- resourceReplace(ctx, a, "p-e") }()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("condition timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(func() bool {
		return scalar(`SELECT count(*)::text FROM pg_stat_activity WHERE query LIKE '%WITH gate AS MATERIALIZED%' AND wait_event='advisory' AND pid<>pg_backend_pid()`) != "0"
	})
	sql(`UPDATE execution_totals SET tokens_input=30 WHERE execution_id='p-e'`)
	if e = gate.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal(e)
	}
	resourceSourceSQL = originalSQL
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='p-e'`, "1")
	replace("p-e")
	if got := num("A", "input"); got != "30" {
		t.Fatal(got)
	}
	// Source move with stale initial scope hints forces full transaction restart, never project-after-execution locks.
	sql(`INSERT INTO resource_work(execution_id) VALUES('p-e')`)
	gate, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('execution:p-e',0))`); e != nil {
		t.Fatal(e)
	}
	go func() { result <- resourceReplace(ctx, a, "p-e") }()
	wait(func() bool {
		return scalar(`SELECT count(*)::text FROM pg_stat_activity WHERE wait_event='advisory' AND pid<>pg_backend_pid()`) != "0"
	})
	sql(`UPDATE execution SET project_id='B' WHERE id='p-e'`)
	if e = gate.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal(e)
	}
	build("B")
	if num("A", "input") != "0" || num("B", "input") != "30" {
		t.Fatal("move failed")
	}
	sql(`UPDATE execution SET project_id=NULL WHERE id='p-e'`)
	replace("p-e")
	if header("B").Executions != 0 {
		t.Fatal("NULL move retained contribution")
	}
	sql(`UPDATE execution SET project_id='A' WHERE id='p-e'`)
	replace("p-e")
	// Deliberate retained mismatch must refuse publication until separate parent-first repair.
	sql(`UPDATE execution_totals SET project_id='legacy-wrong' WHERE execution_id='p-e'`)
	rev = scalar(`SELECT revision::text FROM resource_project_summary WHERE project_id='A'`)
	replace("p-e")
	equal(`SELECT revision::text FROM resource_project_summary WHERE project_id='A'`, rev)
	equal(`SELECT project_id FROM execution_totals WHERE execution_id='p-e'`, "A")
	sql(`UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='p-e'`)
	replace("p-e")
	// Parallel processors, duplicates and counted extrema: deleting one max preserves the next.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('p-second','agent_run','succeeded','A'); INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,peak_memory_bytes,peak_memory_bytes_basis) VALUES('p-second','A',0,'measured',1024,'measured')`)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- resourceReplace(ctx, a, "p-second") }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if header("A").Executions != 2 || *header("A").PeakMemoryBytes.Value != 1024 {
		t.Fatal("parallel duplicate")
	}
	sql(`DELETE FROM execution_totals WHERE execution_id='p-second'; DELETE FROM execution WHERE id='p-second'`)
	replace("p-second")
	replace("p-second")
	if *header("A").PeakMemoryBytes.Value != 512 || header("A").Executions != 1 {
		t.Fatal("extreme subtraction")
	}
	// First method survives new tariffs, corrections and an explicit rebuild.
	sql(`INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('fixture-new','m','input_tokens',9,'USD','2023-01-01','2025-01-01','owner')`)
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='p-e'`, "0")
	before := scalar(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='USD'`)
	method := scalar(`SELECT method_sha FROM resource_valuation WHERE execution_id='p-e'`)
	if e = resourceRebuild(ctx, a, "A"); e != nil {
		t.Fatal(e)
	}
	replace("p-e")
	equal(`SELECT ready::text FROM resource_project_summary WHERE project_id='A'`, "false")
	build("A")
	equal(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='USD'`, before)
	equal(`SELECT method_sha FROM resource_valuation WHERE execution_id='p-e'`, method)
	// Missing first rate is frozen as unavailable, even when a rate arrives before reconstruction.
	sql(`INSERT INTO execution(id,kind,state,project_id,model,started_at) VALUES('p-missing','agent_run','succeeded','A','never-priced','2024-01-01'); INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis) VALUES('p-missing','A',1,'measured')`)
	replace("p-missing")
	sql(`INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('fixture-late','never-priced','input_tokens',1,'USD','2020-01-01','2025-01-01','owner')`)
	if e = resourceRebuild(ctx, a, "A"); e != nil {
		t.Fatal(e)
	}
	build("A")
	equal(`SELECT facts#>>'{costs,0,basis}' FROM resource_contribution WHERE execution_id='p-missing'`, "unavailable")
	// Metadata union matches old/new names WITHOUT connection: prefix, and accounts by full ID.
	sql(`INSERT INTO execution(id,kind,state,project_id,account_id,data) VALUES('p-meta','tool_run','succeeded','A','connection:old','{"connection":"old","tool":"x"}'); INSERT INTO document(id,kind,data) VALUES('connection:old','api-connection','{"provider":"old-provider"}')`)
	replace("p-meta")
	sql(`UPDATE document SET id='connection:new',data='{"provider":"new-provider"}' WHERE id='connection:old'`)
	var repairs []string
	tx(func(q *Request) error {
		rs, e := q.tx.Query(ctx, `SELECT repair_id::text FROM resource_metadata_repair WHERE metadata_id IN ('connection:old','connection:new')`)
		if e != nil {
			return e
		}
		repairs, e = pgx.CollectRows(rs, pgx.RowTo[string])
		return e
	})
	if len(repairs) < 2 {
		t.Fatal("missing metadata jobs")
	}
	for _, r := range repairs {
		if e = resourceMetadataPage(ctx, a, r); e != nil {
			t.Fatal(e)
		}
		equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='p-meta' AND repair_id=$1::uuid`, "1", r)
		if e = resourceMetadataPage(ctx, a, r); e != nil {
			t.Fatal(e)
		}
		equal(`SELECT count(*)::text FROM resource_repair_scope WHERE repair_id=$1::uuid`, "1", r)
	}
	replace("p-meta")
	for _, r := range repairs {
		if e = resourceMetadataPage(ctx, a, r); e != nil {
			t.Fatal(e)
		}
	}
	equal(`SELECT count(*)::text FROM resource_repair_scope WHERE repair_id=ANY($1::uuid[])`, "0", repairs)
	// A connection deletion reaches current users; replayed old metadata jobs always read current classification.
	sql(`INSERT INTO execution(id,kind,state,project_id,account_id,data) VALUES('p-meta-new','tool_run','succeeded','A','connection:new','{"connection":"new","tool":"x"}');
 INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES('p-meta-new','A','{"currency":"USD","amount":"1"}','measured')`)
	replace("p-meta-new")
	equal(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='USD' AND basis='measured'`, "1")
	sql(`DELETE FROM document WHERE id='connection:new'`)
	deletion := scalar(`SELECT repair_id::text FROM resource_metadata_repair WHERE metadata_id='connection:new'`)
	if e = resourceMetadataPage(ctx, a, deletion); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='p-meta-new' AND repair_id=$1::uuid`, "1", deletion)
	if e = resourceMetadataPage(ctx, a, deletion); e != nil {
		t.Fatal(e)
	}
	replace("p-meta-new")
	if e = resourceMetadataPage(ctx, a, deletion); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_cost WHERE project_id='A' AND currency='USD' AND basis='measured'`, "0")
	equal(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='USD' AND basis='subscription'`, "1")

	// FORCE RLS also applies to derived rows; local fixture role is not superuser.
	tx(func(q *Request) error {
		if e := exec(q, `SELECT set_config('fleet.project_ids','B',true)`); e != nil {
			return e
		}
		var n int
		if e := q.tx.QueryRow(ctx, `SELECT count(*) FROM resource_contribution WHERE project_id='A'`).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return errors.New("derived scope leak")
		}
		return nil
	})
	// Retain EVERY tool and exact decimal credits/money, with null, measured-zero and partial support.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('p-high','agent_run','succeeded','A');
 INSERT INTO execution_totals(execution_id,project_id,tool_calls,tool_calls_basis,cpu_missing_reason,memory_missing_reason,history_incomplete)
 SELECT 'p-high','A',jsonb_build_object('byTool',jsonb_object_agg('tool-'||i,jsonb_build_object('calls',1,'errors',0))),'measured','not_reported','not_reported',1 FROM generate_series(1,55) i`)
	replace("p-high")
	equal(`SELECT count(*)::text FROM resource_tool WHERE project_id='A' AND name LIKE 'tool-%'`, "55")
	if h := header("A"); h.PeakMemoryBytes.Value == nil || h.PeakMemoryBytes.Basis != contract.ResourceBasisUnavailable || h.PeakMemoryBytes.Partial == nil {
		t.Fatalf("lost partial memory %+v", h.PeakMemoryBytes)
	}
	equal(`SELECT sum((reasons->>'not_reported')::bigint)::text FROM resource_metric WHERE project_id='A' AND metric='cpuMs'`, "1")
	sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('p-credit1','tool_run','succeeded','A','{"connection":"credits","tool":"one"}'),('p-credit2','tool_run','failed','A','{"connection":"credits","tool":"two"}');
 INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES
 ('p-credit1','A','{"unit":"credits","credits":"0.1","amount":"0.1"}','measured'),('p-credit2','A','{"unit":"credits","credits":"0.2","amount":"0.2"}','measured')`)
	replace("p-credit1")
	replace("p-credit2")
	equal(`SELECT sum(sum_value)::text FROM resource_metric WHERE project_id='A' AND metric='credits'`, "0.3")
	sql(`INSERT INTO execution(id,kind,state,project_id,account_id) VALUES('p-direct','tool_run','succeeded','A','subscription-account');
 INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES('p-direct','A','{"currency":"EUR","amount":"0.1234567890123456789"}','measured')`)
	replace("p-direct")
	equal(`SELECT value::text FROM resource_cost WHERE project_id='A' AND currency='EUR' AND basis='subscription'`, "0.1234567890123456789")
	if header("A").ItemCounts.Tools < 55 {
		t.Fatal("truncated prepared items")
	}
	sql(`DELETE FROM execution_totals WHERE execution_id='p-high'; DELETE FROM execution WHERE id='p-high'`)
	replace("p-high")
	equal(`SELECT coalesce(sum((reasons->>'not_reported')::bigint),0)::text FROM resource_metric WHERE project_id='A' AND metric='cpuMs'`, "0")
	equal(`SELECT count(*)::text FROM resource_tool WHERE project_id='A' AND name LIKE 'tool-%'`, "0")

	// Shared legacy semantics include confirmed turns on host_job leaves, while ignoring their tool JSON.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('p-turns-job','host_job','succeeded','TURNS');
 INSERT INTO execution_totals(execution_id,project_id,turn_count,turn_count_basis,tool_calls,tool_calls_basis) VALUES('p-turns-job','TURNS',7,'measured','{"byTool":{"ignored":{"calls":99,"errors":1}}}','estimated')`)
	replace("p-turns-job")
	if h := header("TURNS"); h.Tools.Turns != 7 || h.Tools.TotalCalls != 0 || h.Tools.Basis != contract.ResourceBasisUnavailable {
		t.Fatalf("non-agent parity %+v", h.Tools)
	}

	// P1 regression: contribution creation is earlier than the first usable valuation snapshot.
	sql(`INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('creation-price','creation-model','input_tokens',0.02,'USD','2020-01-01','2020-01-01','owner');
 INSERT INTO execution(id,kind,state,project_id,model) VALUES('p-creation','agent_run','preparing','VALUATION','creation-latest')`)
	replace("p-creation")
	equal(`SELECT count(*)::text FROM resource_valuation WHERE execution_id='p-creation'`, "0")
	sql(`UPDATE execution SET state='running',model='creation-model',started_at='2024-01-01' WHERE id='p-creation';
 INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis) VALUES('p-creation','VALUATION',5,'measured')`)
	replace("p-creation")
	equal(`SELECT value::text FROM resource_cost WHERE project_id='VALUATION' AND currency='USD'`, "0.1")
	equal(`SELECT count(*)::text FROM resource_valuation WHERE execution_id='p-creation'`, "1")

	// An actual failing page seed rolls work, scopes AND checkpoint back; a committed page resumes.
	sql(`INSERT INTO execution(id,kind,state,project_id,data) SELECT 'crash-'||lpad(i::text,4,'0'),'tool_run','succeeded','CRASH','{"connection":"crash","tool":"x"}'::jsonb FROM generate_series(1,105) i;
 DELETE FROM resource_work WHERE execution_id LIKE 'crash-%'`)
	tx(func(q *Request) error { return resourceEnsureProject(q, "CRASH") })
	if e = resourceBuildPage(ctx, a, "CRASH"); e == nil {
		t.Fatal("fixture must abort build seed")
	}
	equal(`SELECT coalesce(after_execution_id,'none')||'/'||scan_done::text FROM resource_build WHERE project_id='CRASH'`, "none/false")
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id LIKE 'crash-%' AND origin='build'`, "0")
	sql(`DELETE FROM resource_fixture_failure WHERE kind='build'`)
	if e = resourceBuildPage(ctx, a, "CRASH"); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT after_execution_id FROM resource_build WHERE project_id='CRASH'`, "crash-0100")
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id LIKE 'crash-%' AND origin='build'`, "100")
	sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('crash-0000','tool_run','succeeded','CRASH','{"connection":"crash","tool":"x"}');DELETE FROM execution WHERE id='crash-0001'`)
	build("CRASH")
	if header("CRASH").Executions != 105 {
		t.Fatal("checkpoint missed insert/deletion")
	}
	// Metadata pages obey the same atomic rollback rule and retain coverage through work drain.
	sql(`INSERT INTO document(id,kind,data) VALUES('connection:crash','api-connection','{"provider":"fixture"}')`)
	repair := scalar(`SELECT repair_id::text FROM resource_metadata_repair WHERE metadata_id='connection:crash'`)
	if e = resourceMetadataPage(ctx, a, repair); e == nil {
		t.Fatal("fixture must abort metadata seed")
	}
	equal(`SELECT coalesce(after_execution_id,'none')||'/'||done::text FROM resource_metadata_repair WHERE repair_id=$1::uuid`, "none/false", repair)
	equal(`SELECT count(*)::text FROM resource_work WHERE repair_id=$1::uuid`, "0", repair)
	sql(`DELETE FROM resource_fixture_failure WHERE kind='metadata'`)
	if e = resourceMetadataPage(ctx, a, repair); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE repair_id=$1::uuid`, "100", repair)
	if e = resourceMetadataPage(ctx, a, repair); e != nil {
		t.Fatal(e)
	}
	if e = resourceMetadataPage(ctx, a, repair); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT done::text FROM resource_metadata_repair WHERE repair_id=$1::uuid`, "true", repair)
	if e = resourceMetadataPage(ctx, a, repair); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_repair_scope WHERE repair_id=$1::uuid`, "1", repair)
	build("CRASH")
	if e = resourceMetadataPage(ctx, a, repair); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_repair_scope WHERE repair_id=$1::uuid`, "0", repair)

	// P2 regression: a completed metadata enumeration waiting on failed work cannot starve later jobs.
	sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('p-meta-blocked','tool_run','succeeded','META','{"connection":"blocked","tool":"x"}');
 INSERT INTO document(id,kind,data) VALUES('connection:blocked','api-connection','{"provider":"fixture"}')`)
	replace("p-meta-blocked")
	blocked := scalar(`SELECT repair_id::text FROM resource_metadata_repair WHERE metadata_id='connection:blocked'`)
	if e = resourceMetadataPage(ctx, a, blocked); e != nil {
		t.Fatal(e)
	}
	if e = resourceMetadataPage(ctx, a, blocked); e != nil {
		t.Fatal(e)
	}
	sql(`UPDATE resource_work SET next_attempt_at=clock_timestamp()+interval '1 hour',last_error_class='projection_failed' WHERE repair_id=$1::uuid`, blocked)
	sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('p-meta-later','tool_run','succeeded','META','{"connection":"later","tool":"x"}');
 INSERT INTO document(id,kind,data) VALUES('connection:later','api-connection','{"provider":"fixture"}')`)
	later := scalar(`SELECT repair_id::text FROM resource_metadata_repair WHERE metadata_id='connection:later'`)
	if e = resourceProcessorPass(ctx, a); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT after_execution_id FROM resource_metadata_repair WHERE repair_id=$1::uuid`, "p-meta-later", later)
	equal(`SELECT count(*)::text FROM resource_repair_scope WHERE repair_id=$1::uuid`, "1", blocked)
	if e = resourceMetadataPage(ctx, a, later); e != nil {
		t.Fatal(e)
	}
	if e = resourceMetadataPage(ctx, a, later); e != nil {
		t.Fatal(e)
	}
	sql(`UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE repair_id=$1::uuid`, blocked)
	replace("p-meta-blocked")
	if e = resourceMetadataPage(ctx, a, blocked); e != nil {
		t.Fatal(e)
	}

	t.Log("PostgreSQL initial aggregates, replay, snapshot/claim race, A/B/NULL, repair, deletion, parallelism, extremes, frozen rebuild, metadata union and FORCE RLS passed")
}

// Requirement: both bound-project enumerations apply to every new read before cursor/anchor
// interpretation. Risk: the generic host path exposes a foreign allowed project or loses binding.
func TestResourceAPIBoundPaths(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated API fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	err = runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
		p, err := resolveAccessTx(ctx, q.tx, "member@trial.test")
		if err != nil {
			return err
		}
		q.person = p
		for _, name := range []string{"project_detail", "project_resources", "project_resource_items"} {
			for _, args := range []doc{{}, {"projectId": "desiign", "knownVersion": "foreign:revision"}, {"projectId": "desiign", "around": doc{"key": doc{"kind": "tool", "name": "foreign-name", "connection": nil}, "direction": "atOrAfter"}}} {
				bound, err := boundArguments(q, name, args, "api-agent")
				if err != nil {
					return err
				}
				if bound["projectId"] != "desiign" {
					t.Fatalf("%s: lost project binding", name)
				}
			}
			if _, err := boundArguments(q, name, doc{"projectId": "hello-world", "cursor": "foreign"}, "api-agent"); err == nil {
				t.Fatalf("%s: accepted foreign project", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
