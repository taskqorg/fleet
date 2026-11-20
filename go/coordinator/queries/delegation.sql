-- Delegation website sources read on the work kit (delegation_work.go, docs/designs/fleet-standard.md S6).

-- name: DelegationSourceWorkDue :many
SELECT delegation_id,attempts FROM delegation_source_work WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz ORDER BY next_attempt_at,delegation_id LIMIT sqlc.arg(lim);

-- name: DelegationSourceWorkEnsure :execrows
-- The files to read for a delegation; nothing when they are already asked for.
INSERT INTO delegation_source_work(delegation_id,payload) VALUES($1,$2) ON CONFLICT DO NOTHING;

-- name: DelegationSourceWorkFiles :one
SELECT payload FROM delegation_source_work WHERE delegation_id=$1;

-- name: DelegationSourceWorkDelete :exec
DELETE FROM delegation_source_work WHERE delegation_id=$1;

-- name: DelegationParentTaskCancelled :one
-- Whether the task of a delegation's parent execution (its own, else its session's) is cancelled.
SELECT EXISTS(SELECT 1 FROM execution p LEFT JOIN session s ON s.id=p.session_id JOIN task t ON t.id=coalesce(p.task_id,s.task_id)
 WHERE p.id=$1 AND t.state='cancelled')::boolean AS cancelled;

-- name: DelegationExecutionsWithArtifacts :many
-- Of ids, the executions with a stored artifact (a typed row or a legacy artifact record).
SELECT DISTINCT execution_id::text AS id FROM artifact WHERE state='stored' AND execution_id=ANY(sqlc.arg(ids)::text[])
UNION SELECT DISTINCT (data->>'executionId')::text FROM document WHERE kind='artifact' AND data->>'executionId'=ANY(sqlc.arg(ids)::text[]);
