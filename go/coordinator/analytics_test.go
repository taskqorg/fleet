package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: fleet-analytics.md analytics_query accepts RFC3339 timestamps, dates, and epoch milliseconds; risk: boundary timestamps misparse or silently accept invalid values.
func TestParseAnalyticsBound(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Time
	}{
		{`"2026-11-10T01:02:03Z"`, time.Date(2026, 11, 10, 1, 2, 3, 0, time.UTC)},
		{`"2026-11-10T01:02:03.456Z"`, time.Date(2026, 11, 10, 1, 2, 3, 456000000, time.UTC)},
		{`"2026-11-10"`, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)},
		{`1794268800000`, time.UnixMilli(1794268800000).UTC()},
	} {
		got, err := parseAnalyticsBound([]byte(tc.raw))
		if err != nil {
			t.Fatalf("parseAnalyticsBound(%s) error = %v", tc.raw, err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("parseAnalyticsBound(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	for _, bad := range []string{``, `null`, `"not-a-date"`, `12.5`} {
		if _, err := parseAnalyticsBound([]byte(bad)); err == nil {
			t.Fatalf("parseAnalyticsBound(%s) succeeded, want 400 error", bad)
		}
	}
}

// Requirement: fleet-analytics.md resource basis rules preserve partial, subscription, estimated, measured, and unavailable distinctions; risk: mixed or partial series masquerade as measured.
func TestPickMeasuredNumber(t *testing.T) {
	val := 42.0
	if got := pickMeasuredNumber(nil, 1, 1, 0, 0); got.Basis != contract.ResourceBasisUnavailable || got.Value != nil {
		t.Fatalf("nil value gave %+v, want unavailable", got)
	}
	if got := pickMeasuredNumber(&val, 3, 2, 0, 0); got.Basis != contract.ResourceBasisUnavailable || got.Partial == nil || *got.Partial != "2 of 3 measured" {
		t.Fatalf("partial gave %+v, want partial 2 of 3", got)
	}
	if got := pickMeasuredNumber(&val, 2, 2, 1, 0); got.Basis != contract.ResourceBasisSubscription {
		t.Fatalf("subscription gave %+v, want subscription", got)
	}
	if got := pickMeasuredNumber(&val, 2, 2, 0, 1); got.Basis != contract.ResourceBasisEstimated {
		t.Fatalf("estimated gave %+v, want estimated", got)
	}
	if got := pickMeasuredNumber(&val, 2, 2, 0, 0); got.Basis != contract.ResourceBasisMeasured || *got.Value != 42 {
		t.Fatalf("measured gave %+v, want measured 42", got)
	}
}

// Requirement: CA-04/fleet-analytics.md CostFigure[] groups by (currency, basis) without mixing subscription into metered spend or turning unavailable into zero; risk: currencies collapse or subscription spend offsets/downgrades metered spend.
func TestDecodeCostFiguresAndMerge(t *testing.T) {
	usd := "USD"
	eur := "EUR"
	v1, v2, vSub, vEur := 1.5, 2.5, 9.0, 0.75
	note := subscriptionCostNote
	u1 := int64(2)

	leafA := resourceLeafRow{
		Costs: []contract.CostFigure{
			{Value: &v1, Currency: &usd, Basis: contract.ResourceBasisMeasured},
			{Value: &v2, Currency: &usd, Basis: contract.ResourceBasisEstimated, Prices: []contract.CostPriceRef{{Id: "p2", ObservedAt: 20}, {Id: "p1", ObservedAt: 10}}},
			{Value: &vSub, Currency: &usd, Basis: contract.ResourceBasisSubscription, Note: &note, Prices: []contract.CostPriceRef{{Id: "p1", ObservedAt: 10}}},
		},
	}
	leafB := resourceLeafRow{
		Costs: []contract.CostFigure{
			{Value: &v1, Currency: &usd, Basis: contract.ResourceBasisEstimated, Prices: []contract.CostPriceRef{{Id: "p1", ObservedAt: 10}}},
			{Value: &vEur, Currency: &eur, Basis: contract.ResourceBasisEstimated, Prices: []contract.CostPriceRef{{Id: "pe", ObservedAt: 30}}},
			{Value: nil, Currency: nil, Basis: contract.ResourceBasisUnavailable, Usages: &u1},
		},
	}

	merged := mergeCostFigures([]resourceLeafRow{leafA, leafB})
	if len(merged) != 5 {
		t.Fatalf("len(merged) = %d, want 5 distinct (currency, basis) + unavailable figures: %+v", len(merged), merged)
	}
	byKey := map[string]contract.CostFigure{}
	for _, f := range merged {
		curr := "<nil>"
		if f.Currency != nil {
			curr = *f.Currency
		}
		byKey[curr+":"+string(f.Basis)] = f
	}
	if f, ok := byKey["USD:measured"]; !ok || *f.Value != 1.5 {
		t.Fatalf("USD:measured = %+v, want 1.5", f)
	}
	if f, ok := byKey["USD:estimated"]; !ok || *f.Value != 4.0 || len(f.Prices) != 2 {
		t.Fatalf("USD:estimated = %+v, want 4.0 with 2 deduplicated prices", f)
	}
	if f, ok := byKey["USD:subscription"]; !ok || *f.Value != 9.0 || f.Note == nil || *f.Note != subscriptionCostNote {
		t.Fatalf("USD:subscription = %+v, want isolated 9.0 with subscription note", f)
	}
	if f, ok := byKey["EUR:estimated"]; !ok || *f.Value != 0.75 {
		t.Fatalf("EUR:estimated = %+v, want 0.75", f)
	}
	if f, ok := byKey["<nil>:unavailable"]; !ok || f.Value != nil || f.Usages == nil || *f.Usages != 2 {
		t.Fatalf("unavailable = %+v, want nil value with usages=2", f)
	}

	decoded, err := decodeCostFigures([]byte(`[{"value":3.25,"currency":"USD","basis":"estimated","prices":[{"id":"p1","observedAt":123}]}]`))
	if err != nil {
		t.Fatalf("decodeCostFigures error = %v", err)
	}
	wantDecoded := []contract.CostFigure{{
		Value:    ptr(3.25),
		Currency: ptr("USD"),
		Basis:    contract.ResourceBasisEstimated,
		Prices:   []contract.CostPriceRef{{Id: "p1", ObservedAt: 123}},
	}}
	if !reflect.DeepEqual(decoded, wantDecoded) {
		t.Fatalf("decodeCostFigures = %+v, want %+v", decoded, wantDecoded)
	}
}

// Requirement: CA-01/fleet-analytics.md distinguishes overlapping elapsedMs from summed executionWallMs and shields agent CPU/memory/tokens basis from HTTP tool_run nulls; risk: parallel leaves sum elapsed time or gateway calls downgrade measured agent metrics.
func TestSummarizeResourceLeaves(t *testing.T) {
	s1, e1 := int64(1000), int64(2500)
	s2, e2 := int64(1500), int64(4000)
	in1, out1 := int64(100), int64(40)
	wall1, cpu1, mem1 := int64(1500), int64(300), int64(4096)
	wall2 := int64(250)
	conn, tool := "github", "read"

	leaves := []resourceLeafRow{
		{
			ID: "a1", Kind: "agent_run", State: "succeeded", StartedMs: &s1, EndedMs: &e1, HasTotals: true,
			TokensInput: &in1, TokensInputBasis: contract.ResourceBasisMeasured,
			TokensOutput: &out1, TokensOutputBasis: contract.ResourceBasisMeasured,
			TokensCacheReadBasis: contract.ResourceBasisUnavailable, TokensCacheWriteBasis: contract.ResourceBasisUnavailable,
			WallMs: &wall1, WallMsBasis: contract.ResourceBasisMeasured,
			ActiveMs: &wall1, ActiveMsBasis: contract.ResourceBasisMeasured,
			CpuMs: &cpu1, CpuMsBasis: contract.ResourceBasisMeasured,
			PeakMemoryBytes: &mem1, PeakMemoryBytesBasis: contract.ResourceBasisMeasured,
			ToolCallsBasis: contract.ResourceBasisUnavailable,
		},
		{
			ID: "t1", Kind: "tool_run", State: "succeeded", StartedMs: &s2, EndedMs: &e2, HasTotals: true,
			TokensInputBasis: contract.ResourceBasisUnavailable, TokensOutputBasis: contract.ResourceBasisUnavailable,
			TokensCacheReadBasis: contract.ResourceBasisUnavailable, TokensCacheWriteBasis: contract.ResourceBasisUnavailable,
			WallMs: &wall2, WallMsBasis: contract.ResourceBasisMeasured,
			ActiveMs: &wall2, ActiveMsBasis: contract.ResourceBasisMeasured,
			CpuMsBasis: contract.ResourceBasisUnavailable, PeakMemoryBytesBasis: contract.ResourceBasisUnavailable,
			ToolConnection: &conn, ToolName: &tool, ToolCallsBasis: contract.ResourceBasisMeasured,
		},
	}

	got := summarizeResourceLeaves(leaves)
	if got.Executions != 2 {
		t.Fatalf("Executions = %d, want 2", got.Executions)
	}
	if got.Durations.ElapsedMs == nil || *got.Durations.ElapsedMs != 3000 {
		t.Fatalf("Durations.ElapsedMs = %v, want 3000 (4000 - 1000)", got.Durations.ElapsedMs)
	}
	if got.Durations.ExecutionWallMs == nil || *got.Durations.ExecutionWallMs != 1750 {
		t.Fatalf("Durations.ExecutionWallMs = %v, want 1750 (1500 + 250)", got.Durations.ExecutionWallMs)
	}
	if got.Durations.Basis != contract.ResourceBasisMeasured || got.Durations.CpuMs == nil || *got.Durations.CpuMs != 300 {
		t.Fatalf("Durations = %+v, want measured CpuMs=300 (tool_run must not downgrade)", got.Durations)
	}
	if got.PeakMemoryBytes.Basis != contract.ResourceBasisMeasured || got.PeakMemoryBytes.Value == nil || *got.PeakMemoryBytes.Value != 4096 {
		t.Fatalf("PeakMemoryBytes = %+v, want measured 4096", got.PeakMemoryBytes)
	}
	if got.Tokens.Basis != contract.ResourceBasisMeasured || got.Tokens.Input == nil || *got.Tokens.Input != 100 {
		t.Fatalf("Tokens = %+v, want measured input=100", got.Tokens)
	}
	if got.Tools.TotalCalls != 1 || len(got.Tools.ByTool) != 1 || got.Tools.ByTool[0].Connection == nil || *got.Tools.ByTool[0].Connection != "github" {
		t.Fatalf("Tools = %+v, want 1 github/read call", got.Tools)
	}
}

// Requirement: CA-01/fleet-analytics.md tokens basis degrades to unavailable when any expected leaf lacks input or output, and tools basis preserves weakest leaf toolCalls basis; risk: partial output tokens or estimated tool calls masquerade as measured.
func TestSummarizeResourceLeavesPartialTokensAndToolBasis(t *testing.T) {
	in1, out1, in2 := int64(100), int64(40), int64(60)
	gotPartial := summarizeResourceLeaves([]resourceLeafRow{
		{
			ID: "a1", Kind: "agent_run", State: "succeeded", HasTotals: true,
			TokensInput: &in1, TokensInputBasis: contract.ResourceBasisMeasured,
			TokensOutput: &out1, TokensOutputBasis: contract.ResourceBasisMeasured,
			TokensCacheReadBasis: contract.ResourceBasisUnavailable, TokensCacheWriteBasis: contract.ResourceBasisUnavailable,
			WallMsBasis: contract.ResourceBasisUnavailable, ActiveMsBasis: contract.ResourceBasisUnavailable,
			CpuMsBasis: contract.ResourceBasisUnavailable, PeakMemoryBytesBasis: contract.ResourceBasisUnavailable,
			ToolCallsRaw: []byte(`{"byTool":{"bash":{"calls":2,"errors":0}}}`), ToolCallsBasis: contract.ResourceBasisEstimated,
		},
		{
			ID: "a2", Kind: "agent_run", State: "succeeded", HasTotals: true,
			TokensInput: &in2, TokensInputBasis: contract.ResourceBasisMeasured,
			TokensOutput: nil, TokensOutputBasis: contract.ResourceBasisUnavailable,
			TokensCacheReadBasis: contract.ResourceBasisUnavailable, TokensCacheWriteBasis: contract.ResourceBasisUnavailable,
			WallMsBasis: contract.ResourceBasisUnavailable, ActiveMsBasis: contract.ResourceBasisUnavailable,
			CpuMsBasis: contract.ResourceBasisUnavailable, PeakMemoryBytesBasis: contract.ResourceBasisUnavailable,
			ToolCallsRaw: []byte(`{"byTool":{"read":{"calls":1,"errors":0}}}`), ToolCallsBasis: contract.ResourceBasisMeasured,
		},
	})
	if gotPartial.Tokens.Basis != contract.ResourceBasisUnavailable {
		t.Fatalf("partial output token summary gave Basis=%q, want unavailable", gotPartial.Tokens.Basis)
	}
	if gotPartial.Tools.Basis != contract.ResourceBasisEstimated || gotPartial.Tools.TotalCalls != 3 {
		t.Fatalf("estimated tool calls gave Tools=%+v, want basis=estimated totalCalls=3", gotPartial.Tools)
	}
}

// Requirement: fleet-analytics.md spend_summary matches analytics_query totals/dimensions/costs, respects fleet.project_ids RLS and hidden count, and stays consistent across backdated price_entry stale invalidation. Risk: execution_spend rollup drifts from live analytics_query, leaks hidden project counts, or serves stale cached prices after price_entry updates.
func TestSpendSummaryInvariant(t *testing.T) {
	e := openTestCoordinator(t, false)
	ctx := e.ctx
	t0 := time.Now().UTC().Truncate(time.Second)

	tx, err := e.ownerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL fleet.project_ids='*'`, nil},
		{`INSERT INTO api_connection(id,name,provider,payload) VALUES ('conn:gh','github','github','{}')`, nil},
		{`INSERT INTO definition(kind,id,path,head,ever_shipped,access_version) VALUES ('agent','coder','agents/coder.json',1,true,1)`, nil},
		{`INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message,at) VALUES ('agent','coder',1,'{"category":"engineering"}','sha1','editor','person','init',$1)`, []any{t0.Add(-5 * time.Minute)}},
		{`INSERT INTO task(id,project_id,title,state,priority,completion_requirements,created_at,updated_at,state_at,accepted_at) VALUES ('t-done','p1','Done task','completed','normal','{}',$1,$1,$1,$1)`, []any{t0.Add(-5 * time.Minute)}},
		{`INSERT INTO price_entry(id,subject,unit,price,currency,source,observed_at,effective_from) VALUES
			('pr:m1:in','m1','input_tokens',0.000002,'USD','owner',$1,$2),
			('pr:m1:out','m1','output_tokens',0.000010,'USD','owner',$1,$2),
			('pr:m2:in','m2','input_tokens',0.000005,'EUR','owner',$1,$2),
			('pr:gh:call','github','call',0.5,'USD','owner',$1,$2)`, []any{t0.Add(-5 * time.Minute), t0.Add(-time.Hour)}},
		{`INSERT INTO execution(id,kind,state,project_id,task_id,session_id,profile_id,model,host_id,started_at,ended_at,system_actor,tombstoned_at,data) VALUES
			('e-p1-task','agent_run','succeeded','p1','t-done',NULL,'coder','m1','h1',$1,$2,NULL,NULL,'{}'),
			('e-p1-conv','agent_run','failed','p1',NULL,'s1','coder','m2','h1',$1,$2,NULL,NULL,'{"account":{"id":"acct-1"}}'),
			('e-p1-tool','tool_run','succeeded','p1','t-done',NULL,'coder',NULL,'h1',$1,$2,NULL,NULL,'{"connection":"github"}'),
			('e-p1-unpriced','agent_run','succeeded','p1',NULL,NULL,NULL,'unknown-model','h2',$1,$2,NULL,NULL,'{}'),
			('e-p1-sys','tool_run','succeeded','p1',NULL,NULL,NULL,NULL,'h1',$1,$2,'coordinator',NULL,'{"connection":"github"}'),
			('e-p1-tomb','agent_run','succeeded','p1','t-done',NULL,'coder','m1','h1',$1,$2,NULL,$1,'{}'),
			('e-p2-other','agent_run','succeeded','p2',NULL,NULL,'coder','m1','h2',$1,$2,NULL,NULL,'{}'),
			('e-directed','agent_run','succeeded',NULL,NULL,NULL,NULL,'m1','h1',$1,$2,NULL,NULL,'{}')`, []any{t0.Add(-5 * time.Minute), t0.Add(-4 * time.Minute)}},
		{`INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,connection_cost,connection_cost_basis,billing_basis,model) VALUES
			('e-p1-task','p1',1000000,'measured',500000,'measured',NULL,'unavailable','api','m1'),
			('e-p1-conv','p1',500000,'measured',0,'unavailable','{"unit":"call","amount":2,"currency":"EUR"}','measured','api','m2'),
			('e-p1-tool','p1',NULL,'unavailable',NULL,'unavailable','{"unit":"call","amount":3}','measured','api',NULL),
			('e-p1-unpriced','p1',100,'measured',0,'measured',NULL,'unavailable','api','unknown-model'),
			('e-p2-other','p2',1000000,'measured',0,'measured',NULL,'unavailable','api','m1'),
			('e-directed',NULL,1000000,'measured',0,'measured',NULL,'unavailable','api','m1')`, nil},
	} {
		if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	win := contract.AnalyticsWindow{
		From: json.RawMessage(fmt.Sprintf("%d", t0.Add(-10*time.Minute).UnixMilli())),
		To:   json.RawMessage(fmt.Sprintf("%d", t0.Add(10*time.Minute).UnixMilli())),
	}
	measureInt := func(m map[string]contract.MeasuredNumber, k string) int64 {
		v, ok := m[k]
		if !ok || v.Value == nil {
			return 0
		}
		return int64(math.Round(*v.Value))
	}

	checkParity := func(label string, person *Person, sys *contract.SystemActivity) {
		t.Helper()
		dims := []contract.SpendGroupBy{
			contract.SpendGroupByPurpose,
			contract.SpendGroupByProject,
			contract.SpendGroupByCategory,
			contract.SpendGroupByProfile,
			contract.SpendGroupByModel,
			contract.SpendGroupByHost,
		}
		for _, by := range dims {
			var sum contract.SpendSummaryReply
			var aq contract.AnalyticsQueryReply
			var req *Request
			if person != nil {
				req = &Request{person: person, r: httptest.NewRequest("POST", "/", nil)}
			}
			adim, err := spendDimension(by)
			if err != nil {
				t.Fatalf("%s/%s: %v", label, by, err)
			}
			if err := e.app.store.Read(ctx, req, func(q *Request) error {
				var err error
				sum, err = spendSummary(q, &contract.SpendSummaryRequest{By: by, Window: win, System: sys})
				if err != nil {
					return err
				}
				aq, err = analyticsQuery(q, &contract.AnalyticsQueryRequest{
					Window:     win,
					System:     sys,
					Dimensions: []contract.AnalyticsDimension{adim},
					Measures: []contract.AnalyticsMeasure{
						contract.AnalyticsMeasureExecutions,
						contract.AnalyticsMeasureFailed,
						contract.AnalyticsMeasureTokensInput,
						contract.AnalyticsMeasureTokensOutput,
						contract.AnalyticsMeasureCost,
					},
				})
				return err
			}); err != nil {
				t.Fatalf("%s/%s: %v", label, by, err)
			}

			for _, m := range []string{"executions", "failed", "tokens_input", "tokens_output"} {
				sm, am := sum.Totals.Measures[m], aq.Totals.Measures[m]
				if (sm.Value == nil) != (am.Value == nil) || (sm.Value != nil && math.Abs(*sm.Value-*am.Value) > 1e-9) || sm.Basis != am.Basis {
					t.Fatalf("%s/%s total %s mismatch: spend=%+v analytics=%+v", label, by, m, sm, am)
				}
			}
			if !reflect.DeepEqual(sum.Totals.Cost, aq.Totals.Cost) {
				t.Fatalf("%s/%s total cost mismatch: spend=%+v analytics=%+v", label, by, sum.Totals.Cost, aq.Totals.Cost)
			}
			if len(sum.Rows) != len(aq.Rows) {
				t.Fatalf("%s/%s row count mismatch: spend=%d analytics=%d", label, by, len(sum.Rows), len(aq.Rows))
			}
			aqByKey := make(map[string]contract.AnalyticsBucketRow, len(aq.Rows))
			for _, ar := range aq.Rows {
				k := ar.Dimensions[string(adim)]
				if k == "" {
					k = "unknown"
				}
				aqByKey[k] = ar
			}
			for _, sr := range sum.Rows {
				ar, ok := aqByKey[sr.Key]
				if !ok {
					t.Fatalf("%s/%s missing bucket %q in analytics_query", label, by, sr.Key)
				}
				if sr.Runs != measureInt(ar.Measures, "executions") || sr.Failed != measureInt(ar.Measures, "failed") {
					t.Fatalf("%s/%s bucket %q runs/failed mismatch: spend=%+v analytics=%+v", label, by, sr.Key, sr, ar.Measures)
				}
				if !reflect.DeepEqual(sr.Cost, ar.Cost) {
					t.Fatalf("%s/%s bucket %q cost mismatch: spend=%+v analytics=%+v", label, by, sr.Key, sr.Cost, ar.Cost)
				}
			}
		}
	}

	// Owner sees everything (including p2 and directed NULL project execution); hidden must be 0.
	checkParity("owner", nil, nil)
	var ownerSum contract.SpendSummaryReply
	if err := e.app.store.Read(ctx, nil, func(q *Request) error {
		var err error
		ownerSum, err = spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByCategory, Window: win})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ownerSum.Bought.CompletedTasks != 1 || ownerSum.Hidden != 0 || len(ownerSum.ByPurpose) == 0 {
		t.Fatalf("unexpected owner spend_summary metadata: bought=%+v hidden=%d", ownerSum.Bought, ownerSum.Hidden)
	}
	// Multi-project filters (projectIds, excludeProjectIds, '-' for no project) must agree across equivalent selectors.
	if err := e.app.store.Read(ctx, nil, func(q *Request) error {
		p1Single, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ProjectId: ptr("p1")})
		if err != nil {
			return err
		}
		p1List, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ProjectIds: []string{"p1"}})
		if err != nil {
			return err
		}
		p1Excl, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ExcludeProjectIds: []string{"p2", "-"}})
		if err != nil {
			return err
		}
		p1Both, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ProjectIds: []string{"p1", "p2"}, ExcludeProjectIds: []string{"p2"}})
		if err != nil {
			return err
		}
		for i, got := range []contract.SpendSummaryReply{p1List, p1Excl, p1Both} {
			if !reflect.DeepEqual(got, p1Single) {
				t.Fatalf("p1 multi-project variant %d mismatch: got=%+v want=%+v", i, got, p1Single)
			}
		}
		p1Dash, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ProjectIds: []string{"p1", "-"}})
		if err != nil {
			return err
		}
		exclP2, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win, ExcludeProjectIds: []string{"p2"}})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(p1Dash, exclP2) || len(p1Dash.Rows) != 2 {
			t.Fatalf("p1 + '-' vs exclude p2 mismatch: p1Dash=%+v exclP2=%+v", p1Dash, exclP2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Restricted member granted only p1: cannot see p2 or project_id IS NULL directed execution, and sees hidden=2.
	var member Person
	_ = json.Unmarshal([]byte(`{"email":"m@x","roles":["operator"],"grants":[{"kind":"project","id":"p1"}]}`), &member)
	checkParity("member-p1", &member, nil)
	var memSum contract.SpendSummaryReply
	if err := e.app.store.Read(ctx, &Request{person: &member, r: httptest.NewRequest("POST", "/", nil)}, func(q *Request) error {
		var err error
		memSum, err = spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByProject, Window: win})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if memSum.Hidden != 2 {
		t.Fatalf("member hidden = %d, want 2 (p2 + directed)", memSum.Hidden)
	}

	// Backdated price_entry triggers stale fallback on e-p1-task/e-p2-other/e-directed; spend_summary must still match live analytics_query.
	if _, err := e.ownerStore.pool.Exec(ctx, `
		INSERT INTO price_entry(id,subject,unit,price,currency,source,observed_at,effective_from)
		VALUES ('pr:m1:in:v2','m1','input_tokens',0.000004,'USD','owner',$1,$2)
	`, t0, t0.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var staleCount int64
	if err := e.app.store.Read(ctx, nil, func(q *Request) error {
		return q.tx.QueryRow(q.ctx(), `SELECT count(*) FROM execution_spend WHERE stale`).Scan(&staleCount)
	}); err != nil {
		t.Fatal(err)
	}
	if staleCount == 0 {
		t.Fatal("expected backdated price_entry to mark execution_spend rows stale")
	}
	checkParity("after-backdated-price", nil, nil)
	// Requirement: fleet.md list_spend matches canonical projectResources across stale execution_spend rows and enforces project RLS.
	// Risk: stale cached spend rows or RLS bypass returns drifted or unauthorized list spend.
	if err := e.app.store.Read(ctx, &Request{person: &member, r: httptest.NewRequest("POST", "/", nil)}, func(q *Request) error {
		got, err := listSpend(q, &contract.ListSpendRequest{Kind: contract.ListSpendKindProjects, RowIds: []string{"p1", "p2", "p1"}})
		if err != nil {
			return err
		}
		if _, ok := got.Rows["p2"]; ok {
			t.Fatalf("restricted member saw unauthorized project p2 in listSpend")
		}
		p1Res, err := projectResources(q, "p1")
		if err != nil {
			return err
		}
		gotJSON, _ := json.Marshal(got.Rows["p1"])
		for i := range p1Res.Cost {
			p1Res.Cost[i].Prices = nil
		}
		wantJSON, _ := json.Marshal(p1Res.Cost)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("listSpend(p1) = %s, want %s", gotJSON, wantJSON)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
