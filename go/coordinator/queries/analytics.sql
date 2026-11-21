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

-- name: AnalyticsTaskLineage :one
WITH RECURSIVE
 candidate_sessions AS (
  SELECT DISTINCT session_id FROM execution WHERE task_id = sqlc.arg(task_id)::text AND session_id IS NOT NULL
 ),
 first_task AS (
  SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
  FROM execution e
  WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
  ORDER BY e.session_id, e.seq
 ),
 direct_sessions AS (
  SELECT id AS session_id FROM session
  WHERE (task_id = sqlc.arg(task_id)::text OR (coalesce(nullif(task_id, ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = sqlc.arg(task_id)::text)))
    AND parent_execution_id IS NULL
 ),
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (
      e.task_id = sqlc.arg(task_id)::text
      OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
      OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
         SELECT p.id FROM execution p
         WHERE p.kind <> 'child_agent'
           AND p.delegation IS NULL
           AND (p.task_id = sqlc.arg(task_id)::text OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
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
  WHERE t.id <> sqlc.arg(task_id)::text
    AND (
      t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
      OR t.created_by_session_id IN (
       SELECT session_id FROM direct_sessions
       UNION SELECT session_id FROM own_execs WHERE session_id IS NOT NULL
       UNION SELECT session_id FROM del_tree WHERE session_id IS NOT NULL
      )
    )
 ),
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
 ),
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
 )
SELECT
 coalesce((SELECT array_agg(scope ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scopes,
 coalesce((SELECT array_agg(scope_key ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scope_keys,
 coalesce((SELECT array_agg(leaf_id ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS leaf_ids,
 coalesce((SELECT jsonb_agg(jsonb_build_object('taskId', id, 'code', code, 'title', title, 'state', state, 'projectId', project_id) ORDER BY seq ASC, id ASC) FROM spawned_tasks), '[]')::jsonb AS spawned_tasks;

-- name: AnalyticsSessionLineage :one
WITH RECURSIVE
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (e.session_id = sqlc.arg(session_id)::text OR (e.kind = 'tool_run' AND e.parent_execution_id IN (SELECT id FROM execution WHERE session_id = sqlc.arg(session_id)::text AND kind <> 'child_agent' AND delegation IS NULL)))
 ),
 del_seed AS (
  SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
     OR (e.delegation IS NOT NULL AND (e.delegation->>'parentSessionId' = sqlc.arg(session_id)::text OR e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs)))
  UNION
  SELECT e.id, e.session_id, true AS follow_session
  FROM execution e
  JOIN session s ON s.id = e.session_id
  WHERE e.session_id <> sqlc.arg(session_id)::text
    AND (
      s.parent_execution_id IN (SELECT id FROM own_execs)
      OR (s.created_by_execution_id IN (SELECT id FROM own_execs) AND coalesce(nullif(s.task_id, ''), '') = '')
      OR (s.created_by_session_id = sqlc.arg(session_id)::text AND coalesce(nullif(s.task_id, ''), '') = '')
    )
 ),
 del_tree AS (
  SELECT id, session_id, follow_session FROM del_seed
  UNION
  SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  JOIN del_tree dt ON e.parent_execution_id = dt.id
     OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id <> sqlc.arg(session_id)::text AND e.task_id IS NULL)
     OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
 ),
 primary_task AS (
  SELECT nullif(task_id, '') AS task_id FROM session WHERE id = sqlc.arg(session_id)::text
 ),
 spawned_tasks AS (
  SELECT t.id, t.code, t.title, t.state::text AS state, t.project_id, t.seq
  FROM task t
  WHERE t.id IS DISTINCT FROM (SELECT task_id FROM primary_task)
    AND (
      t.created_by_session_id = sqlc.arg(session_id)::text
      OR t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
      OR t.created_by_session_id IN (SELECT session_id FROM del_tree WHERE session_id IS NOT NULL)
    )
 ),
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
 ),
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
 )
SELECT
 coalesce((SELECT array_agg(scope ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scopes,
 coalesce((SELECT array_agg(scope_key ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS scope_keys,
 coalesce((SELECT array_agg(leaf_id ORDER BY scope, scope_key, leaf_id) FROM scoped_leaves), '{}')::text[] AS leaf_ids,
 coalesce((SELECT jsonb_agg(jsonb_build_object('taskId', id, 'code', code, 'title', title, 'state', state, 'projectId', project_id) ORDER BY seq ASC, id ASC) FROM spawned_tasks), '[]')::jsonb AS spawned_tasks;

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
-- container descendants resolved to leaves. The visible child/parent edges are materialized once: an OR join inside
-- the recursive term rescans every execution for every tree row and spills wide session rows; the narrow edge relation
-- preserves all four links.
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
 -- A session's parent execution read by key: a join to session planned from an estimate of one execution row scans
 -- every session for each execution (seconds at 20k executions before the tables are analyzed).
 session_parents AS (
  SELECT coalesce(jsonb_object_agg(id, parent_execution_id), '{}'::jsonb) AS m FROM session WHERE parent_execution_id IS NOT NULL
 ),
 edges AS MATERIALIZED (
  SELECT DISTINCT c.id AS child_id, p.parent_id::text AS parent_id
  FROM execution c
  CROSS JOIN LATERAL (VALUES
   (c.parent_execution_id),
   (CASE WHEN c.delegation IS NOT NULL THEN c.delegation->>'parentExecutionId' END),
   ((SELECT m FROM session_parents) ->> c.session_id),
   (CASE WHEN c.kind = 'tool_run' THEN c.data->>'pipelineRunId' END)
  ) p(parent_id)
  WHERE p.parent_id IS NOT NULL
 ),
 tree AS (
  SELECT root_id AS exec_id, def_id, rev FROM pinned
  UNION
  SELECT c.child_id AS exec_id, t.def_id, t.rev
  FROM edges c JOIN tree t ON c.parent_id = t.exec_id
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
 LEFT JOIN LATERAL (
  SELECT m.provider::text AS provider, m.is_api_conn::boolean AS is_api_conn
  FROM fleet_exec_conn_meta(e.data->>'connection', e.account_id) m
  WHERE e.data ? 'connection' OR e.account_id IS NOT NULL
 ) conn ON true WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL) AND e.id IN (SELECT leaf_id FROM scoped_leaves)),
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
GROUP BY coalesce(project_id, '')
ORDER BY n DESC, project_id ASC;
