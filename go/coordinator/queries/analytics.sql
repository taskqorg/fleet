-- Analytics (analytics.go, docs/designs/fleet-analytics.md): the fixed statements. analytics_query composes its SQL
-- from the requested dimensions and filters (sqlAnalytics, sql_builder.go).

-- name: AnalyticsWindowLeafCount :one
-- Every leaf execution in [from, to), past the caller's row scope (CA-05).
SELECT fleet_window_leaf_count(sqlc.arg(win_from)::pg_catalog.timestamptz, sqlc.arg(win_to)::pg_catalog.timestamptz)::bigint AS n;

-- name: AnalyticsTaskHiddenLineage :one
SELECT fleet_task_hidden_lineage_count(sqlc.arg(task_id)::text)::bigint AS n;

-- name: AnalyticsSessionHiddenLineage :one
SELECT fleet_session_hidden_lineage_count(sqlc.arg(session_id)::text)::bigint AS n;

-- name: AnalyticsJIT :one
SELECT current_setting('jit')::text AS jit;

-- name: AnalyticsSetJIT :exec
SELECT set_config('jit', sqlc.arg(jit)::text, true);

-- The resource rollups (analytics.go): a lineage query names each rollup's (scope, scope key, leaf) triples, sorted,
-- as three parallel arrays (and, for a task or session, its visible spawned tasks in order); AnalyticsLeafRows then
-- reads those leaves once each with their costs, one row per scope a leaf counts in, without double-counting cost
-- items across scopes.

-- name: AnalyticsTaskLineage :many
SELECT root.id, l.scopes::text[] AS scopes, l.scope_keys::text[] AS scope_keys, l.leaf_ids::text[] AS leaf_ids, l.spawned_tasks::jsonb AS spawned_tasks
FROM task root
CROSS JOIN LATERAL fleet_task_resource_lineage(root.id, sqlc.arg(total_only)::boolean) l
WHERE root.id = ANY(sqlc.arg(row_ids)::text[]);

-- name: AnalyticsSessionLineage :many
SELECT root.id, l.scopes::text[] AS scopes, l.scope_keys::text[] AS scope_keys, l.leaf_ids::text[] AS leaf_ids, l.spawned_tasks::jsonb AS spawned_tasks
FROM session root
CROSS JOIN LATERAL fleet_session_resource_lineage(root.id, sqlc.arg(total_only)::boolean) l
WHERE root.id = ANY(sqlc.arg(row_ids)::text[])
  AND (NOT sqlc.arg(total_only)::boolean OR coalesce(root.actor, '') <> 'program-worker');

-- name: AnalyticsBoundaryLineage :one
-- Each boundary execution with its direct tool_run children.
WITH scoped_leaves AS (
  SELECT 'boundary'::text AS scope, e.id AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.id = ANY(sqlc.arg(ids)::text[])
  UNION
  SELECT 'boundary'::text AS scope, e.parent_execution_id AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.kind = 'tool_run' AND e.parent_execution_id = ANY(sqlc.arg(ids)::text[])
 )
SELECT
 coalesce((SELECT array_agg(scope ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scopes,
 coalesce((SELECT array_agg(scope_key ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scope_keys,
 coalesce((SELECT array_agg(leaf_id ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS leaf_ids;

-- name: AnalyticsDefinitionLineage :one
-- The leaves of non-draft pinned executions of the last days, per definition id (total) and per id:revision (rev),
-- container descendants resolved to leaves. The walk goes down from the pinned roots only: plain-column links
-- (parent execution, a session's parent) are index lookups per tree row; the two links held in JSON come from one map
-- read by key. Materializing every execution's links instead scanned and sorted the whole execution table on each call
-- (seconds at 200k executions, FLT-41).
WITH RECURSIVE
 pinned AS (
  SELECT e.id AS root_id, d->>'id' AS def_id, CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::int END AS rev
  FROM execution e
  CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
  WHERE e.definitions @> sqlc.arg(pin)::jsonb AND d->>'kind' = sqlc.arg(kind)::text AND (sqlc.arg(id)::text = '' OR d->>'id' = sqlc.arg(id)::text)
    AND coalesce((d->>'draft')::boolean, false) = false
    AND coalesce(d->>'revision', '') ~ '^[0-9]+$'
    AND coalesce(e.started_at, e.updated_at) >= now() - make_interval(days => sqlc.arg(days)::int)
 ),
 -- The two links held in JSON (a delegation's parent, a pipeline tool call's run) are read once each from their
 -- partial indexes into one map from parent to children, read by key: under row security a non-leakproof ->> cannot be
 -- an index condition, so a lookup per tree row would rescan the whole partial index.
 json_children AS MATERIALIZED (
  SELECT coalesce(jsonb_object_agg(parent_id, kids), '{}'::jsonb) AS m
  FROM (
   SELECT parent_id, jsonb_agg(child_id) AS kids
   FROM (
    SELECT c.id AS child_id, c.delegation->>'parentExecutionId' AS parent_id FROM execution c WHERE c.delegation IS NOT NULL
    UNION ALL
    SELECT c.id, c.data->>'pipelineRunId' FROM execution c WHERE c.data ? 'pipelineRunId' AND c.kind = 'tool_run'
   ) j
   WHERE parent_id IS NOT NULL
   GROUP BY parent_id
  ) g
 ),
 tree AS (
  SELECT root_id AS exec_id, def_id, rev FROM pinned
  UNION
  SELECT ch.exec_id, t.def_id, t.rev
  FROM tree t
  CROSS JOIN LATERAL (
   SELECT c.id AS exec_id FROM execution c WHERE c.parent_execution_id = t.exec_id
   UNION ALL
   SELECT c.id FROM session s JOIN execution c ON c.session_id = s.id WHERE s.parent_execution_id = t.exec_id
   UNION ALL
   SELECT k.child_id FROM jsonb_array_elements_text((SELECT m FROM json_children) -> t.exec_id) k(child_id)
  ) ch
 ),
 leaf_hits AS (
  SELECT DISTINCT tr.def_id, tr.rev, e.id AS leaf_id
  FROM tree tr
  JOIN execution e ON e.id = tr.exec_id
  LEFT JOIN execution_totals t ON t.execution_id = e.id
  WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL)
 ),
 scoped_leaves AS (
  SELECT 'total'::text AS scope, def_id AS scope_key, leaf_id FROM leaf_hits
  UNION
  SELECT 'rev'::text AS scope, def_id || ':' || rev::text AS scope_key, leaf_id FROM leaf_hits
 )
SELECT
 coalesce((SELECT array_agg(scope ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scopes,
 coalesce((SELECT array_agg(scope_key ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scope_keys,
 coalesce((SELECT array_agg(leaf_id ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS leaf_ids;

-- name: AnalyticsProjectLineage :one
-- The project's work: its leaves without Fleet's own background calls (system_actor).
WITH scoped_leaves AS (
  SELECT 'project'::text AS scope, ''::text AS scope_key, e.id AS leaf_id
  FROM execution e
  WHERE e.project_id = sqlc.arg(project_id)::text AND e.system_actor IS NULL
 )
SELECT
 coalesce((SELECT array_agg(scope ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scopes,
 coalesce((SELECT array_agg(scope_key ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scope_keys,
 coalesce((SELECT array_agg(leaf_id ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS leaf_ids;

-- name: AnalyticsLeafRows :many
-- The given leaves (the analytics leaf projection), their prices resolved per leaf before any grouping (CA-04), one
-- row per (scope, scope key) a leaf counts in. Each leaf's scopes and costs are one jsonb object keyed by leaf id, read
-- by key: the CTEs carry no row estimates, so a join of two of them can plan as a nested loop over every pair (leaves x
-- scoped_leaves, leaves x cost items: seconds at a few thousand leaves); a key lookup cannot.
WITH scoped_leaves AS (
 SELECT s.scope, s.scope_key, s.leaf_id FROM ROWS FROM(unnest(sqlc.arg(scopes)::text[]), unnest(sqlc.arg(scope_keys)::text[]), unnest(sqlc.arg(leaf_ids)::text[])) s(scope, scope_key, leaf_id)
),
conn_meta AS MATERIALIZED (
 SELECT m.id::text AS id, m.provider::text AS provider FROM fleet_conn_meta_all() m
),
leaves AS MATERIALIZED (
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
   WHEN t.connection_cost IS NOT NULL AND coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
   THEN coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END)::float8
  END AS credits,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN t.connection_cost->>'currency'
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN t.connection_cost->>'unit'
   END
  END AS direct_currency,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency'))::numeric
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit'))::numeric
   END
  END AS direct_amount,
  CASE
   WHEN t.connection_cost IS NOT NULL AND coalesce(t.connection_cost->>'amount', CASE WHEN nullif(t.connection_cost->>'unit', '') IS NOT NULL THEN t.connection_cost->>(t.connection_cost->>'unit') END) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
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
 LEFT JOIN conn_meta cm_conn ON e.data ? 'connection' AND nullif(e.data->>'connection', '') IS NOT NULL AND cm_conn.id = 'connection:' || (e.data->>'connection')
 LEFT JOIN conn_meta cm_acc ON e.account_id IS NOT NULL AND cm_acc.id = e.account_id
 WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL) AND e.id IN (SELECT leaf_id FROM scoped_leaves)),
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
),
leaf_scope_map AS (
 SELECT coalesce(jsonb_object_agg(leaf_id, scopes), '{}'::jsonb) AS m
 FROM (SELECT leaf_id, jsonb_agg(jsonb_build_array(scope, scope_key)) AS scopes FROM scoped_leaves GROUP BY leaf_id) x
),
leaf_cost_map AS (
 SELECT coalesce(jsonb_object_agg(id, costs), '{}'::jsonb) AS m
 FROM (
  SELECT id, jsonb_agg(jsonb_build_object('currency', currency, 'basis', cost_basis, 'value', value, 'prices', prices, 'unpriced', unpriced)) AS costs
  FROM (
   SELECT ci.id, ci.currency, ci.cost_basis, sum(ci.amount)::float8 AS value, sum(ci.unpriced)::bigint AS unpriced,
    coalesce(jsonb_agg(DISTINCT jsonb_build_object('id', ci.price_id, 'observedAt', ci.obs_ms)) FILTER (WHERE ci.price_id IS NOT NULL), '[]'::jsonb) AS prices
   FROM leaf_cost_items ci
   GROUP BY ci.id, ci.currency, ci.cost_basis
  ) cg
  GROUP BY id
 ) x
)
SELECT
 s.scope::text AS scope,
 s.scope_key::text AS scope_key,
 jsonb_build_object(
  'id', l.id, 'kind', l.kind, 'state', l.state,
  'started_ms', CASE WHEN l.started_at IS NOT NULL THEN (extract(epoch FROM l.started_at) * 1000)::bigint END,
  'ended_ms', CASE WHEN l.ended_at IS NOT NULL THEN (extract(epoch FROM l.ended_at) * 1000)::bigint END,
  'has_totals', l.has_totals,
  'tokens_input', l.tokens_input, 'tokens_input_basis', l.tokens_input_basis,
  'tokens_output', l.tokens_output, 'tokens_output_basis', l.tokens_output_basis,
  'tokens_cache_read', l.tokens_cache_read, 'tokens_cache_read_basis', l.tokens_cache_read_basis,
  'tokens_cache_write', l.tokens_cache_write, 'tokens_cache_write_basis', l.tokens_cache_write_basis,
  'tokens_reasoning', l.tokens_reasoning, 'tokens_reasoning_basis', l.tokens_reasoning_basis,
  'api_ms', l.api_ms, 'api_ms_basis', l.api_ms_basis,
  'wall_ms', l.wall_ms, 'wall_ms_basis', l.wall_ms_basis,
  'active_ms', l.active_ms, 'active_ms_basis', l.active_ms_basis,
  'cpu_ms', l.cpu_ms, 'cpu_ms_basis', l.cpu_ms_basis,
  'peak_memory_bytes', l.peak_memory_bytes, 'peak_memory_bytes_basis', l.peak_memory_bytes_basis,
  'turn_count', l.turn_count,
  'tool_calls', l.tool_calls, 'tool_calls_basis', l.tool_calls_basis,
  'connection', l.connection, 'tool_name', l.tool_name,
  'credits', l.credits,
  'has_conn_cost', (l.connection_cost IS NOT NULL),
  'leaf_costs', coalesce(lc.costs, '[]'::jsonb))::jsonb AS leaf
FROM leaves l
CROSS JOIN LATERAL jsonb_array_elements((SELECT m FROM leaf_scope_map) -> l.id) sc
CROSS JOIN LATERAL (SELECT sc->>0 AS scope, sc->>1 AS scope_key) s
LEFT JOIN LATERAL (SELECT (SELECT m FROM leaf_cost_map) -> l.id AS costs) lc ON true;

-- name: AnalyticsCompletedTasksByProject :many
-- Completed non-tombstoned tasks per project within [win_from, win_to), visible under the caller's RLS scope.
SELECT coalesce(project_id, '')::text AS project_id, count(*)::bigint AS n
FROM task
WHERE state = 'completed'
  AND tombstoned_at IS NULL
  AND coalesce(accepted_at, state_at) >= sqlc.arg(win_from)::pg_catalog.timestamptz
  AND coalesce(accepted_at, state_at) < sqlc.arg(win_to)::pg_catalog.timestamptz
  AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id)::text)
  AND (sqlc.narg(project_ids)::text[] IS NULL OR project_id = ANY(sqlc.narg(project_ids)::text[]) OR (sqlc.arg(project_ids_null)::boolean AND project_id IS NULL))
  AND NOT coalesce(project_id = ANY(sqlc.narg(exclude_ids)::text[]), false)
  AND (NOT sqlc.arg(exclude_null)::boolean OR project_id IS NOT NULL)
GROUP BY coalesce(project_id, '')
ORDER BY n DESC, project_id ASC;

-- name: AnalyticsAgentCategories :many
-- Current head category of each agent definition (parsed once per query instead of per execution row).
SELECT da.id::text AS id, coalesce(nullif((dar.content::jsonb)->>'category', ''), '')::text AS category
FROM definition da
JOIN definition_revision dar ON dar.kind = da.kind AND dar.id = da.id AND dar.revision = da.head
WHERE da.kind = 'agent' AND dar.content IS NOT NULL AND left(dar.content, 1) = '{';

-- name: AnalyticsWindowVisibleLeafCount :one
-- Unfiltered visible leaf count in [win_from, win_to) under caller RLS.
SELECT count(*)::bigint AS n
FROM execution_spend
WHERE started_at >= sqlc.arg(win_from)::pg_catalog.timestamptz
  AND started_at < sqlc.arg(win_to)::pg_catalog.timestamptz;

-- name: AnalyticsSpendSummary :many
-- Single-pass spend_summary rollup over precomputed execution_spend (Index-Only Scan on fast path),
-- falling back to live LATERAL price_entry only for stale rows.
WITH stale_ids AS MATERIALIZED (
 SELECT s.execution_id
 FROM execution_spend s
 WHERE s.stale
   AND s.started_at >= sqlc.arg(win_from)::pg_catalog.timestamptz
   AND s.started_at < sqlc.arg(win_to)::pg_catalog.timestamptz
   AND (sqlc.narg(project_id)::text IS NULL OR s.project_id = sqlc.narg(project_id)::text)
   AND (sqlc.narg(project_ids)::text[] IS NULL OR s.project_id = ANY(sqlc.narg(project_ids)::text[]) OR (sqlc.arg(project_ids_null)::boolean AND s.project_id IS NULL))
   AND NOT coalesce(s.project_id = ANY(sqlc.narg(exclude_ids)::text[]), false)
   AND (NOT sqlc.arg(exclude_null)::boolean OR s.project_id IS NOT NULL)
   AND (
    sqlc.arg(system_mode)::text = 'include'
    OR (sqlc.arg(system_mode)::text = 'only' AND s.system_actor IS NOT NULL)
    OR (sqlc.arg(system_mode)::text = 'exclude' AND s.system_actor IS NULL)
   )
),
stale_conn AS MATERIALIZED (
 SELECT m.id::text AS id, m.provider::text AS provider FROM fleet_conn_meta_all() m WHERE EXISTS (SELECT 1 FROM stale_ids)
),
stale_leaves AS MATERIALIZED (
 SELECT
  e.id,
  e.kind::text AS kind,
  e.state::text AS state,
  e.project_id,
  e.bot_id,
  CASE
   WHEN e.system_actor IS NOT NULL THEN 'system'
   WHEN e.experiment_id IS NOT NULL THEN 'lab'
   WHEN e.task_id IS NOT NULL THEN 'task'
   WHEN e.session_id IS NOT NULL THEN 'conversation'
   ELSE 'other'
  END AS purpose,
  e.profile_id,
  coalesce(t.model, e.model) AS model,
  e.host_id,
  e.started_at AS leaf_at,
  (coalesce(t.billing_basis, '') = 'subscription' OR (coalesce(t.billing_basis, '') IN ('', 'unavailable') AND e.account_id IS NOT NULL AND cm_acc.id IS NULL)) AS is_sub,
  t.tokens_input,
  coalesce(t.tokens_input_basis, 'unavailable') AS tokens_input_basis,
  t.tokens_output,
  coalesce(t.tokens_output_basis, 'unavailable') AS tokens_output_basis,
  t.tokens_cache_read,
  t.tokens_cache_write,
  t.connection_cost,
  coalesce(e.data->>'connection', '') || '/' || coalesce(e.data->>'tool', '') AS conn_subject,
  nullif(t.connection_cost->>'unit', '') AS conn_unit,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN t.connection_cost->>'currency'
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN t.connection_cost->>'unit'
   END
  END AS direct_currency,
  CASE WHEN t.connection_cost IS NOT NULL AND t.connection_cost_basis = 'measured' THEN
   CASE
    WHEN nullif(t.connection_cost->>'currency', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN coalesce(t.connection_cost->>'cost', t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'currency'))::numeric
    WHEN nullif(t.connection_cost->>'unit', '') ~ '^[A-Z]{3}$'
     AND coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit')) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
     THEN coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit'))::numeric
   END
  END AS direct_amount,
  CASE
   WHEN t.connection_cost IS NOT NULL AND coalesce(t.connection_cost->>'amount', CASE WHEN nullif(t.connection_cost->>'unit', '') IS NOT NULL THEN t.connection_cost->>(t.connection_cost->>'unit') END) ~ '^[-+]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][-+]?[0-9]+)?$'
   THEN coalesce(t.connection_cost->>'amount', t.connection_cost->>(t.connection_cost->>'unit'))::numeric
  END AS conn_amount
 FROM execution e
 JOIN stale_ids si ON si.execution_id = e.id
 LEFT JOIN execution_totals t ON t.execution_id = e.id
 LEFT JOIN stale_conn cm_acc ON e.account_id IS NOT NULL AND cm_acc.id = e.account_id
 WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL)
),
stale_priced AS (
 SELECT
  l.*,
  pi.id AS pi_id, pi.price AS pi_price, pi.currency AS pi_currency, pi.obs_ms AS pi_obs,
  po.id AS po_id, po.price AS po_price, po.currency AS po_currency, po.obs_ms AS po_obs,
  pcr.id AS pcr_id, pcr.price AS pcr_price, pcr.currency AS pcr_currency, pcr.obs_ms AS pcr_obs,
  pcw.id AS pcw_id, pcw.price AS pcw_price, pcw.currency AS pcw_currency, pcw.obs_ms AS pcw_obs,
  pc.id AS pc_id, pc.price AS pc_price, pc.currency AS pc_currency, pc.obs_ms AS pc_obs
 FROM stale_leaves l
 LEFT JOIN LATERAL (SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms FROM price_entry p WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_input > 0 AND p.subject = l.model AND p.unit = 'input_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1) pi ON true
 LEFT JOIN LATERAL (SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms FROM price_entry p WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_output > 0 AND p.subject = l.model AND p.unit = 'output_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1) po ON true
 LEFT JOIN LATERAL (SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms FROM price_entry p WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_cache_read > 0 AND p.subject = l.model AND p.unit = 'cache_read_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1) pcr ON true
 LEFT JOIN LATERAL (SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms FROM price_entry p WHERE l.kind <> 'tool_run' AND l.connection_cost IS NULL AND l.model IS NOT NULL AND l.tokens_cache_write > 0 AND p.subject = l.model AND p.unit = 'cache_write_tokens' AND l.leaf_at IS NOT NULL AND p.effective_from <= l.leaf_at ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1) pcw ON true
 LEFT JOIN LATERAL (SELECT p.id, p.price, p.currency, (extract(epoch FROM p.observed_at) * 1000)::bigint AS obs_ms FROM price_entry p WHERE (l.kind = 'tool_run' OR l.connection_cost IS NOT NULL) AND l.direct_currency IS NULL AND l.conn_unit IS NOT NULL AND l.conn_amount IS NOT NULL AND l.leaf_at IS NOT NULL AND p.subject = l.conn_subject AND p.unit = l.conn_unit AND p.effective_from <= l.leaf_at ORDER BY p.effective_from DESC, p.seq DESC LIMIT 1) pc ON true
),
stale_cost_items AS (
 SELECT
  p.id,
  CASE sqlc.arg(group_by)::text
   WHEN 'purpose' THEN p.purpose
   WHEN 'project' THEN coalesce(nullif(p.project_id, ''), 'unknown')
   WHEN 'profile' THEN coalesce(nullif(p.profile_id, ''), 'unknown')
   WHEN 'category' THEN coalesce(nullif(p.profile_id, ''), '')
   WHEN 'model' THEN coalesce(nullif(p.model, ''), 'unknown')
   WHEN 'host' THEN coalesce(nullif(p.host_id, ''), 'unknown')
   WHEN 'bot' THEN coalesce(nullif(p.bot_id, ''), 'unknown')
  END AS dim_key,
  p.purpose,
  coalesce(p.project_id, '') AS proj_key,
  p.kind, p.state, p.tokens_input, p.tokens_input_basis, p.tokens_output, p.tokens_output_basis,
  ci.currency, ci.cost_basis, ci.amount, ci.price_id, ci.obs_ms, ci.unpriced,
  row_number() OVER (PARTITION BY p.id ORDER BY ci.currency NULLS LAST, ci.cost_basis NULLS LAST, ci.price_id NULLS LAST) AS rn
 FROM stale_priced p
 LEFT JOIN LATERAL (
  SELECT
   CASE WHEN p.direct_currency IS NOT NULL THEN p.direct_currency WHEN p.pc_id IS NOT NULL THEN p.pc_currency END AS currency,
   CASE WHEN p.direct_currency IS NOT NULL THEN CASE WHEN p.is_sub THEN 'subscription' ELSE 'measured' END WHEN p.pc_id IS NOT NULL THEN CASE WHEN p.is_sub THEN 'subscription' ELSE 'estimated' END END AS cost_basis,
   CASE WHEN p.direct_currency IS NOT NULL THEN p.direct_amount WHEN p.pc_id IS NOT NULL THEN p.pc_price * p.conn_amount END AS amount,
   CASE WHEN p.direct_currency IS NULL THEN p.pc_id END AS price_id,
   CASE WHEN p.direct_currency IS NULL THEN p.pc_obs END AS obs_ms,
   CASE WHEN p.direct_currency IS NULL AND p.pc_id IS NULL THEN 1::bigint ELSE 0::bigint END AS unpriced
  WHERE p.kind = 'tool_run' OR p.connection_cost IS NOT NULL
  UNION ALL
  SELECT p.pi_currency, CASE WHEN p.is_sub THEN 'subscription' ELSE 'estimated' END, p.pi_price * p.tokens_input::numeric, p.pi_id, p.pi_obs, CASE WHEN p.pi_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE p.kind <> 'tool_run' AND p.connection_cost IS NULL AND p.tokens_input > 0
  UNION ALL
  SELECT p.po_currency, CASE WHEN p.is_sub THEN 'subscription' ELSE 'estimated' END, p.po_price * p.tokens_output::numeric, p.po_id, p.po_obs, CASE WHEN p.po_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE p.kind <> 'tool_run' AND p.connection_cost IS NULL AND p.tokens_output > 0
  UNION ALL
  SELECT p.pcr_currency, CASE WHEN p.is_sub THEN 'subscription' ELSE 'estimated' END, p.pcr_price * p.tokens_cache_read::numeric, p.pcr_id, p.pcr_obs, CASE WHEN p.pcr_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE p.kind <> 'tool_run' AND p.connection_cost IS NULL AND p.tokens_cache_read > 0
  UNION ALL
  SELECT p.pcw_currency, CASE WHEN p.is_sub THEN 'subscription' ELSE 'estimated' END, p.pcw_price * p.tokens_cache_write::numeric, p.pcw_id, p.pcw_obs, CASE WHEN p.pcw_id IS NULL THEN 1::bigint ELSE 0::bigint END
  WHERE p.kind <> 'tool_run' AND p.connection_cost IS NULL AND p.tokens_cache_write > 0
  UNION ALL
  SELECT NULL::text, NULL::text, NULL::numeric, NULL::text, NULL::bigint, 1::bigint
  WHERE p.kind IN ('agent_run', 'child_agent') AND p.connection_cost IS NULL AND coalesce(p.tokens_input, p.tokens_output, p.tokens_cache_read, p.tokens_cache_write) IS NULL
 ) ci ON true
),
spend_items AS (
 -- Fast Index-Only Scan path
 SELECT
  CASE sqlc.arg(group_by)::text
   WHEN 'purpose' THEN s.purpose
   WHEN 'project' THEN coalesce(nullif(s.project_id, ''), 'unknown')
   WHEN 'profile' THEN coalesce(nullif(s.profile_id, ''), 'unknown')
   WHEN 'category' THEN coalesce(nullif(s.profile_id, ''), '')
   WHEN 'model' THEN coalesce(nullif(s.model, ''), 'unknown')
   WHEN 'host' THEN coalesce(nullif(s.host_id, ''), 'unknown')
   WHEN 'bot' THEN coalesce(nullif(s.bot_id, ''), 'unknown')
  END AS dim_key,
  s.purpose,
  coalesce(s.project_id, '') AS proj_key,
  s.cost_currency,
  s.cost_basis,
  s.cost_price_refs,
  1::bigint AS runs,
  s.failed_i::bigint AS failed,
  s.tokens_input::float8 AS tin,
  CASE WHEN s.tokens_input IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tin_have,
  s.tin_sub_i::bigint AS tin_sub,
  s.tin_est_i::bigint AS tin_est,
  s.tin_exp_i::bigint AS tin_exp,
  s.tokens_output::float8 AS tout,
  CASE WHEN s.tokens_output IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tout_have,
  s.tout_sub_i::bigint AS tout_sub,
  s.tout_est_i::bigint AS tout_est,
  s.tout_exp_i::bigint AS tout_exp,
  s.cost_value::float8 AS cost_val,
  s.cost_unpriced::bigint AS unpriced
 FROM execution_spend s
 WHERE NOT s.stale AND s.payload IS NULL
   AND s.started_at >= sqlc.arg(win_from)::pg_catalog.timestamptz
   AND s.started_at < sqlc.arg(win_to)::pg_catalog.timestamptz
   AND (sqlc.narg(project_id)::text IS NULL OR s.project_id = sqlc.narg(project_id)::text)
   AND (sqlc.narg(project_ids)::text[] IS NULL OR s.project_id = ANY(sqlc.narg(project_ids)::text[]) OR (sqlc.arg(project_ids_null)::boolean AND s.project_id IS NULL))
   AND NOT coalesce(s.project_id = ANY(sqlc.narg(exclude_ids)::text[]), false)
   AND (NOT sqlc.arg(exclude_null)::boolean OR s.project_id IS NOT NULL)
   AND (
    sqlc.arg(system_mode)::text = 'include'
    OR (sqlc.arg(system_mode)::text = 'only' AND s.system_actor IS NOT NULL)
    OR (sqlc.arg(system_mode)::text = 'exclude' AND s.system_actor IS NULL)
   )

 UNION ALL

 -- Multi-currency single-leaf overflow path (empty in normal workloads)
 SELECT
  CASE sqlc.arg(group_by)::text
   WHEN 'purpose' THEN s.purpose
   WHEN 'project' THEN coalesce(nullif(s.project_id, ''), 'unknown')
   WHEN 'profile' THEN coalesce(nullif(s.profile_id, ''), 'unknown')
   WHEN 'category' THEN coalesce(nullif(s.profile_id, ''), '')
   WHEN 'model' THEN coalesce(nullif(s.model, ''), 'unknown')
   WHEN 'host' THEN coalesce(nullif(s.host_id, ''), 'unknown')
   WHEN 'bot' THEN coalesce(nullif(s.bot_id, ''), 'unknown')
  END AS dim_key,
  s.purpose,
  coalesce(s.project_id, '') AS proj_key,
  ci.currency AS cost_currency,
  ci.cost_basis AS cost_basis,
  ci.cost_price_refs AS cost_price_refs,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN 1::bigint ELSE 0::bigint END AS runs,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.failed_i::bigint ELSE 0::bigint END AS failed,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tokens_input::float8 END AS tin,
  CASE WHEN coalesce(ci.ord, 1) = 1 AND s.tokens_input IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tin_have,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tin_sub_i::bigint ELSE 0::bigint END AS tin_sub,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tin_est_i::bigint ELSE 0::bigint END AS tin_est,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tin_exp_i::bigint ELSE 0::bigint END AS tin_exp,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tokens_output::float8 END AS tout,
  CASE WHEN coalesce(ci.ord, 1) = 1 AND s.tokens_output IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tout_have,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tout_sub_i::bigint ELSE 0::bigint END AS tout_sub,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tout_est_i::bigint ELSE 0::bigint END AS tout_est,
  CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.tout_exp_i::bigint ELSE 0::bigint END AS tout_exp,
  ci.cost_value AS cost_val,
  (CASE WHEN coalesce(ci.ord, 1) = 1 THEN s.cost_unpriced ELSE 0::bigint END + coalesce(ci.unpriced, 0))::bigint AS unpriced
 FROM execution_spend s
 LEFT JOIN LATERAL (
  SELECT
   elem->>'currency' AS currency,
   elem->>'basis' AS cost_basis,
   (elem->>'value')::float8 AS cost_value,
   coalesce((elem->>'unpriced')::bigint, 0) AS unpriced,
   nullif((elem->'prices')::text, '[]') AS cost_price_refs,
   ord
  FROM jsonb_array_elements(s.payload) WITH ORDINALITY AS x(elem, ord)
 ) ci ON true
 WHERE NOT s.stale AND s.payload IS NOT NULL
   AND s.started_at >= sqlc.arg(win_from)::pg_catalog.timestamptz
   AND s.started_at < sqlc.arg(win_to)::pg_catalog.timestamptz
   AND (sqlc.narg(project_id)::text IS NULL OR s.project_id = sqlc.narg(project_id)::text)
   AND (sqlc.narg(project_ids)::text[] IS NULL OR s.project_id = ANY(sqlc.narg(project_ids)::text[]) OR (sqlc.arg(project_ids_null)::boolean AND s.project_id IS NULL))
   AND NOT coalesce(s.project_id = ANY(sqlc.narg(exclude_ids)::text[]), false)
   AND (NOT sqlc.arg(exclude_null)::boolean OR s.project_id IS NOT NULL)
   AND (
    sqlc.arg(system_mode)::text = 'include'
    OR (sqlc.arg(system_mode)::text = 'only' AND s.system_actor IS NOT NULL)
    OR (sqlc.arg(system_mode)::text = 'exclude' AND s.system_actor IS NULL)
   )

 UNION ALL

 -- Stale live-pricing fallback path (empty except immediately after backdated price_entry or connection edit)
 SELECT
  sc.dim_key,
  sc.purpose,
  sc.proj_key,
  sc.currency AS cost_currency,
  sc.cost_basis,
  CASE WHEN sc.price_id IS NOT NULL THEN jsonb_build_array(jsonb_build_object('id', sc.price_id, 'observedAt', sc.obs_ms))::text END AS cost_price_refs,
  CASE WHEN sc.rn = 1 THEN 1::bigint ELSE 0::bigint END AS runs,
  CASE WHEN sc.rn = 1 AND sc.state = 'failed' THEN 1::bigint ELSE 0::bigint END AS failed,
  CASE WHEN sc.rn = 1 THEN sc.tokens_input::float8 END AS tin,
  CASE WHEN sc.rn = 1 AND sc.tokens_input IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tin_have,
  CASE WHEN sc.rn = 1 AND sc.tokens_input IS NOT NULL AND sc.tokens_input_basis = 'subscription' THEN 1::bigint ELSE 0::bigint END AS tin_sub,
  CASE WHEN sc.rn = 1 AND sc.tokens_input IS NOT NULL AND sc.tokens_input_basis = 'estimated' THEN 1::bigint ELSE 0::bigint END AS tin_est,
  CASE WHEN sc.rn = 1 AND (sc.kind IN ('agent_run', 'child_agent') OR sc.tokens_input IS NOT NULL) THEN 1::bigint ELSE 0::bigint END AS tin_exp,
  CASE WHEN sc.rn = 1 THEN sc.tokens_output::float8 END AS tout,
  CASE WHEN sc.rn = 1 AND sc.tokens_output IS NOT NULL THEN 1::bigint ELSE 0::bigint END AS tout_have,
  CASE WHEN sc.rn = 1 AND sc.tokens_output IS NOT NULL AND sc.tokens_output_basis = 'subscription' THEN 1::bigint ELSE 0::bigint END AS tout_sub,
  CASE WHEN sc.rn = 1 AND sc.tokens_output IS NOT NULL AND sc.tokens_output_basis = 'estimated' THEN 1::bigint ELSE 0::bigint END AS tout_est,
  CASE WHEN sc.rn = 1 AND (sc.kind IN ('agent_run', 'child_agent') OR sc.tokens_output IS NOT NULL) THEN 1::bigint ELSE 0::bigint END AS tout_exp,
  sc.amount::float8 AS cost_val,
  coalesce(sc.unpriced, 0)::bigint AS unpriced
 FROM stale_cost_items sc
)
SELECT
 dim_key::text AS dim_key,
 purpose::text AS purpose,
 proj_key::text AS proj_key,
 cost_currency,
 cost_basis,
 cost_price_refs,
 sum(runs)::bigint AS runs,
 sum(failed)::bigint AS failed,
 coalesce(sum(tin), 0)::float8 AS tin,
 sum(tin_have)::bigint AS tin_have,
 sum(tin_sub)::bigint AS tin_sub,
 sum(tin_est)::bigint AS tin_est,
 sum(tin_exp)::bigint AS tin_exp,
 coalesce(sum(tout), 0)::float8 AS tout,
 sum(tout_have)::bigint AS tout_have,
 sum(tout_sub)::bigint AS tout_sub,
 sum(tout_est)::bigint AS tout_est,
 sum(tout_exp)::bigint AS tout_exp,
 coalesce(sum(cost_val), 0)::float8 AS cost_val,
 sum(unpriced)::bigint AS unpriced
FROM spend_items
GROUP BY 1, 2, 3, 4, 5, 6;

-- name: AnalyticsBotNames :many
SELECT b.id, s.name
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
WHERE b.tombstoned_at IS NULL;
