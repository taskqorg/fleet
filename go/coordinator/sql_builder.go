// The one place this package assembles SQL at run time (docs/designs/fleet-standard.md S2, DESIGN_RULES.md). Every
// fixed statement is a typed query (queries/*.sql); sqlc cannot express a statement whose shape follows the request:
// a keyset page of the resource item table a category names, in the requested direction from an exclusive or
// inclusive position, an analytics_query grouped by the requested dimensions under the requested filters, and a list
// page (tasks, sessions), its counts and groups under the list's filter, group and sort.
// Identifiers and expressions come only from the allowlists in this file, chosen by typed inputs (a contract enum or a
// typed constant); every request value is a bind parameter.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
func sqlAnalytics(from, to time.Time, system *contract.SystemActivity, project *string, filters []sqlAnalyticsFilter, dims []contract.AnalyticsDimension, cost, wantCalls, wantP50, wantP95, needWindowVisible bool) (string, []any) {
	params := []any{from, to}
	baseConds := []string{analyticsLeafFilter, "e.started_at >= $1", "e.started_at < $2", systemFilter(system, "e.system_actor")}
	if project != nil {
		params = append(params, *project)
		baseConds = append(baseConds, fmt.Sprintf("e.project_id = $%d", len(params)))
	}
	conds := []string{}
	needCategory := false
	for _, f := range filters {
		if len(f.values) > 0 && f.dim == contract.AnalyticsDimensionCategory {
			needCategory = true
		}
		appendStringFilter(&conds, &params, analyticsDimExpr[f.dim], f.values)
	}
	filterSQL := ""
	if len(conds) > 0 {
		filterSQL = " WHERE " + strings.Join(conds, " AND ")
	}

	dimCols := make([]string, len(dims))
	dimNullChecks := make([]string, len(dims))
	dimGroupCols := make([]string, len(dims))
	for i, d := range dims {
		if d == contract.AnalyticsDimensionCategory {
			needCategory = true
		}
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

	ctes := []string{
		`conn_meta AS MATERIALIZED (SELECT m.id::text AS id, m.provider::text AS provider FROM fleet_conn_meta_all() m)`,
	}
	if needCategory {
		ctes = append(ctes, `agent_cats AS MATERIALIZED (
 SELECT da.id, nullif((dar.content::jsonb)->>'category', '') AS category
 FROM definition da
 JOIN definition_revision dar ON dar.kind = da.kind AND dar.id = da.id AND dar.revision = da.head
 WHERE da.kind = 'agent' AND dar.content IS NOT NULL AND left(dar.content, 1) = '{'
)`)
	}
	ctes = append(ctes,
		fmt.Sprintf(`base_leaves AS (%s WHERE %s)`, analyticsBaseLeavesSelect(needCategory, wantCalls), strings.Join(baseConds, " AND ")),
		fmt.Sprintf(`leaves AS MATERIALIZED (
 SELECT l.*, %s%s
 FROM base_leaves l%s
)`,
			func() string {
				if len(dimCols) > 0 {
					return strings.Join(dimCols, ", ") + ", "
				}
				return ""
			}(),
			fmt.Sprintf("(%s) AS has_unknown_dim", unknownExpr),
			filterSQL),
	)
	leavesBaseSQL := "WITH " + strings.Join(ctes, ",\n")
	windowVisibleExpr := "0::bigint"
	if needWindowVisible {
		windowVisibleExpr = "(SELECT count(*)::bigint FROM execution_spend WHERE started_at >= $1 AND started_at < $2)"
	}
	measureSQL := sqlMeasureSelect(wantP50, wantP95)

	var querySQL string
	if cost {
		querySQL = leavesBaseSQL + ",\n" + analyticsLeafCostCTEs + ",\n"
	} else {
		querySQL = leavesBaseSQL + ",\n"
	}
	if !cost {
		querySQL = strings.TrimSuffix(querySQL, ",\n") + "\n"
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
 %s AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves`, measureSQL, windowVisibleExpr)
		} else {
			querySQL += fmt.Sprintf(`SELECT true AS is_total, '[]'::jsonb AS dims, %s AS stats, '[]'::jsonb AS costs,
 %s AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves`, measureSQL, windowVisibleExpr)
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
 %s AS window_visible,
 m.unknown_cnt
FROM metrics_agg m
LEFT JOIN cost_json c ON c.is_total = m.is_total AND c.dims = m.dims
ORDER BY m.is_total ASC, %s`, dimJSONBuild, dimListSQL, measureSQL, dimListSQL,
				dimJSONBuild, dimListSQL, windowVisibleExpr,
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
 %s AS window_visible,
 (count(*) FILTER (WHERE has_unknown_dim))::bigint AS unknown_cnt
FROM leaves
GROUP BY GROUPING SETS ((%s), ())
ORDER BY is_total ASC, %s`, dimJSONBuild, measureSQL, windowVisibleExpr, dimListSQL,
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
	contract.AnalyticsDimensionPurpose:          `l.purpose`,
	contract.AnalyticsDimensionCategory:         `l.category`,
	contract.AnalyticsDimensionBot:              `l.bot_id`,
	contract.AnalyticsDimensionApp:              `l.app_id`,
}

// analyticsBaseLeavesSelect builds the column projection over `execution e` + `execution_totals t` + `conn_meta`,
// skipping agent definition JSONB parsing and tool_calls jsonb_each unless requested.
func analyticsBaseLeavesSelect(needCategory, needCalls bool) string {
	catExpr := "NULL::text AS category,"
	catJoin := ""
	if needCategory {
		catExpr = "ac.category AS category,"
		catJoin = "\n LEFT JOIN agent_cats ac ON ac.id = e.profile_id"
	}
	callsExpr := `0::bigint AS leaf_calls,
  0::bigint AS leaf_call_errors`
	if needCalls {
		callsExpr = `(CASE WHEN e.kind = 'tool_run' THEN 1::bigint
        ELSE coalesce((SELECT sum((v->>'calls')::bigint) FROM jsonb_each(coalesce(t.tool_calls->'byTool', '{}'::jsonb)) kv(k, v) WHERE (v->>'calls') ~ '^[0-9]+$'), 0)
           + coalesce((CASE WHEN (t.tool_calls->'other'->>'calls') ~ '^[0-9]+$' THEN (t.tool_calls->'other'->>'calls')::bigint END), 0)
   END) AS leaf_calls,
  (CASE WHEN e.kind = 'tool_run' THEN (CASE WHEN e.state IN ('failed', 'cancelled') THEN 1::bigint ELSE 0::bigint END)
        ELSE coalesce((SELECT sum((v->>'errors')::bigint) FROM jsonb_each(coalesce(t.tool_calls->'byTool', '{}'::jsonb)) kv(k, v) WHERE (v->>'errors') ~ '^[0-9]+$'), 0)
           + coalesce((CASE WHEN (t.tool_calls->'other'->>'errors') ~ '^[0-9]+$' THEN (t.tool_calls->'other'->>'errors')::bigint END), 0)
   END) AS leaf_call_errors`
	}
	return `
 SELECT
  e.id,
  e.kind::text AS kind,
  e.state::text AS state,
  e.project_id,
  e.task_id,
  e.session_id,
  e.bot_id,
  e.app_id,
  e.parent_execution_id,
  e.pipeline_id,
  e.pipeline_revision,
  e.step_id,
  e.profile_id,
  e.profile_revision,
  CASE
   WHEN e.system_actor IS NOT NULL THEN 'system'
   WHEN e.experiment_id IS NOT NULL THEN 'lab'
   WHEN e.task_id IS NOT NULL THEN 'task'
   WHEN e.session_id IS NOT NULL THEN 'conversation'
   ELSE 'other'
  END AS purpose,
  ` + catExpr + `
  e.harness,
  coalesce(t.model, e.model) AS model,
  coalesce(cm_conn.provider, cm_acc.provider, nullif(e.data->>'provider', ''), CASE e.harness WHEN 'codex' THEN 'openai' WHEN 'claude' THEN 'anthropic' WHEN 'antigravity' THEN 'google' END) AS provider,
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
  (coalesce(t.billing_basis, '') = 'subscription' OR (coalesce(t.billing_basis, '') IN ('', 'unavailable') AND e.account_id IS NOT NULL AND cm_acc.id IS NULL)) AS is_sub,
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
  ` + callsExpr + `
 FROM execution e
 LEFT JOIN execution_totals t ON t.execution_id = e.id` + catJoin + `
 LEFT JOIN conn_meta cm_conn ON e.data ? 'connection' AND nullif(e.data->>'connection', '') IS NOT NULL AND cm_conn.id = 'connection:' || (e.data->>'connection')
 LEFT JOIN conn_meta cm_acc ON e.account_id IS NOT NULL AND cm_acc.id = e.account_id`
}

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

func sqlMeasureSelect(wantP50, wantP95 bool) string {
	p50Expr := "NULL::float8"
	if wantP50 {
		p50Expr = "(percentile_cont(0.5) WITHIN GROUP (ORDER BY wall_ms))::float8"
	}
	p95Expr := "NULL::float8"
	if wantP95 {
		p95Expr = "(percentile_cont(0.95) WITHIN GROUP (ORDER BY wall_ms))::float8"
	}
	return `jsonb_build_object(
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
 'wallMsP50', ` + p50Expr + `,
 'wallMsP95', ` + p95Expr + `,
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
}

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

// Lists (task_page, sessions_list, list_membership, nav_counts): a list's filter is read once into a rowFilter; its
// conditions, group and sort keys come from the allowlists below, every filter value is a bind parameter, and the rows
// scan into the typed rows of tasks.go (taskListDBRow) and sessions.go (sessionListDBRow).

// rowFilter: a list filter as sent. HasProjects: projectId was sent (Projects, and ProjectsNull for null, work
// outside projects); HasExclude: excludeProjectId was sent ("-" in Exclude is null: no project, as the other
// operations name it); States: the state filter (an element that is not a string reads ""); Search: the list search
// syntax (search.go parseSearchQuery); Archived: archived sessions instead of the others. Sessions only: BotIDs,
// Subjects, Rooms and Origins (each matches any of its values) and Listed (false: the hidden conversations instead).
type rowFilter struct {
	HasProjects  bool
	Projects     []string
	ProjectsNull bool
	HasExclude   bool
	Exclude      []string
	States       []string
	Search       *string
	Archived     bool
	BotIDs       []string
	Subjects     []string
	Rooms        []string
	Origins      []string
	Listed       *bool
}

// rowFilterOf reads a filter as sent (a null projectId item is work outside projects, which a typed list of strings
// cannot hold). A field the list does not implement, or one of the wrong type, is a 400, never a silent "all rows".
func rowFilterOf(raw json.RawMessage, known ...string) (rowFilter, error) {
	var f rowFilter
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return f, nil
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	slices.Sort(names)
	jsonKind := func(v json.RawMessage) byte {
		v = bytes.TrimSpace(v)
		if len(v) == 0 {
			return 0
		}
		return v[0]
	}
	for _, k := range names {
		if !slices.Contains(known, k) {
			return f, fail(400, "Unknown filter field "+k)
		}
		v, ok := fields[k], false
		switch k {
		case "state", "excludeProjectId":
			ok = jsonKind(v) == '['
		case "projectId":
			var l []json.RawMessage
			ok = jsonKind(v) == '[' && json.Unmarshal(v, &l) == nil && len(l) > 0
		case "search":
			ok = jsonKind(v) == '"'
		case "archived", "listed":
			ok = jsonKind(v) == 't' || jsonKind(v) == 'f'
		case "botId", "subject", "room", "origin":
			var l []string
			ok = jsonKind(v) == '[' && json.Unmarshal(v, &l) == nil && len(l) > 0
		}
		if !ok {
			return f, fail(400, "Invalid filter field "+k)
		}
	}
	// ids: the strings of a project list and whether it holds null; any other element is a 400.
	ids := func(field string) ([]string, bool, error) {
		var l []*string
		if err := json.Unmarshal(fields[field], &l); err != nil {
			return nil, false, fail(400, "Invalid "+field)
		}
		out, null := []string{}, false
		for _, x := range l {
			if x == nil {
				null = true
			} else {
				out = append(out, *x)
			}
		}
		return out, null, nil
	}
	var err error
	if _, f.HasProjects = fields["projectId"]; f.HasProjects {
		if f.Projects, f.ProjectsNull, err = ids("projectId"); err != nil {
			return f, err
		}
	}
	if _, f.HasExclude = fields["excludeProjectId"]; f.HasExclude {
		var null bool
		if f.Exclude, null, err = ids("excludeProjectId"); err != nil {
			return f, err
		}
		if null {
			f.Exclude = append(f.Exclude, "-")
		}
	}
	if v, ok := fields["state"]; ok {
		var l []json.RawMessage
		json.Unmarshal(v, &l)
		for _, x := range l {
			var s string
			json.Unmarshal(x, &s)
			f.States = append(f.States, s)
		}
	}
	if v, ok := fields["search"]; ok {
		var s string
		json.Unmarshal(v, &s)
		f.Search = &s
	}
	if v, ok := fields["archived"]; ok {
		json.Unmarshal(v, &f.Archived)
	}
	if v, ok := fields["listed"]; ok {
		f.Listed = new(bool)
		json.Unmarshal(v, f.Listed)
	}
	for k, to := range map[string]*[]string{"botId": &f.BotIDs, "subject": &f.Subjects, "room": &f.Rooms, "origin": &f.Origins} {
		if v, ok := fields[k]; ok {
			json.Unmarshal(v, to)
		}
	}
	for _, o := range f.Origins {
		if !slices.Contains(sessionOrigins, o) {
			return f, fail(400, "origin must be one of "+strings.Join(sessionOrigins, ", "))
		}
	}
	return f, nil
}

// projectConds: the projectId filter and the excludeProjectId exclusion (the person's ignored projects, OR-34; a
// project in both is left out) as conditions on col; set (not empty): a text[] expression of more projects a row
// belongs to, which projectId matches too (a conversation's project set). The exclusion stays on col.
func (f rowFilter) projectConds(p *[]any, col, set string, c []string) []string {
	if f.HasProjects {
		o := []string{}
		if len(f.Projects) > 0 {
			*p = append(*p, f.Projects)
			o = append(o, fmt.Sprintf("%s=ANY($%d)", col, len(*p)))
			if set != "" {
				o = append(o, fmt.Sprintf("%s && $%d::text[]", set, len(*p)))
			}
		}
		if f.ProjectsNull {
			o = append(o, col+" IS NULL")
		}
		if len(o) > 0 {
			c = append(c, "("+strings.Join(o, " OR ")+")")
		}
	}
	if f.HasExclude {
		c = projectNotIn(c, p, col, f.Exclude)
	}
	return c
}

// stateConds: the state filter as a condition on expr; a state the list does not have is a 400.
func (f rowFilter) stateConds(p *[]any, expr string, states []string, c []string) ([]string, error) {
	if len(f.States) == 0 {
		return c, nil
	}
	for _, s := range f.States {
		if !slices.Contains(states, s) {
			return nil, fail(400, "Invalid state")
		}
	}
	*p = append(*p, f.States)
	return append(c, fmt.Sprintf("%s=ANY($%d)", expr, len(*p))), nil
}

// projectNotIn: the condition "col is not one of ids" ('-' is no project) appended to c; none when ids is empty. A row
// without a project stays unless '-' is among ids.
func projectNotIn(c []string, p *[]any, col string, ids []string) []string {
	named := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
	return notInProjects(c, p, col, named, len(named) < len(ids))
}

// notInProjects: the conditions "col is none of named" and, with null, "col is set" appended to c.
func notInProjects(c []string, p *[]any, col string, named []string, null bool) []string {
	if len(named) > 0 {
		*p = append(*p, named)
		c = append(c, fmt.Sprintf("NOT coalesce(%s=ANY($%d),false)", col, len(*p)))
	}
	if null {
		c = append(c, col+" IS NOT NULL")
	}
	return c
}

func where(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c, " AND ")
}

// searchWhere: the list search syntax (parseSearchQuery) as SQL conditions, one syntax for Spotlight and every list
// (OR-36): each word must be in one of the text columns; each field term in its column (state exact, the others contain).
// A field the list has no column for matches nothing, never everything.
func searchWhere(raw string, p *[]any, text []string, fields map[string]string, withState bool) []string {
	s, c := parseSearchQuery(raw), []string{}
	for _, w := range strings.Fields(s.text) {
		*p = append(*p, likePattern(w))
		o := []string{}
		for _, col := range text {
			o = append(o, fmt.Sprintf("lower(coalesce(%s,'')) LIKE $%d", col, len(*p)))
		}
		c = append(c, "("+strings.Join(o, " OR ")+")")
	}
	for _, k := range searchFields {
		v, ok := s.fields[k]
		if !ok {
			continue
		}
		col, has := fields[k]
		switch {
		case !has:
			c = append(c, "false")
		case k == "state" && !withState: // per-state counts: every state, like the state filter
		case k == "state":
			*p = append(*p, strings.ToLower(v))
			c = append(c, fmt.Sprintf("%s=$%d", col, len(*p)))
		default:
			*p = append(*p, likePattern(v))
			c = append(c, fmt.Sprintf("lower(coalesce(%s,'')) LIKE $%d", col, len(*p)))
		}
	}
	return c
}

// sqlJSONTime: a timestamp as its JSON text (to_jsonb), the form the list rows answer with.
func sqlJSONTime(col string) string { return "(to_jsonb(" + col + ")#>>'{}')" }

// Tasks (tasks.go task_page, taskMembership, taskSummary; nav_counts.go).

// sqlTaskState: a task's list state; "held" is a paused task with pause_reason 'held' (fleet-pm-agent.md §4).
const sqlTaskState = "(CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)"

type taskGroup struct {
	sql     string
	derived bool
}

var taskGroups = map[string]taskGroup{
	"none":     {"left(t.id,0)", false},
	"priority": {"(CASE t.priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END)", false},
	"project":  {"coalesce(t.project_id,'~')", false},
	"agent":    {"coalesce(d.agent,'~')", true},
	"host":     {"coalesce(d.host,'~')", true},
	"state":    {"(CASE " + sqlTaskState + " WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'in_progress' THEN '3' WHEN 'queued' THEN '4' WHEN 'held' THEN '5' WHEN 'paused' THEN '6' WHEN 'completed' THEN '7' ELSE '8' END)", false},
}

// taskSorts: the second sort key inside a group (owner review P7), ascending numbers; "-" before a name is descending.
// The default is recently changed first. Migrations 47 and 61 index each sort for the priority and project groupings and for no grouping;
// ponytail: the other groupings sort the filtered tasks, add their indexes when those lists outgrow a scan.
var taskSorts = map[string]string{"changed": "(extract(epoch FROM t.updated_at AT TIME ZONE 'UTC')*1000)::float8", "created": "(t.seq::float8)"}

const defaultTaskSort = "-changed"

// defaultTaskGroup is the grouping a view without one uses (view-url.ts DEFAULT_TASK_GROUP).
const defaultTaskGroup = "project"

func taskSortOf(v *string) (string, error) {
	s := defaultTaskSort
	if v != nil && *v != "" {
		s = *v
	}
	expr, ok := taskSorts[strings.TrimPrefix(s, "-")]
	if !ok {
		return "", fail(400, "sort must be changed, created, -changed or -created")
	}
	if strings.HasPrefix(s, "-") {
		return "(-" + expr + ")", nil
	}
	return expr, nil
}

var taskLabels = map[string]string{"priority": "t.priority", "state": sqlTaskState, "project": "t.project_id", "agent": "d.agent", "host": "d.host"}

func groupOf(g *string) (string, taskGroup, error) {
	if g == nil {
		return "none", taskGroups["none"], nil
	}
	G, found := taskGroups[*g]
	if !found {
		return "", G, fail(400, "Invalid group")
	}
	return *g, G, nil
}

// Task columns of the search syntax for task_page (taskWhere); agent and host are subqueries, so every task query can
// use them without a join. agent: the harness of the task's latest execution (task_derived.last_agent;
// task_derived.agent is set only while one runs). The search queries (queries/entities.sql) mirror them.
var taskSearchText = []string{"t.title", "t.id", "t.code"}
var taskSearchFields = map[string]string{"agent": "(SELECT last_agent FROM task_derived WHERE task_id=t.id)", "host": "(SELECT host FROM task_derived WHERE task_id=t.id)",
	"project": "t.project_id", "state": sqlTaskState}

func taskWhere(f rowFilter, p *[]any, withState bool) ([]string, error) {
	c := f.projectConds(p, "t.project_id", "", []string{})
	if withState {
		var err error
		if c, err = f.stateConds(p, sqlTaskState, listStates, c); err != nil {
			return nil, err
		}
	}
	if f.Search != nil {
		c = append(c, searchWhere(*f.Search, p, taskSearchText, taskSearchFields, withState)...)
	}
	return c, nil
}

// sqlActiveExec: latest preparing/running execution for task t with its start time, observation, last contact, and stuck problem flag.
var sqlActiveExec = `(SELECT e.state::text AS active_state,` + sqlJSONTime("e.started_at") + ` AS active_started_at,e.observation::text AS active_observation,` +
	sqlJSONTime("e.last_seen_at") + ` AS active_last_seen_at,` +
	`EXISTS (SELECT 1 FROM problem pb WHERE pb.cause='problem:execution:'||e.id||':stuck' AND pb.state='open') AS active_stalled ` +
	`FROM execution e WHERE e.task_id=t.id AND e.state IN ('preparing','running') ORDER BY e.seq DESC LIMIT 1)`

// sqlTaskListCols: the columns of taskListDBRow (tasks.go), then its group key g and sort key sk. last_started_at: the
// latest execution's start (index execution_task_seq), so a row shows the time since the latest attempt.
var sqlTaskListCols = "t.id,t.code,t.project_id,t.title,t.state::text AS state,t.pause_reason,t.held_by,t.held_by_name,t.wait_reason,t.priority," +
	sqlJSONTime("t.created_at") + " AS created_at," + sqlJSONTime("t.updated_at") + " AS updated_at,d.agent,d.host," +
	sqlJSONTime("d.started_at") + " AS started_at,coalesce(d.open_questions,0)::bigint AS open_questions," + sqlJSONTime("t.state_at") +
	" AS state_at,coalesce(d.executions,0)::bigint AS executions,d.last_agent," +
	"(SELECT " + sqlJSONTime("le.started_at") + " FROM execution le WHERE le.task_id=t.id AND le.started_at IS NOT NULL ORDER BY le.seq DESC LIMIT 1) AS last_started_at," +
	"ae.active_state,ae.active_started_at,ae.active_observation,ae.active_last_seen_at,coalesce(ae.active_stalled,false) AS active_stalled"

// sqlTaskPage: one task_page page of at most limit rows after the sort key after (nil: the first page).
func sqlTaskPage(f rowFilter, G taskGroup, S string, after []any, limit int) (string, []any, error) {
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	// No grouping orders by the sort key alone, so the sort's own index serves it (a constant group key would not).
	none := G.sql == taskGroups["none"].sql
	if after != nil {
		if none {
			p = append(p, num(after[1]), fmt.Sprint(after[2]))
			c = append(c, fmt.Sprintf("(%s,t.id) > ($%d::float8,$%d)", S, len(p)-1, len(p)))
		} else {
			p = append(p, fmt.Sprint(after[0]), num(after[1]), fmt.Sprint(after[2]))
			c = append(c, fmt.Sprintf("(%s,%s,t.id) > ($%d,$%d::float8,$%d)", G.sql, S, len(p)-2, len(p)-1, len(p)))
		}
	}
	g, lim := G.sql, strconv.Itoa(limit)
	order, outer := g+",", "t.g,"
	if none {
		order, outer = "", ""
	}
	if G.derived {
		return "SELECT " + sqlTaskListCols + ",t.g,t.sk FROM (SELECT t.*," + g + " AS g," + S + " AS sk FROM task t LEFT JOIN task_derived d ON d.task_id=t.id" + where(c) +
			" ORDER BY " + order + S + ",t.id LIMIT " + lim + ") t LEFT JOIN task_derived d ON d.task_id=t.id LEFT JOIN LATERAL " + sqlActiveExec + " ae ON true ORDER BY t.g,t.sk,t.id", p, nil
	}
	return "SELECT " + sqlTaskListCols + ",t.g," + S + " AS sk FROM (SELECT t.*," + g + " AS g FROM task t" + where(c) + " ORDER BY " + order + S +
		",t.id LIMIT " + lim + ") t LEFT JOIN task_derived d ON d.task_id=t.id LEFT JOIN LATERAL " + sqlActiveExec + " ae ON true ORDER BY " + outer + S + ",t.id", p, nil
}

// sqlTaskRows: the rows of ids that the filtered list holds (list_membership), with their group and sort keys.
func sqlTaskRows(f rowFilter, G taskGroup, S string, ids []string) (string, []any, error) {
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	p = append(p, ids)
	c = append(c, fmt.Sprintf("t.id=ANY($%d)", len(p)))
	return "SELECT " + sqlTaskListCols + "," + G.sql + " AS g," + S + " AS sk FROM task t LEFT JOIN task_derived d ON d.task_id=t.id LEFT JOIN LATERAL " + sqlActiveExec + " ae ON true" + where(c), p, nil
}

// sqlTaskStateCounts: the filtered tasks per state, without the state filter (listCount rows, g empty); by group too
// with G.
func sqlTaskStateCounts(f rowFilter, G *taskGroup) (string, []any, error) {
	p := []any{}
	c, err := taskWhere(f, &p, false)
	if err != nil {
		return "", nil, err
	}
	if G == nil {
		return "SELECT ''::text AS g," + sqlTaskState + " AS state,count(*) AS n,NULL::text AS label FROM task t" + where(c) + " GROUP BY 2", p, nil
	}
	return "SELECT " + G.sql + " AS g," + sqlTaskState + " AS state,count(*) AS n,NULL::text AS label " + taskFrom(*G) + where(c) + " GROUP BY 1,2", p, nil
}

// sqlTaskGroups: the groups of the filtered list (with the state filter) in key order, with their labels.
func sqlTaskGroups(f rowFilter, name string, G taskGroup) (string, []any) {
	p := []any{}
	c, _ := taskWhere(f, &p, true)
	return "SELECT " + G.sql + " AS g,''::text AS state,count(*) AS n,min(" + taskLabels[name] + ")::text AS label " + taskFrom(G) + where(c) + " GROUP BY 1 ORDER BY 1", p
}

func taskFrom(G taskGroup) string {
	if G.derived {
		return "FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"
	}
	return "FROM task t"
}

// sqlTaskCount: the counter of the nav: the filtered tasks in the focus (focus: the condition on t.project_id, which
// adds its parameters to p) and in all.
func sqlTaskCount(f rowFilter, focus func(p *[]any, col string) string) (string, []any, error) {
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	return "SELECT count(*) FILTER (WHERE " + focus(&p, "t.project_id") + "),count(*) FROM task t" + where(c), p, nil
}

// Sessions (sessions.go sessions_list, list_membership; nav_counts.go).

var sessionStates = []string{"idle", "preparing", "running", "waiting", "succeeded", "failed", "cancelled"}

// sqlSessionBase: every session as a list row. Its state, activity, run count and open questions come from
// session_summary (migration 116), which triggers keep in the same transaction as the writes; the first message is read
// per session by its indexed session_id (migration 77). activity_at is the latest change of the session, its messages
// and events, or the start or end of its executions (the default sort). Not execution.updated_at: host receipts touch it on every observation,
// which is not conversation activity. created_at and archived_at are the session record's (its columns, else its
// payload, matching sessions.go).
var sqlSessionBase = `WITH x AS (SELECT d.id, d.code, d.project_id, d.task_id, d.pipeline_run_id, d.bot_id, d.seq, nullif(d.title,'') AS title,` + sessionModelCols + `,
 d.archived_at, d.actor, d.host_id, d.payload->'agentConfig'->>'harness' AS agent_harness, d.payload->'agentConfig'->>'id' AS profile_id,
 CASE WHEN d.created_at IS NOT NULL THEN (extract(epoch FROM d.created_at)*1000)::bigint::float8 WHEN jsonb_typeof(d.payload->'createdAt')='number' THEN (d.payload->>'createdAt')::float8 END AS created_ms,
 CASE WHEN d.archived_at IS NOT NULL THEN (extract(epoch FROM d.archived_at)*1000)::bigint::float8 WHEN jsonb_typeof(d.payload->'archivedAt')='number' THEN (d.payload->>'archivedAt')::float8 END AS archived_ms,
 coalesce(ss.state,'idle') AS state, ss.harness,
 (SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1) AS first_message,
 greatest(d.updated_at,ss.activity_at) AS activity_at, coalesce(ss.executions,0) AS executions, coalesce(ss.open_questions,0) AS open_questions
 FROM session d LEFT JOIN session_summary ss ON ss.session_id=d.id)`

// sqlSessionCountBase: the sessions as sqlSessionBase gives them to sessionWhere without a search (id, code, project,
// archive, actor and state): a counter repeated on execution changes skips the message and activity scans.
var sqlSessionCountBase = `WITH x AS (SELECT d.id, d.code, d.project_id, d.pipeline_run_id, d.bot_id,` + sessionModelCols + `, d.archived_at, d.actor, coalesce(ss.state,'idle') AS state
 FROM session d LEFT JOIN session_summary ss ON ss.session_id=d.id)`

// sqlSessionDoneBase: sqlSessionCountBase with the latest activity, for Home's counts of sessions done since a time
// (home_view.go homeDone).
var sqlSessionDoneBase = `WITH x AS (SELECT d.id, d.code, d.project_id, d.pipeline_run_id, d.bot_id,` + sessionModelCols + `, d.archived_at, d.actor, coalesce(ss.state,'idle') AS state,
 greatest(d.updated_at,ss.activity_at) AS activity_at
 FROM session d LEFT JOIN session_summary ss ON ss.session_id=d.id)`

// sqlSessionNow: the source of a row's now line (sessions.go nowLine): the newest event of the session with an agent
// text block (one probe of the partial index event_agent_text, migration 101, whose predicate this repeats), its last
// such block, and the attached task's title. Only in the list columns: PostgreSQL evaluates them for the page's rows
// after ORDER BY and LIMIT, never for the counts.
const sqlSessionNow = `(SELECT (SELECT t.b->>'markdown' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(v.payload->'data'->'fleetNormalized'->'blocks')='array' THEN v.payload->'data'->'fleetNormalized'->'blocks' ELSE '[]'::jsonb END) WITH ORDINALITY AS t(b,i)
  WHERE t.b->>'kind'='text' AND coalesce(t.b->>'role','agent')='agent' AND coalesce(t.b->>'markdown','')<>'' ORDER BY t.i DESC LIMIT 1)
 FROM event v WHERE v.session_id=x.id AND NOT v.delta AND jsonb_path_exists(v.payload,'` + agentTextPath + `') ORDER BY v.seq DESC LIMIT 1) AS now_text,
 (SELECT k.title FROM task k WHERE k.id=x.task_id) AS task_title`

// agentTextPath: an event that holds an agent text block, the predicate of the index event_agent_text (migration 101;
// TestAgentTextIndexPredicate keeps them equal).
const agentTextPath = `$.data.fleetNormalized.blocks[*] ? (@.kind == "text" && @.markdown != "" && (@.role == "agent" || !exists(@.role)))`

// sqlSessionListCols: the columns of sessionListDBRow (sessions.go) over x.
var sqlSessionListCols = "x.id,x.code,x.title,x.first_message,x.state,x.harness,x.agent_harness,x.profile_id,x.project_id,x.bot_id,x.origin,x.subject,(x.pipeline_run_id IS NULL) AS listed,x.created_ms,x.archived_ms," +
	sqlJSONTime("x.activity_at") + " AS activity_at,x.executions,x.open_questions," + sqlSessionNow + "," + sqlSessionWork

// sqlSessionWork: a row's work facts (controls/work-facts.md), for the page's rows only like sqlSessionNow: since when
// the oldest open question waits (question_session), the start of the current and of the latest run, and the failed
// runs after the latest run that did not fail (execution_session). Tool calls are not runs of the conversation.
var sqlSessionWork = sqlJSONTime("(SELECT min(q.asked_at) FROM question q WHERE q.session_id=x.id AND q.state='open')") + ` AS waiting_since,` +
	sqlJSONTime("(SELECT e.started_at FROM execution e WHERE e.session_id=x.id AND e.kind<>'tool_run' AND e.state IN ('preparing','running') ORDER BY e.seq DESC LIMIT 1)") + ` AS run_started_at,` +
	sqlJSONTime("(SELECT e.started_at FROM execution e WHERE e.session_id=x.id AND e.kind<>'tool_run' ORDER BY e.seq DESC LIMIT 1)") + ` AS last_started_at,
 (SELECT count(*) FROM execution e WHERE e.session_id=x.id AND e.kind<>'tool_run' AND e.state='failed'
  AND e.seq>coalesce((SELECT max(o.seq) FROM execution o WHERE o.session_id=x.id AND o.kind<>'tool_run' AND o.state<>'failed'),-1)) AS failed_starts`

// sessionModelCols: a session's conversation model fields over d (fleet-conversations.md#entities), for sessionWhere
// in every session base: its subject, its origin and its project set. Only the subject is stored. The origin follows
// from the columns a start writes (sessionOrigin); the project set is the focus (focus_set, session_update), else the
// checkout project (sessionProjects in sessions.go reads the same). listed is pipeline_run_id IS NULL.
const sessionModelCols = ` d.subject, ` + sessionOrigin + ` AS origin,
 (CASE WHEN jsonb_typeof(d.payload->'focus'->'projectIds')='array' THEN ARRAY(SELECT jsonb_array_elements_text(d.payload->'focus'->'projectIds'))
  WHEN d.project_id IS NOT NULL THEN ARRAY[d.project_id] ELSE '{}'::text[] END) AS project_ids`

// sessionOrigin: how a conversation began (contract SessionOrigin), first match: an agent step of a Lab trial (its
// pipeline run has an experiment) or of a pipeline run; a fork (forkOf, session_fork); a bot's conversation, trigger
// when it has a subject (a firing opens those, bots_runtime.go botConversation), bot without one; a start from a
// Telegram message (its start request is the update tg:<id>, telegram.go telegramDeliver); a start for a task by task
// autostart, by an agent (created_by_session_id) or a delegation child; else a person's start.
const sessionOrigin = `(CASE WHEN d.pipeline_run_id IS NOT NULL THEN CASE WHEN EXISTS (SELECT 1 FROM execution r WHERE r.id=d.pipeline_run_id AND r.experiment_id IS NOT NULL) THEN 'lab' ELSE 'pipeline' END
 WHEN jsonb_typeof(d.payload->'forkOf')='object' THEN 'fork'
 WHEN d.bot_id IS NOT NULL THEN CASE WHEN d.subject IS NULL THEN 'bot' ELSE 'trigger' END
 WHEN d.payload->>'startRequest' LIKE '%:tg:%' THEN 'telegram'
 WHEN d.task_id IS NOT NULL AND (d.payload->>'startRequest' LIKE '%:autostart:%' OR d.created_by_session_id IS NOT NULL OR d.parent_execution_id IS NOT NULL) THEN 'task'
 ELSE 'person' END)`

// sessionOrigins: the values of sessionOrigin (contract SessionOrigin), the origin filter's allowlist.
var sessionOrigins = []string{"person", "trigger", "task", "telegram", "fork", "pipeline", "lab", "bot"}

// sessionAgent: the harness of the last execution, else the configured one (the row's harness mark and the agent group).
const sessionAgent = "coalesce(x.harness,x.agent_harness)"

// sessionGroups: the first sort key (sessions_list group). Byte order (COLLATE "C") so the client's key comparison
// (app/src/lists.ts compareKeys) places a live row where the server would. State groups follow attention order. The
// project grouping puts the sessions waiting for an answer first, as one group with the empty key and the label
// "waiting" (pages/conversations.md, owner 2026-11-25: Waiting for you first, then by project).
var sessionGroups = map[string]string{
	"none":    `left(x.id,0)`,
	"project": `(CASE WHEN x.state='waiting' THEN '' ELSE coalesce(x.project_id,'~') END COLLATE "C")`,
	"agent":   `(` + sessionLine + ` COLLATE "C")`,
	"room":    `(coalesce('r:' || (` + sessionRoom + `),'~') COLLATE "C")`,
	"state":   `(CASE x.state WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'running' THEN '3' WHEN 'preparing' THEN '4' WHEN 'idle' THEN '5' WHEN 'succeeded' THEN '6' ELSE '7' END)`,
}
var sessionGroupLabels = map[string]string{"project": "CASE WHEN x.state='waiting' THEN 'waiting' ELSE x.project_id END", "agent": sessionLineLabel, "room": sessionRoomLabel, "state": "x.state"}

// sessionRoom: the Telegram room a conversation is bound to: its open work item, else its newest closed one. A private
// chat is a room too, and its current conversation one of its work items (conversation model step 5).
const sessionRoom = `(SELECT w.room_id FROM telegram_work w WHERE w.session_id=x.id ORDER BY w.closed_at DESC NULLS FIRST LIMIT 1)`

// sessionLine: the agent grouping of the conversation model (fleet-conversations.md#entities; board D1): the bot, else
// the Telegram room, else the PM of the project, else none. Keys sort bots, PMs, rooms, none (b:, p:, r:, ~).
const sessionLine = `(CASE WHEN x.bot_id IS NOT NULL THEN 'b:' || x.bot_id WHEN (` + sessionRoom + `) IS NOT NULL THEN 'r:' || (` + sessionRoom + `)
 WHEN x.profile_id='pm' THEN 'p:' || coalesce(x.project_id,'') ELSE '~' END)`

// sessionRoomLabel: one label per room (chat and topic): a group's general chat "Telegram · <group>", a forum topic
// "Telegram · <group> › <topic>" (the topic alone while the group's title is unknown), a private chat "Telegram · private
// chat"; null without a room ("No room"). The group's title is the room's chat_title, else its general room's title.
const sessionRoomLabel = `(SELECT 'Telegram · ' || CASE WHEN r.kind='private' THEN 'private chat'
   WHEN r.thread_id=0 THEN coalesce(nullif(r.title,''),nullif(r.chat_title,''),'group')
   ELSE coalesce(coalesce(nullif(r.chat_title,''),(SELECT nullif(g.title,'') FROM telegram_room g WHERE g.chat_id=r.chat_id AND g.thread_id=0)) || ' › ','')
     || coalesce(nullif(r.title,''),'topic ' || r.thread_id) END
 FROM telegram_room r WHERE r.id=(` + sessionRoom + `))`

const sessionLineLabel = `(CASE WHEN x.bot_id IS NOT NULL THEN (SELECT r.name FROM bot_revision r WHERE r.bot_id=x.bot_id ORDER BY r.revision DESC LIMIT 1)
 WHEN (` + sessionRoom + `) IS NOT NULL THEN ` + sessionRoomLabel + `
 WHEN x.profile_id='pm' THEN 'PM · ' || coalesce(x.project_id,'outside projects') END)`

// sessionSorts: the second sort key inside a group, ascending numbers; "-" before a name is descending.
var sessionSorts = map[string]string{"activity": "(extract(epoch FROM x.activity_at)*1000)::float8", "created": "(x.seq::float8)"}

const defaultSessionGroup, defaultSessionSort = "project", "-activity"

// sessionOrder: a sessions list's group and sort (name and expressions); an unknown one is a 400, never a silent default.
type sessionOrder struct{ name, g, s string }

func sessionOrderOf(group, sort *string) (sessionOrder, error) {
	g := defaultSessionGroup
	if group != nil {
		g = *group
	}
	G, found := sessionGroups[g]
	if !found {
		return sessionOrder{}, fail(400, "group must be project, state, agent, room or none")
	}
	s := defaultSessionSort
	if sort != nil {
		s = *sort
	}
	S, found := sessionSorts[strings.TrimPrefix(s, "-")]
	if !found {
		return sessionOrder{}, fail(400, "sort must be activity, created, -activity or -created")
	}
	if strings.HasPrefix(s, "-") {
		S = "(-" + S + ")"
	}
	return sessionOrder{g, G, S}, nil
}

// cols: the group and sort keys of each row; by: the row order (ids in byte order too, like the client).
func (o sessionOrder) cols() string { return o.g + " AS g," + o.s + " AS sk" }
func (o sessionOrder) by() string   { return o.g + "," + o.s + `,x.id COLLATE "C"` }

func sessionWhere(f rowFilter, p *[]any, withState bool) ([]string, error) {
	// Host command sessions (program-worker: pipeline script steps, readiness checks, host tools) hold one host job and
	// no conversation; Runners and executions list them, the Conversations list does not. Pipeline and Lab agent steps
	// are hidden conversations (fleet-conversations.md#entities, listed false): their run and trial pages open them.
	c := []string{"x.archived_at IS NULL", "coalesce(x.actor,'')<>'program-worker'", "x.pipeline_run_id IS NULL"}
	if f.Archived {
		c[0] = "x.archived_at IS NOT NULL"
	}
	if f.Listed != nil && !*f.Listed {
		c[2] = "x.pipeline_run_id IS NOT NULL"
	}
	c = f.projectConds(p, "x.project_id", "x.project_ids", c)
	for _, m := range []struct {
		expr string
		vals []string
	}{{"x.bot_id", f.BotIDs}, {"x.subject", f.Subjects}, {sessionRoom, f.Rooms}, {"x.origin", f.Origins}} {
		if len(m.vals) > 0 {
			*p = append(*p, m.vals)
			c = append(c, fmt.Sprintf("%s=ANY($%d)", m.expr, len(*p)))
		}
	}
	if withState {
		var err error
		if c, err = f.stateConds(p, "x.state", sessionStates, c); err != nil {
			return nil, err
		}
	}
	if f.Search != nil { // the list search syntax (searchWhere)
		c = append(c, searchWhere(*f.Search, p, []string{"x.title", "x.first_message", "x.id", "x.code"}, map[string]string{"agent": sessionAgent, "host": "coalesce((SELECT e.host_id FROM execution e WHERE e.session_id=x.id ORDER BY e.seq DESC LIMIT 1),x.host_id)", "project": "x.project_id", "state": "x.state"}, withState)...)
	}
	return c, nil
}

// sqlSessionPage: one sessions_list page of at most limit rows after the sort key after (nil: the first page).
func sqlSessionPage(f rowFilter, o sessionOrder, after []any, limit int) (string, []any, error) {
	p := []any{}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	if after != nil {
		p = append(p, fmt.Sprint(after[0]), num(after[1]), fmt.Sprint(after[2]))
		c = append(c, fmt.Sprintf(`(%s,%s,x.id COLLATE "C") > ($%d,$%d::float8,$%d)`, o.g, o.s, len(p)-2, len(p)-1, len(p)))
	}
	return fmt.Sprintf("%s SELECT %s,%s FROM x WHERE %s ORDER BY %s LIMIT %d", sqlSessionBase, sqlSessionListCols, o.cols(), strings.Join(c, " AND "), o.by(), limit), p, nil
}

// sqlSessionRows: the rows of ids that the filtered list holds (list_membership), in list order.
func sqlSessionRows(f rowFilter, o sessionOrder, ids []string) (string, []any, error) {
	p := []any{}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	p = append(p, ids)
	c = append(c, fmt.Sprintf("x.id=ANY($%d)", len(p)))
	return sqlSessionBase + " SELECT " + sqlSessionListCols + "," + o.cols() + " FROM x WHERE " + strings.Join(c, " AND ") + " ORDER BY " + o.by(), p, nil
}

// sqlSessionSummary: per state and group key the rows matching the filter without states (n) and those that also match
// the states (ng), in group key order, with the group's label.
func sqlSessionSummary(f rowFilter, o sessionOrder) (string, []any, error) {
	p := []any{}
	cc, err := sessionWhere(f, &p, false)
	if err != nil {
		return "", nil, err
	}
	gc, err := sessionWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	label := "NULL::text"
	if l, ok := sessionGroupLabels[o.name]; ok {
		label = l
	}
	return sqlSessionBase + " SELECT x.state," + o.g + " AS g,min(" + label + ") AS label,count(*) AS n,count(*) FILTER (WHERE " +
		strings.Join(gc, " AND ") + ") AS ng FROM x WHERE " + strings.Join(cc, " AND ") + " GROUP BY 1,2 ORDER BY 2", p, nil
}

// sqlSessionDone: Home's counts (home_view.go homeDone): the filtered succeeded sessions with activity since today and
// since since, as today and since.
func sqlSessionDone(f rowFilter, today, since time.Time) (string, []any, error) {
	p := []any{}
	f.States = []string{"succeeded"}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	p = append(p, today, since)
	n, m := len(p)-1, len(p)
	c = append(c, fmt.Sprintf("x.activity_at >= least($%d::timestamptz,$%d::timestamptz)", n, m))
	return fmt.Sprintf("%s SELECT count(*) FILTER (WHERE x.activity_at >= $%d::timestamptz) AS today, count(*) FILTER (WHERE x.activity_at >= $%d::timestamptz) AS since FROM x WHERE %s",
		sqlSessionDoneBase, n, m, strings.Join(c, " AND ")), p, nil
}

// sqlSessionModel: one conversation's subject and origin (session_view's head), over the same origin rule as the list.
func sqlSessionModel(id string) (string, []any) {
	return "SELECT d.subject, " + sessionOrigin + " AS origin FROM session d WHERE d.id = $1", []any{id}
}

// sqlSessionOrigins: the filtered conversations by origin, the origin filter left out (the Started by chips), and the
// hidden ones of the same filter, the origin and listed filters left out (the Steps toggle): rows (origin, n), the hidden count as
// origin ''. One statement over the count base.
func sqlSessionOrigins(f rowFilter) (string, []any, error) {
	g := f
	g.Origins = nil
	p := []any{}
	c, err := sessionWhere(g, &p, true)
	if err != nil {
		return "", nil, err
	}
	h := g
	h.Listed = new(bool)
	hc, err := sessionWhere(h, &p, true)
	if err != nil {
		return "", nil, err
	}
	base := sqlSessionBase
	if f.Search == nil {
		base = sqlSessionCountBase
	}
	// One pass over every conversation the base gives: the shown ones by origin, then the hidden count.
	return base + " SELECT x.origin AS origin, count(*) AS n FROM x" + where(c) + " GROUP BY x.origin" +
		" UNION ALL SELECT '' AS origin, count(*) AS n FROM x" + where(hc), p, nil
}

// sqlSessionCount: the counter of the nav: the filtered sessions in the focus (focus: the condition on x.project_id)
// and in all. Without a search the count needs only each session's state (sqlSessionCountBase).
func sqlSessionCount(f rowFilter, focus func(p *[]any, col string) string) (string, []any, error) {
	p := []any{}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return "", nil, err
	}
	base := sqlSessionBase
	if f.Search == nil {
		base = sqlSessionCountBase
	}
	return base + " SELECT count(*) FILTER (WHERE " + focus(&p, "x.project_id") + "),count(*) FROM x" + where(c), p, nil
}

// Delete and purge (cleanup.go): a cascade's statements over the tables and columns of referenceMap, objectTables,
// tombstoned and purgeOrder (and the foreign keys referenceKeys reads from the schema); the ids are bind parameters,
// one text[] per kind (args).

// sql: the reference over the row: its column, or its expression.
func (r objectRef) sql() string {
	if r.expr != "" {
		return r.expr
	}
	return r.column
}

// match: the reference names one of the ids param holds.
func (r objectRef) match(param string) string {
	m := r.sql() + " = ANY(" + param + ")"
	if r.array {
		m = r.sql() + " && " + param
	}
	return m
}

// args builds one SQL predicate's parameters: one text[] per kind.
type args struct {
	c    *cascade
	vals []any
	at   map[string]int
}

func (a *args) of(kind string) string {
	if a.at == nil {
		a.at = map[string]int{}
	}
	n, ok := a.at[kind]
	if !ok {
		a.vals = append(a.vals, a.c.sets[kind])
		n = len(a.vals)
		a.at[kind] = n
	}
	return fmt.Sprintf("$%d::text[]", n)
}

// goesWith: the predicate of rows of table that belong to the cascade, "false" when none can.
func (c *cascade) goesWith(table string, a *args) string {
	conds := []string{}
	for _, r := range c.refs {
		if r.table == table && (r.mode == refOwn || (r.mode == refWithProject && c.kind == "project")) && len(c.sets[r.kind]) > 0 {
			conds = append(conds, r.match(a.of(r.kind)))
		}
	}
	if len(conds) == 0 {
		return "false"
	}
	return "(" + strings.Join(conds, " OR ") + ")"
}

func andFilter(filter, where string) string {
	if filter == "" {
		return where
	}
	return filter + " AND " + where
}

// sqlCascadeIDs: the ids of table's rows that go with the cascade (where: goesWith).
func sqlCascadeIDs(table, filter, where string) string {
	return "SELECT id FROM " + table + " WHERE " + andFilter(filter, where) + " ORDER BY id"
}

// sqlKeptBy: the first row of table that keeps an object of the cascade (where), with the index of its reference
// (label: CASE WHEN <match> THEN <i> ...) and its id.
func sqlKeptBy(table, label, where, kept string) string {
	return "SELECT (" + label + " END)::int AS ref, coalesce(to_jsonb(x)->>'id', to_jsonb(x)->>'task_id', '') AS rid FROM " + table +
		" x WHERE " + andFilter(kept, where) + " LIMIT 1"
}

// sqlTombstoneObjects: the objects of table whose ids are $1 get their tombstone.
func sqlTombstoneObjects(table, filter string) string {
	return "UPDATE " + table + " SET tombstoned_at=now() WHERE " + andFilter(filter, "id=ANY($1) AND tombstoned_at IS NULL")
}

// sqlTombstoneRows: the rows of table that go with the cascade (where) get their tombstone.
func sqlTombstoneRows(table, where, kept string) string {
	return "UPDATE " + table + " SET tombstoned_at=now() WHERE " + andFilter(kept, where+" AND tombstoned_at IS NULL")
}

// sqlPurgeRows: the rows of table that go with the cascade (where) are deleted.
func sqlPurgeRows(table, where, kept string) string {
	return "DELETE FROM " + table + " WHERE " + andFilter(kept, where)
}

// has: whether the statement sql (built here: a resource item page's next row) returns a row.
func has(q *Request, sql string, args ...any) (bool, error) {
	var ok bool
	err := q.tx.QueryRow(q.ctx(), "SELECT EXISTS("+sql+")", args...).Scan(&ok)
	return ok, err
}

// sqlHistoryPage: a page of activity (history_view) under the request's scope, newest first, below seq before (0: from
// the newest). Every condition is a plain comparison of a column the indexes of migration 122 lead with, so under RLS a
// scoped page starts from its index.
func sqlHistoryPage(r *contract.HistoryViewRequest, before int64, limit int) (string, []any) {
	var c []string
	var p []any
	arg := func(v any) string { p = append(p, v); return "$" + strconv.Itoa(len(p)) }
	eq := map[string]*string{"task_id": r.TaskId, "session_id": r.SessionId, "host_id": r.HostId, "bot_id": r.BotId, "experiment_id": r.ExperimentId, "actor": r.Actor}
	for _, col := range []string{"task_id", "session_id", "host_id", "bot_id", "experiment_id", "actor"} {
		if v := eq[col]; v != nil {
			c = append(c, col+" = "+arg(*v))
		}
	}
	if r.Section != nil {
		c = append(c, "section = "+arg(string(*r.Section)))
	}
	if r.Outcome != nil {
		c = append(c, "outcome = "+arg(string(*r.Outcome)))
	}
	if len(r.Kinds) > 0 {
		ks := make([]string, len(r.Kinds))
		for i, k := range r.Kinds {
			ks[i] = string(k)
		}
		c = append(c, "kind = ANY("+arg(ks)+"::text[])")
	}
	if len(r.ProjectIds) > 0 {
		ids, none := projectSplit(r.ProjectIds)
		var or []string
		if len(ids) > 0 {
			or = append(or, "project_id = ANY("+arg(ids)+"::text[])")
		}
		if none {
			or = append(or, "project_id IS NULL")
		}
		c = append(c, "("+strings.Join(or, " OR ")+")")
	}
	if len(r.ExcludeProjectIds) > 0 {
		ids, none := projectSplit(r.ExcludeProjectIds)
		if len(ids) > 0 {
			c = append(c, "(project_id IS NULL OR NOT (project_id = ANY("+arg(ids)+"::text[])))")
		}
		if none {
			c = append(c, "project_id IS NOT NULL")
		}
	}
	if before > 0 {
		c = append(c, "seq < "+arg(before))
	}
	return `SELECT seq, (extract(epoch FROM at) * 1000)::float8, kind, verb, outcome, section, subject, title, actor, project_id, task_id,
	 session_id, host_id, bot_id, experiment_id, group_key, detail FROM activity` + where(c) + " ORDER BY seq DESC LIMIT " + arg(limit), p
}
