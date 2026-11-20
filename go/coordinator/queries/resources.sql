-- The resource projection (resource_projection.go, resource_apply.go, docs/designs/fleet-analytics.md): source
-- snapshots, replacement, support writes, publication and the queue selections. The state machines' own statements
-- are in resource_states.sql.

-- name: ResourceClaimSnapshot :one
-- Materialization claims only work rows due at $2 (the kit's clock) before evaluating the source in this same
-- statement snapshot; later work is neither observed as claimed nor deleted. The source part is ResourceSource: source,
-- classification, old contribution and retained valuation in ONE statement snapshot (the leaf is the analytics leaf
-- projection with exact numerics, its connection metadata joined directly so that classification shares the snapshot;
-- the SECURITY DEFINER helper fleet_exec_conn_meta can take fresh ones). Only work rows are claimed; no source row is
-- locked by replacement.
WITH claimed AS MATERIALIZED (
 SELECT work_id FROM resource_work WHERE execution_id=sqlc.arg(execution_id)::text AND next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz
 ORDER BY work_id FOR UPDATE SKIP LOCKED),
leaf AS MATERIALIZED (
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
   THEN coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END)::numeric
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
  SELECT coalesce(
   (SELECT coalesce(provider,payload->>'provider') FROM api_connection WHERE id='connection:'||(e.data->>'connection')),
   (SELECT coalesce(provider,payload->>'provider') FROM api_connection WHERE id=e.account_id)) AS provider,
   EXISTS(SELECT 1 FROM api_connection WHERE id=e.account_id) AS is_api_conn
 ) conn ON true WHERE e.id=sqlc.arg(execution_id)::text)
SELECT (SELECT coalesce(array_agg(work_id::text ORDER BY work_id),'{}'::text[]) FROM claimed)::text[] AS claimed,
 CASE WHEN EXISTS(SELECT 1 FROM claimed) THEN (SELECT jsonb_build_object(
 'leaf',(SELECT to_jsonb(l) || jsonb_build_object(
  'started_ms',CASE WHEN l.started_at IS NOT NULL THEN (extract(epoch FROM l.started_at)*1000)::bigint END,
  'ended_ms',CASE WHEN l.ended_at IS NOT NULL THEN (extract(epoch FROM l.ended_at)*1000)::bigint END,
  'turn_count',t.turn_count,'turn_count_basis',t.turn_count_basis,'cpu_missing_reason',t.cpu_missing_reason,
  'memory_missing_reason',t.memory_missing_reason,'history_incomplete',coalesce(t.history_incomplete,0))
  FROM leaf l LEFT JOIN execution_totals t ON t.execution_id=l.id),
 'fingerprint',(SELECT jsonb_build_array(resource_execution_facts(e),
  to_jsonb(t)-ARRAY['computed_at','rate_limit_last','rate_limit_last_basis'],l.provider,l.is_sub)
  FROM execution e JOIN leaf l ON l.id=e.id LEFT JOIN execution_totals t ON t.execution_id=e.id),
 'scopes',(SELECT coalesce(jsonb_agg(p ORDER BY p COLLATE "C"),'[]') FROM (SELECT project_id p FROM execution WHERE id=sqlc.arg(execution_id)::text UNION SELECT project_id FROM execution_totals WHERE execution_id=sqlc.arg(execution_id)::text UNION SELECT project_id FROM resource_contribution WHERE execution_id=sqlc.arg(execution_id)::text UNION SELECT ws.project_id FROM resource_work_scope ws JOIN resource_work w USING(work_id) WHERE w.execution_id=sqlc.arg(execution_id)::text) scopes WHERE p IS NOT NULL),
 'valuationInput',EXISTS(SELECT 1 FROM leaf l WHERE l.leaf_at IS NOT NULL AND (
  (nullif(l.model,'') IS NOT NULL AND l.model NOT LIKE '%-latest' AND coalesce(l.tokens_input,l.tokens_output,l.tokens_cache_read,l.tokens_cache_write) IS NOT NULL)
  OR l.direct_amount IS NOT NULL OR (l.conn_amount IS NOT NULL AND nullif(l.conn_unit,'') IS NOT NULL))),
 'mismatch',EXISTS(SELECT 1 FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.id=sqlc.arg(execution_id)::text AND e.project_id IS DISTINCT FROM t.project_id),
 'old',(SELECT to_jsonb(c) FROM resource_contribution c WHERE c.execution_id=sqlc.arg(execution_id)::text),
 'valuation',(SELECT method FROM resource_valuation WHERE execution_id=sqlc.arg(execution_id)::text),
 'candidates',coalesce((SELECT jsonb_agg(jsonb_build_object('subject',u.subject,'unit',u.unit,
  'priceId',p.id,'rate',p.price::text,'currency',p.currency,'observedAt',(extract(epoch FROM p.observed_at)*1000)::bigint)
  ORDER BY u.subject COLLATE "C",u.unit COLLATE "C") FROM leaf l
  CROSS JOIN LATERAL (VALUES(coalesce(l.model,''),'input_tokens'),(coalesce(l.model,''),'output_tokens'),
   (coalesce(l.model,''),'cache_read_tokens'),(coalesce(l.model,''),'cache_write_tokens'),(l.conn_subject,coalesce(l.conn_unit,''))) u(subject,unit)
  LEFT JOIN LATERAL (SELECT id,price,currency,observed_at FROM price_entry p
   WHERE p.subject=u.subject AND p.unit=u.unit AND p.effective_from<=l.leaf_at
    AND NOT EXISTS(SELECT 1 FROM resource_valuation WHERE execution_id=sqlc.arg(execution_id)::text)
   ORDER BY p.effective_from DESC,p.seq DESC LIMIT 1) p ON true),'[]'))) ELSE NULL END::jsonb AS snapshot;


-- name: ResourceSource :one
-- The source part of ResourceClaimSnapshot alone (the law harness compares stored contributions with it).
WITH leaf AS MATERIALIZED (
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
   THEN coalesce(t.connection_cost->>'credits', CASE WHEN t.connection_cost->>'unit' = 'credits' THEN t.connection_cost->>'amount' END)::numeric
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
  SELECT coalesce(
   (SELECT coalesce(provider,payload->>'provider') FROM api_connection WHERE id='connection:'||(e.data->>'connection')),
   (SELECT coalesce(provider,payload->>'provider') FROM api_connection WHERE id=e.account_id)) AS provider,
   EXISTS(SELECT 1 FROM api_connection WHERE id=e.account_id) AS is_api_conn
 ) conn ON true WHERE e.id=sqlc.arg(execution_id)::text)
SELECT jsonb_build_object(
 'leaf',(SELECT to_jsonb(l) || jsonb_build_object(
  'started_ms',CASE WHEN l.started_at IS NOT NULL THEN (extract(epoch FROM l.started_at)*1000)::bigint END,
  'ended_ms',CASE WHEN l.ended_at IS NOT NULL THEN (extract(epoch FROM l.ended_at)*1000)::bigint END,
  'turn_count',t.turn_count,'turn_count_basis',t.turn_count_basis,'cpu_missing_reason',t.cpu_missing_reason,
  'memory_missing_reason',t.memory_missing_reason,'history_incomplete',coalesce(t.history_incomplete,0))
  FROM leaf l LEFT JOIN execution_totals t ON t.execution_id=l.id),
 'fingerprint',(SELECT jsonb_build_array(resource_execution_facts(e),
  to_jsonb(t)-ARRAY['computed_at','rate_limit_last','rate_limit_last_basis'],l.provider,l.is_sub)
  FROM execution e JOIN leaf l ON l.id=e.id LEFT JOIN execution_totals t ON t.execution_id=e.id),
 'scopes',(SELECT coalesce(jsonb_agg(p ORDER BY p COLLATE "C"),'[]') FROM (SELECT project_id p FROM execution WHERE id=sqlc.arg(execution_id)::text UNION SELECT project_id FROM execution_totals WHERE execution_id=sqlc.arg(execution_id)::text UNION SELECT project_id FROM resource_contribution WHERE execution_id=sqlc.arg(execution_id)::text UNION SELECT ws.project_id FROM resource_work_scope ws JOIN resource_work w USING(work_id) WHERE w.execution_id=sqlc.arg(execution_id)::text) scopes WHERE p IS NOT NULL),
 'valuationInput',EXISTS(SELECT 1 FROM leaf l WHERE l.leaf_at IS NOT NULL AND (
  (nullif(l.model,'') IS NOT NULL AND l.model NOT LIKE '%-latest' AND coalesce(l.tokens_input,l.tokens_output,l.tokens_cache_read,l.tokens_cache_write) IS NOT NULL)
  OR l.direct_amount IS NOT NULL OR (l.conn_amount IS NOT NULL AND nullif(l.conn_unit,'') IS NOT NULL))),
 'mismatch',EXISTS(SELECT 1 FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.id=sqlc.arg(execution_id)::text AND e.project_id IS DISTINCT FROM t.project_id),
 'old',(SELECT to_jsonb(c) FROM resource_contribution c WHERE c.execution_id=sqlc.arg(execution_id)::text),
 'valuation',(SELECT method FROM resource_valuation WHERE execution_id=sqlc.arg(execution_id)::text),
 'candidates',coalesce((SELECT jsonb_agg(jsonb_build_object('subject',u.subject,'unit',u.unit,
  'priceId',p.id,'rate',p.price::text,'currency',p.currency,'observedAt',(extract(epoch FROM p.observed_at)*1000)::bigint)
  ORDER BY u.subject COLLATE "C",u.unit COLLATE "C") FROM leaf l
  CROSS JOIN LATERAL (VALUES(coalesce(l.model,''),'input_tokens'),(coalesce(l.model,''),'output_tokens'),
   (coalesce(l.model,''),'cache_read_tokens'),(coalesce(l.model,''),'cache_write_tokens'),(l.conn_subject,coalesce(l.conn_unit,''))) u(subject,unit)
  LEFT JOIN LATERAL (SELECT id,price,currency,observed_at FROM price_entry p
   WHERE p.subject=u.subject AND p.unit=u.unit AND p.effective_from<=l.leaf_at
    AND NOT EXISTS(SELECT 1 FROM resource_valuation WHERE execution_id=sqlc.arg(execution_id)::text)
   ORDER BY p.effective_from DESC,p.seq DESC LIMIT 1) p ON true),'[]'))::jsonb AS snapshot;


-- name: ResourceScopes :many
-- Every known old and current project scope of one execution.
SELECT p::text AS project_id FROM (
 SELECT project_id p FROM execution WHERE id=$1 UNION SELECT project_id FROM execution_totals WHERE execution_id=$1
 UNION SELECT project_id FROM resource_contribution WHERE execution_id=$1
 UNION SELECT s.project_id FROM resource_work_scope s JOIN resource_work w USING(work_id) WHERE w.execution_id=$1
 ) v WHERE p IS NOT NULL ORDER BY p COLLATE "C";

-- name: ResourceEnsureBuild :exec
INSERT INTO resource_build(project_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: ResourceEnsureSummary :exec
INSERT INTO resource_project_summary(project_id,build_generation) SELECT b.project_id,b.generation FROM resource_build b WHERE b.project_id=$1 ON CONFLICT DO NOTHING;

-- name: ResourceLockSummary :one
SELECT b.phase FROM resource_build b JOIN resource_project_summary s USING(project_id) WHERE b.project_id=$1 FOR UPDATE OF s;

-- name: ResourceFreezeValuation :exec
INSERT INTO resource_valuation(execution_id,method_sha,method) VALUES($1,$2,$3) ON CONFLICT DO NOTHING;

-- name: ResourceContributionPut :exec
INSERT INTO resource_contribution(execution_id,project_id,source_sha,included,facts,method,method_sha) VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,source_sha=excluded.source_sha,included=excluded.included,facts=excluded.facts,method=excluded.method,method_sha=excluded.method_sha,updated_at=clock_timestamp();

-- name: ResourceContributionDelete :exec
DELETE FROM resource_contribution WHERE execution_id=$1;

-- name: ResourceWorkConsume :exec
DELETE FROM resource_work WHERE work_id=ANY(sqlc.arg(work_ids)::text[]::uuid[]);

-- name: ResourceBatchHints :many
-- Only a scheduling hint (locked per-leaf snapshots stay authoritative): the selected executions whose every known
-- scope is their one current project. The selected-ID union avoids repeating an unrelated pending-scope scan per leaf.
WITH candidates AS MATERIALIZED (
 SELECT id,project_id FROM execution WHERE id=ANY($1::text[]) AND project_id IS NOT NULL AND project_id<>''
), selected_work AS MATERIALIZED (
 SELECT work_id,execution_id FROM resource_work WHERE execution_id=ANY($1::text[])
), selected_work_scopes AS MATERIALIZED (
 SELECT work_id,project_id FROM resource_work_scope WHERE work_id=ANY(ARRAY(SELECT work_id FROM selected_work))
), scopes AS (
 SELECT id,project_id FROM candidates
 UNION SELECT execution_id,project_id FROM execution_totals WHERE execution_id=ANY($1::text[])
 UNION SELECT execution_id,project_id FROM resource_contribution WHERE execution_id=ANY($1::text[])
 UNION SELECT w.execution_id,s.project_id FROM selected_work w JOIN selected_work_scopes s USING(work_id)
), merged AS MATERIALIZED (
 SELECT id,min(project_id) project_id,count(DISTINCT project_id) scope_count FROM scopes WHERE project_id IS NOT NULL GROUP BY id
)
SELECT c.id::text AS id,c.project_id::text AS project_id FROM candidates c JOIN merged m USING(id) WHERE m.scope_count=1 AND m.project_id=c.project_id;

-- name: ResourceWorkAttempts :many
SELECT execution_id,max(attempts)::int AS attempts FROM resource_work WHERE execution_id=ANY(sqlc.arg(ids)::text[]) GROUP BY 1;

-- name: ResourceExecutionLock :one
SELECT project_id FROM execution WHERE id=$1 FOR UPDATE;

-- name: ResourceTotalsFollowParent :exec
-- An ownership mismatch repair: the execution's totals follow their parent.
UPDATE execution_totals SET project_id=sqlc.narg(project_id)::text WHERE execution_id=sqlc.arg(execution_id)::text AND project_id IS DISTINCT FROM sqlc.narg(project_id)::text;

-- name: ResourceHeaderSummary :batchone
-- The prepared header reads only fixed-size metric support and indexed extremes; detail support counts are maintained
-- during replacement, so high-cardinality tools/prices never expand it.
SELECT executions,other_calls,tool_count,cost_count,price_count FROM resource_project_summary WHERE project_id=$1;

-- name: ResourceHeaderMetrics :batchmany
SELECT metric::text AS metric,basis,present_count,expected_count,sum_value::text AS sum_value FROM resource_metric WHERE project_id=$1 ORDER BY metric,basis;

-- name: ResourceHeaderExtremes :batchmany
-- The first start, last end and peak memory present.
(SELECT metric,value FROM resource_extreme WHERE project_id=sqlc.arg(project_id)::text AND metric='start' ORDER BY value LIMIT 1)
UNION ALL (SELECT metric,value FROM resource_extreme WHERE project_id=sqlc.arg(project_id)::text AND metric='end' ORDER BY value DESC LIMIT 1)
UNION ALL (SELECT metric,value FROM resource_extreme WHERE project_id=sqlc.arg(project_id)::text AND metric='peakMemory' ORDER BY value DESC LIMIT 1);

-- name: ResourcePublish :one
-- The stored header/version and the public resource_summary change commit together; the revision trigger uses the
-- permission-filtered change/XID/SSE path. The wake column is PostgreSQL void, read as text.
WITH published AS (
 UPDATE resource_project_summary SET header=$2,revision=revision+1,published_at=clock_timestamp()
 WHERE project_id=$1 RETURNING project_id,epoch,revision)
SELECT (epoch::text||':'||revision::text)::text AS version,fleet_enqueue_wake('fleet_resource_summary',project_id)::text AS woken FROM published;

-- name: ResourceSeedWork :exec
-- One statement seeds identities AND all known old/current project scopes.
WITH seeded AS (
 INSERT INTO resource_work(execution_id,origin,build_generation,repair_id)
 SELECT unnest(sqlc.arg(ids)::text[]),sqlc.arg(origin)::text,sqlc.narg(generation)::text::uuid,sqlc.narg(repair)::text::uuid ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id RETURNING work_id,execution_id,first_pending_at)
INSERT INTO resource_work_scope(work_id,project_id,first_pending_at)
SELECT w.work_id,v.project_id,w.first_pending_at FROM seeded w CROSS JOIN LATERAL (
 SELECT project_id FROM execution WHERE id=w.execution_id UNION SELECT project_id FROM resource_contribution WHERE execution_id=w.execution_id) v
WHERE v.project_id IS NOT NULL ON CONFLICT DO NOTHING;

-- name: ResourceBuildsDue :many
-- Projects are discovered from the configuration catalogue, never by rescanning execution history.
SELECT key::text AS key,attempts::int AS attempts,generation,updated_at FROM (
 SELECT id AS key,0 AS attempts,NULL::text AS generation,NULL::pg_catalog.timestamptz AS updated_at FROM definition d WHERE kind='project' AND NOT EXISTS(SELECT 1 FROM resource_build b WHERE b.project_id=d.id)
 UNION ALL SELECT project_id,attempts,generation::text,updated_at FROM resource_build WHERE NOT ready AND next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz) due
ORDER BY updated_at NULLS FIRST,key COLLATE "C" LIMIT sqlc.arg(max)::int;

-- name: ResourceRepairsDue :many
-- A repair whose enumeration is done waits until its seeded work drains.
SELECT repair_id::text AS repair_id,attempts,after_execution_id,done FROM resource_metadata_repair r WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz
 AND (NOT done OR NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id)) ORDER BY next_attempt_at,created_at LIMIT sqlc.arg(max)::int;

-- name: ResourceCandidates :many
-- An execution is one item: a paused execution (a failed row still waiting) is no candidate, even through a new due
-- row, and when it is picked every failed row is due, so a success consumes them all. Executions with a clearing scope
-- yield, so they cannot occupy every slot.
SELECT execution_id FROM (SELECT execution_id,next_attempt_at,first_pending_at FROM resource_work candidate
 WHERE candidate.next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz
 AND NOT EXISTS(SELECT 1 FROM resource_work paused WHERE paused.execution_id=candidate.execution_id AND paused.attempts>0 AND paused.next_attempt_at>sqlc.arg(now)::pg_catalog.timestamptz)
 AND (NOT EXISTS(SELECT 1 FROM resource_build WHERE phase='clearing') OR NOT EXISTS(
  SELECT 1 FROM resource_work blocked JOIN resource_work_scope scope USING(work_id)
  JOIN resource_build build ON build.project_id=scope.project_id
  WHERE blocked.execution_id=candidate.execution_id AND build.phase='clearing'))
 ORDER BY next_attempt_at,first_pending_at,work_id LIMIT sqlc.arg(max)::int) candidates
GROUP BY execution_id ORDER BY min(next_attempt_at),min(first_pending_at),execution_id;

-- name: ResourcePrepared :one
-- The caller's ordinary project RLS applies to this read.
SELECT s.header,s.epoch::text AS epoch,s.revision::text AS revision,s.ready,transaction_timestamp()::pg_catalog.timestamptz AS validated_at,s.published_at,b.last_error
FROM resource_project_summary s JOIN resource_build b USING(project_id) WHERE s.project_id=$1;

-- name: ResourceValidatedAt :one
SELECT transaction_timestamp()::pg_catalog.timestamptz AS validated_at;

-- name: ResourcePreparedProgress :one
-- The maintenance-only progress check, read unscoped in the same snapshot after the authorized summary row; it
-- returns no source facts. The oldest pending age is the least present one (ordered: a missing age sorts last); the
-- error class is the work's, else the repair's.
SELECT
 (EXISTS(SELECT 1 FROM resource_work_scope WHERE project_id=sqlc.arg(project_id)::text)
  OR EXISTS(SELECT 1 FROM resource_repair_scope WHERE project_id=sqlc.arg(project_id)::text)
  OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE NOT done))::boolean AS pending,
 (SELECT at FROM (
  (SELECT first_pending_at at FROM resource_work_scope WHERE project_id=sqlc.arg(project_id)::text AND first_pending_at IS NOT NULL ORDER BY first_pending_at LIMIT 1)
  UNION ALL SELECT min(w.first_pending_at) FROM resource_work_scope ws JOIN resource_work w USING(work_id) WHERE ws.project_id=sqlc.arg(project_id)::text AND ws.first_pending_at IS NULL
  UNION ALL SELECT min(r.created_at) FROM resource_metadata_repair r WHERE NOT done OR EXISTS(SELECT 1 FROM resource_repair_scope rs WHERE rs.repair_id=r.repair_id AND rs.project_id=sqlc.arg(project_id)::text)) ages
  ORDER BY at LIMIT 1) AS oldest_pending_at,
 (SELECT w.last_error FROM resource_work w WHERE w.last_error IS NOT NULL AND EXISTS(SELECT 1 FROM resource_work_scope ws WHERE ws.work_id=w.work_id AND ws.project_id=sqlc.arg(project_id)::text) ORDER BY w.first_pending_at,w.work_id LIMIT 1) AS work_error,
 (SELECT r.last_error FROM resource_metadata_repair r WHERE r.last_error IS NOT NULL AND (NOT done OR EXISTS(SELECT 1 FROM resource_repair_scope rs WHERE rs.repair_id=r.repair_id AND rs.project_id=sqlc.arg(project_id)::text)) ORDER BY r.created_at,r.repair_id LIMIT 1) AS repair_error;

-- Support writes of one replacement transaction (resource_apply.go, resourceDelta.flush), sent as one batch. Per table,
-- keys whose every component only grows take one upsert; keys whose references reach zero are deleted; the rest are
-- updated in place. CHECK constraints apply to an INSERT's proposed row before ON CONFLICT, so a key that loses anything
-- never takes the insert path. An upsert returns whether it created its row (xmax=0); a delete returns how many rows it
-- removed: both change the project's item counts.

-- name: ResourceSummaryAdd :batchexec
UPDATE resource_project_summary SET executions=executions+sqlc.arg(executions)::bigint,history_incomplete=history_incomplete+sqlc.arg(history)::bigint,
 other_calls=other_calls+sqlc.arg(other_calls)::bigint,other_errors=other_errors+sqlc.arg(other_errors)::bigint WHERE project_id=sqlc.arg(project_id)::text;

-- name: ResourceMetricsGain :batchexec
INSERT INTO resource_metric AS r(project_id,metric,basis,present_count,expected_count,sum_value,reasons)
SELECT sqlc.arg(project_id)::text,d.m::resource_metric_name,d.b,d.p,d.e,d.s::numeric,d.r::jsonb FROM ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(present)::bigint[]),unnest(sqlc.arg(expected)::bigint[]),unnest(sqlc.arg(sums)::text[]),unnest(sqlc.arg(reasons)::text[])) d(m,b,p,e,s,r)
ON CONFLICT(project_id,metric,basis) DO UPDATE SET present_count=r.present_count+excluded.present_count,
 expected_count=r.expected_count+excluded.expected_count,sum_value=r.sum_value+excluded.sum_value,reasons=CASE WHEN excluded.reasons='{}' THEN r.reasons ELSE resource_reasons_merge(r.reasons,excluded.reasons) END;

-- name: ResourceMetricsLose :batchexec
UPDATE resource_metric r SET present_count=r.present_count+d.p,expected_count=r.expected_count+d.e,sum_value=r.sum_value+d.s::numeric,
 reasons=CASE WHEN d.r='{}' THEN r.reasons ELSE resource_reasons_merge(r.reasons,d.r::jsonb) END
FROM ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(present)::bigint[]),unnest(sqlc.arg(expected)::bigint[]),unnest(sqlc.arg(sums)::text[]),unnest(sqlc.arg(reasons)::text[])) d(m,b,p,e,s,r)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.metric=d.m::resource_metric_name AND r.basis=d.b;

-- name: ResourceMetricsPrune :batchexec
DELETE FROM resource_metric r USING ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(bases)::text[])) d(m,b)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.metric=d.m::resource_metric_name AND r.basis=d.b AND r.present_count=0 AND r.expected_count=0 AND r.reasons='{}';

-- name: ResourceExtremesGain :batchexec
INSERT INTO resource_extreme AS r(project_id,metric,value,multiplicity) SELECT sqlc.arg(project_id)::text,d.m,d.v,d.n FROM ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(vals)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(m,v,n)
ON CONFLICT(project_id,metric,value) DO UPDATE SET multiplicity=r.multiplicity+excluded.multiplicity;

-- name: ResourceExtremesDrop :batchexec
DELETE FROM resource_extreme r USING ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(vals)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(m,v,n)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.metric=d.m AND r.value=d.v AND r.multiplicity+d.n=0;

-- name: ResourceExtremesLose :batchexec
UPDATE resource_extreme r SET multiplicity=r.multiplicity+d.n FROM ROWS FROM(unnest(sqlc.arg(metrics)::text[]),unnest(sqlc.arg(vals)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(m,v,n)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.metric=d.m AND r.value=d.v;

-- name: ResourceToolsGain :batchmany
INSERT INTO resource_tool AS r(project_id,connection,name,calls,errors,references_count)
SELECT sqlc.arg(project_id)::text,d.c,d.n,d.calls,d.errs,d.refs FROM ROWS FROM(unnest(sqlc.arg(connections)::text[]),unnest(sqlc.arg(names)::text[]),unnest(sqlc.arg(calls)::bigint[]),unnest(sqlc.arg(errs)::bigint[]),unnest(sqlc.arg(refs)::bigint[])) d(c,n,calls,errs,refs)
ON CONFLICT(project_id,connection,name) DO UPDATE SET calls=r.calls+excluded.calls,errors=r.errors+excluded.errors,references_count=r.references_count+excluded.references_count
RETURNING (xmax=0)::boolean AS created;

-- name: ResourceToolsDrop :batchone
WITH gone AS (DELETE FROM resource_tool r USING ROWS FROM(unnest(sqlc.arg(connections)::text[]),unnest(sqlc.arg(names)::text[]),unnest(sqlc.arg(refs)::bigint[])) d(c,n,refs)
 WHERE r.project_id=sqlc.arg(project_id)::text AND r.connection=d.c AND r.name=d.n AND r.references_count+d.refs=0 RETURNING 1)
SELECT count(*)::bigint AS removed FROM gone;

-- name: ResourceToolsLose :batchexec
UPDATE resource_tool r SET calls=r.calls+d.calls,errors=r.errors+d.errs,references_count=r.references_count+d.refs
FROM ROWS FROM(unnest(sqlc.arg(connections)::text[]),unnest(sqlc.arg(names)::text[]),unnest(sqlc.arg(calls)::bigint[]),unnest(sqlc.arg(errs)::bigint[]),unnest(sqlc.arg(refs)::bigint[])) d(c,n,calls,errs,refs)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.connection=d.c AND r.name=d.n;

-- name: ResourceCostAdd :batchmany
-- One contribution's cost of a key written one by one (an unknown amount makes the value NULL until the row goes).
INSERT INTO resource_cost(project_id,currency,basis,value,usages,references_count) VALUES(sqlc.arg(project_id)::text,sqlc.arg(currency)::text,sqlc.arg(basis)::text,sqlc.narg(amount)::text::numeric,sqlc.arg(usages)::bigint,1)
ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=resource_cost.value+excluded.value,usages=resource_cost.usages+excluded.usages,references_count=resource_cost.references_count+1 RETURNING (xmax=0)::boolean AS created;

-- name: ResourceCostSubtract :batchone
-- One statement: the last reference deletes the row (a later add recreates its value), others subtract.
WITH gone AS (DELETE FROM resource_cost WHERE project_id=sqlc.arg(project_id)::text AND currency=sqlc.arg(currency)::text AND basis=sqlc.arg(basis)::text AND references_count=1 RETURNING 1),
kept AS (UPDATE resource_cost SET value=value-sqlc.narg(amount)::text::numeric,usages=usages-sqlc.arg(usages)::bigint,references_count=references_count-1 WHERE project_id=sqlc.arg(project_id)::text AND currency=sqlc.arg(currency)::text AND basis=sqlc.arg(basis)::text AND references_count>1 RETURNING 1)
SELECT count(*)::bigint AS removed FROM gone;

-- name: ResourceCostsGain :batchmany
INSERT INTO resource_cost AS r(project_id,currency,basis,value,usages,references_count)
SELECT sqlc.arg(project_id)::text,d.c,d.b,d.v::numeric,d.u,d.refs FROM ROWS FROM(unnest(sqlc.arg(currencies)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(vals)::text[]),unnest(sqlc.arg(usages)::bigint[]),unnest(sqlc.arg(refs)::bigint[])) d(c,b,v,u,refs)
ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=r.value+excluded.value,usages=r.usages+excluded.usages,references_count=r.references_count+excluded.references_count
RETURNING (xmax=0)::boolean AS created;

-- name: ResourcePricesGain :batchmany
INSERT INTO resource_price_ref AS r(project_id,currency,basis,price_id,observed_at,references_count)
SELECT sqlc.arg(project_id)::text,d.c,d.b,d.id,d.at,d.n FROM ROWS FROM(unnest(sqlc.arg(currencies)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(price_ids)::text[]),unnest(sqlc.arg(observed)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(c,b,id,at,n)
ON CONFLICT(project_id,currency,basis,price_id,observed_at) DO UPDATE SET references_count=r.references_count+excluded.references_count
RETURNING (xmax=0)::boolean AS created;

-- name: ResourcePricesDrop :batchone
WITH gone AS (DELETE FROM resource_price_ref r USING ROWS FROM(unnest(sqlc.arg(currencies)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(price_ids)::text[]),unnest(sqlc.arg(observed)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(c,b,id,at,n)
 WHERE r.project_id=sqlc.arg(project_id)::text AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at AND r.references_count+d.n=0 RETURNING 1)
SELECT count(*)::bigint AS removed FROM gone;

-- name: ResourcePricesLose :batchexec
UPDATE resource_price_ref r SET references_count=r.references_count+d.n
FROM ROWS FROM(unnest(sqlc.arg(currencies)::text[]),unnest(sqlc.arg(bases)::text[]),unnest(sqlc.arg(price_ids)::text[]),unnest(sqlc.arg(observed)::bigint[]),unnest(sqlc.arg(deltas)::bigint[])) d(c,b,id,at,n)
WHERE r.project_id=sqlc.arg(project_id)::text AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at;

-- The net change of each of the project's item counts, written after the batch only when it changed: the count
-- columns may carry update triggers.

-- name: ResourceSummaryTools :exec
UPDATE resource_project_summary SET tool_count=tool_count+sqlc.arg(delta)::bigint WHERE project_id=sqlc.arg(project_id)::text;

-- name: ResourceSummaryCosts :exec
UPDATE resource_project_summary SET cost_count=cost_count+sqlc.arg(delta)::bigint WHERE project_id=sqlc.arg(project_id)::text;

-- name: ResourceSummaryPrices :exec
UPDATE resource_project_summary SET price_count=price_count+sqlc.arg(delta)::bigint WHERE project_id=sqlc.arg(project_id)::text;
