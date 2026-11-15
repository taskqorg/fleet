package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Requirement: batched metric statements preserve atomic replacement on SQL failure and
// actual backend loss; risk: partial support/publication commits or retries double count.
func TestResourceMetricBatchPostgres(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	sql := func(s string) {
		t.Helper()
		if e := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return exec(q, s) }); e != nil {
			t.Fatal(e)
		}
	}
	scalar := func(s string) string {
		t.Helper()
		var v string
		if e := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(q.ctx(), s).Scan(&v) }); e != nil {
			t.Fatal(e)
		}
		return v
	}
	snapshot := func() string {
		return scalar(`SELECT jsonb_build_object(
 'summary',(SELECT to_jsonb(s) FROM resource_project_summary s WHERE project_id='BATCH'),
 'metrics',(SELECT jsonb_agg(m ORDER BY metric,basis) FROM resource_metric m WHERE project_id='BATCH'),
 'contribution',(SELECT to_jsonb(c) FROM resource_contribution c WHERE execution_id='batch-leaf'),
 'valuation',(SELECT to_jsonb(v) FROM resource_valuation v WHERE execution_id='batch-leaf'))::text`)
	}
	sql(`INSERT INTO execution(id,kind,state,project_id,harness,model,started_at) VALUES('batch-leaf','agent_run','succeeded','BATCH','codex','batch-model','2024-01-01');
 INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis) VALUES('batch-leaf','BATCH',10,'measured',7,'measured')`)
	if err = resourceReplace(ctx, a, "batch-leaf"); err != nil {
		t.Fatal(err)
	}
	before := snapshot()
	sql(`UPDATE execution_totals SET tokens_input=20 WHERE execution_id='batch-leaf'`)
	work := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id='batch-leaf'`)
	sql(`INSERT INTO resource_fixture_failure VALUES('batch-error')`)
	if err = resourceReplace(ctx, a, "batch-leaf"); err == nil || !strings.Contains(err.Error(), "fixture metric batch failure") {
		t.Fatalf("SQL error not propagated: %v", err)
	}
	if got := snapshot(); got != before {
		t.Fatal("failed batch committed partial support/contribution/publication")
	}
	if got := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id='batch-leaf'`); got != work {
		t.Fatal("failed batch lost immutable claim", got, work)
	}
	sql(`DELETE FROM resource_fixture_failure WHERE kind='batch-error';
 INSERT INTO resource_fixture_failure VALUES('batch-gate');
 UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='batch-leaf'`)
	gate, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close(context.Background())
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('fixture:metric-batch',0))`); err != nil {
		t.Fatal(err)
	}
	defer gate.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('fixture:metric-batch',0))`)
	done := make(chan error, 1)
	go func() { done <- resourceReplace(ctx, a, "batch-leaf") }()
	var backend int32
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		err = gate.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND usename=current_user AND wait_event='advisory' AND query LIKE '%resource_metric%' AND pid<>pg_backend_pid() LIMIT 1),0)`).Scan(&backend)
		if err != nil {
			t.Fatal(err)
		}
		if backend != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("metric batch did not reach controlled backend boundary")
		}
	}
	var killed bool
	if err = gate.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backend).Scan(&killed); err != nil || !killed {
		t.Fatalf("backend termination: %v %v", killed, err)
	}
	if err = <-done; err == nil {
		t.Fatal("terminated batch succeeded")
	}
	if got := snapshot(); got != before {
		t.Fatal("backend loss committed partial support/contribution/publication")
	}
	if got := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id='batch-leaf'`); got != work {
		t.Fatal("backend loss lost immutable claim")
	}
	sql(`DELETE FROM resource_fixture_failure WHERE kind='batch-gate';UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='batch-leaf'`)
	if err = resourceReplace(ctx, a, "batch-leaf"); err != nil {
		t.Fatal(err)
	}
	if got := scalar(`SELECT sum_value::text FROM resource_metric WHERE project_id='BATCH' AND metric='input'`); got != "20" {
		t.Fatal("retry input", got)
	}
	if got := scalar(`SELECT sum_value::text FROM resource_metric WHERE project_id='BATCH' AND metric='output'`); got != "7" {
		t.Fatal("retry output", got)
	}
	if got := scalar(`SELECT count(*)::text FROM resource_work WHERE execution_id='batch-leaf'`); got != "0" {
		t.Fatal("retry did not drain", got)
	}
	after := snapshot()
	if err = resourceReplace(ctx, a, "batch-leaf"); err != nil {
		t.Fatal(err)
	}
	if snapshot() != after {
		t.Fatal("replay changed support/publication")
	}
	t.Logf("SQL failure and terminated backend %d rolled back metric batch; immutable claim retained; exact retry/replay passed", backend)
}

// Requirement: no-op elimination preserves zero/missing coverage, support multiplicity
// and last-reference removal; risk: numeric zero hides an item or drops extrema/reasons.
func TestResourceNoopSupportPostgres(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	scalar := func(sql string) string {
		t.Helper()
		var v string
		if e := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(q.ctx(), sql).Scan(&v) }); e != nil {
			t.Fatal(e)
		}
		return v
	}
	equal := func(sql, want string) {
		t.Helper()
		if got := scalar(sql); got != want {
			t.Fatalf("%s = %q want %q", sql, got, want)
		}
	}
	c := contract.ResourceContributionV1{SchemaVersion: 1, ExecutionId: "zero-support", ProjectId: ptr("SUPPORT_NOOP"), Included: true, StartedMs: ptr(int64(100)), EndedMs: ptr(int64(200)), Metrics: map[string]contract.ResourceMetricV1{
		"input":           {Value: ptr("0"), Basis: contract.ResourceBasisMeasured, Expected: true},
		"output":          {Basis: contract.ResourceBasisUnavailable, Expected: true, Reason: ptr("fixture_missing")},
		"credits":         {Basis: contract.ResourceBasisUnavailable},
		"turns":           {Basis: contract.ResourceBasisUnavailable, Reason: ptr("retained_reason")},
		"peakMemoryBytes": {Value: ptr("0"), Basis: contract.ResourceBasisMeasured, Expected: true},
	}, Tools: []contract.ResourceToolV1{{Name: "zero-tool", Calls: 0, Errors: 0}}, Costs: []contract.ResourceCostV1{{Currency: ptr("USD"), Basis: contract.ResourceBasisMeasured, Amount: ptr("0"), PriceRefs: []contract.ResourcePriceRefV1{{PriceId: "zero-price", ObservedAt: 1704067200000}}}}}
	apply := func(sign int64) error {
		return runTxLocks(ctx, a, nil, true, []string{"project:SUPPORT_NOOP"}, func(q *Request) error {
			if e := resourceEnsureProject(q, "SUPPORT_NOOP"); e != nil {
				return e
			}
			if e := resourceApply(q, c, sign); e != nil {
				return e
			}
			return resourcePublish(q, "SUPPORT_NOOP")
		})
	}
	if err = apply(1); err != nil {
		t.Fatal(err)
	}
	if err = apply(1); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT present_count||':'||expected_count||':'||sum_value FROM resource_metric WHERE project_id='SUPPORT_NOOP' AND metric='input'`, "2:2:0")
	equal(`SELECT present_count||':'||expected_count||':'||(reasons->>'fixture_missing') FROM resource_metric WHERE project_id='SUPPORT_NOOP' AND metric='output'`, "0:2:2")
	equal(`SELECT reasons->>'retained_reason' FROM resource_metric WHERE project_id='SUPPORT_NOOP' AND metric='turns'`, "2")
	equal(`SELECT count(*)::text FROM resource_metric WHERE project_id='SUPPORT_NOOP' AND metric='credits'`, "0")
	equal(`SELECT tool_count||':'||cost_count||':'||price_count FROM resource_project_summary WHERE project_id='SUPPORT_NOOP'`, "1:1:1")
	equal(`SELECT calls||':'||errors||':'||references_count FROM resource_tool WHERE project_id='SUPPORT_NOOP'`, "0:0:2")
	equal(`SELECT value::text||':'||references_count FROM resource_cost WHERE project_id='SUPPORT_NOOP'`, "0:2")
	equal(`SELECT references_count::text FROM resource_price_ref WHERE project_id='SUPPORT_NOOP'`, "2")
	equal(`SELECT count(*)::text FROM resource_fixture_item_updates`, "3")
	// A callback failure after real publication must roll support, revision AND its wake intent back.
	before := scalar(`SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) FROM resource_project_summary s WHERE project_id='SUPPORT_NOOP'),'metric',(SELECT jsonb_agg(m ORDER BY metric,basis) FROM resource_metric m WHERE project_id='SUPPORT_NOOP'))::text`)
	failure := errors.New("fixture rollback after atomic publication")
	var failedXID string
	err = runTxLocks(ctx, a, nil, true, []string{"project:SUPPORT_NOOP"}, func(q *Request) error {
		if e := q.tx.QueryRow(q.ctx(), `SELECT pg_current_xact_id()::text`).Scan(&failedXID); e != nil {
			return e
		}
		if e := resourceApply(q, c, 1); e != nil {
			return e
		}
		if e := resourcePublish(q, "SUPPORT_NOOP"); e != nil {
			return e
		}
		var intents int64
		if e := q.tx.QueryRow(q.ctx(), `SELECT count(*) FROM wake_outbox WHERE source_xid=pg_current_xact_id()`).Scan(&intents); e != nil {
			return e
		}
		if intents == 0 {
			return errors.New("publication did not create its transactional wake intent")
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after := scalar(`SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) FROM resource_project_summary s WHERE project_id='SUPPORT_NOOP'),'metric',(SELECT jsonb_agg(m ORDER BY metric,basis) FROM resource_metric m WHERE project_id='SUPPORT_NOOP'))::text`)
	if after != before {
		t.Fatal("failed replacement leaked support/publication")
	}
	equal(`SELECT count(*)::text FROM wake_outbox WHERE source_xid::text='`+failedXID+`'`, "0")
	// The third fixed-header SELECT fails after earlier batch results are valid.
	// It must abort before a new public version can be acknowledged.
	err = runTxLocks(ctx, a, nil, true, []string{"project:SUPPORT_NOOP"}, func(q *Request) error {
		if e := resourceApply(q, c, 1); e != nil {
			return e
		}
		if e := exec(q, `SET LOCAL fleet.fixture_header_error='on'`); e != nil {
			return e
		}
		return resourcePublish(q, "SUPPORT_NOOP")
	})
	if err == nil || !strings.Contains(err.Error(), "fixture header batch failure") {
		t.Fatalf("last header batch SQL error not propagated: %v", err)
	}
	if got := scalar(`SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) FROM resource_project_summary s WHERE project_id='SUPPORT_NOOP'),'metric',(SELECT jsonb_agg(m ORDER BY metric,basis) FROM resource_metric m WHERE project_id='SUPPORT_NOOP'))::text`); got != before {
		t.Fatal("header batch failure leaked support/publication")
	}

	if err = apply(-1); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT count(*)::text FROM resource_fixture_item_updates`, "3")
	equal(`SELECT min(multiplicity)::text FROM resource_extreme WHERE project_id='SUPPORT_NOOP'`, "1")
	if err = apply(-1); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT tool_count||':'||cost_count||':'||price_count FROM resource_project_summary WHERE project_id='SUPPORT_NOOP'`, "0:0:0")
	equal(`SELECT count(*)::text FROM resource_fixture_item_updates`, "6")
	equal(`SELECT ((SELECT count(*) FROM resource_metric WHERE project_id='SUPPORT_NOOP')+(SELECT count(*) FROM resource_extreme WHERE project_id='SUPPORT_NOOP')+(SELECT count(*) FROM resource_tool WHERE project_id='SUPPORT_NOOP')+(SELECT count(*) FROM resource_cost WHERE project_id='SUPPORT_NOOP')+(SELECT count(*) FROM resource_price_ref WHERE project_id='SUPPORT_NOOP'))::text`, "0")
}

// Requirement: one bounded project cohort commits exact independent leaves atomically;
// risk: failed members consume healthy claims, late facts disappear, or publication assumes one leaf.
func TestResourceCohortPostgres(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	sql := func(s string, args ...any) {
		t.Helper()
		if e := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return exec(q, s, args...) }); e != nil {
			t.Fatal(e)
		}
	}
	scalar := func(s string, args ...any) string {
		t.Helper()
		var v string
		if e := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(q.ctx(), s, args...).Scan(&v) }); e != nil {
			t.Fatal(e)
		}
		return v
	}
	equal := func(s, want string, args ...any) {
		t.Helper()
		if got := scalar(s, args...); got != want {
			t.Fatalf("%s = %q want %q", s, got, want)
		}
	}
	ids := make([]string, resourceCohortSize)
	for i := range ids {
		ids[i] = fmt.Sprintf("cohort-%02d", i)
		sql(`INSERT INTO execution(id,kind,state,project_id,harness,model,started_at) VALUES($1,'agent_run','succeeded','COHORT','codex','cohort-model','2024-01-01');`, ids[i])
		sql(`INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis) VALUES($1,'COHORT',$2,'measured')`, ids[i], i%3)
	}
	if err = runTxLocks(ctx, a, nil, true, []string{"project:COHORT"}, func(q *Request) error { return resourceEnsureProject(q, "COHORT") }); err != nil {
		t.Fatal(err)
	}
	work := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id=ANY($1::text[])`, ids)
	pristine := func() {
		t.Helper()
		equal(`SELECT count(*)::text FROM resource_contribution WHERE project_id='COHORT'`, "0")
		equal(`SELECT count(*)::text FROM resource_valuation WHERE execution_id=ANY($1::text[])`, "0", ids)
		equal(`SELECT count(*)::text FROM interface_publication WHERE project='COHORT'`, "0")
		equal(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id=ANY($1::text[])`, work, ids)
	}
	sql(`INSERT INTO resource_fixture_cohort_fault VALUES('cohort-01','error')`)
	if err = resourceReplaceBatch(ctx, a, ids); err == nil || !strings.Contains(err.Error(), "fixture cohort failure") {
		t.Fatalf("cohort SQL failure: %v", err)
	}
	pristine()
	sql(`UPDATE resource_fixture_cohort_fault SET mode='gate'`)
	gate, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close(context.Background())
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('fixture:cohort',0))`); err != nil {
		t.Fatal(err)
	}
	defer gate.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('fixture:cohort',0))`)
	done := make(chan error, 1)
	launch := func() int32 {
		t.Helper()
		go func() { done <- resourceReplaceBatch(ctx, a, ids) }()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			var pid int32
			if e := gate.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND usename=current_user AND wait_event='advisory' AND query LIKE '%resource_contribution%' AND pid<>pg_backend_pid() LIMIT 1),0)`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if pid != 0 {
				return pid
			}
			if time.Now().After(deadline) {
				t.Fatal("cohort did not reach controlled contribution boundary")
			}
		}
	}
	backend := launch()
	var killed bool
	if err = gate.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backend).Scan(&killed); err != nil || !killed {
		t.Fatalf("terminate backend: %v %v", killed, err)
	}
	if err = <-done; err == nil {
		t.Fatal("terminated cohort succeeded")
	}
	pristine()
	launch()
	// All cohort snapshots were captured before any support mutation. A later source
	// transaction must commit without waiting for the processor and retain its new UUID.
	sql(`UPDATE execution_totals SET tokens_input=7 WHERE execution_id='cohort-00'`)
	late := scalar(`SELECT work_id::text FROM resource_work WHERE execution_id='cohort-00' ORDER BY first_pending_at DESC,work_id DESC LIMIT 1`)
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('fixture:cohort',0))`); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE project_id='COHORT'`, "16")
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "15")
	equal(`SELECT present_count::text||':'||expected_count::text FROM resource_metric WHERE project_id='COHORT' AND metric='input' AND basis='measured'`, "16:16")
	equal(`SELECT count(*)::text FROM resource_valuation WHERE execution_id=ANY($1::text[])`, "16", ids)
	equal(`SELECT count(*)::text FROM resource_contribution WHERE project_id='COHORT' AND facts#>>'{metrics,cpuMs,value}' IS NOT NULL`, "0")
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id=ANY($1::text[])`, "1", ids)
	equal(`SELECT work_id::text FROM resource_work WHERE execution_id='cohort-00'`, late)
	equal(`SELECT count(*)::text FROM interface_publication WHERE project='COHORT'`, "1")
	equal(`SELECT jsonb_array_length(leaves)::text FROM interface_publication WHERE project='COHORT'`, "16")
	equal(`SELECT count(*)::text FROM change c JOIN interface_publication p ON p.publication_xid=c.xid WHERE c.kind='resource_summary' AND c.id='COHORT'`, "1")
	method := scalar(`SELECT method_sha FROM resource_valuation WHERE execution_id='cohort-00'`)
	sql(`DELETE FROM resource_fixture_cohort_fault; INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('cohort-late','cohort-model','input_tokens',9,'USD','2020-01-01','2025-01-01','owner')`)
	if err = resourceReplace(ctx, a, "cohort-00"); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "22")
	equal(`SELECT method_sha FROM resource_valuation WHERE execution_id='cohort-00'`, method)
	revision := scalar(`SELECT revision::text FROM resource_project_summary WHERE project_id='COHORT'`)
	// Durable replay also covers retry after the caller loses acknowledgement: already
	// committed immutable claims cannot create another revision or refreeze a method.
	if err = resourceReplaceBatch(ctx, a, ids); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT revision::text FROM resource_project_summary WHERE project_id='COHORT'`, revision)
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id=ANY($1::text[])`, "0", ids)
	// A retryable SQL poison gets one cohort attempt, then independent bounded leaves.
	for _, id := range ids {
		sql(`UPDATE execution_totals SET tokens_input=tokens_input+1 WHERE execution_id=$1`, id)
	}
	sql(`INSERT INTO resource_fixture_cohort_fault VALUES('cohort-01','retryable')`)
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('cohort-poison-other','tool_run','succeeded','COHORT_POISON_OTHER')`)
	started := time.Now()
	if err = resourceReplaceCandidates(ctx, a, append(append([]string{}, ids...), "cohort-poison-other")); err == nil {
		t.Fatal("poison failure hidden")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("cohort poison retried without bound")
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id=ANY($1::text[])`, "1", ids)
	equal(`SELECT execution_id FROM resource_work WHERE execution_id=ANY($1::text[])`, "cohort-01", ids)
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='cohort-poison-other'`, "1")
	equal(`SELECT attempts::text||':'||(next_attempt_at>first_pending_at+interval '90 milliseconds')::text FROM resource_work WHERE execution_id='cohort-01'`, "1:true")
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "37")
	sql(`DELETE FROM resource_fixture_cohort_fault; UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='cohort-01'`)
	if err = resourceReplace(ctx, a, "cohort-01"); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "38")

	// Lose the actual server COMMIT response, not an invented callback error. The
	// independent connection must observe all committed leaves before durable replay.
	for _, id := range ids {
		sql(`UPDATE execution_totals SET tokens_input=tokens_input+1 WHERE execution_id=$1`, id)
	}
	cfg := st.pool.Config()
	var drop atomic.Bool
	dial := cfg.ConnConfig.DialFunc
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, e := dial(ctx, network, address)
		if e != nil {
			return nil, e
		}
		return &resourceLostCommitConn{Conn: c, reader: bufio.NewReader(c), drop: &drop}, nil
	}
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	ambiguous := &App{store: &Store{pool: pool, readPool: st.readPool}}
	beforePub := scalar(`SELECT count(*)::text FROM interface_publication WHERE project='COHORT'`)
	drop.Store(true)
	if e = resourceReplaceBatch(ctx, ambiguous, ids); e == nil {
		t.Fatal("lost COMMIT acknowledgement reported success")
	}
	if drop.Load() {
		t.Fatalf("test did not intercept committed response: %v", e)
	}
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "54")
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id=ANY($1::text[])`, "0", ids)
	afterPub := scalar(`SELECT count(*)::text FROM interface_publication WHERE project='COHORT'`)
	equal(`SELECT (count(*)=$1::bigint+1)::text FROM interface_publication WHERE project='COHORT'`, "true", beforePub)
	if e = resourceReplaceCandidates(ctx, a, ids); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM interface_publication WHERE project='COHORT'`, afterPub)
	equal(`SELECT method_sha FROM resource_valuation WHERE execution_id='cohort-00'`, method)

	// Change scope after the bulk hint but before the canonical lock set is acquired.
	// Rechecking all members must roll back, without adding a lock out of order.
	sql(`UPDATE execution_totals SET tokens_input=tokens_input+1 WHERE execution_id='cohort-00'`)
	beforeMove := scalar(`SELECT revision::text FROM resource_project_summary WHERE project_id='COHORT'`)
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('project:COHORT',0))`); e != nil {
		t.Fatal(e)
	}
	go func() { done <- resourceReplaceBatch(ctx, a, ids) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var waiting bool
		if e = gate.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND usename=current_user AND wait_event='advisory' AND pid<>pg_backend_pid())`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cohort did not wait for project lock")
		}
	}
	sql(`UPDATE execution SET project_id='COHORT_ALT' WHERE id='cohort-00'`)
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('project:COHORT',0))`); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, errResourceScope) {
		t.Fatalf("changed cohort scope: %v", e)
	}
	equal(`SELECT revision::text FROM resource_project_summary WHERE project_id='COHORT'`, beforeMove)
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='cohort-00'`, "2")
	if e = resourceReplaceCandidates(ctx, a, ids); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT' AND metric='input'`, "45")
	equal(`SELECT sum_value::text FROM resource_metric WHERE project_id='COHORT_ALT' AND metric='input'`, "10")
	sql(`UPDATE execution SET project_id=NULL WHERE id='cohort-00'`)
	if e = resourceReplaceCandidates(ctx, a, ids); e != nil {
		t.Fatal(e)
	}
	equal(`SELECT count(*)::text FROM resource_metric WHERE project_id='COHORT_ALT'`, "0")
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id=ANY($1::text[])`, "0", ids)
	if e = st.Tx(ctx, &Scope{ProjectIDs: "COHORT", Person: "cohort-reader"}, false, NOLOCK, func(tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM resource_contribution WHERE execution_id='cohort-00'`).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return errors.New("NULL derived scope leak")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}

	// A stalled clearing project must not fill every candidate slot indefinitely.
	sql(`INSERT INTO execution(id,kind,state,project_id) SELECT 'cohort-clear-'||i,'tool_run','succeeded','COHORT_CLEAR' FROM generate_series(1,105) i;
 INSERT INTO resource_build(project_id,phase) VALUES('COHORT_CLEAR','clearing');
 UPDATE resource_work SET next_attempt_at=clock_timestamp()-interval '1 day',first_pending_at=clock_timestamp()-interval '1 day' WHERE execution_id LIKE 'cohort-clear-%';
 INSERT INTO execution(id,kind,state,project_id) VALUES('cohort-healthy-other','tool_run','succeeded','COHORT_OTHER')`)
	var candidates []string
	if e = runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
		rows, e := q.tx.Query(ctx, resourceCandidatesSQL)
		if e != nil {
			return e
		}
		candidates, e = pgx.CollectRows(rows, pgx.RowTo[string])
		return e
	}); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, id := range candidates {
		if id == "cohort-healthy-other" {
			found = true
		}
	}
	if !found {
		t.Fatal("clearing hints starved a healthy project outside the first 100 rows")
	}
	t.Logf("16 independent leaves/one publication; SQL and backend %d rollback; late UUID retained; frozen method; lost COMMIT response/replay; bounded poison isolation; locked scope change/NULL/RLS passed", backend)
}

// The fixture reads PostgreSQL protocol frames and withholds exactly the first
// armed CommandComplete(COMMIT). PostgreSQL has committed before emitting it.
type resourceLostCommitConn struct {
	net.Conn
	reader  *bufio.Reader
	pending []byte
	drop    *atomic.Bool
}

func (c *resourceLostCommitConn) Read(dst []byte) (int, error) {
	if len(c.pending) == 0 {
		header := make([]byte, 5)
		if _, e := io.ReadFull(c.reader, header); e != nil {
			return 0, e
		}
		n := int(binary.BigEndian.Uint32(header[1:]))
		if n < 4 || n > 16*1024*1024 {
			return 0, errors.New("invalid fixture PostgreSQL frame")
		}
		body := make([]byte, n-4)
		if _, e := io.ReadFull(c.reader, body); e != nil {
			return 0, e
		}
		if header[0] == 'C' && string(body) == "COMMIT\x00" && c.drop.CompareAndSwap(true, false) {
			c.Conn.Close()
			return 0, errors.New("fixture lost committed response")
		}
		c.pending = append(header, body...)
	}
	n := copy(dst, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// Requirement: bounded selected-ID hints preserve every scope exclusion under FORCE RLS;
// risk: correlated scope scans amplify backlog, or missing/moving data authorizes an unsafe cohort.
func TestResourceBatchHintsPostgres(t *testing.T) {
	url := os.Getenv("FLEET_RESOURCE_TEST_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	sql := func(s string, args ...any) {
		t.Helper()
		if e := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return exec(q, s, args...) }); e != nil {
			t.Fatal(e)
		}
	}
	scalar := func(s string, args ...any) string {
		t.Helper()
		var v string
		if e := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(ctx, s, args...).Scan(&v) }); e != nil {
			t.Fatal(e)
		}
		return v
	}
	if got := scalar(`SELECT (relpages>0 AND reltuples=0)::text FROM pg_class WHERE relname='resource_work_scope'`); got != "true" {
		t.Fatal("fixture lacks retained empty queue pages/statistics", got)
	}
	seedStarted := time.Now()
	sql(`INSERT INTO execution(id,kind,state,project_id,harness,started_at,ended_at) SELECT 'hint-'||lpad(i::text,4,'0'),'agent_run','succeeded','HINT_A','codex','2024-01-01','2024-01-01 00:00:01' FROM generate_series(0,999)i;
 INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis) SELECT id,project_id,1,'measured' FROM execution WHERE project_id='HINT_A'`)
	t.Logf("scope fixture source seeding_ms=%.3f", time.Since(seedStarted).Seconds()*1000)
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("hint-%04d", i)
	}
	for _, n := range []int{100, 16} {
		queryStarted := time.Now()
		hints, e := resourceBatchHints(ctx, a, ids[:n])
		t.Logf("scope selected=%d acquire_query_decode_ms=%.3f", n, time.Since(queryStarted).Seconds()*1000)
		if e != nil {
			t.Fatal(e)
		}
		if len(hints) != n {
			t.Fatal("lost selected scopes", n, len(hints))
		}
		for _, id := range ids[:n] {
			if hints[id] != "HINT_A" {
				t.Fatal("selected scope", id, hints[id])
			}
		}
	}
	// The work queue has existing pages but empty ANALYZE statistics, as after a
	// drain. Count actual scope-row visits, not a wall-time threshold or a chosen node type.
	var raw json.RawMessage
	if e := st.readPool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+resourceBatchHintsSQL, ids).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var plans []map[string]any
	if e := json.Unmarshal(raw, &plans); e != nil {
		t.Fatal(e)
	}
	var scopeRows float64
	var walk func(map[string]any)
	walk = func(n map[string]any) {
		if n["Relation Name"] == "resource_work_scope" {
			rows, _ := n["Actual Rows"].(float64)
			filtered, _ := n["Rows Removed by Filter"].(float64)
			loops, _ := n["Actual Loops"].(float64)
			scopeRows += (rows + filtered) * loops
		}
		if children, ok := n["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(plans[0]["Plan"].(map[string]any))
	t.Logf("scope explain execution_ms=%v actual_work_scope_rows=%.0f", plans[0]["Execution Time"], scopeRows)
	var queueScopes int
	if e := st.readPool.QueryRow(ctx, `SELECT count(*) FROM resource_work_scope`).Scan(&queueScopes); e != nil {
		t.Fatal(e)
	}
	if scopeRows > float64(queueScopes+len(ids)) {
		t.Fatalf("selected hints revisited unrelated queue scopes: %.0f > %d", scopeRows, queueScopes+len(ids))
	}
	// Same canonical union as the scalar hint: NULL/empty source, deleted/missing
	// source, old contribution and retained cross-project work all remain distinct.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('hint-null','agent_run','succeeded',NULL),('hint-empty','agent_run','succeeded',''),('hint-moving','agent_run','succeeded','HINT_A'),('hint-pending-multi','agent_run','succeeded','HINT_A'),('hint-deleted','agent_run','succeeded','HINT_A'),('hint-from-null','agent_run','succeeded',NULL)`)
	for _, id := range []string{"hint-moving", "hint-deleted", "hint-from-null"} {
		if e := resourceReplace(ctx, a, id); e != nil {
			t.Fatal(e)
		}
	}
	sql(`UPDATE execution SET project_id='HINT_B' WHERE id IN ('hint-moving','hint-pending-multi');
 UPDATE execution SET project_id='HINT_A' WHERE id IN ('hint-pending-multi','hint-from-null');
 DELETE FROM execution WHERE id='hint-deleted'`)
	edge := []string{"hint-null", "hint-empty", "hint-moving", "hint-pending-multi", "hint-deleted", "hint-from-null", "hint-missing", "hint-0000", "hint-0000"}
	hints, e := resourceBatchHints(ctx, a, edge)
	if e != nil {
		t.Fatal(e)
	}
	expected := map[string]string{}
	for _, id := range edge {
		project := scalar(`SELECT coalesce((SELECT project_id FROM execution WHERE id=$1),'')`, id)
		scopes, e := resourceScopeHint(ctx, a, id)
		if e != nil {
			t.Fatal(e)
		}
		if project != "" && len(scopes) == 1 && scopes[0] == project {
			expected[id] = project
		}
	}
	if !reflect.DeepEqual(hints, expected) {
		t.Fatalf("bulk/scalar scope union mismatch: got%v expected%v", hints, expected)
	}
	if len(hints) != 2 || hints["hint-0000"] != "HINT_A" || hints["hint-from-null"] != "HINT_A" {
		t.Fatalf("unsafe scope exclusions: %v", hints)
	}
	before := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id IN ('hint-from-null','hint-0000')`)
	if e = resourceReplaceBatch(ctx, a, []string{"hint-from-null", "hint-0000"}); !errors.Is(e, errResourceScope) {
		t.Fatalf("a hint authorized an old NULL contribution: %v", e)
	}
	if after := scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id IN ('hint-from-null','hint-0000')`); after != before {
		t.Fatal("rejected authoritative scope lost work")
	}
	if e = st.Tx(ctx, &Scope{ProjectIDs: "HINT_B", Person: "hint-reader"}, false, NOLOCK, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, resourceBatchHintsSQL, ids)
		if e != nil {
			return e
		}
		defer rows.Close()
		if rows.Next() {
			return errors.New("selected-ID hint SQL leaked another project's source")
		}
		return rows.Err()
	}); e != nil {
		t.Fatal(e)
	}
	if e = resourceReplaceCandidates(ctx, a, []string{"hint-moving", "hint-pending-multi", "hint-deleted", "hint-from-null", "hint-0000"}); e != nil {
		t.Fatal(e)
	}
	if got := scalar(`SELECT count(*)::text FROM resource_work WHERE execution_id IN ('hint-moving','hint-pending-multi','hint-deleted','hint-from-null','hint-0000')`); got != "0" {
		t.Fatal("independent scope fallback did not drain", got)
	}
	// The empty fast path cannot acquire a connection or invent a scope.
	closed, e := openStore(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	closed.Close()
	if empty, e := resourceBatchHints(ctx, &App{store: closed}, nil); e != nil || len(empty) != 0 {
		t.Fatal("empty hints used database or invented rows", empty, e)
	}
	t.Logf("selected100/16 scopes equivalent; actual work-scope rows examined %.0f of %d, retained empty-page statistics; NULL/missing/moving/multi/duplicate exclusion, authoritative rejection, fallback and FORCE RLS passed", scopeRows, queueScopes)
}
