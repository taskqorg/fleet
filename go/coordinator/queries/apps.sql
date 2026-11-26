-- Project apps (docs/designs/fleet-project-apps.md).

-- name: ProjectAppInsert :exec
INSERT INTO project_app (id, project_id, name, state, request_id, asked_by, payload)
VALUES (@id, @project_id, @name, 'asked', @request_id, @asked_by, @payload);

-- name: ProjectAppGet :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE id = $1 AND deleted_at IS NULL;

-- name: ProjectAppLock :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ProjectAppByRequest :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE project_id = @project_id AND request_id = @request_id AND deleted_at IS NULL;

-- name: ProjectAppByQuestion :one
SELECT id FROM project_app WHERE question_id = $1;

-- name: ProjectAppsOfProject :many
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at;

-- name: ProjectAppIDTaken :one
-- App ids are unique in Fleet, across projects (SECURITY DEFINER: the caller sees only its projects).
SELECT fleet_project_app_id_taken($1)::boolean;

-- name: ProjectAppSetQuestion :exec
UPDATE project_app SET question_id = @question_id, payload = @payload, name = @name, updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppSetState :exec
UPDATE project_app SET state = @state, approved_by = coalesce(sqlc.narg(approved_by), approved_by), updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppSetPayload :exec
UPDATE project_app SET payload = @payload, updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppKeyInsert :exec
INSERT INTO project_app_key (id, app_id, project_id, directed_by, sha256) VALUES (@id, @app_id, @project_id, @directed_by, @sha256);

-- name: ProjectAppKeys :many
SELECT id, app_id, created_at, taken_at, last_used_at, retired_at FROM project_app_key WHERE app_id = ANY(@app_ids::text[]) ORDER BY created_at;

-- name: ProjectAppKeyNewestUntaken :one
SELECT id FROM project_app_key WHERE app_id = $1 AND taken_at IS NULL AND retired_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: ProjectAppKeyTaken :one
SELECT taken_at FROM project_app_key WHERE app_id = $1 AND retired_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: ProjectAppKeyMarkTaken :exec
UPDATE project_app_key SET taken_at = clock_timestamp() WHERE id = $1 AND taken_at IS NULL;

-- name: ProjectAppKeysRetire :exec
UPDATE project_app_key SET retired_at = clock_timestamp() WHERE app_id = $1 AND retired_at IS NULL;

-- name: ProjectAppKeysRetireBefore :exec
-- A rotation's grace ends at the new key's first use: the app's older live keys are retired.
UPDATE project_app_key SET retired_at = clock_timestamp() WHERE app_id = @app_id AND retired_at IS NULL AND created_at < @created_at;

-- name: ProjectAppKeyBySecret :one
-- The live key a request presents (read before any scope is known, like a host secret).
SELECT k.id, k.app_id, k.project_id, k.directed_by, k.created_at, k.last_used_at FROM project_app_key k
WHERE k.sha256 = $1 AND k.retired_at IS NULL;

-- name: ProjectAppKeyUsed :exec
UPDATE project_app_key SET last_used_at = clock_timestamp() WHERE id = $1;

-- name: ProjectAppUsed :exec
UPDATE project_app SET last_used_at = clock_timestamp() WHERE id = $1;

-- name: AppsEdgeGet :one
SELECT e.id, e.state, e.request_id, e.question_id, e.rotate, e.payload, e.updated_at, w.last_error FROM apps_edge e LEFT JOIN apps_edge_work w ON w.id = e.id WHERE e.id = 'edge';

-- name: AppsEdgeLock :one
SELECT id, state, request_id, question_id, rotate, payload, updated_at FROM apps_edge WHERE id = 'edge' FOR UPDATE;

-- name: AppsEdgeUpsert :exec
INSERT INTO apps_edge (id, state, request_id, question_id, rotate, payload) VALUES ('edge', @state, @request_id, @question_id, @rotate, @payload)
ON CONFLICT (id) DO UPDATE SET state = excluded.state, request_id = excluded.request_id, question_id = excluded.question_id,
 rotate = excluded.rotate, payload = excluded.payload, updated_at = clock_timestamp();

-- name: AppsEdgeSet :exec
UPDATE apps_edge SET state = @state, payload = @payload, rotate = @rotate, updated_at = clock_timestamp() WHERE id = 'edge';

-- name: AppsEdgeQueue :exec
UPDATE apps_edge SET state = 'working', updated_at = clock_timestamp() WHERE id = 'edge';

-- name: AppsEdgeWorkPut :exec
INSERT INTO apps_edge_work (id) VALUES ('edge') ON CONFLICT (id) DO NOTHING;

-- name: AppsEdgeWorkDelete :exec
DELETE FROM apps_edge_work WHERE id = 'edge';

-- name: AppsEdgeWorkDue :many
SELECT id, attempts FROM apps_edge_work WHERE next_attempt_at <= @now LIMIT @lim;

-- name: AppsEdgeByQuestion :one
SELECT id FROM apps_edge WHERE question_id = $1;

-- name: ExecutionSetApp :exec
-- The app a pipeline run belongs to; its steps and leaves take it from their parent (fleet_inherit_app).
UPDATE execution SET app_id = @app_id WHERE id = @id AND app_id IS DISTINCT FROM @app_id;

-- name: ProjectAppPayload :one
SELECT payload FROM project_app WHERE id = $1 AND state = 'active' AND deleted_at IS NULL;

-- name: AppRunsSince :one
-- Runs the app started since a time (limits.runsPerDay), by execution_app (app_id, started_at).
SELECT count(*)::bigint FROM execution WHERE app_id = @app_id AND started_at >= @since AND kind = 'pipeline_run';

-- name: AppRunsInFlight :one
-- The app's runs not ended yet (limits.concurrentRuns); a run ends with its ended_at.
SELECT count(*)::bigint FROM execution WHERE app_id = @app_id AND kind = 'pipeline_run' AND ended_at IS NULL;

-- name: AppSpendSince :one
-- Measured and estimated spend in currency of the app's executions started since a time (limits.spendPerMonth): each
-- leaf's execution_spend row, its one cost or, when it has several currencies or bases, its payload groups.
SELECT coalesce(sum(CASE WHEN s.payload IS NULL THEN
  CASE WHEN s.cost_currency = @currency::text AND s.cost_basis IN ('measured', 'estimated') THEN s.cost_value END
 ELSE (SELECT sum((g->>'value')::float8) FROM jsonb_array_elements(s.payload) g
  WHERE g->>'currency' = @currency::text AND g->>'basis' IN ('measured', 'estimated')) END), 0)::float8
FROM execution e JOIN execution_spend s ON s.execution_id = e.id
WHERE e.app_id = @app_id AND e.started_at >= @since AND s.tombstoned_at IS NULL;

-- name: AppCallbackPut :exec
INSERT INTO app_callback (run_id, app_id, url) VALUES (@run_id, @app_id, @url) ON CONFLICT (run_id) DO NOTHING;

-- name: AppCallbackDue :many
SELECT run_id, attempts FROM app_callback WHERE delivered_at IS NULL AND next_attempt_at <= @now ORDER BY next_attempt_at LIMIT @lim;

-- name: AppCallbackGet :one
SELECT run_id, app_id, url FROM app_callback WHERE run_id = $1 AND delivered_at IS NULL;

-- name: AppCallbackDelivered :exec
UPDATE app_callback SET delivered_at = clock_timestamp() WHERE run_id = $1;

-- name: ProjectAppUse :many
-- Each app's real use since @since (fleet-project-apps.md, Screens): its pipeline runs (execution_app) and the failed
-- ones, and the cost of their leaf executions only (fleet-analytics.md: containers never count, a leaf counts once),
-- summed per currency and basis from execution_spend; a leaf without a fresh spend row counts as unpriced.
WITH RECURSIVE runs AS MATERIALIZED (
 SELECT e.id, e.app_id AS caller, (e.state = 'failed') AS failed
 FROM execution e
 WHERE e.app_id = ANY(@app_ids::text[]) AND e.started_at >= @since::timestamptz AND e.kind = 'pipeline_run'
), tree(root, id) AS (
 SELECT r.id, r.id FROM runs r
 UNION
 SELECT t.root, c.id FROM tree t JOIN execution c ON c.parent_execution_id = t.id
), leaves AS MATERIALIZED (
 SELECT DISTINCT ON (x.id) r.caller, x.id
 FROM tree t JOIN runs r ON r.id = t.root JOIN execution x ON x.id = t.id
 LEFT JOIN execution_totals tt ON tt.execution_id = x.id
 WHERE x.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (x.kind = 'child_agent' AND tt.execution_id IS NULL)
 ORDER BY x.id, r.caller
), figures AS (
 SELECT l.caller, s.cost_currency AS currency, s.cost_basis AS basis, s.cost_value AS value, 0::bigint AS unpriced
 FROM leaves l JOIN execution_spend s ON s.execution_id = l.id
 WHERE NOT s.stale AND s.payload IS NULL AND s.cost_currency IS NOT NULL AND s.cost_value IS NOT NULL
 UNION ALL
 SELECT l.caller, f.currency, f.basis, f.value, 0::bigint
 FROM leaves l JOIN execution_spend s ON s.execution_id = l.id CROSS JOIN LATERAL jsonb_to_recordset(s.payload) f(currency text, basis text, value float8)
 WHERE NOT s.stale AND s.payload IS NOT NULL AND f.currency IS NOT NULL AND f.value IS NOT NULL
 UNION ALL
 SELECT l.caller, NULL, NULL, NULL, CASE WHEN s.execution_id IS NULL OR s.stale THEN 1 ELSE s.cost_unpriced END::bigint
 FROM leaves l LEFT JOIN execution_spend s ON s.execution_id = l.id
 WHERE s.execution_id IS NULL OR s.stale OR s.cost_unpriced > 0
), costs AS (
 SELECT z.caller, jsonb_agg(jsonb_build_object('currency', z.currency, 'basis', z.basis, 'value', z.value, 'unpriced', z.unpriced)) AS j
 FROM (SELECT caller, currency, basis, sum(value)::float8 AS value, sum(unpriced)::bigint AS unpriced FROM figures GROUP BY 1, 2, 3) z
 GROUP BY z.caller
)
SELECT r.caller::text AS caller, count(*)::bigint AS runs, (count(*) FILTER (WHERE r.failed))::bigint AS failed,
 coalesce(min(k.j::text), '[]')::jsonb AS costs
FROM runs r LEFT JOIN costs k ON k.caller = r.caller
GROUP BY r.caller;

-- name: ProjectPipelineUse :many
-- Who ran a project's pipeline since @since (the pipeline page's Used by): one row per app, and an empty caller for agents and
-- people; Lab runs (experiment_id) are left out. Runs, failed runs and the leaf cost as ProjectAppUse counts them.
WITH RECURSIVE runs AS MATERIALIZED (
 SELECT e.id, coalesce(e.app_id, '') AS caller, (e.state = 'failed') AS failed
 FROM execution e
 WHERE e.project_id = @project_id::text AND e.started_at >= @since::timestamptz AND e.pipeline_id = @pipeline_id::text
  AND e.experiment_id IS NULL AND e.kind = 'pipeline_run'
), tree(root, id) AS (
 SELECT r.id, r.id FROM runs r
 UNION
 SELECT t.root, c.id FROM tree t JOIN execution c ON c.parent_execution_id = t.id
), leaves AS MATERIALIZED (
 SELECT DISTINCT ON (x.id) r.caller, x.id
 FROM tree t JOIN runs r ON r.id = t.root JOIN execution x ON x.id = t.id
 LEFT JOIN execution_totals tt ON tt.execution_id = x.id
 WHERE x.kind NOT IN ('pipeline_run', 'pipeline_step') AND NOT (x.kind = 'child_agent' AND tt.execution_id IS NULL)
 ORDER BY x.id, r.caller
), figures AS (
 SELECT l.caller, s.cost_currency AS currency, s.cost_basis AS basis, s.cost_value AS value, 0::bigint AS unpriced
 FROM leaves l JOIN execution_spend s ON s.execution_id = l.id
 WHERE NOT s.stale AND s.payload IS NULL AND s.cost_currency IS NOT NULL AND s.cost_value IS NOT NULL
 UNION ALL
 SELECT l.caller, f.currency, f.basis, f.value, 0::bigint
 FROM leaves l JOIN execution_spend s ON s.execution_id = l.id CROSS JOIN LATERAL jsonb_to_recordset(s.payload) f(currency text, basis text, value float8)
 WHERE NOT s.stale AND s.payload IS NOT NULL AND f.currency IS NOT NULL AND f.value IS NOT NULL
 UNION ALL
 SELECT l.caller, NULL, NULL, NULL, CASE WHEN s.execution_id IS NULL OR s.stale THEN 1 ELSE s.cost_unpriced END::bigint
 FROM leaves l LEFT JOIN execution_spend s ON s.execution_id = l.id
 WHERE s.execution_id IS NULL OR s.stale OR s.cost_unpriced > 0
), costs AS (
 SELECT z.caller, jsonb_agg(jsonb_build_object('currency', z.currency, 'basis', z.basis, 'value', z.value, 'unpriced', z.unpriced)) AS j
 FROM (SELECT caller, currency, basis, sum(value)::float8 AS value, sum(unpriced)::bigint AS unpriced FROM figures GROUP BY 1, 2, 3) z
 GROUP BY z.caller
)
SELECT r.caller::text AS caller, coalesce(min(a.name), '')::text AS name, count(*)::bigint AS runs,
 (count(*) FILTER (WHERE r.failed))::bigint AS failed, coalesce(min(k.j::text), '[]')::jsonb AS costs
FROM runs r LEFT JOIN costs k ON k.caller = r.caller LEFT JOIN project_app a ON a.id = r.caller
GROUP BY r.caller
ORDER BY (r.caller = ''), count(*) DESC, r.caller;
