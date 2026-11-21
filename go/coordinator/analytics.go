// Analytics query (analytics_query) and unified resource / multi-currency cost rollups
// shared across task_get, session_get, thread boundaries, definition_usage, and project_context.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

const (
	subscriptionCostNote  = "subscription (list-price equivalent)"
	unavailableCostReason = "no usage amount or price entry"
)

var defaultAnalyticsMeasures = []contract.AnalyticsMeasure{
	contract.AnalyticsMeasureExecutions,
	contract.AnalyticsMeasureFailed,
	contract.AnalyticsMeasureTokensInput,
	contract.AnalyticsMeasureTokensOutput,
	contract.AnalyticsMeasureTokensCacheRead,
	contract.AnalyticsMeasureTokensCacheWrite,
	contract.AnalyticsMeasureWallMs,
	contract.AnalyticsMeasureCost,
}

func init() {
	op("analytics_query", analyticsQuery)
	op("spend_summary", spendSummary)
}

// systemFilter: the condition that keeps project work (exclude, the default), adds Fleet's own background calls
// (include) or keeps them alone (only) (docs/designs/fleet-analytics.md, System activity). col is the system_actor column.
func systemFilter(s *contract.SystemActivity, col string) string {
	var v contract.SystemActivity
	if s != nil {
		v = *s
	}
	switch v {
	case contract.SystemActivityInclude:
		return "true"
	case contract.SystemActivityOnly:
		return col + " IS NOT NULL"
	}
	return col + " IS NULL"
}

// systemOf: the system activity of a request; unset with a named parent, that parent's children, system ones included
// (a read token's calls are listed under its id).
func systemOf(s *contract.SystemActivity, parentNamed bool) *contract.SystemActivity {
	if s == nil && parentNamed {
		include := contract.SystemActivityInclude
		return &include
	}
	return s
}

func basisOrder(b contract.ResourceBasis) int {
	switch b {
	case contract.ResourceBasisUnavailable:
		return 0
	case contract.ResourceBasisSubscription:
		return 1
	case contract.ResourceBasisEstimated:
		return 2
	case contract.ResourceBasisMeasured:
		return 3
	default:
		return 0
	}
}

func weakestBasis(bases ...contract.ResourceBasis) contract.ResourceBasis {
	if len(bases) == 0 {
		return contract.ResourceBasisUnavailable
	}
	w := bases[0]
	for _, b := range bases[1:] {
		if basisOrder(b) < basisOrder(w) {
			w = b
		}
	}
	return w
}

func costFigurePriority(b contract.ResourceBasis) int {
	switch b {
	case contract.ResourceBasisMeasured:
		return 0
	case contract.ResourceBasisEstimated:
		return 1
	case contract.ResourceBasisSubscription:
		return 2
	case contract.ResourceBasisUnavailable:
		return 3
	default:
		return 4
	}
}

func sortCostFigures(cs []contract.CostFigure) {
	sort.Slice(cs, func(i, j int) bool {
		pi, pj := costFigurePriority(cs[i].Basis), costFigurePriority(cs[j].Basis)
		if pi != pj {
			return pi < pj
		}
		return deref(cs[i].Currency) < deref(cs[j].Currency)
	})
}

func parseAnalyticsBound(raw json.RawMessage) (time.Time, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return time.Time{}, fail(400, "window.from and window.to are required")
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return time.Time{}, fail(400, "Invalid window timestamp")
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, fail(400, "Invalid window timestamp: expected epoch ms, RFC 3339, or YYYY-MM-DD")
	}
	var num json.Number
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		return time.Time{}, fail(400, "Invalid window timestamp")
	}
	if i, err := num.Int64(); err == nil {
		return time.UnixMilli(i).UTC(), nil
	}
	f, err := num.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
		return time.Time{}, fail(400, "Invalid window timestamp")
	}
	return time.UnixMilli(int64(f)).UTC(), nil
}

func rawFilterStrings(raws []json.RawMessage) ([]string, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		s := strings.TrimSpace(string(r))
		if strings.HasPrefix(s, `"`) {
			var v string
			if err := json.Unmarshal(r, &v); err != nil {
				return nil, fail(400, "Invalid revision filter")
			}
			out = append(out, v)
		} else {
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fail(400, "Invalid revision filter: expected integer or string")
			}
			out = append(out, strconv.FormatInt(i, 10))
		}
	}
	return out, nil
}

func decodeCostFigures(raw []byte) ([]contract.CostFigure, error) {
	type sqlCostRow struct {
		Currency *string                 `json:"currency"`
		Basis    contract.ResourceBasis  `json:"basis"`
		Value    *float64                `json:"value"`
		Prices   []contract.CostPriceRef `json:"prices"`
		Unpriced *int64                  `json:"unpriced"`
	}
	if len(raw) == 0 {
		return []contract.CostFigure{}, nil
	}
	var rows []sqlCostRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]contract.CostFigure, 0, len(rows))
	var unpricedTotal int64
	for _, r := range rows {
		if r.Unpriced != nil && *r.Unpriced > 0 {
			unpricedTotal += *r.Unpriced
			continue
		}
		if r.Currency == nil || r.Value == nil {
			continue
		}
		prices := r.Prices
		if prices == nil {
			prices = []contract.CostPriceRef{}
		}
		sort.Slice(prices, func(i, j int) bool {
			if prices[i].Id != prices[j].Id {
				return prices[i].Id < prices[j].Id
			}
			return prices[i].ObservedAt < prices[j].ObservedAt
		})
		cf := contract.CostFigure{
			Value:    r.Value,
			Currency: r.Currency,
			Basis:    r.Basis,
			Prices:   prices,
		}
		if r.Basis == contract.ResourceBasisSubscription {
			cf.Note = ptr(subscriptionCostNote)
		}
		out = append(out, cf)
	}
	if unpricedTotal > 0 {
		out = append(out, contract.CostFigure{
			Value:  nil,
			Basis:  contract.ResourceBasisUnavailable,
			Reason: ptr(unavailableCostReason),
			Usages: ptr(unpricedTotal),
		})
	}
	sortCostFigures(out)
	return out, nil
}

type sqlMeasureStats struct {
	ApiMs         *float64 `json:"apiMs"`
	ApiMsHave     int64    `json:"apiMsHave"`
	ApiMsSub      int64    `json:"apiMsSub"`
	ApiMsEst      int64    `json:"apiMsEst"`
	ApiMsExpected int64    `json:"apiMsExpected"`

	TokensR         *float64 `json:"tokensReasoning"`
	TokensRHave     int64    `json:"tokensReasoningHave"`
	TokensRSub      int64    `json:"tokensReasoningSub"`
	TokensREst      int64    `json:"tokensReasoningEst"`
	TokensRExpected int64    `json:"tokensReasoningExpected"`

	Count             int64    `json:"count"`
	Failed            int64    `json:"failed"`
	TokensIn          *float64 `json:"tokensIn"`
	TokensInHave      int64    `json:"tokensInHave"`
	TokensInSub       int64    `json:"tokensInSub"`
	TokensInEst       int64    `json:"tokensInEst"`
	TokensInExpected  int64    `json:"tokensInExpected"`
	TokensOut         *float64 `json:"tokensOut"`
	TokensOutHave     int64    `json:"tokensOutHave"`
	TokensOutSub      int64    `json:"tokensOutSub"`
	TokensOutEst      int64    `json:"tokensOutEst"`
	TokensOutExpected int64    `json:"tokensOutExpected"`
	TokensCR          *float64 `json:"tokensCR"`
	TokensCRHave      int64    `json:"tokensCRHave"`
	TokensCRSub       int64    `json:"tokensCRSub"`
	TokensCREst       int64    `json:"tokensCREst"`
	TokensCRExpected  int64    `json:"tokensCRExpected"`
	TokensCW          *float64 `json:"tokensCW"`
	TokensCWHave      int64    `json:"tokensCWHave"`
	TokensCWSub       int64    `json:"tokensCWSub"`
	TokensCWEst       int64    `json:"tokensCWEst"`
	TokensCWExpected  int64    `json:"tokensCWExpected"`
	WallMs            *float64 `json:"wallMs"`
	WallMsP50         *float64 `json:"wallMsP50"`
	WallMsP95         *float64 `json:"wallMsP95"`
	WallMsHave        int64    `json:"wallMsHave"`
	WallMsSub         int64    `json:"wallMsSub"`
	WallMsEst         int64    `json:"wallMsEst"`
	WallMsExpected    int64    `json:"wallMsExpected"`
	ActiveMs          *float64 `json:"activeMs"`
	ActiveMsHave      int64    `json:"activeMsHave"`
	ActiveMsSub       int64    `json:"activeMsSub"`
	ActiveMsEst       int64    `json:"activeMsEst"`
	ActiveMsExpected  int64    `json:"activeMsExpected"`
	CpuMs             *float64 `json:"cpuMs"`
	CpuMsHave         int64    `json:"cpuMsHave"`
	CpuMsSub          int64    `json:"cpuMsSub"`
	CpuMsEst          int64    `json:"cpuMsEst"`
	CpuMsExpected     int64    `json:"cpuMsExpected"`
	PeakMem           *float64 `json:"peakMem"`
	PeakMemHave       int64    `json:"peakMemHave"`
	PeakMemSub        int64    `json:"peakMemSub"`
	PeakMemEst        int64    `json:"peakMemEst"`
	PeakMemExpected   int64    `json:"peakMemExpected"`
	Calls             *float64 `json:"calls"`
	CallsHave         int64    `json:"callsHave"`
	CallsExpected     int64    `json:"callsExpected"`
	Credits           *float64 `json:"credits"`
	CreditsHave       int64    `json:"creditsHave"`
	CreditsExpected   int64    `json:"creditsExpected"`
}

func pickMeasuredNumber(val *float64, expected, have, sub, est int64) contract.MeasuredNumber {
	if expected == 0 || have == 0 || val == nil {
		return contract.MeasuredNumber{Basis: contract.ResourceBasisUnavailable}
	}
	v := *val
	if have < expected {
		return contract.MeasuredNumber{
			Value:   &v,
			Basis:   contract.ResourceBasisUnavailable,
			Partial: ptr(fmt.Sprintf("%d of %d measured", have, expected)),
		}
	}
	basis := contract.ResourceBasisMeasured
	if sub > 0 {
		basis = contract.ResourceBasisSubscription
	} else if est > 0 {
		basis = contract.ResourceBasisEstimated
	}
	return contract.MeasuredNumber{Value: &v, Basis: basis}
}

// nativeMeasuredNumber explains unsupported native observations without substituting wall time or zero.
func nativeMeasuredNumber(value *float64, expected, have, sub, est int64, reason string) contract.MeasuredNumber {
	n := pickMeasuredNumber(value, expected, have, sub, est)
	if n.Value == nil {
		n.Reason = &reason
	}
	return n
}

func buildAnalyticsMeasureMap(s sqlMeasureStats, measures []contract.AnalyticsMeasure) map[string]contract.MeasuredNumber {
	out := make(map[string]contract.MeasuredNumber, len(measures))
	for _, m := range measures {
		switch m {
		case contract.AnalyticsMeasureExecutions, contract.AnalyticsMeasureCount:
			v := float64(s.Count)
			out[string(m)] = contract.MeasuredNumber{Value: &v, Basis: contract.ResourceBasisMeasured}
		case contract.AnalyticsMeasureFailed:
			v := float64(s.Failed)
			out[string(m)] = contract.MeasuredNumber{Value: &v, Basis: contract.ResourceBasisMeasured}
		case contract.AnalyticsMeasureTokensInput:
			out[string(m)] = pickMeasuredNumber(s.TokensIn, s.TokensInExpected, s.TokensInHave, s.TokensInSub, s.TokensInEst)
		case contract.AnalyticsMeasureTokensOutput:
			out[string(m)] = pickMeasuredNumber(s.TokensOut, s.TokensOutExpected, s.TokensOutHave, s.TokensOutSub, s.TokensOutEst)
		case contract.AnalyticsMeasureTokensCacheRead:
			out[string(m)] = pickMeasuredNumber(s.TokensCR, s.TokensCRExpected, s.TokensCRHave, s.TokensCRSub, s.TokensCREst)
		case contract.AnalyticsMeasureTokensCacheWrite:
			out[string(m)] = pickMeasuredNumber(s.TokensCW, s.TokensCWExpected, s.TokensCWHave, s.TokensCWSub, s.TokensCWEst)
		case contract.AnalyticsMeasureTokensReasoning:
			out[string(m)] = nativeMeasuredNumber(s.TokensR, s.TokensRExpected, s.TokensRHave, s.TokensRSub, s.TokensREst, "harness did not report reasoning tokens")
		case contract.AnalyticsMeasureWallMs:
			out[string(m)] = pickMeasuredNumber(s.WallMs, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureWallMsP50:
			out[string(m)] = pickMeasuredNumber(s.WallMsP50, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureWallMsP95:
			out[string(m)] = pickMeasuredNumber(s.WallMsP95, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureActiveMs:
			out[string(m)] = pickMeasuredNumber(s.ActiveMs, s.ActiveMsExpected, s.ActiveMsHave, s.ActiveMsSub, s.ActiveMsEst)
		case contract.AnalyticsMeasureApiMs:
			out[string(m)] = nativeMeasuredNumber(s.ApiMs, s.ApiMsExpected, s.ApiMsHave, s.ApiMsSub, s.ApiMsEst, "harness did not report API duration")
		case contract.AnalyticsMeasureCpuMs:
			out[string(m)] = pickMeasuredNumber(s.CpuMs, s.CpuMsExpected, s.CpuMsHave, s.CpuMsSub, s.CpuMsEst)
		case contract.AnalyticsMeasurePeakMemoryBytes:
			out[string(m)] = pickMeasuredNumber(s.PeakMem, s.PeakMemExpected, s.PeakMemHave, s.PeakMemSub, s.PeakMemEst)
		case contract.AnalyticsMeasureCalls:
			out[string(m)] = pickMeasuredNumber(s.Calls, s.CallsExpected, s.CallsHave, 0, 0)
		case contract.AnalyticsMeasureCredits:
			out[string(m)] = pickMeasuredNumber(s.Credits, s.CreditsExpected, s.CreditsHave, 0, 0)
		case contract.AnalyticsMeasureCost:
			// Cost lives in the multi-currency CostFigure[] field on each row and totals.
		}
	}
	return out
}

func analyticsQuery(q *Request, r *contract.AnalyticsQueryRequest) (contract.AnalyticsQueryReply, error) {
	var rep contract.AnalyticsQueryReply
	from, err := parseAnalyticsBound(r.Window.From)
	if err != nil {
		return rep, err
	}
	to, err := parseAnalyticsBound(r.Window.To)
	if err != nil {
		return rep, err
	}
	if !from.Before(to) {
		return rep, fail(400, "window.from must be before window.to")
	}

	dims := make([]contract.AnalyticsDimension, 0, len(r.Dimensions))
	seenDims := make(map[contract.AnalyticsDimension]bool, len(r.Dimensions))
	for _, d := range r.Dimensions {
		if _, ok := analyticsDimExpr[d]; !ok {
			return rep, fail(400, "Unknown dimension: "+string(d))
		}
		if !seenDims[d] {
			seenDims[d] = true
			dims = append(dims, d)
		}
	}

	measures := r.Measures
	if len(measures) == 0 {
		measures = defaultAnalyticsMeasures
	}
	wantCost := false
	seenMeasures := make(map[contract.AnalyticsMeasure]bool, len(measures))
	dedupMeasures := make([]contract.AnalyticsMeasure, 0, len(measures))
	for _, m := range measures {
		if !seenMeasures[m] {
			seenMeasures[m] = true
			dedupMeasures = append(dedupMeasures, m)
			if m == contract.AnalyticsMeasureCost {
				wantCost = true
			}
		}
	}

	// Fleet's own background calls stay out of the counts unless asked for; hidden still compares unfiltered windows.
	// Only when every value names a parent: "unknown" (and "", read as it) is no parent.
	parentNamed := r.Filters != nil && len(r.Filters.Parent) > 0 &&
		!slices.ContainsFunc(r.Filters.Parent, func(p string) bool { return p == "" || p == "unknown" })
	if r.ProjectId != nil && *r.ProjectId == "" {
		return rep, fail(400, "Invalid projectId")
	}
	var filters []sqlAnalyticsFilter
	if f := r.Filters; f != nil {
		pipelineRevs, err := rawFilterStrings(f.PipelineRevision)
		if err != nil {
			return rep, err
		}
		profileRevs, err := rawFilterStrings(f.ProfileRevision)
		if err != nil {
			return rep, err
		}
		filters = []sqlAnalyticsFilter{
			{contract.AnalyticsDimensionProject, f.Project}, {contract.AnalyticsDimensionProject, f.ProjectId},
			{contract.AnalyticsDimensionKind, f.Kind}, {contract.AnalyticsDimensionParent, f.Parent},
			{contract.AnalyticsDimensionPipeline, f.Pipeline}, {contract.AnalyticsDimensionPipelineRevision, pipelineRevs},
			{contract.AnalyticsDimensionStep, f.Step}, {contract.AnalyticsDimensionProfileId, f.ProfileId},
			{contract.AnalyticsDimensionProfileRevision, profileRevs}, {contract.AnalyticsDimensionHarness, f.Harness},
			{contract.AnalyticsDimensionModel, f.Model}, {contract.AnalyticsDimensionProvider, f.Provider},
			{contract.AnalyticsDimensionAccount, f.Account}, {contract.AnalyticsDimensionHost, f.Host},
			{contract.AnalyticsDimensionConnection, f.Connection}, {contract.AnalyticsDimensionExperiment, f.Experiment},
			{contract.AnalyticsDimensionVariant, f.Variant}, {contract.AnalyticsDimensionState, f.State},
			{contract.AnalyticsDimensionOutcome, f.Outcome}, {contract.AnalyticsDimensionPerson, f.Person},
			{contract.AnalyticsDimensionPurpose, f.Purpose}, {contract.AnalyticsDimensionCategory, f.Category},
		}
	}
	querySQL, params := sqlAnalytics(from, to, systemOf(r.System, parentNamed), r.ProjectId, filters, dims, wantCost)

	sqlRows, err := q.tx.Query(q.ctx(), querySQL, params...)
	if err != nil {
		return rep, fmt.Errorf("analytics_query: %w", err)
	}
	defer sqlRows.Close()

	rep.Rows = []contract.AnalyticsBucketRow{}
	rep.Totals = contract.AnalyticsTotals{
		Measures: map[string]contract.MeasuredNumber{},
		Cost:     []contract.CostFigure{},
	}
	var windowVisible int64
	for sqlRows.Next() {
		var isTotal bool
		var dimsRaw, statsRaw, costsRaw []byte
		var unknownCnt int64
		if err := sqlRows.Scan(&isTotal, &dimsRaw, &statsRaw, &costsRaw, &windowVisible, &unknownCnt); err != nil {
			return rep, err
		}
		var stats sqlMeasureStats
		if err := json.Unmarshal(statsRaw, &stats); err != nil {
			return rep, err
		}
		costFigs, err := decodeCostFigures(costsRaw)
		if err != nil {
			return rep, err
		}
		if !wantCost {
			costFigs = []contract.CostFigure{}
		}
		mMap := buildAnalyticsMeasureMap(stats, dedupMeasures)
		if isTotal {
			rep.Totals = contract.AnalyticsTotals{Measures: mMap, Cost: costFigs}
			rep.Unknown = unknownCnt
			if len(dims) == 0 && stats.Count > 0 {
				rep.Rows = append(rep.Rows, contract.AnalyticsBucketRow{
					Dimensions: map[string]string{},
					Measures:   mMap,
					Cost:       costFigs,
				})
			}
		} else {
			var dimVals []string
			if err := json.Unmarshal(dimsRaw, &dimVals); err != nil {
				return rep, err
			}
			dimMap := make(map[string]string, len(dims))
			for i, d := range dims {
				val := "unknown"
				if i < len(dimVals) && dimVals[i] != "" {
					val = dimVals[i]
				}
				dimMap[string(d)] = val
			}
			rep.Rows = append(rep.Rows, contract.AnalyticsBucketRow{
				Dimensions: dimMap,
				Measures:   mMap,
				Cost:       costFigs,
			})
		}
	}
	if err := sqlRows.Err(); err != nil {
		return rep, err
	}

	// Hidden: unfiltered count of leaf executions in [from, to) excluded by caller's RLS scope (CA-05).
	if q.person != nil && !fullAccess(q.person) {
		windowAll, err := q.db().AnalyticsWindowLeafCount(q.ctx(), db.AnalyticsWindowLeafCountParams{WinFrom: from, WinTo: to})
		if err != nil {
			return rep, err
		}
		if windowAll > windowVisible {
			rep.Hidden = windowAll - windowVisible
		}
	}

	return rep, nil
}

// ---- Shared ResourceSummary & ScopedResourceRollup SQL / Go Builders ----

type resourceLeafRow struct {
	Scope                 string                 `json:"-"`
	ScopeKey              string                 `json:"-"`
	ID                    string                 `json:"id"`
	Kind                  string                 `json:"kind"`
	State                 string                 `json:"state"`
	StartedMs             *int64                 `json:"started_ms"`
	EndedMs               *int64                 `json:"ended_ms"`
	HasTotals             bool                   `json:"has_totals"`
	TokensInput           *int64                 `json:"tokens_input"`
	TokensInputBasis      contract.ResourceBasis `json:"tokens_input_basis"`
	TokensOutput          *int64                 `json:"tokens_output"`
	TokensOutputBasis     contract.ResourceBasis `json:"tokens_output_basis"`
	TokensCacheRead       *int64                 `json:"tokens_cache_read"`
	TokensCacheReadBasis  contract.ResourceBasis `json:"tokens_cache_read_basis"`
	TokensCacheWrite      *int64                 `json:"tokens_cache_write"`
	TokensCacheWriteBasis contract.ResourceBasis `json:"tokens_cache_write_basis"`
	TokensReasoning       *int64                 `json:"tokens_reasoning"`
	TokensReasoningBasis  contract.ResourceBasis `json:"tokens_reasoning_basis"`
	ApiMs                 *int64                 `json:"api_ms"`
	ApiMsBasis            contract.ResourceBasis `json:"api_ms_basis"`
	WallMs                *int64                 `json:"wall_ms"`
	WallMsBasis           contract.ResourceBasis `json:"wall_ms_basis"`
	ActiveMs              *int64                 `json:"active_ms"`
	ActiveMsBasis         contract.ResourceBasis `json:"active_ms_basis"`
	CpuMs                 *int64                 `json:"cpu_ms"`
	CpuMsBasis            contract.ResourceBasis `json:"cpu_ms_basis"`
	PeakMemoryBytes       *int64                 `json:"peak_memory_bytes"`
	PeakMemoryBytesBasis  contract.ResourceBasis `json:"peak_memory_bytes_basis"`
	TurnCount             int64                  `json:"turn_count"`
	ToolCallsRaw          json.RawMessage        `json:"tool_calls"`
	ToolCallsBasis        contract.ResourceBasis `json:"tool_calls_basis"`
	ToolConnection        *string                `json:"connection"`
	ToolName              *string                `json:"tool_name"`
	Credits               *float64               `json:"credits"`
	HasConnectionCost     bool                   `json:"has_conn_cost"`
	CostsRaw              json.RawMessage        `json:"leaf_costs"`
	Costs                 []contract.CostFigure  `json:"-"`
}

// summarizeResourceQuery summarizes the scoped leaves of one rollup (a lineage query of queries/analytics.sql) per
// scope and scope key ("scope\x00key"), reading each leaf once with its costs (AnalyticsLeafRows), so cost items are
// never counted twice across scopes.
func summarizeResourceQuery(q *Request, scopes, keys, leaves []string) (map[string]contract.ResourceSummary, error) {
	rows, err := q.db().AnalyticsLeafRows(q.ctx(), db.AnalyticsLeafRowsParams{Scopes: scopes, ScopeKeys: keys, LeafIds: leaves})
	if err != nil {
		return nil, err
	}
	grouped := map[string][]resourceLeafRow{}
	for _, row := range rows {
		var r resourceLeafRow
		if err := json.Unmarshal(row.Leaf, &r); err != nil {
			return nil, err
		}
		r.Scope, r.ScopeKey = row.Scope, row.ScopeKey
		r.Costs, err = decodeCostFigures(r.CostsRaw)
		if err != nil {
			return nil, err
		}
		k := r.Scope + "\x00" + r.ScopeKey
		grouped[k] = append(grouped[k], r)
	}
	out := make(map[string]contract.ResourceSummary, len(grouped))
	for k, ls := range grouped {
		out[k] = summarizeResourceLeaves(ls)
	}
	return out, nil
}

func emptyResourceSummary() contract.ResourceSummary {
	return contract.ResourceSummary{
		Executions:      0,
		Tokens:          contract.TokenBreakdown{Basis: contract.ResourceBasisUnavailable},
		Durations:       contract.DurationBreakdown{Basis: contract.ResourceBasisUnavailable, MeasuredCpuRuns: 0, TotalRuns: 0},
		PeakMemoryBytes: contract.MeasuredInt{Basis: contract.ResourceBasisUnavailable},
		Tools:           contract.ToolUsageSummary{Basis: contract.ResourceBasisUnavailable, ByTool: []contract.ToolUsageCount{}},
		Cost:            []contract.CostFigure{},
	}
}

func mergeCostFigures(leaves []resourceLeafRow) []contract.CostFigure {
	type groupKey struct {
		currency string
		basis    contract.ResourceBasis
	}
	type acc struct {
		value  float64
		prices map[string]int64
	}
	groups := map[groupKey]*acc{}
	var unpriced int64
	for _, l := range leaves {
		for _, cf := range l.Costs {
			if cf.Basis == contract.ResourceBasisUnavailable || cf.Currency == nil || cf.Value == nil {
				if cf.Usages != nil {
					unpriced += *cf.Usages
				} else {
					unpriced++
				}
				continue
			}
			k := groupKey{*cf.Currency, cf.Basis}
			a := groups[k]
			if a == nil {
				a = &acc{prices: map[string]int64{}}
				groups[k] = a
			}
			a.value += *cf.Value
			for _, pr := range cf.Prices {
				a.prices[pr.Id] = pr.ObservedAt
			}
		}
	}
	out := make([]contract.CostFigure, 0, len(groups)+1)
	for k, a := range groups {
		v := a.value
		curr := k.currency
		prices := make([]contract.CostPriceRef, 0, len(a.prices))
		for id, obs := range a.prices {
			prices = append(prices, contract.CostPriceRef{Id: id, ObservedAt: obs})
		}
		sort.Slice(prices, func(i, j int) bool {
			if prices[i].Id != prices[j].Id {
				return prices[i].Id < prices[j].Id
			}
			return prices[i].ObservedAt < prices[j].ObservedAt
		})
		cf := contract.CostFigure{Value: &v, Currency: &curr, Basis: k.basis, Prices: prices}
		if k.basis == contract.ResourceBasisSubscription {
			cf.Note = ptr(subscriptionCostNote)
		}
		out = append(out, cf)
	}
	if unpriced > 0 {
		out = append(out, contract.CostFigure{
			Value:  nil,
			Basis:  contract.ResourceBasisUnavailable,
			Reason: ptr(unavailableCostReason),
			Usages: ptr(unpriced),
		})
	}
	sortCostFigures(out)
	return out
}

func summarizeResourceLeaves(leaves []resourceLeafRow) contract.ResourceSummary {
	if len(leaves) == 0 {
		return emptyResourceSummary()
	}
	out := emptyResourceSummary()
	out.Executions = int64(len(leaves))

	// Tokens (agent_run / child_agent rows determine token basis so gateway tool_runs don't downgrade).
	var inSum, outSum, crSum, cwSum, reasoningSum, apiSum int64
	var reasoningHave, apiHave int64
	var inHave, outHave, crHave, cwHave, tokExpected int64
	var tokBases []contract.ResourceBasis

	// Durations & compute.
	var wallSum, activeSum, cpuSum int64
	var wallHave, activeHave, cpuHave, procRuns int64
	var minStart, maxEnd *int64
	var wallBases []contract.ResourceBasis

	// Peak memory.
	var maxMem *int64
	var memHave int64
	var memBases []contract.ResourceBasis

	// Credits.
	var creditsSum float64
	var creditsHave int64

	// Tools.
	type toolKey struct{ conn, name string }
	type toolStat struct{ calls, errors int64 }
	toolStats := map[toolKey]*toolStat{}
	var totalCalls, failedCalls, turns, otherCalls, toolMeasured, toolExpected int64
	var toolBases []contract.ResourceBasis

	for _, l := range leaves {
		if l.StartedMs != nil && (minStart == nil || *l.StartedMs < *minStart) {
			v := *l.StartedMs
			minStart = &v
		}
		if l.EndedMs != nil && (maxEnd == nil || *l.EndedMs > *maxEnd) {
			v := *l.EndedMs
			maxEnd = &v
		}

		isTokActor := l.Kind == "agent_run" || l.Kind == "child_agent" || l.TokensInput != nil || l.TokensOutput != nil
		if isTokActor {
			tokExpected++
			if l.TokensInput != nil {
				inSum += *l.TokensInput
				inHave++
				tokBases = append(tokBases, l.TokensInputBasis)
			}
			if l.TokensOutput != nil {
				outSum += *l.TokensOutput
				outHave++
				tokBases = append(tokBases, l.TokensOutputBasis)
			}
			if l.TokensCacheRead != nil {
				crSum += *l.TokensCacheRead
				crHave++
				tokBases = append(tokBases, l.TokensCacheReadBasis)
			}
			if l.TokensCacheWrite != nil {
				cwSum += *l.TokensCacheWrite
				cwHave++
				tokBases = append(tokBases, l.TokensCacheWriteBasis)
			}
		}

		if l.TokensReasoning != nil {
			reasoningSum += *l.TokensReasoning
			reasoningHave++
		}
		if l.ApiMs != nil {
			apiSum += *l.ApiMs
			apiHave++
		}
		if l.WallMs != nil {
			wallSum += *l.WallMs
			wallHave++
			wallBases = append(wallBases, l.WallMsBasis)
		} else {
			wallBases = append(wallBases, contract.ResourceBasisUnavailable)
		}
		if l.ActiveMs != nil {
			activeSum += *l.ActiveMs
			activeHave++
		}
		if l.Kind != "tool_run" {
			procRuns++
			if l.CpuMs != nil {
				cpuSum += *l.CpuMs
				cpuHave++
			}
			if l.PeakMemoryBytes != nil {
				if maxMem == nil || *l.PeakMemoryBytes > *maxMem {
					v := *l.PeakMemoryBytes
					maxMem = &v
				}
				memHave++
				memBases = append(memBases, l.PeakMemoryBytesBasis)
			} else {
				memBases = append(memBases, contract.ResourceBasisUnavailable)
			}
		}

		if l.Credits != nil {
			creditsSum += *l.Credits
			creditsHave++
		}

		turns += l.TurnCount
		if l.Kind == "tool_run" {
			toolExpected++
			toolMeasured++
			b := l.ToolCallsBasis
			if b == "" || b == contract.ResourceBasisUnavailable {
				b = contract.ResourceBasisMeasured
			}
			toolBases = append(toolBases, b)
			conn := deref(l.ToolConnection)
			name := deref(l.ToolName)
			if name == "" {
				name = "tool_run"
			}
			tk := toolKey{conn, name}
			ts := toolStats[tk]
			if ts == nil {
				ts = &toolStat{}
				toolStats[tk] = ts
			}
			ts.calls++
			totalCalls++
			if l.State == "failed" || l.State == "cancelled" {
				ts.errors++
				failedCalls++
			}
		} else if l.Kind == "agent_run" || l.Kind == "child_agent" {
			toolExpected++
			if len(l.ToolCallsRaw) > 0 && string(l.ToolCallsRaw) != "null" {
				var tc struct {
					ByTool map[string]struct {
						Calls  int64 `json:"calls"`
						Errors int64 `json:"errors"`
					} `json:"byTool"`
					Other *struct {
						Calls  int64 `json:"calls"`
						Errors int64 `json:"errors"`
					} `json:"other"`
				}
				if err := json.Unmarshal(l.ToolCallsRaw, &tc); err == nil {
					toolMeasured++
					b := l.ToolCallsBasis
					if b == "" {
						b = contract.ResourceBasisUnavailable
					}
					toolBases = append(toolBases, b)
					for name, c := range tc.ByTool {
						tk := toolKey{name: name}
						ts := toolStats[tk]
						if ts == nil {
							ts = &toolStat{}
							toolStats[tk] = ts
						}
						ts.calls += c.Calls
						ts.errors += c.Errors
						totalCalls += c.Calls
						failedCalls += c.Errors
					}
					if tc.Other != nil {
						otherCalls += tc.Other.Calls
						totalCalls += tc.Other.Calls
						failedCalls += tc.Other.Errors
					}
				}
			}
		}
	}

	// Token breakdown.
	if tokExpected > 0 && (inHave > 0 || outHave > 0 || crHave > 0 || cwHave > 0) {
		if inHave > 0 {
			out.Tokens.Input = &inSum
		}
		if outHave > 0 {
			out.Tokens.Output = &outSum
		}
		if crHave > 0 {
			out.Tokens.CacheRead = &crSum
		}
		if cwHave > 0 {
			out.Tokens.CacheWrite = &cwSum
		}
		totalTok := inSum + outSum + crSum + cwSum
		out.Tokens.Total = &totalTok
		if inHave < tokExpected || outHave < tokExpected {
			out.Tokens.Basis = contract.ResourceBasisUnavailable
		} else {
			out.Tokens.Basis = weakestBasis(tokBases...)
		}
	}

	if reasoningHave > 0 {
		out.Tokens.Reasoning = &reasoningSum
	}
	if apiHave > 0 {
		out.Durations.ApiMs = &apiSum
	}
	// Durations.
	out.Durations.TotalRuns = procRuns
	out.Durations.MeasuredCpuRuns = cpuHave
	if minStart != nil && maxEnd != nil && *maxEnd >= *minStart {
		el := *maxEnd - *minStart
		out.Durations.ElapsedMs = &el
	}
	if wallHave > 0 {
		out.Durations.ExecutionWallMs = &wallSum
	}
	if activeHave > 0 {
		out.Durations.ActiveMs = &activeSum
	}
	if cpuHave > 0 {
		out.Durations.CpuMs = &cpuSum
	}
	if wallHave > 0 && wallHave == int64(len(leaves)) {
		out.Durations.Basis = weakestBasis(wallBases...)
	} else {
		out.Durations.Basis = contract.ResourceBasisUnavailable
	}

	// Peak memory.
	if maxMem != nil {
		b := contract.ResourceBasisUnavailable
		if memHave == procRuns {
			b = weakestBasis(memBases...)
		}
		var partial *string
		if memHave < procRuns {
			partial = ptr(fmt.Sprintf("%d of %d measured", memHave, procRuns))
		}
		out.PeakMemoryBytes = contract.MeasuredInt{Value: maxMem, Basis: b, Partial: partial}
	}

	// Credits.
	if creditsHave > 0 {
		out.Credits = &creditsSum
	}

	// Tools summary (failed calls for tool_run added accurately via SQL rollup helper when needed; see summarizeResourceLeavesSQL below).
	toolEntries := make([]contract.ToolUsageCount, 0, len(toolStats))
	for tk, ts := range toolStats {
		var cPtr *string
		if tk.conn != "" {
			c := tk.conn
			cPtr = &c
		}
		toolEntries = append(toolEntries, contract.ToolUsageCount{
			Name:       tk.name,
			Connection: cPtr,
			Calls:      ts.calls,
			Errors:     ts.errors,
		})
	}
	sort.Slice(toolEntries, func(i, j int) bool {
		if toolEntries[i].Errors != toolEntries[j].Errors {
			return toolEntries[i].Errors > toolEntries[j].Errors
		}
		if toolEntries[i].Calls != toolEntries[j].Calls {
			return toolEntries[i].Calls > toolEntries[j].Calls
		}
		if toolEntries[i].Name != toolEntries[j].Name {
			return toolEntries[i].Name < toolEntries[j].Name
		}
		return deref(toolEntries[i].Connection) < deref(toolEntries[j].Connection)
	})
	if len(toolEntries) > 20 {
		for _, extra := range toolEntries[20:] {
			otherCalls += extra.Calls
		}
		toolEntries = toolEntries[:20]
	}
	if otherCalls > 0 {
		out.Tools.Other = &otherCalls
	}
	out.Tools.ByTool = toolEntries
	out.Tools.TotalCalls = totalCalls
	out.Tools.FailedCalls = failedCalls
	out.Tools.Turns = turns
	if toolExpected > 0 && toolMeasured == toolExpected && len(toolBases) > 0 {
		out.Tools.Basis = weakestBasis(toolBases...)
	}

	out.Cost = mergeCostFigures(leaves)
	return out
}

// resourceLineageQuery suppresses JIT only while computing a resource lineage.
// On error, the enclosing transaction rolls back its LOCAL setting; do not
// send a restore query into a canceled or aborted transaction.
func resourceLineageQuery[T any](q *Request, run func() (T, error)) (out T, err error) {
	d := q.db()
	previous, err := d.AnalyticsJIT(q.ctx())
	if err != nil {
		return out, err
	}
	if previous == "off" {
		return run()
	}
	if err = d.AnalyticsSetJIT(q.ctx(), "off"); err != nil {
		return out, err
	}
	if out, err = run(); err != nil {
		return out, err
	}
	return out, d.AnalyticsSetJIT(q.ctx(), previous)
}

func taskResourceRollup(q *Request, taskID string) (contract.ScopedResourceRollup, error) {
	return resourceLineageQuery(q, func() (contract.ScopedResourceRollup, error) { return taskResourceRollupQuery(q, taskID) })
}

func taskResourceRollupQuery(q *Request, taskID string) (contract.ScopedResourceRollup, error) {
	out := contract.ScopedResourceRollup{
		Own:          emptyResourceSummary(),
		Delegated:    emptyResourceSummary(),
		Spawned:      emptyResourceSummary(),
		Total:        emptyResourceSummary(),
		SpawnedTasks: []contract.SpawnedTaskRef{},
	}

	lineage, err := q.db().AnalyticsTaskLineage(q.ctx(), taskID)
	if err != nil {
		return out, err
	}
	summaries, err := summarizeResourceQuery(q, lineage.Scopes, lineage.ScopeKeys, lineage.LeafIds)
	if err != nil {
		return out, err
	}
	if s, ok := summaries["own\x00"]; ok {
		out.Own = s
	}
	if s, ok := summaries["delegated\x00"]; ok {
		out.Delegated = s
	}
	if s, ok := summaries["spawned\x00"]; ok {
		out.Spawned = s
	}
	if s, ok := summaries["total\x00"]; ok {
		out.Total = s
	}

	// The visible spawned tasks in order.
	if err := spawnedTasks(&out, lineage.SpawnedTasks, summaries); err != nil {
		return out, err
	}

	// Hidden direct lineage count under RLS (CA-05): compare visible vs total downstream leaf delegations + spawned tasks via SECURITY DEFINER helper.
	if q.person != nil && !fullAccess(q.person) {
		if out.Hidden, err = q.db().AnalyticsTaskHiddenLineage(q.ctx(), taskID); err != nil {
			return out, err
		}
	}

	return out, nil
}

func sessionResourceRollup(q *Request, sessionID string) (contract.ScopedResourceRollup, error) {
	return resourceLineageQuery(q, func() (contract.ScopedResourceRollup, error) { return sessionResourceRollupQuery(q, sessionID) })
}

func sessionResourceRollupQuery(q *Request, sessionID string) (contract.ScopedResourceRollup, error) {
	out := contract.ScopedResourceRollup{
		Own:          emptyResourceSummary(),
		Delegated:    emptyResourceSummary(),
		Spawned:      emptyResourceSummary(),
		Total:        emptyResourceSummary(),
		SpawnedTasks: []contract.SpawnedTaskRef{},
	}

	lineage, err := q.db().AnalyticsSessionLineage(q.ctx(), sessionID)
	if err != nil {
		return out, err
	}
	summaries, err := summarizeResourceQuery(q, lineage.Scopes, lineage.ScopeKeys, lineage.LeafIds)
	if err != nil {
		return out, err
	}
	if s, ok := summaries["own\x00"]; ok {
		out.Own = s
	}
	if s, ok := summaries["delegated\x00"]; ok {
		out.Delegated = s
	}
	if s, ok := summaries["spawned\x00"]; ok {
		out.Spawned = s
	}
	if s, ok := summaries["total\x00"]; ok {
		out.Total = s
	}

	// The visible spawned tasks in order.
	if err := spawnedTasks(&out, lineage.SpawnedTasks, summaries); err != nil {
		return out, err
	}

	if q.person != nil && !fullAccess(q.person) {
		if out.Hidden, err = q.db().AnalyticsSessionHiddenLineage(q.ctx(), sessionID); err != nil {
			return out, err
		}
	}

	return out, nil
}

// boundaryResourcesMap returns ResourceSummary per boundary execution id (including its direct tool_run children).
func boundaryResourcesMap(q *Request, ids []string) (map[string]contract.ResourceSummary, error) {
	if len(ids) == 0 {
		return map[string]contract.ResourceSummary{}, nil
	}
	lineage, err := q.db().AnalyticsBoundaryLineage(q.ctx(), ids)
	if err != nil {
		return nil, err
	}
	raw, err := summarizeResourceQuery(q, lineage.Scopes, lineage.ScopeKeys, lineage.LeafIds)
	if err != nil {
		return nil, err
	}
	out := make(map[string]contract.ResourceSummary, len(ids))
	const prefix = "boundary\x00"
	for k, v := range raw {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out, nil
}

// definitionUsageResources computes ResourceSummary overall per definition ID and per (id, revision)
// across non-draft pinned executions in the last `days`, resolving container descendants to leaves without double-counting.
// definitionUsageResources materializes visible child/parent edges once. An OR
// join inside the recursive term rescans every execution for every tree row and
// spills wide session rows; the narrow edge relation preserves all four links.
func definitionUsageResources(q *Request, kind, id string, days int64) (map[string]contract.ResourceSummary, map[string]map[int64]contract.ResourceSummary, error) {
	pin := map[string]string{"kind": kind}
	if id != "" {
		pin["id"] = id
	}
	lineage, err := q.db().AnalyticsDefinitionLineage(q.ctx(), db.AnalyticsDefinitionLineageParams{Pin: jsonBytes([]map[string]string{pin}), Kind: kind, ID: id, Days: int32(days)})
	if err != nil {
		return nil, nil, err
	}
	raw, err := summarizeResourceQuery(q, lineage.Scopes, lineage.ScopeKeys, lineage.LeafIds)
	if err != nil {
		return nil, nil, err
	}
	tot := map[string]contract.ResourceSummary{}
	byRev := map[string]map[int64]contract.ResourceSummary{}
	for k, v := range raw {
		if strings.HasPrefix(k, "total\x00") {
			tot[strings.TrimPrefix(k, "total\x00")] = v
		} else if strings.HasPrefix(k, "rev\x00") {
			rest := strings.TrimPrefix(k, "rev\x00")
			idx := strings.LastIndex(rest, ":")
			if idx > 0 {
				did := rest[:idx]
				revNum, convErr := strconv.ParseInt(rest[idx+1:], 10, 64)
				if convErr == nil {
					if byRev[did] == nil {
						byRev[did] = map[int64]contract.ResourceSummary{}
					}
					byRev[did][revNum] = v
				}
			}
		}
	}
	return tot, byRev, nil
}

// projectResources summarizes all visible leaf executions belonging to projectID: its work, without Fleet's own
// background calls (system_actor).
func projectResources(q *Request, projectID string) (contract.ResourceSummary, error) {
	if projectID == "" {
		return emptyResourceSummary(), nil
	}
	lineage, err := q.db().AnalyticsProjectLineage(q.ctx(), projectID)
	if err != nil {
		return emptyResourceSummary(), err
	}
	raw, err := summarizeResourceQuery(q, lineage.Scopes, lineage.ScopeKeys, lineage.LeafIds)
	if err != nil {
		return emptyResourceSummary(), err
	}
	if s, ok := raw["project\x00"]; ok {
		return s, nil
	}
	return emptyResourceSummary(), nil
}

// spawnedTasks sets the rollup's spawned tasks (a lineage query's jsonb array, in order) with their resources.
func spawnedTasks(out *contract.ScopedResourceRollup, raw []byte, summaries map[string]contract.ResourceSummary) error {
	var tasks []contract.SpawnedTaskRef
	if err := json.Unmarshal(raw, &tasks); err != nil {
		return err
	}
	for _, st := range tasks {
		if s, ok := summaries["spawned_task\x00"+st.TaskId]; ok {
			st.Resources = s
		} else {
			st.Resources = emptyResourceSummary()
		}
		out.SpawnedTasks = append(out.SpawnedTasks, st)
	}
	return nil
}

// spendDimension maps a spend_summary breakdown key to the analytics_query dimension it aggregates over.
func spendDimension(by contract.SpendGroupBy) (contract.AnalyticsDimension, error) {
	switch by {
	case contract.SpendGroupByPurpose:
		return contract.AnalyticsDimensionPurpose, nil
	case contract.SpendGroupByProject:
		return contract.AnalyticsDimensionProject, nil
	case contract.SpendGroupByCategory:
		return contract.AnalyticsDimensionCategory, nil
	case contract.SpendGroupByProfile:
		return contract.AnalyticsDimensionProfileId, nil
	case contract.SpendGroupByModel:
		return contract.AnalyticsDimensionModel, nil
	case contract.SpendGroupByHost:
		return contract.AnalyticsDimensionHost, nil
	}
	return "", fail(400, "Unknown spend group by: "+string(by))
}

func measureInt(m map[string]contract.MeasuredNumber, key string) int64 {
	if v, ok := m[key]; ok && v.Value != nil {
		return int64(math.Round(*v.Value))
	}
	return 0
}

func costUsdAndUnpriced(cost []contract.CostFigure) (float64, int64) {
	var usd float64
	var unpriced int64
	for _, c := range cost {
		if c.Basis == contract.ResourceBasisUnavailable {
			if c.Usages != nil {
				unpriced += *c.Usages
			} else {
				unpriced++
			}
			continue
		}
		if c.Currency != nil && *c.Currency == "USD" && c.Value != nil {
			usd += *c.Value
		}
	}
	return usd, unpriced
}

func spendRowsFromBucket(rows []contract.AnalyticsBucketRow, dim contract.AnalyticsDimension, totalsUsd float64) []contract.SpendRow {
	out := make([]contract.SpendRow, 0, len(rows))
	for _, row := range rows {
		key := row.Dimensions[string(dim)]
		if key == "" {
			key = "unknown"
		}
		tokens := measureInt(row.Measures, "tokens_input") + measureInt(row.Measures, "tokens_output")
		usd, unpriced := costUsdAndUnpriced(row.Cost)
		sr := contract.SpendRow{
			Key:      key,
			Label:    key,
			Runs:     measureInt(row.Measures, "executions"),
			Failed:   measureInt(row.Measures, "failed"),
			Tokens:   tokens,
			Cost:     row.Cost,
			Unpriced: unpriced,
		}
		if totalsUsd > 0 && usd > 0 {
			s := usd / totalsUsd
			sr.Share = &s
		}
		out = append(out, sr)
	}
	sort.Slice(out, func(i, j int) bool {
		ui, _ := costUsdAndUnpriced(out[i].Cost)
		uj, _ := costUsdAndUnpriced(out[j].Cost)
		if ui != uj {
			return ui > uj
		}
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// spendSummary returns spend breakdown for the requested dimension, always accompanied by purpose totals and
// completed-task efficiency ("what it bought").
func spendSummary(q *Request, r *contract.SpendSummaryRequest) (contract.SpendSummaryReply, error) {
	var out contract.SpendSummaryReply
	dim, err := spendDimension(r.By)
	if err != nil {
		return out, err
	}
	from, err := parseAnalyticsBound(r.Window.From)
	if err != nil {
		return out, err
	}
	to, err := parseAnalyticsBound(r.Window.To)
	if err != nil {
		return out, err
	}
	measures := []contract.AnalyticsMeasure{
		contract.AnalyticsMeasureExecutions,
		contract.AnalyticsMeasureFailed,
		contract.AnalyticsMeasureTokensInput,
		contract.AnalyticsMeasureTokensOutput,
		contract.AnalyticsMeasureCost,
	}
	mainRep, err := analyticsQuery(q, &contract.AnalyticsQueryRequest{
		Window:     r.Window,
		Dimensions: []contract.AnalyticsDimension{dim},
		Measures:   measures,
		ProjectId:  r.ProjectId,
		System:     r.System,
	})
	if err != nil {
		return out, err
	}
	purposeRep := mainRep
	if dim != contract.AnalyticsDimensionPurpose {
		purposeRep, err = analyticsQuery(q, &contract.AnalyticsQueryRequest{
			Window:     r.Window,
			Dimensions: []contract.AnalyticsDimension{contract.AnalyticsDimensionPurpose},
			Measures:   measures,
			ProjectId:  r.ProjectId,
			System:     r.System,
		})
		if err != nil {
			return out, err
		}
	}
	taskByProjRep, err := analyticsQuery(q, &contract.AnalyticsQueryRequest{
		Window:     r.Window,
		Dimensions: []contract.AnalyticsDimension{contract.AnalyticsDimensionProject},
		Measures:   []contract.AnalyticsMeasure{contract.AnalyticsMeasureExecutions, contract.AnalyticsMeasureCost},
		Filters:    &contract.AnalyticsFilter{Purpose: []string{"task"}},
		ProjectId:  r.ProjectId,
		System:     r.System,
	})
	if err != nil {
		return out, err
	}
	completedRows, err := q.db().AnalyticsCompletedTasksByProject(q.ctx(), db.AnalyticsCompletedTasksByProjectParams{
		WinFrom:   from,
		WinTo:     to,
		ProjectID: r.ProjectId,
	})
	if err != nil {
		return out, err
	}

	totalUsd, _ := costUsdAndUnpriced(mainRep.Totals.Cost)
	byPurpose := spendRowsFromBucket(purposeRep.Rows, contract.AnalyticsDimensionPurpose, totalUsd)

	bought := contract.SpendBought{
		TaskCost:         []contract.CostFigure{},
		ConversationCost: []contract.CostFigure{},
		ByProject:        []contract.SpendBoughtProject{},
	}
	for _, row := range purposeRep.Rows {
		switch row.Dimensions["purpose"] {
		case "task":
			bought.TaskCost = row.Cost
		case "conversation":
			bought.ConversationRuns = measureInt(row.Measures, "executions")
			bought.ConversationCost = row.Cost
		}
	}

	taskSpend := map[string]contract.AnalyticsBucketRow{}
	for _, row := range taskByProjRep.Rows {
		pid := row.Dimensions["project"]
		if pid == "unknown" {
			pid = ""
		}
		taskSpend[pid] = row
	}
	completedMap := map[string]int64{}
	seenProj := map[string]bool{}
	projKeys := []string{}
	for _, c := range completedRows {
		completedMap[c.ProjectID] = c.N
		bought.CompletedTasks += c.N
		if !seenProj[c.ProjectID] {
			seenProj[c.ProjectID] = true
			projKeys = append(projKeys, c.ProjectID)
		}
	}
	for pid := range taskSpend {
		if !seenProj[pid] {
			seenProj[pid] = true
			projKeys = append(projKeys, pid)
		}
	}
	for _, pid := range projKeys {
		done := completedMap[pid]
		bucket, ok := taskSpend[pid]
		cost := []contract.CostFigure{}
		if ok {
			cost = bucket.Cost
		}
		usd, unpriced := costUsdAndUnpriced(cost)
		label := pid
		if label == "" {
			label = "unknown"
		}
		item := contract.SpendBoughtProject{
			ProjectId:      label,
			CompletedTasks: done,
			TaskCost:       cost,
			Unpriced:       unpriced,
		}
		if done > 0 && usd > 0 {
			cpt := usd / float64(done)
			item.CostPerTaskUsd = &cpt
		}
		bought.ByProject = append(bought.ByProject, item)
	}
	sort.Slice(bought.ByProject, func(i, j int) bool {
		if bought.ByProject[i].CompletedTasks != bought.ByProject[j].CompletedTasks {
			return bought.ByProject[i].CompletedTasks > bought.ByProject[j].CompletedTasks
		}
		return bought.ByProject[i].ProjectId < bought.ByProject[j].ProjectId
	})

	out = contract.SpendSummaryReply{
		By:        r.By,
		Totals:    mainRep.Totals,
		Rows:      spendRowsFromBucket(mainRep.Rows, dim, totalUsd),
		ByPurpose: byPurpose,
		Bought:    bought,
		Hidden:    mainRep.Hidden,
		Unknown:   mainRep.Unknown,
	}
	return out, nil
}
