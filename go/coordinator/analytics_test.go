package main

import (
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
