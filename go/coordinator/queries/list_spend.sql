-- name: ListSpendCached :many
-- A row counts each visible execution once. Project rows use the same non-system
-- ownership as AnalyticsProjectLineage. Use the canonical live pricing query for
-- stale entries and for legacy executions outside execution_spend's coverage.
WITH scoped AS MATERIALIZED (
 SELECT DISTINCT x.row_id, x.leaf_id
 FROM ROWS FROM(unnest(sqlc.arg(row_ids)::text[]), unnest(sqlc.arg(leaf_ids)::text[])) x(row_id, leaf_id)
 UNION
 SELECT e.project_id, e.id FROM execution e
 WHERE e.project_id = ANY(sqlc.arg(project_ids)::text[])
   AND e.system_actor IS NULL AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
),
sources AS MATERIALIZED (
 SELECT l.row_id, l.leaf_id, s.cost_currency, s.cost_basis, s.cost_value, s.payload,
  CASE WHEN s.stale THEN 0 ELSE coalesce(s.cost_unpriced, 0) END::bigint + CASE WHEN coalesce(t.history_incomplete, 0) > 0 THEN 1 ELSE 0 END AS unpriced,
  (s.execution_id IS NULL OR s.stale) AS needs_live
 FROM scoped l
 JOIN execution e ON e.id = l.leaf_id
 LEFT JOIN execution_spend s ON s.execution_id = e.id AND s.project_id IS NOT DISTINCT FROM e.project_id
 LEFT JOIN execution_totals t ON t.execution_id = e.id
 WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step') AND (e.kind <> 'child_agent' OR t.execution_id IS NOT NULL)
),
figures AS (
 SELECT s.row_id, s.cost_currency AS currency, s.cost_basis AS basis, s.cost_value AS value
 FROM sources s WHERE NOT s.needs_live AND s.payload IS NULL AND s.cost_currency IS NOT NULL AND s.cost_value IS NOT NULL
 UNION ALL
 SELECT s.row_id, f.currency, f.basis, f.value
 FROM sources s CROSS JOIN LATERAL jsonb_to_recordset(s.payload) f(currency text, basis text, value float8)
 WHERE NOT s.needs_live AND f.currency IS NOT NULL AND f.value IS NOT NULL
),
summary AS (
 SELECT row_id, currency, basis, sum(value)::float8 AS value, 0::bigint AS unpriced, ''::text AS live_leaf
 FROM figures GROUP BY row_id, currency, basis
 UNION ALL
 SELECT row_id, NULL, NULL, NULL, sum(unpriced)::bigint, ''
 FROM sources WHERE unpriced > 0 GROUP BY row_id
 UNION ALL
 SELECT row_id, NULL, NULL, NULL, 0::bigint, leaf_id FROM sources WHERE needs_live
)
SELECT row_id::text, coalesce(currency, '')::text AS currency, coalesce(basis, '')::text AS basis,
 coalesce(value, 0)::float8 AS value, unpriced::bigint, live_leaf::text
FROM summary;

-- name: ListSpendWakeGateway :exec
-- The gateway commits calls independently. Wake spend readers once per project
-- or projectless conversation after the pass, keeping individual tool rows silent;
-- and once per connection the pass saw a call of, so access_status shows its latest call.
WITH published AS (
 INSERT INTO change (kind, id, project_id, session_id, op)
 SELECT DISTINCT 'execution', coalesce(e.project_id, e.session_id), e.project_id,
  CASE WHEN e.project_id IS NULL THEN e.session_id END, 'U'
 FROM execution e
 WHERE e.id = ANY(sqlc.arg(call_ids)::text[]) AND e.system_actor IS NULL
   AND (e.project_id IS NOT NULL OR e.session_id IS NOT NULL)
 RETURNING id
), connections AS (
 INSERT INTO change (kind, id, op, holder)
 SELECT DISTINCT 'api-connection', c.id, 'U', c.holder
 FROM execution e JOIN api_connection c ON c.name = e.data->>'connection'
 WHERE e.id = ANY(sqlc.arg(call_ids)::text[])
 RETURNING id
)
SELECT fleet_enqueue_wake('fleet_change', '') WHERE EXISTS (SELECT 1 FROM published) OR EXISTS (SELECT 1 FROM connections);
