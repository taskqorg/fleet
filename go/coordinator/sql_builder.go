// The one place this package assembles SQL at run time (docs/designs/fleet-standard.md S2, DESIGN_RULES.md). Every
// fixed statement is a typed query (queries/*.sql); sqlc cannot express a statement whose shape follows the request:
// a keyset page of the resource item table a category names, in the requested direction from an exclusive or
// inclusive position, and an analytics_query grouped by the requested dimensions under the requested filters.
// Identifiers and expressions come only from the allowlists in this file, chosen by typed inputs (a contract enum or a
// typed constant); every request value is a bind parameter.
package main

import (
	"fmt"
	"strings"
	"time"

	"fleet/contract"
)

// sqlItemTable: one resource item table (resource_projection.go), its C-order key tuple, the key and item it returns,
// and the condition of a text query or of a price group.
type sqlItemTable struct {
	name      string
	tuple     [2]string
	key, item string
	search    string // with $2: the text query (tools)
	group     string // with $2, $3: the currency and basis of a price group (prices)
}

const sqlToolKey = `jsonb_build_object('kind','tool','name',name,'connection',nullif(connection,''))`
const sqlCostKey = `jsonb_build_object('kind','cost','currency',nullif(currency,''),'basis',basis)`
const sqlPriceKey = `jsonb_build_object('kind','price','priceId',price_id,'observedAt',observed_at)`

var sqlItemTables = map[contract.ResourceCategory]sqlItemTable{
	"tools": {name: "resource_tool", tuple: [2]string{`name COLLATE "C"`, `connection COLLATE "C"`},
		key: sqlToolKey, item: sqlToolKey + ` || jsonb_build_object('calls',calls,'errors',errors)`,
		search: `strpos(lower(name),lower($2))>0`},
	"costs": {name: "resource_cost", tuple: [2]string{`currency COLLATE "C"`, `basis COLLATE "C"`},
		key: sqlCostKey, item: sqlCostKey + ` || jsonb_build_object('value',value,'usages',usages,'note',CASE WHEN basis='subscription' THEN 'Subscription list-price equivalent' END,'reason',CASE WHEN basis='unavailable' THEN 'No recorded valuation' END,'priceCount',(SELECT count(*) FROM resource_price_ref p WHERE p.project_id=resource_cost.project_id AND p.currency=resource_cost.currency AND p.basis=resource_cost.basis))`},
	"prices": {name: "resource_price_ref", tuple: [2]string{`price_id COLLATE "C"`, `observed_at`},
		key: sqlPriceKey, item: sqlPriceKey + ` || jsonb_build_object('group',jsonb_build_object('currency',nullif(currency,''),'basis',basis))`,
		group: `currency=$2 AND basis=$3`},
}

// sqlComparison: a keyset position relative to a key tuple.
type sqlComparison string

const (
	sqlAfter      sqlComparison = ">"
	sqlAtOrAfter  sqlComparison = ">="
	sqlBefore     sqlComparison = "<"
	sqlAtOrBefore sqlComparison = "<="
)

// inclusive: the comparison that also admits the position itself.
func (c sqlComparison) inclusive() sqlComparison {
	if c == sqlBefore || c == sqlAtOrBefore {
		return sqlAtOrBefore
	}
	return sqlAtOrAfter
}

// sqlItems: the rows of one project's item table that a request selects.
type sqlItems struct {
	table sqlItemTable
	where string
	args  []any
}

// sqlResourceItems selects category's rows of project: those whose name contains query (tools, when query is not
// empty) or those of one price group (prices: group is required).
func sqlResourceItems(category contract.ResourceCategory, project, query string, group *contract.ResourceGroup) sqlItems {
	t := sqlItemTables[category]
	s := sqlItems{table: t, where: "project_id=$1", args: []any{project}}
	if t.search != "" && query != "" {
		s.where += " AND " + t.search
		s.args = append(s.args, query)
	}
	if t.group != "" {
		currency := ""
		if group.Currency != nil {
			currency = *group.Currency
		}
		s.where += " AND " + t.group
		s.args = append(s.args, currency, string(group.Basis))
	}
	return s
}

// position: the condition and arguments of the selected rows at cmp to the key tuple values (two bind parameters), or
// of every selected row for nil values.
func (s sqlItems) position(values []any, cmp sqlComparison) (string, []any) {
	args := append([]any{}, s.args...)
	where := s.where
	if values != nil {
		a := len(args) + 1
		args = append(args, values...)
		where += fmt.Sprintf(" AND (%s,%s) %s ($%d,$%d)", s.table.tuple[0], s.table.tuple[1], cmp, a, a+1)
	}
	return where, args
}

// page: at most limit (key, item) rows at cmp to values in key order, descending or ascending. The direction applies to
// both tuple elements (ORDER BY tuple DESC would not use the two-column C-order index).
func (s sqlItems) page(values []any, cmp sqlComparison, descending bool, limit int64) (string, []any) {
	where, args := s.position(values, cmp)
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	args = append(args, limit)
	return fmt.Sprintf("SELECT %s,%s FROM %s WHERE %s ORDER BY %s %s,%s %s LIMIT $%d", s.table.key, s.table.item, s.table.name, where,
		s.table.tuple[0], direction, s.table.tuple[1], direction, len(args)), args
}

// one: one selected row at cmp to values, if there is one.
func (s sqlItems) one(values []any, cmp sqlComparison) (string, []any) {
	where, args := s.position(values, cmp)
	return "SELECT 1 FROM " + s.table.name + " WHERE " + where + " LIMIT 1", args
}

// count: how many rows are selected.
func (s sqlItems) count() (string, []any) {
	return "SELECT count(*) FROM " + s.table.name + " WHERE " + s.where, s.args
}

// sqlAnalyticsFilter: one requested filter, the leaves whose dimension value ('unknown' for none) is one of values; no
// values filter nothing.
type sqlAnalyticsFilter struct {
	dim    contract.AnalyticsDimension
	values []string
}

// sqlAnalytics: the analytics_query statement and its arguments. It reads the caller's visible leaves started in
// [from, to) (inside the caller's RLS transaction, never escalating fleet.project_ids) under the system activity, the
// project and the filters, grouped by dims (each one of analyticsDimExpr) with a total row, with costs when cost.
// The unfiltered window leaf CTE is evaluated once so the reply-level hidden count (CA-05) needs no rescan.
func sqlAnalytics(from, to time.Time, system *contract.SystemActivity, project *string, filters []sqlAnalyticsFilter, dims []contract.AnalyticsDimension, cost bool) (string, []any) {
	params := []any{from, to}
	conds := []string{systemFilter(system, "l.system_actor")}
	if project != nil {
		params = append(params, *project)
		conds = append(conds, fmt.Sprintf("l.project_id = $%d", len(params)))
	}
	for _, f := range filters {
		appendStringFilter(&conds, &params, analyticsDimExpr[f.dim], f.values)
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
		dimCols[i] = fmt.Sprintf("coalesce(nullif(%s, ''), 'unknown') AS %s", expr, col)
		dimNullChecks[i] = fmt.Sprintf("(nullif(%s, '') IS NULL)", expr)
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
	if cost {
		querySQL = leavesBaseSQL + ",\n" + analyticsLeafCostCTEs + ",\n"
	} else {
		querySQL = leavesBaseSQL + "\n"
	}

	dimListSQL := strings.Join(dimGroupCols, ", ")
	if len(dims) == 0 {
		if cost {
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
		if cost {
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

	return querySQL, params
}

const (
	analyticsNumericRegex = `^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$`
	analyticsLeafFilter   = `e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL)`
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

// analyticsBaseLeavesSelect is the shared column projection over `execution e` + `execution_totals t` + `fleet_exec_conn_meta conn`.
// The rollups read the same projection (AnalyticsLeafRows, queries/analytics.sql) and a replacement an exact-numeric
// variant of it (ResourceClaimSnapshot, queries/resources.sql); a change here is made there too.
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
  coalesce(conn.provider, nullif(e.data->>'provider', ''), CASE e.harness WHEN 'codex' THEN 'openai' WHEN 'claude' THEN 'anthropic' WHEN 'antigravity' THEN 'google' END) AS provider,
  e.account_id,
  e.host_id,
  nullif(e.data->>'connection', '') AS connection,
  nullif(e.data->>'tool', '') AS tool_name,
  e.experiment_id,
  e.variant,
  e.system_actor,
  coalesce(nullif(e.data->>'outcome', ''), CASE WHEN e.state IN ('succeeded', 'failed', 'cancelled') THEN e.state::text END) AS outcome,
  coalesce(e.directed_by, t.directed_by) AS person,
  e.started_at,
  e.ended_at,
  coalesce(e.started_at, CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint / 1000.0) END, e.ended_at, e.last_seen_at) AS leaf_at,
  (t.execution_id IS NOT NULL) AS has_totals,
  (coalesce(t.billing_basis, '') = 'subscription' OR (coalesce(t.billing_basis, '') IN ('', 'unavailable') AND e.account_id IS NOT NULL AND NOT coalesce(conn.is_api_conn, false))) AS is_sub,
  t.tokens_input,
  coalesce(t.tokens_input_basis, 'unavailable') AS tokens_input_basis,
  t.tokens_reasoning,
  coalesce(t.tokens_reasoning_basis, 'unavailable') AS tokens_reasoning_basis,
  t.api_ms,
  coalesce(t.api_ms_basis, 'unavailable') AS api_ms_basis,
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
 LEFT JOIN LATERAL (
  SELECT m.provider, m.is_api_conn
  FROM fleet_exec_conn_meta(e.data->>'connection', e.account_id) m
  WHERE e.data ? 'connection' OR e.account_id IS NOT NULL
 ) conn ON true`

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

func appendStringFilter(conds *[]string, params *[]any, expr string, vals []string) {
	if len(vals) == 0 {
		return
	}
	*params = append(*params, vals)
	*conds = append(*conds, fmt.Sprintf("coalesce(nullif(%s, ''), 'unknown') = ANY($%d)", expr, len(*params)))
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
) || jsonb_build_object(
 'tokensReasoning', sum(tokens_reasoning)::float8,
 'tokensReasoningHave', (count(*) FILTER (WHERE tokens_reasoning IS NOT NULL))::bigint,
 'tokensReasoningSub', (count(*) FILTER (WHERE tokens_reasoning IS NOT NULL AND tokens_reasoning_basis = 'subscription'))::bigint,
 'tokensReasoningEst', (count(*) FILTER (WHERE tokens_reasoning IS NOT NULL AND tokens_reasoning_basis = 'estimated'))::bigint,
 'tokensReasoningExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR tokens_reasoning IS NOT NULL))::bigint,
 'apiMs', sum(api_ms)::float8,
 'apiMsHave', (count(*) FILTER (WHERE api_ms IS NOT NULL))::bigint,
 'apiMsSub', (count(*) FILTER (WHERE api_ms IS NOT NULL AND api_ms_basis = 'subscription'))::bigint,
 'apiMsEst', (count(*) FILTER (WHERE api_ms IS NOT NULL AND api_ms_basis = 'estimated'))::bigint,
 'apiMsExpected', (count(*) FILTER (WHERE kind IN ('agent_run', 'child_agent') OR api_ms IS NOT NULL))::bigint
)`

// taskLineageCTEs: a task's own executions, its delegated tree and its spawned tasks with their executions
// (AnalyticsTaskLineage, queries/analytics.sql, holds the same lineage). tasks.go composes the accepted-result read of
// a completed task from it; it leaves this file when that read is a typed query.
const taskLineageCTEs = `WITH RECURSIVE
 candidate_sessions AS (
  SELECT DISTINCT session_id FROM execution WHERE task_id = $1 AND session_id IS NOT NULL
 ),
 first_task AS (
  SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
  FROM execution e
  WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
  ORDER BY e.session_id, e.seq
 ),
 direct_sessions AS (
  SELECT id AS session_id FROM session
  WHERE (task_id = $1 OR (coalesce(nullif(task_id, ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = $1)))
    AND parent_execution_id IS NULL
 ),
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (
      e.task_id = $1
      OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
      OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
         SELECT p.id FROM execution p
         WHERE p.kind <> 'child_agent'
           AND p.delegation IS NULL
           AND (p.task_id = $1 OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
      ))
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
  JOIN session s ON s.id = e.session_id
  WHERE s.parent_execution_id IN (SELECT id FROM own_execs)
     OR (s.created_by_execution_id IN (SELECT id FROM own_execs) AND coalesce(nullif(s.task_id, ''), '') = '')
     OR (s.created_by_session_id IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.task_id, ''), '') = '')
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
 )` + spawnedExecsCTEs

// spawnedExecsCTEs: the executions of the spawned tasks (usually none), joined from those tasks: executions through
// indexed column equalities, their sessions by session.task_id (indexed; under row-level security a JSON path
// is never an index condition, migration 70), and only when a spawned task exists. One join of every execution with every task under an OR condition was planned as a nested loop over
// both tables once statistics were current: 40 s for one task of a 20000-task store, under the global write lock
// when task_update returns the task.
const spawnedExecsCTEs = `,
 spawned_task_ids AS MATERIALIZED (SELECT id FROM spawned_tasks),
 spawned_sessions AS MATERIALIZED (
  SELECT st.id AS task_id, d.id AS session_id
  FROM spawned_task_ids st
  JOIN session d ON d.task_id = st.id
 ),
 spawned_runs AS MATERIALIZED (
  SELECT st.id AS task_id, e.id
  FROM spawned_task_ids st
  JOIN execution e ON e.task_id = st.id
  UNION
  SELECT ss.task_id, e.id
  FROM spawned_sessions ss
  JOIN execution e ON e.task_id IS NULL AND e.session_id = ss.session_id
 ),
 spawned_execs AS (
  SELECT task_id, id AS exec_id FROM spawned_runs
  UNION
  SELECT sr.task_id, e.id AS exec_id
  FROM spawned_runs sr
  JOIN execution e ON e.kind = 'tool_run' AND e.parent_execution_id = sr.id
 )`
