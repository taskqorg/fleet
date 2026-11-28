-- name: DesignVerifierLatest :many
SELECT DISTINCT ON (field) field, at, value
FROM audit_log
WHERE project_id = 'fleet' AND target = 'design-verifier:taskqorg/fleet' AND actor = 'fleet-design-verifier'
ORDER BY field, seq DESC;

-- name: DesignVerifierPublishingSources :many
-- Managed executor observations, fenced to the upstream PR's exact original SHA and branch.
SELECT DISTINCT e.id AS execution, COALESCE(e.task_id, s.task_id, '')::text AS task
FROM audit_log a
JOIN execution e ON e.id = a.value->>'sourceExecutionId'
LEFT JOIN session s ON s.id = e.session_id AND s.project_id = 'fleet' AND s.tombstoned_at IS NULL
WHERE a.project_id = 'fleet' AND a.target = 'publishing:taskqorg/fleet'
  AND a.field IN ('publish_draft', 'status', 'mark_ready', 'request_merge')
  AND a.value->>'commandId' <> '' AND COALESCE(a.value->>'error', '') = ''
  AND a.value->>'provider' = 'github' AND a.value->>'host' = 'github.com'
  AND a.value->>'kind' = 'pull_request' AND a.value->>'state' = 'OPEN' AND a.value->>'baseBranch' = 'main'
  AND a.value->>'number' = @number::text AND a.value->>'branch' = @branch::text AND a.value->>'headSha' = @head::text
  AND e.project_id = 'fleet' AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL;
