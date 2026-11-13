// Analytics query (analytics_query) and unified resource / multi-currency cost rollups
// shared across task_get, session_get, thread boundaries, definition_usage, and project_context.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

const (
	analyticsNumericRegex = `^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$`
	analyticsLeafFilter   = `e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL)`
	subscriptionCostNote  = "subscription (list-price equivalent)"
	unavailableCostReason = "no usage amount or price entry"
)

// analyticsDimExpr maps every contract AnalyticsDimension to its nullable PostgreSQL expression
// over the materialized leaves CTE alias `l` (sourced strictly from typed execution/join columns).
var analyticsDimExpr = map[contract.AnalyticsDimension]string{
	contract.AnalyticsDimensionDay:              `to_char(l.started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')`,
	contract.AnalyticsDimensionHour:             `to_char(l.started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24":00:00Z"')`,
	contract.AnalyticsDimensionWeek:             `to_char(date_trunc('week', l.started_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`,
	contract.AnalyticsDimensionProject:          `l.project_id`,
	contract.AnalyticsDimensionKind:             `l.kind`,
	contract.AnalyticsDimensionParent:           `l.parent_execution_id`,
	contract.AnalyticsDimensionPipeline:         `l.pipeline_id`,
	contract.AnalyticsDimensionPipelineRevision: `l.pipeline_revision::text`,
	contract.AnalyticsDimensionStep:             `l.step_id`,
	contract.AnalyticsDimensionProfileId:        `l.profile_id`,
	contract.AnalyticsDimensionProfileRevision:  `l.profile_revision::text`,
	contract.AnalyticsDimensionHarness:          `l.harness`,
	contract.AnalyticsDimensionModel:            `l.model`,
	contract.AnalyticsDimensionProvider:         `l.provider`,
	contract.AnalyticsDimensionAccount:          `l.account_id`,
	contract.AnalyticsDimensionHost:             `l.host_id`,
	contract.AnalyticsDimensionConnection:       `l.connection`,
	contract.AnalyticsDimensionExperiment:       `l.experiment_id`,
	contract.AnalyticsDimensionVariant:          `l.variant`,
	contract.AnalyticsDimensionState:            `l.state`,
	contract.AnalyticsDimensionOutcome:          `l.outcome`,
	contract.AnalyticsDimensionPerson:           `l.person`,
}

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
	gatedObjOp("analytics_query", true, typedOp(analyticsQuery))
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

// analyticsBaseLeavesSelect is the shared column projection over `execution e` + `execution_totals t` + `document conn`.
const analyticsBaseLeavesSelect = `
 SELECT
  e.id,
  e.kind::text AS kind,
  e.state::text AS state,
  e.project_id,
  e.task_id,
  e.session_id,
  e.parent_execution_id,
  e.pipeline_id,
  e.pipeline_revision,
  e.step_id,
  e.profile_id,
  e.profile_revision,
  e.harness,
  coalesce(t.model, e.model) AS model,
  coalesce(conn.data->>'provider', nullif(e.data->>'provider', ''), CASE e.harness WHEN 'codex' THEN 'openai' WHEN 'claude' THEN 'anthropic' WHEN 'antigravity' THEN 'google' END) AS provider,
  e.account_id,
  e.host_id,
  nullif(e.data->>'connection', '') AS connection,
  nullif(e.data->>'tool', '') AS tool_name,
  e.experiment_id,
  e.variant,
  coalesce(nullif(e.data->>'outcome', ''), CASE WHEN e.state IN ('succeeded', 'failed', 'cancelled') THEN e.state::text END) AS outcome,
  coalesce(e.directed_by, t.directed_by) AS person,
  e.started_at,
  e.ended_at,
  coalesce(e.started_at, CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint / 1000.0) END, e.ended_at, e.last_seen_at) AS leaf_at,
  (t.execution_id IS NOT NULL) AS has_totals,
  (coalesce(t.billing_basis, '') = 'subscription' OR (coalesce(t.billing_basis, '') IN ('', 'unavailable') AND e.account_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM document a WHERE a.kind = 'api-connection' AND a.id = e.account_id))) AS is_sub,
  t.tokens_input,
  coalesce(t.tokens_input_basis, 'unavailable') AS tokens_input_basis,
  t.tokens_output,
  coalesce(t.tokens_output_basis, 'unavailable') AS tokens_output_basis,
  t.tokens_cache_read,
  coalesce(t.tokens_cache_read_basis, 'unavailable') AS tokens_cache_read_basis,
  t.tokens_cache_write,
  coalesce(t.tokens_cache_write_basis, 'unavailable') AS tokens_cache_write_basis,
  t.wall_ms,
  coalesce(t.wall_ms_basis, 'unavailable') AS wall_ms_basis,
  t.active_ms,
  coalesce(t.active_ms_basis, 'unavailable') AS active_ms_basis,
  t.cpu_ms,
  coalesce(t.cpu_ms_basis, 'unavailable') AS cpu_ms_basis,
  t.peak_memory_bytes,
  coalesce(t.peak_memory_bytes_basis, 'unavailable') AS peak_memory_bytes_basis,
  coalesce(t.turn_count, 0)::bigint AS turn_count,
  t.tool_calls,
  coalesce(t.tool_calls_basis, 'unavailable') AS tool_calls_basis,
  t.connection_cost,
  coalesce(t.connection_cost_basis, 'unavailable') AS connection_cost_basis,
  coalesce(e.data->>'connection', '') || '/' || coalesce(e.data->>'tool', '') AS conn_subject,
  nullif(t.connection_cost->>'unit', '') AS conn_unit,
  CASE
   WHEN t.connection_cost IS NOT NULL AND coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END) ~ '` + analyticsNumericRegex + `'
   THEN coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END)::float8
  END AS credits,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '` + analyticsNumericRegex + `'
     THEN t.connection_cost->>'currency'
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '` + analyticsNumericRegex + `'
     THEN t.connection_cost->>'unit'
   END
  END AS direct_currency,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '` + analyticsNumericRegex + `'
     THEN coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency'))::numeric
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '` + analyticsNumericRegex + `'
     THEN coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit'))::numeric
   END
  END AS direct_amount,
  CASE
   WHEN t.connection_cost IS NOT NULL AND coalesce(t.connection_cost->>'amount', CASE WHEN nullif(t.connection_cost->>'unit', '') IS NOT NULL THEN t.connection_cost->>(t.connection_cost->>'unit') END) ~ '` + analyticsNumericRegex + `'
   THEN coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit'))::numeric
  END AS conn_amount,
  (CASE WHEN e.kind = 'tool_run' THEN 1::bigint
        ELSE coalesce((SELECT sum((v->>'calls')::bigint) FROM jsonb_each(coalesce(t.tool_calls->'byTool', '{}'::jsonb)) kv(k, v) WHERE (v->>'calls') ~ '^[0-9]+$'), 0)
           + coalesce((CASE WHEN (t.tool_calls->'other'->>'calls') ~ '^[0-9]+$' THEN (t.tool_calls->'other'->>'calls')::bigint END), 0)
   END) AS leaf_calls,
  (CASE WHEN e.kind = 'tool_run' THEN (CASE WHEN e.state IN ('failed', 'cancelled') THEN 1::bigint ELSE 0::bigint END)
        ELSE coalesce((SELECT sum((v->>'errors')::bigint) FROM jsonb_each(coalesce(t.tool_calls->'byTool', '{}'::jsonb)) kv(k, v) WHERE (v->>'errors') ~ '^[0-9]+$'), 0)
           + coalesce((CASE WHEN (t.tool_calls->'other'->>'errors') ~ '^[0-9]+$' THEN (t.tool_calls->'other'->>'errors')::bigint END), 0)
   END) AS leaf_call_errors
 FROM execution e
 LEFT JOIN execution_totals t ON t.execution_id = e.id
 LEFT JOIN document conn ON conn.kind = 'api-connection' AND (conn.id = 'connection:' || coalesce(e.data->>'connection', '') OR conn.id = e.account_id)`

// analyticsLeafCostCTEs assumes a `leaves` CTE is in scope and resolves every leaf's prices via LATERAL
// lookups on `price_entry_subject` BEFORE any outer GROUP BY (`CA-04`).
const analyticsLeafCostCTEs = `
leaf_priced AS (
 SELECT
  l.*,
  pi.id AS pi_id, pi.price AS pi_price, pi.currency AS pi_currency, pi.obs_ms AS pi_obs,
  po.id AS po_id, po.price AS po_price, po.currency AS po_currency, po.obs_ms AS po_obs,
  pcr.id AS pcr_id, pcr.price AS pcr_price, pcr.currency AS pcr_currency, pcr.obs_ms AS pcr_obs,
  pcw.id AS pcw_id, pcw.price AS pcw_price, pcw.currency AS pcw_currency, pcw.obs_ms AS pcw_obs,
  pc.id AS pc_id, pc.price AS pc_price, pc.currency AS pc_currency, pc.obs_ms AS pc_obs
 FROM leaves l
 LEFT JOIN LATERAL (
  SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms
  FROM price_entry p
  WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_input > 0
    AND p.subject = l.model AND p.unit = 'input_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at
  ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1
 ) pi ON true
 LEFT JOIN LATERAL (
  SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms
  FROM price_entry p
  WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_output > 0
    AND p.subject = l.model AND p.unit = 'output_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at
  ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1
 ) po ON true
 LEFT JOIN LATERAL (
  SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms
  FROM price_entry p
  WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_cache_read > 0
    AND p.subject = l.model AND p.unit = 'cache_read_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at
  ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1
 ) pcr ON true
 LEFT JOIN LATERAL (
  SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms
  FROM price_entry p
  WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_cache_write > 0
    AND p.subject = l.model AND p.unit = 'cache_write_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at
  ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1
 ) pcw ON true
 LEFT JOIN LATERAL (
  SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms
  FROM price_entry p
  WHERE (l.kind = 'tool_run' OR l.connection_cost IS NOT NULL) AND l.direct_currency IS NULL
    AND l.conn_unit IS NOT NULL AND l.conn_amount IS NOT NULL AND l.leaf_at IS NOT NULL
    AND p.subject = l.conn_subject AND p.unit = l.conn_unit AND p.effective_from <= l.leaf_at
  ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1
 ) pc ON true
),
leaf_cost_items AS (
 SELECT lp.*, ci.currency, ci.cost_basis, ci.amount, ci.price_id, ci.obs_ms, ci.unpriced
 FROM leaf_priced lp
 CROSS JOIN LATERAL (
  SELECT
   CASE
    WHEN lp.direct_currency IS NOT NULL THEN lp.direct_currency
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.pc_id IS NOT NULL THEN lp.pc_currency
   END AS currency,
   CASE
    WHEN lp.direct_currency IS NOT NULL THEN CASE WHEN lp.is_sub THEN 'subscription' ELSE 'measured' END
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.pc_id IS NOT NULL THEN CASE WHEN lp.is_sub THEN 'subscription' ELSE 'estimated' END
   END AS cost_basis,
   CASE
    WHEN lp.direct_currency IS NOT NULL THEN lp.direct_amount
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.pc_id IS NOT NULL THEN lp.pc_price * lp.conn_amount
   END AS amount,
   CASE
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.direct_currency IS NULL THEN lp.pc_id
   END AS price_id,
   CASE
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.direct_currency IS NULL THEN lp.pc_obs
   END AS obs_ms,
   CASE
    WHEN (lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL) AND lp.direct_currency IS NULL AND lp.pc_id IS NULL THEN 1::bigint
    ELSE 0::bigint
   END AS unpriced
  WHERE lp.kind = 'tool_run' OR lp.connection_cost IS NOT NULL

  UNION ALL

  SELECT lp.pi_currency, CASE WHEN lp.is_sub THEN 'subscription' ELSE 'estimated' END, lp.pi_price * lp.tokens_input::numeric, lp.pi_id, lp.pi_obs, CASE WHEN lp.pi_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE lp.kind <> 'tool_run' AND lp.connection_cost IS NULL AND lp.tokens_input > 0

  UNION ALL

  SELECT lp.po_currency, CASE WHEN lp.is_sub THEN 'subscription' ELSE 'estimated' END, lp.po_price * lp.tokens_output::numeric, lp.po_id, lp.po_obs, CASE WHEN lp.po_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE lp.kind <> 'tool_run' AND lp.connection_cost IS NULL AND lp.tokens_output > 0

  UNION ALL

  SELECT lp.pcr_currency, CASE WHEN lp.is_sub THEN 'subscription' ELSE 'estimated' END, lp.pcr_price * lp.tokens_cache_read::numeric, lp.pcr_id, lp.pcr_obs, CASE WHEN lp.pcr_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE lp.kind <> 'tool_run' AND lp.connection_cost IS NULL AND lp.tokens_cache_read > 0

  UNION ALL

  SELECT lp.pcw_currency, CASE WHEN lp.is_sub THEN 'subscription' ELSE 'estimated' END, lp.pcw_price * lp.tokens_cache_write::numeric, lp.pcw_id, lp.pcw_obs, CASE WHEN lp.pcw_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE lp.kind <> 'tool_run' AND lp.connection_cost IS NULL AND lp.tokens_cache_write > 0

  UNION ALL

  SELECT NULL::text, NULL::text, NULL::numeric, NULL::text, NULL::bigint, 1::bigint
  WHERE lp.kind IN ('agent_run', 'child_agent') AND lp.connection_cost IS NULL
    AND coalesce(lp.tokens_input, lp.tokens_output, lp.tokens_cache_read, lp.tokens_cache_write) IS NULL
 ) ci
)`

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

func appendStringFilter(conds *[]string, params *[]any, expr string, vals []string) {
	if len(vals) == 0 {
		return
	}
	*params = append(*params, vals)
	*conds = append(*conds, fmt.Sprintf("coalesce(%s, 'unknown') = ANY($%d)", expr, len(*params)))
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
	Count               int64    `json:"count"`
	Failed              int64    `json:"failed"`
	TokensIn            *float64 `json:"tokensIn"`
	TokensInHave        int64    `json:"tokensInHave"`
	TokensInSub         int64    `json:"tokensInSub"`
	TokensInEst         int64    `json:"tokensInEst"`
	TokensInExpected    int64    `json:"tokensInExpected"`
	TokensOut           *float64 `json:"tokensOut"`
	TokensOutHave       int64    `json:"tokensOutHave"`
	TokensOutSub        int64    `json:"tokensOutSub"`
	TokensOutEst        int64    `json:"tokensOutEst"`
	TokensOutExpected   int64    `json:"tokensOutExpected"`
	TokensCR            *float64 `json:"tokensCR"`
	TokensCRHave        int64    `json:"tokensCRHave"`
	TokensCRSub         int64    `json:"tokensCRSub"`
	TokensCREst         int64    `json:"tokensCREst"`
	TokensCRExpected    int64    `json:"tokensCRExpected"`
	TokensCW            *float64 `json:"tokensCW"`
	TokensCWHave        int64    `json:"tokensCWHave"`
	TokensCWSub         int64    `json:"tokensCWSub"`
	TokensCWEst         int64    `json:"tokensCWEst"`
	TokensCWExpected    int64    `json:"tokensCWExpected"`
	WallMs              *float64 `json:"wallMs"`
	WallMsP50           *float64 `json:"wallMsP50"`
	WallMsP95           *float64 `json:"wallMsP95"`
	WallMsHave          int64    `json:"wallMsHave"`
	WallMsSub           int64    `json:"wallMsSub"`
	WallMsEst           int64    `json:"wallMsEst"`
	WallMsExpected      int64    `json:"wallMsExpected"`
	ActiveMs            *float64 `json:"activeMs"`
	ActiveMsHave        int64    `json:"activeMsHave"`
	ActiveMsSub         int64    `json:"activeMsSub"`
	ActiveMsEst         int64    `json:"activeMsEst"`
	ActiveMsExpected    int64    `json:"activeMsExpected"`
	CpuMs               *float64 `json:"cpuMs"`
	CpuMsHave           int64    `json:"cpuMsHave"`
	CpuMsSub            int64    `json:"cpuMsSub"`
	CpuMsEst            int64    `json:"cpuMsEst"`
	CpuMsExpected       int64    `json:"cpuMsExpected"`
	PeakMem             *float64 `json:"peakMem"`
	PeakMemHave         int64    `json:"peakMemHave"`
	PeakMemSub          int64    `json:"peakMemSub"`
	PeakMemEst          int64    `json:"peakMemEst"`
	PeakMemExpected     int64    `json:"peakMemExpected"`
	Calls               *float64 `json:"calls"`
	CallsHave           int64    `json:"callsHave"`
	CallsExpected       int64    `json:"callsExpected"`
	Credits             *float64 `json:"credits"`
	CreditsHave         int64    `json:"creditsHave"`
	CreditsExpected     int64    `json:"creditsExpected"`
}

const sqlMeasureSelect = `jsonb_build_object(
 'count', count(*)::bigint,
 'failed', (count(*) FILTER (WHERE state = 'failed'))::bigint,
 'tokensIn', sum(tokens_input)::float8,
 'tokensInHave', (count(*) FILTER (WHERE tokens_input IS NOT NULL))::bigint,
 'tokensInSub', (count(*) FILTER (WHERE tokens_input IS NOT NULL AND tokens_input_basis = 'subscription'))::bigint,
 'tokensInEst', (count(*) FILTER (WHERE tokens_input IS NOT NULL AND tokens_input_basis = 'estimated'))::bigint,
 'tokensInExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR tokens_input IS NOT NULL))::bigint,
 'tokensOut', sum(tokens_output)::float8,
 'tokensOutHave', (count(*) FILTER (WHERE tokens_output IS NOT NULL))::bigint,
 'tokensOutSub', (count(*) FILTER (WHERE tokens_output IS NOT NULL AND tokens_output_basis = 'subscription'))::bigint,
 'tokensOutEst', (count(*) FILTER (WHERE tokens_output IS NOT NULL AND tokens_output_basis = 'estimated'))::bigint,
 'tokensOutExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR tokens_output IS NOT NULL))::bigint,
 'tokensCR', sum(tokens_cache_read)::float8,
 'tokensCRHave', (count(*) FILTER (WHERE tokens_cache_read IS NOT NULL))::bigint,
 'tokensCRSub', (count(*) FILTER (WHERE tokens_cache_read IS NOT NULL AND tokens_cache_read_basis = 'subscription'))::bigint,
 'tokensCREst', (count(*) FILTER (WHERE tokens_cache_read IS NOT NULL AND tokens_cache_read_basis = 'estimated'))::bigint,
 'tokensCRExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR tokens_cache_read IS NOT NULL))::bigint,
 'tokensCW', sum(tokens_cache_write)::float8,
 'tokensCWHave', (count(*) FILTER (WHERE tokens_cache_write IS NOT NULL))::bigint,
 'tokensCWSub', (count(*) FILTER (WHERE tokens_cache_write IS NOT NULL AND tokens_cache_write_basis = 'subscription'))::bigint,
 'tokensCWEst', (count(*) FILTER (WHERE tokens_cache_write IS NOT NULL AND tokens_cache_write_basis = 'estimated'))::bigint,
 'tokensCWExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR tokens_cache_write IS NOT NULL))::bigint,
 'wallMs', sum(wall_ms)::float8,
 'wallMsP50', (percentile_cont(0.5) WITHIN GROUP (ORDER BY wall_ms))::float8,
 'wallMsP95', (percentile_cont(0.95) WITHIN GROUP (ORDER BY wall_ms))::float8,
 'wallMsHave', (count(*) FILTER (WHERE wall_ms IS NOT NULL))::bigint,
 'wallMsSub', (count(*) FILTER (WHERE wall_ms IS NOT NULL AND wall_ms_basis = 'subscription'))::bigint,
 'wallMsEst', (count(*) FILTER (WHERE wall_ms IS NOT NULL AND wall_ms_basis = 'estimated'))::bigint,
 'wallMsExpected', count(*)::bigint,
 'activeMs', sum(active_ms)::float8,
 'activeMsHave', (count(*) FILTER (WHERE active_ms IS NOT NULL))::bigint,
 'activeMsSub', (count(*) FILTER (WHERE active_ms IS NOT NULL AND active_ms_basis = 'subscription'))::bigint,
 'activeMsEst', (count(*) FILTER (WHERE active_ms IS NOT NULL AND active_ms_basis = 'estimated'))::bigint,
 'activeMsExpected', count(*)::bigint,
 'cpuMs', sum(cpu_ms)::float8,
 'cpuMsHave', (count(*) FILTER (WHERE cpu_ms IS NOT NULL))::bigint,
 'cpuMsSub', (count(*) FILTER (WHERE cpu_ms IS NOT NULL AND cpu_ms_basis = 'subscription'))::bigint,
 'cpuMsEst', (count(*) FILTER (WHERE cpu_ms IS NOT NULL AND cpu_ms_basis = 'estimated'))::bigint,
 'cpuMsExpected', (count(*) FILTER (WHERE kind <> 'tool_run' OR cpu_ms IS NOT NULL))::bigint,
 'peakMem', max(peak_memory_bytes)::float8,
 'peakMemHave', (count(*) FILTER (WHERE peak_memory_bytes IS NOT NULL))::bigint,
 'peakMemSub', (count(*) FILTER (WHERE peak_memory_bytes IS NOT NULL AND peak_memory_bytes_basis = 'subscription'))::bigint,
 'peakMemEst', (count(*) FILTER (WHERE peak_memory_bytes IS NOT NULL AND peak_memory_bytes_basis = 'estimated'))::bigint,
 'peakMemExpected', (count(*) FILTER (WHERE kind <> 'tool_run' OR peak_memory_bytes IS NOT NULL))::bigint,
 'calls', sum(CASE WHEN kind = 'tool_run' OR tool_calls IS NOT NULL THEN leaf_calls END)::float8,
 'callsHave', (count(*) FILTER (WHERE kind = 'tool_run' OR tool_calls IS NOT NULL))::bigint,
 'callsExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent', 'tool_run')))::bigint,
 'credits', sum(credits)::float8,
 'creditsHave', (count(*) FILTER (WHERE credits IS NOT NULL))::bigint,
 'creditsExpected', (count(*) FILTER (WHERE credits IS NOT NULL OR conn_unit = 'credits'))::bigint
)`

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
			out[string(m)] = contract.MeasuredNumber{Basis: contract.ResourceBasisUnavailable, Reason: ptr("tokens_reasoning not recorded")}
		case contract.AnalyticsMeasureWallMs:
			out[string(m)] = pickMeasuredNumber(s.WallMs, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureWallMsP50:
			out[string(m)] = pickMeasuredNumber(s.WallMsP50, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureWallMsP95:
			out[string(m)] = pickMeasuredNumber(s.WallMsP95, s.WallMsExpected, s.WallMsHave, s.WallMsSub, s.WallMsEst)
		case contract.AnalyticsMeasureActiveMs:
			out[string(m)] = pickMeasuredNumber(s.ActiveMs, s.ActiveMsExpected, s.ActiveMsHave, s.ActiveMsSub, s.ActiveMsEst)
		case contract.AnalyticsMeasureApiMs:
			out[string(m)] = contract.MeasuredNumber{Basis: contract.ResourceBasisUnavailable, Reason: ptr("api_ms not recorded")}
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

	params := []any{from, to}
	var conds []string
	if r.ProjectId != nil {
		if *r.ProjectId == "" {
			return rep, fail(400, "Invalid projectId")
		}
		params = append(params, *r.ProjectId)
		conds = append(conds, fmt.Sprintf("l.project_id = $%d", len(params)))
	}
	if f := r.Filters; f != nil {
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionProject], f.Project)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionProject], f.ProjectId)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionKind], f.Kind)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionParent], f.Parent)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionPipeline], f.Pipeline)
		if revs, err := rawFilterStrings(f.PipelineRevision); err != nil {
			return rep, err
		} else {
			appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionPipelineRevision], revs)
		}
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionStep], f.Step)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionProfileId], f.ProfileId)
		if revs, err := rawFilterStrings(f.ProfileRevision); err != nil {
			return rep, err
		} else {
			appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionProfileRevision], revs)
		}
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionHarness], f.Harness)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionModel], f.Model)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionProvider], f.Provider)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionAccount], f.Account)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionHost], f.Host)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionConnection], f.Connection)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionExperiment], f.Experiment)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionVariant], f.Variant)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionState], f.State)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionOutcome], f.Outcome)
		appendStringFilter(&conds, &params, analyticsDimExpr[contract.AnalyticsDimensionPerson], f.Person)
	}
	filterSQL := ""
	if len(conds) > 0 {
		filterSQL = " WHERE " + strings.Join(conds, " AND ")
	}

	// Build raw leaves CTE inside caller's RLS transaction (never escalating fleet.project_ids).
	// Unfiltered window leaf CTE is evaluated once so we can also compute the unfiltered visible count
	// for reply-level `hidden` (CA-05) without re-scanning.
	dimCols := make([]string, len(dims))
	dimNullChecks := make([]string, len(dims))
	dimGroupCols := make([]string, len(dims))
	for i, d := range dims {
		expr := analyticsDimExpr[d]
		col := fmt.Sprintf("dim_%d", i)
		dimCols[i] = fmt.Sprintf("coalesce(%s, 'unknown') AS %s", expr, col)
		dimNullChecks[i] = fmt.Sprintf("(%s IS NULL)", expr)
		dimGroupCols[i] = col
	}
	unknownExpr := "false"
	if len(dimNullChecks) > 0 {
		unknownExpr = strings.Join(dimNullChecks, " OR ")
	}

	leavesBaseSQL := fmt.Sprintf(`WITH window_leaves AS MATERIALIZED (%s WHERE %s AND e.started_at >= $1 AND e.started_at < $2),
leaves AS MATERIALIZED (
 SELECT l.*, %s%s
 FROM window_leaves l%s
)`, analyticsBaseLeavesSelect, analyticsLeafFilter,
		func() string {
			if len(dimCols) > 0 {
				return strings.Join(dimCols, ", ") + ", "
			}
			return ""
		}(),
		fmt.Sprintf("(%s) AS has_unknown_dim", unknownExpr),
		filterSQL)

	var querySQL string
	if wantCost {
		querySQL = leavesBaseSQL + ",\n" + analyticsLeafCostCTEs + ",\n"
	} else {
		querySQL = leavesBaseSQL + "\n"
	}

	dimListSQL := strings.Join(dimGroupCols, ", ")
	if len(dims) == 0 {
		if wantCost {
			querySQL += fmt.Sprintf(`cost_agg AS (
 SELECT currency, cost_basis, sum(amount)::float8 AS value, sum(unpriced)::bigint AS unpriced,
  coalesce(jsonb_agg(DISTINCT jsonb_build_object('id', price_id, 'observedAt', obs_ms)) FILTER (WHERE price_id IS NOT NULL), '[]'::jsonb) AS prices
 FROM leaf_cost_items
 GROUP BY currency, cost_basis
),
cost_json AS (
 SELECT coalesce(jsonb_agg(jsonb_build_object('currency', currency, 'basis', cost_basis, 'value', value, 'prices', prices, 'unpriced', unpriced)), '[]'::jsonb) AS costs
 FROM cost_agg
)
SELECT true AS is_total, '[]'::jsonb AS dims, %s AS stats, (SELECT costs FROM cost_json) AS costs,
 (SELECT count(*)::bigint FROM window_leaves) AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves`, sqlMeasureSelect)
		} else {
			querySQL += fmt.Sprintf(`SELECT true AS is_total, '[]'::jsonb AS dims, %s AS stats, '[]'::jsonb AS costs,
 (SELECT count(*)::bigint FROM window_leaves) AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves`, sqlMeasureSelect)
		}
	} else {
		dimJSONBuild := "jsonb_build_array(" + dimListSQL + ")"
		if wantCost {
			querySQL += fmt.Sprintf(`metrics_agg AS (
 SELECT (GROUPING(dim_0) = 1) AS is_total,
  CASE WHEN GROUPING(dim_0) = 1 THEN '[]'::jsonb ELSE %s END AS dims,
  %s,
  %s AS stats,
  (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
 FROM leaves
 GROUP BY GROUPING SETS ((%s), ())
),
cost_grouped AS (
 SELECT (GROUPING(dim_0) = 1) AS is_total,
  CASE WHEN GROUPING(dim_0) = 1 THEN '[]'::jsonb ELSE %s END AS dims,
  currency, cost_basis, sum(amount)::float8 AS value, sum(unpriced)::bigint AS unpriced,
  coalesce(jsonb_agg(DISTINCT jsonb_build_object('id', price_id, 'observedAt', obs_ms)) FILTER (WHERE price_id IS NOT NULL), '[]'::jsonb) AS prices
 FROM leaf_cost_items
 GROUP BY GROUPING SETS ((%s, currency, cost_basis), (currency, cost_basis))
),
cost_json AS (
 SELECT is_total, dims,
  coalesce(jsonb_agg(jsonb_build_object('currency', currency, 'basis', cost_basis, 'value', value, 'prices', prices, 'unpriced', unpriced)), '[]'::jsonb) AS costs
 FROM cost_grouped
 GROUP BY is_total, dims
)
SELECT m.is_total, m.dims, m.stats, coalesce(c.costs, '[]'::jsonb) AS costs,
 (SELECT count(*)::bigint FROM window_leaves) AS window_visible,
 m.unknown_cnt
FROM metrics_agg m
LEFT JOIN cost_json c ON c.is_total = m.is_total AND c.dims = m.dims
ORDER BY m.is_total ASC, %s`, dimJSONBuild, dimListSQL, sqlMeasureSelect, dimListSQL,
				dimJSONBuild, dimListSQL,
				func() string {
					parts := make([]string, len(dims))
					for i := range dims {
						parts[i] = fmt.Sprintf("m.dim_%d ASC NULLS LAST", i)
					}
					return strings.Join(parts, ", ")
				}())
		} else {
			querySQL += fmt.Sprintf(`SELECT (GROUPING(dim_0) = 1) AS is_total,
 CASE WHEN GROUPING(dim_0) = 1 THEN '[]'::jsonb ELSE %s END AS dims,
 %s AS stats,
 '[]'::jsonb AS costs,
 (SELECT count(*)::bigint FROM window_leaves) AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves
GROUP BY GROUPING SETS ((%s), ())
ORDER BY is_total ASC, %s`, dimJSONBuild, sqlMeasureSelect, dimListSQL,
				func() string {
					parts := make([]string, len(dims))
					for i := range dims {
						parts[i] = fmt.Sprintf("dim_%d ASC NULLS LAST", i)
					}
					return strings.Join(parts, ", ")
				}())
		}
	}

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
		unscopedTotal, err := unscoped(q, func() (int64, error) {
			var n int64
			qErr := q.tx.QueryRow(q.ctx(),
				`SELECT count(*)::bigint FROM execution e LEFT JOIN execution_totals t ON t.execution_id = e.id WHERE `+analyticsLeafFilter+` AND e.started_at >= $1 AND e.started_at < $2`,
				from, to).Scan(&n)
			return n, qErr
		})
		if err != nil {
			return rep, err
		}
		if unscopedTotal > windowVisible {
			rep.Hidden = unscopedTotal - windowVisible
		}
	}

	return rep, nil
}

// ---- Shared ResourceSummary & ScopedResourceRollup SQL / Go Builders ----

type resourceLeafRow struct {
	Scope                  string
	ScopeKey               string
	ID                     string
	Kind                   string
	State                  string
	StartedMs              *int64
	EndedMs                *int64
	HasTotals              bool
	TokensInput            *int64
	TokensInputBasis       contract.ResourceBasis
	TokensOutput           *int64
	TokensOutputBasis      contract.ResourceBasis
	TokensCacheRead        *int64
	TokensCacheReadBasis   contract.ResourceBasis
	TokensCacheWrite       *int64
	TokensCacheWriteBasis  contract.ResourceBasis
	WallMs                 *int64
	WallMsBasis            contract.ResourceBasis
	ActiveMs               *int64
	ActiveMsBasis          contract.ResourceBasis
	CpuMs                  *int64
	CpuMsBasis             contract.ResourceBasis
	PeakMemoryBytes        *int64
	PeakMemoryBytesBasis   contract.ResourceBasis
	TurnCount              int64
	ToolCallsRaw           []byte
	ToolCallsBasis         contract.ResourceBasis
	ToolConnection         *string
	ToolName               *string
	Credits                *float64
	HasConnectionCost      bool
	Costs                  []contract.CostFigure
}

const resourceLeafRowSelect = `
 SELECT
  s.scope,
  s.scope_key,
  l.id,
  l.kind,
  l.state,
  CASE WHEN l.started_at IS NOT NULL THEN (extract(epoch FROM l.started_at) * 1000)::bigint END AS started_ms,
  CASE WHEN l.ended_at IS NOT NULL THEN (extract(epoch FROM l.ended_at) * 1000)::bigint END AS ended_ms,
  l.has_totals,
  l.tokens_input, l.tokens_input_basis,
  l.tokens_output, l.tokens_output_basis,
  l.tokens_cache_read, l.tokens_cache_read_basis,
  l.tokens_cache_write, l.tokens_cache_write_basis,
  l.wall_ms, l.wall_ms_basis,
  l.active_ms, l.active_ms_basis,
  l.cpu_ms, l.cpu_ms_basis,
  l.peak_memory_bytes, l.peak_memory_bytes_basis,
  l.turn_count,
  l.tool_calls, l.tool_calls_basis,
  l.connection, l.tool_name,
  l.credits,
  (l.connection_cost IS NOT NULL) AS has_conn_cost,
  coalesce(lc.costs, '[]'::jsonb) AS leaf_costs
 FROM scoped_leaves s
 JOIN leaves l ON l.id = s.leaf_id
 LEFT JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object('currency', currency, 'basis', cost_basis, 'value', value, 'prices', prices, 'unpriced', unpriced)) AS costs
  FROM (
   SELECT ci.currency, ci.cost_basis, sum(ci.amount)::float8 AS value, sum(ci.unpriced)::bigint AS unpriced,
    coalesce(jsonb_agg(DISTINCT jsonb_build_object('id', ci.price_id, 'observedAt', ci.obs_ms)) FILTER (WHERE ci.price_id IS NOT NULL), '[]'::jsonb) AS prices
   FROM leaf_cost_items ci
   WHERE ci.id = l.id
   GROUP BY ci.currency, ci.cost_basis
  ) cg
 ) lc ON true`

// queryResourceSummariesSQL executes a query returning (scope, scope_key, ResourceSummary) rows cleanly
// without double-counting cost items across scopes, assuming the caller CTE defines `scoped_leaves(scope, scope_key, leaf_id)`.
func summarizeResourceQuery(q *Request, ctesSQL string, args ...any) (map[string]contract.ResourceSummary, error) {
	fullSQL := fmt.Sprintf(`%s,
leaves AS MATERIALIZED (%s WHERE %s AND e.id IN (SELECT leaf_id FROM scoped_leaves)),
%s
%s`, ctesSQL, analyticsBaseLeavesSelect, analyticsLeafFilter, analyticsLeafCostCTEs, resourceLeafRowSelect)

	rows, err := q.tx.Query(q.ctx(), fullSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	grouped := map[string][]resourceLeafRow{}
	for rows.Next() {
		var r resourceLeafRow
		var tiB, toB, tcrB, tcwB, wB, aB, cB, pmB, tcB string
		var costsRaw []byte
		if err := rows.Scan(
			&r.Scope, &r.ScopeKey, &r.ID, &r.Kind, &r.State,
			&r.StartedMs, &r.EndedMs, &r.HasTotals,
			&r.TokensInput, &tiB, &r.TokensOutput, &toB, &r.TokensCacheRead, &tcrB, &r.TokensCacheWrite, &tcwB,
			&r.WallMs, &wB, &r.ActiveMs, &aB, &r.CpuMs, &cB, &r.PeakMemoryBytes, &pmB,
			&r.TurnCount, &r.ToolCallsRaw, &tcB, &r.ToolConnection, &r.ToolName,
			&r.Credits, &r.HasConnectionCost, &costsRaw,
		); err != nil {
			return nil, err
		}
		r.TokensInputBasis = contract.ResourceBasis(tiB)
		r.TokensOutputBasis = contract.ResourceBasis(toB)
		r.TokensCacheReadBasis = contract.ResourceBasis(tcrB)
		r.TokensCacheWriteBasis = contract.ResourceBasis(tcwB)
		r.WallMsBasis = contract.ResourceBasis(wB)
		r.ActiveMsBasis = contract.ResourceBasis(aB)
		r.CpuMsBasis = contract.ResourceBasis(cB)
		r.PeakMemoryBytesBasis = contract.ResourceBasis(pmB)
		r.ToolCallsBasis = contract.ResourceBasis(tcB)
		r.Costs, err = decodeCostFigures(costsRaw)
		if err != nil {
			return nil, err
		}
		k := r.Scope + "\x00" + r.ScopeKey
		grouped[k] = append(grouped[k], r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	var inSum, outSum, crSum, cwSum int64
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
				toolMeasured++
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
		if inHave < tokExpected && outHave < tokExpected {
			out.Tokens.Basis = contract.ResourceBasisUnavailable
		} else {
			out.Tokens.Basis = weakestBasis(tokBases...)
		}
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
	if toolExpected > 0 && toolMeasured == toolExpected {
		out.Tools.Basis = contract.ResourceBasisMeasured
	}

	out.Cost = mergeCostFigures(leaves)
	return out
}

const taskLineageCTEs = `WITH RECURSIVE
 first_task AS (
  SELECT DISTINCT ON (session_id) session_id, task_id
  FROM execution
  WHERE task_id IS NOT NULL AND session_id IS NOT NULL
  ORDER BY session_id, seq
 ),
 direct_sessions AS (
  SELECT id AS session_id FROM document
  WHERE kind = 'session'
    AND (data->>'taskId' = $1 OR (coalesce(nullif(data->>'taskId', ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = $1)))
    AND NOT (data ? 'parentExecutionId')
 ),
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (
      e.task_id = $1
      OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
    )
 ),
 del_seed AS (
  SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
     OR (e.delegation IS NOT NULL AND (e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs) OR e.delegation->>'parentSessionId' IN (SELECT session_id FROM direct_sessions)))
  UNION
  SELECT e.id, e.session_id, true AS follow_session
  FROM execution e
  JOIN document s ON s.kind = 'session' AND s.id = e.session_id
  WHERE s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
     OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
     OR (s.data->>'createdBySessionId' IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
 ),
 del_tree AS (
  SELECT id, session_id, follow_session FROM del_seed
  UNION
  SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  JOIN del_tree dt ON e.parent_execution_id = dt.id
     OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id NOT IN (SELECT session_id FROM direct_sessions) AND e.task_id IS NULL)
     OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
 ),
 spawned_tasks AS (
  SELECT t.id, t.code, t.title, t.state::text AS state, t.project_id, t.seq
  FROM task t
  WHERE t.id <> $1
    AND (
      t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
      OR t.created_by_session_id IN (
       SELECT session_id FROM direct_sessions
       UNION SELECT session_id FROM own_execs WHERE session_id IS NOT NULL
       UNION SELECT session_id FROM del_tree WHERE session_id IS NOT NULL
      )
    )
 ),
 spawned_execs AS (
  SELECT st.id AS task_id, e.id AS exec_id
  FROM spawned_tasks st
  JOIN execution e ON (
   e.task_id = st.id
   OR (e.task_id IS NULL AND e.session_id IN (
     SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
   ))
  )
 )`

func taskResourceRollup(q *Request, taskID string) (contract.ScopedResourceRollup, error) {
	out := contract.ScopedResourceRollup{
		Own:          emptyResourceSummary(),
		Delegated:    emptyResourceSummary(),
		Spawned:      emptyResourceSummary(),
		Total:        emptyResourceSummary(),
		SpawnedTasks: []contract.SpawnedTaskRef{},
	}

	ctes := taskLineageCTEs + `,
 classified AS (
  SELECT 'spawned_task'::text AS scope, task_id AS scope_key, exec_id AS leaf_id FROM spawned_execs
  UNION
  SELECT 'delegated'::text AS scope, ''::text AS scope_key, id AS leaf_id
  FROM del_tree
  WHERE id NOT IN (SELECT exec_id FROM spawned_execs)
  UNION
  SELECT 'own'::text AS scope, ''::text AS scope_key, id AS leaf_id
  FROM own_execs
  WHERE id NOT IN (SELECT exec_id FROM spawned_execs) AND id NOT IN (SELECT id FROM del_tree)
 ),
 scoped_leaves AS (
  SELECT scope, scope_key, leaf_id FROM classified
  UNION
  SELECT 'spawned'::text AS scope, ''::text AS scope_key, exec_id AS leaf_id FROM spawned_execs
  UNION
  SELECT 'total'::text AS scope, ''::text AS scope_key, leaf_id FROM classified
 )`

	summaries, err := summarizeResourceQuery(q, ctes, taskID)
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

	// Load visible spawned task metadata in order.
	taskRows, err := q.tx.Query(q.ctx(), taskLineageCTEs+` SELECT id, code, title, state, project_id FROM spawned_tasks ORDER BY seq ASC, id ASC`, taskID)
	if err != nil {
		return out, err
	}
	var visibleSpawned, visibleDelegated int64
	for taskRows.Next() {
		var st contract.SpawnedTaskRef
		if err := taskRows.Scan(&st.TaskId, &st.Code, &st.Title, &st.State, &st.ProjectId); err != nil {
			taskRows.Close()
			return out, err
		}
		visibleSpawned++
		if s, ok := summaries["spawned_task\x00"+st.TaskId]; ok {
			st.Resources = s
		} else {
			st.Resources = emptyResourceSummary()
		}
		out.SpawnedTasks = append(out.SpawnedTasks, st)
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return out, err
	}
	taskRows.Close()

	// Hidden direct lineage count under RLS (CA-05): compare visible vs unscoped downstream items.
	if q.person != nil && !fullAccess(q.person) {
		if err := q.tx.QueryRow(q.ctx(), taskLineageCTEs+` SELECT count(DISTINCT id)::bigint FROM del_tree`, taskID).Scan(&visibleDelegated); err != nil {
			return out, err
		}
		totalUnscoped, err := unscoped(q, func() (int64, error) {
			var uSpawned, uDelegated int64
			qErr := q.tx.QueryRow(q.ctx(), taskLineageCTEs+`
 SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks),
        (SELECT count(DISTINCT id)::bigint FROM del_tree)`, taskID).Scan(&uSpawned, &uDelegated)
			return uSpawned + uDelegated, qErr
		})
		if err != nil {
			return out, err
		}
		if totalUnscoped > visibleSpawned+visibleDelegated {
			out.Hidden = totalUnscoped - (visibleSpawned + visibleDelegated)
		}
	}

	return out, nil
}

const sessionLineageCTEs = `WITH RECURSIVE
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (e.session_id = $1 OR (e.kind = 'tool_run' AND e.parent_execution_id IN (SELECT id FROM execution WHERE session_id = $1 AND kind <> 'child_agent' AND delegation IS NULL)))
 ),
 del_seed AS (
  SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
     OR (e.delegation IS NOT NULL AND (e.delegation->>'parentSessionId' = $1 OR e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs)))
  UNION
  SELECT e.id, e.session_id, true AS follow_session
  FROM execution e
  JOIN document s ON s.kind = 'session' AND s.id = e.session_id
  WHERE e.session_id <> $1
    AND (
      s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
      OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
      OR (s.data->>'createdBySessionId' = $1 AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
    )
 ),
 del_tree AS (
  SELECT id, session_id, follow_session FROM del_seed
  UNION
  SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  JOIN del_tree dt ON e.parent_execution_id = dt.id
     OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id <> $1 AND e.task_id IS NULL)
     OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
 ),
 primary_task AS (
  SELECT nullif(data->>'taskId', '') AS task_id FROM document WHERE kind = 'session' AND id = $1
 ),
 spawned_tasks AS (
  SELECT t.id, t.code, t.title, t.state::text AS state, t.project_id, t.seq
  FROM task t
  WHERE t.id IS DISTINCT FROM (SELECT task_id FROM primary_task)
    AND (
      t.created_by_session_id = $1
      OR t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
      OR t.created_by_session_id IN (SELECT session_id FROM del_tree WHERE session_id IS NOT NULL)
    )
 ),
 spawned_execs AS (
  SELECT st.id AS task_id, e.id AS exec_id
  FROM spawned_tasks st
  JOIN execution e ON (
   e.task_id = st.id
   OR (e.task_id IS NULL AND e.session_id IN (
     SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
   ))
  )
 )`

func sessionResourceRollup(q *Request, sessionID string) (contract.ScopedResourceRollup, error) {
	out := contract.ScopedResourceRollup{
		Own:          emptyResourceSummary(),
		Delegated:    emptyResourceSummary(),
		Spawned:      emptyResourceSummary(),
		Total:        emptyResourceSummary(),
		SpawnedTasks: []contract.SpawnedTaskRef{},
	}

	ctes := sessionLineageCTEs + `,
 classified AS (
  SELECT 'spawned_task'::text AS scope, task_id AS scope_key, exec_id AS leaf_id FROM spawned_execs
  UNION
  SELECT 'delegated'::text AS scope, ''::text AS scope_key, id AS leaf_id
  FROM del_tree
  WHERE id NOT IN (SELECT exec_id FROM spawned_execs)
  UNION
  SELECT 'own'::text AS scope, ''::text AS scope_key, id AS leaf_id
  FROM own_execs
  WHERE id NOT IN (SELECT exec_id FROM spawned_execs) AND id NOT IN (SELECT id FROM del_tree)
 ),
 scoped_leaves AS (
  SELECT scope, scope_key, leaf_id FROM classified
  UNION
  SELECT 'spawned'::text AS scope, ''::text AS scope_key, exec_id AS leaf_id FROM spawned_execs
  UNION
  SELECT 'total'::text AS scope, ''::text AS scope_key, leaf_id FROM classified
 )`

	summaries, err := summarizeResourceQuery(q, ctes, sessionID)
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

	taskRows, err := q.tx.Query(q.ctx(), sessionLineageCTEs+` SELECT id, code, title, state, project_id FROM spawned_tasks ORDER BY seq ASC, id ASC`, sessionID)
	if err != nil {
		return out, err
	}
	var visibleSpawned, visibleDelegated int64
	for taskRows.Next() {
		var st contract.SpawnedTaskRef
		if err := taskRows.Scan(&st.TaskId, &st.Code, &st.Title, &st.State, &st.ProjectId); err != nil {
			taskRows.Close()
			return out, err
		}
		visibleSpawned++
		if s, ok := summaries["spawned_task\x00"+st.TaskId]; ok {
			st.Resources = s
		} else {
			st.Resources = emptyResourceSummary()
		}
		out.SpawnedTasks = append(out.SpawnedTasks, st)
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return out, err
	}
	taskRows.Close()

	if q.person != nil && !fullAccess(q.person) {
		if err := q.tx.QueryRow(q.ctx(), sessionLineageCTEs+` SELECT count(DISTINCT id)::bigint FROM del_tree`, sessionID).Scan(&visibleDelegated); err != nil {
			return out, err
		}
		totalUnscoped, err := unscoped(q, func() (int64, error) {
			var uSpawned, uDelegated int64
			qErr := q.tx.QueryRow(q.ctx(), sessionLineageCTEs+`
 SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks),
        (SELECT count(DISTINCT id)::bigint FROM del_tree)`, sessionID).Scan(&uSpawned, &uDelegated)
			return uSpawned + uDelegated, qErr
		})
		if err != nil {
			return out, err
		}
		if totalUnscoped > visibleSpawned+visibleDelegated {
			out.Hidden = totalUnscoped - (visibleSpawned + visibleDelegated)
		}
	}

	return out, nil
}

// boundaryResourcesMap returns ResourceSummary per boundary execution id (including its direct tool_run children).
func boundaryResourcesMap(q *Request, ids []string) (map[string]contract.ResourceSummary, error) {
	if len(ids) == 0 {
		return map[string]contract.ResourceSummary{}, nil
	}
	ctes := `WITH scoped_leaves AS (
  SELECT 'boundary'::text AS scope, e.id AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.id = ANY($1)
  UNION
  SELECT 'boundary'::text AS scope, e.parent_execution_id AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.kind = 'tool_run' AND e.parent_execution_id = ANY($1)
 )`
	raw, err := summarizeResourceQuery(q, ctes, ids)
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
func definitionUsageResources(q *Request, kind, id string, days int64) (map[string]contract.ResourceSummary, map[string]map[int64]contract.ResourceSummary, error) {
	pin := map[string]string{"kind": kind}
	if id != "" {
		pin["id"] = id
	}
	ctes := `WITH RECURSIVE
 pinned AS (
  SELECT e.id AS root_id, d->>'id' AS def_id, (d->>'revision')::int AS rev
  FROM execution e
  CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
  WHERE e.definitions @> $1 AND d->>'kind' = $2 AND ($3 = '' OR d->>'id' = $3)
    AND coalesce((d->>'draft')::boolean, false) = false
    AND coalesce(e.started_at, e.updated_at) >= now() - make_interval(days => $4::int)
 ),
 tree AS (
  SELECT root_id AS exec_id, def_id, rev FROM pinned
  UNION
  SELECT c.id AS exec_id, t.def_id, t.rev
  FROM execution c
  LEFT JOIN document s ON s.kind = 'session' AND s.id = c.session_id
  JOIN tree t ON c.parent_execution_id = t.exec_id
     OR (c.delegation IS NOT NULL AND c.delegation->>'parentExecutionId' = t.exec_id)
     OR s.data->>'parentExecutionId' = t.exec_id
     OR (c.kind = 'tool_run' AND c.data->>'pipelineRunId' = t.exec_id)
 ),
 leaf_hits AS (
  SELECT DISTINCT tr.def_id, tr.rev, e.id AS leaf_id
  FROM tree tr
  JOIN execution e ON e.id = tr.exec_id
  LEFT JOIN execution_totals t ON t.execution_id = e.id
  WHERE ` + analyticsLeafFilter + `
 ),
 scoped_leaves AS (
  SELECT 'total'::text AS scope, def_id AS scope_key, leaf_id FROM leaf_hits
  UNION
  SELECT 'rev'::text AS scope, def_id || ':' || rev::text AS scope_key, leaf_id FROM leaf_hits
 )`
	raw, err := summarizeResourceQuery(q, ctes, jsonBytes([]map[string]string{pin}), kind, id, days)
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

// projectResources summarizes all visible leaf executions belonging to projectID.
func projectResources(q *Request, projectID string) (contract.ResourceSummary, error) {
	if projectID == "" {
		return emptyResourceSummary(), nil
	}
	ctes := `WITH scoped_leaves AS (
  SELECT 'project'::text AS scope, ''::text AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.project_id = $1
 )`
	raw, err := summarizeResourceQuery(q, ctes, projectID)
	if err != nil {
		return emptyResourceSummary(), err
	}
	if s, ok := raw["project\x00"]; ok {
		return s, nil
	}
	return emptyResourceSummary(), nil
}
