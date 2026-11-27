-- name: RulesCleanupFailing :many
-- cleanup-failing (docs/designs/fleet-host-cleanup.md#13-rules): teardowns of the last day that failed to remove a
-- worktree (a failed teardown script or git worktree remove), per host.
SELECT coalesce(e.host_id, '')::text AS host_id, count(*)::bigint AS n
FROM event ev JOIN execution e ON e.id = ev.execution_id
WHERE ev.type = 'teardown' AND ev.at >= now() - interval '1 day'
  AND ev.payload->'data'->>'reason' IN ('teardown-failed', 'worktree-remove-failed')
GROUP BY 1;

-- name: RulesRunFrequency :many
-- run-frequency (docs/designs/fleet-host-cleanup.md#13-rules): executions started in the last hour by one pipeline or
-- bot, with the median of its hourly counts over the 7 days before (hours with runs only).
WITH runs AS (
  SELECT coalesce('pipeline:' || pipeline_id, 'bot:' || bot_id) AS subject, started_at FROM execution
  WHERE started_at >= now() - interval '7 days 1 hour' AND (pipeline_id IS NOT NULL OR bot_id IS NOT NULL)
    AND parent_execution_id IS NULL
), hours AS (
  SELECT subject, date_trunc('hour', started_at) AS h, count(*) AS n FROM runs
  WHERE started_at < now() - interval '1 hour' GROUP BY 1, 2
), base AS (
  SELECT subject, percentile_cont(0.5) WITHIN GROUP (ORDER BY n) AS median FROM hours GROUP BY 1
)
SELECT r.subject::text AS subject, count(*)::bigint AS n, coalesce(max(b.median), 0)::float8 AS median
FROM runs r LEFT JOIN base b ON b.subject = r.subject
WHERE r.started_at >= now() - interval '1 hour'
GROUP BY r.subject;

-- name: RulesInventories :many
-- leftovers and personal-output (docs/designs/fleet-host-cleanup.md#13-rules): each host's latest inventory.
SELECT id::text AS host_id, (payload->'inventory')::jsonb AS inventory FROM host_observation WHERE payload ? 'inventory';

-- name: RulesPersonalScriptFailures :many
-- Personal script jobs whose staging cleanup or original artifact upload failed.
SELECT e.project_id::text AS project_id, count(*)::bigint AS n
FROM execution e
WHERE e.kind='host_job' AND e.session_id IS NULL AND e.project_id IS NOT NULL
  AND e.ended_at IS NOT NULL AND e.tombstoned_at IS NULL
  AND EXISTS (
    SELECT 1 FROM command c WHERE c.execution_id=e.id AND c.kind='script' AND c.session_id IS NULL
      AND c.payload->'payload'->'project'->>'data'='personal'
  )
  AND (
    (jsonb_typeof(e.data->'refusedArtifacts')='array' AND e.data->'refusedArtifacts'<>'[]'::jsonb)
    OR EXISTS (
      SELECT 1 FROM artifact a WHERE a.execution_id=e.id AND a.role='original'
        AND a.state='failed' AND a.tombstoned_at IS NULL
    )
    OR EXISTS (
      SELECT 1 FROM event ev WHERE ev.execution_id=e.id AND ev.type='script.personal-cleanup' AND NOT ev.delta
        AND (ev.payload->'data'->>'removed'='false' OR ev.payload->'data'->>'collectionFailed'='true')
    )
  )
GROUP BY e.project_id;
