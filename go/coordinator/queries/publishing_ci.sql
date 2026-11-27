-- name: CIRequestGet :one
SELECT * FROM publishing_ci_request WHERE id=$1;

-- name: CIRequestInsert :exec
INSERT INTO publishing_ci_request (id, project_id, recovery_id, payload)
VALUES (@id, @project_id, @recovery_id, @payload);

-- name: CIRecoveryInsert :execrows
INSERT INTO publishing_ci_recovery
 (id, project_id, round_key, repo, number, head_sha, run_number, original_attempt, source_execution_id, state, payload)
VALUES (@id, @project_id, @round_key, @repo, @number, @head_sha, @run_number, @original_attempt, @source_execution_id, 'queued', @payload)
ON CONFLICT (round_key) WHERE NOT released DO NOTHING;

-- name: CIRecoveryActive :one
SELECT id FROM publishing_ci_recovery WHERE round_key=$1 AND NOT released;

-- name: CIRecoveryGet :one
SELECT * FROM publishing_ci_recovery WHERE id=$1;

-- name: CIRecoveryList :many
SELECT * FROM publishing_ci_recovery WHERE project_id= @project_id
 AND (sqlc.narg(head_sha)::text IS NULL OR head_sha=sqlc.narg(head_sha))
ORDER BY updated_at DESC, id LIMIT 50;

-- name: CIRecoveryDue :many
SELECT id, attempts FROM publishing_ci_recovery
WHERE state IN ('queued', 'rate_limited', 'dispatch_unknown', 'dispatched', 'observing', 'handoff_pending')
 AND next_attempt_at<=sqlc.arg(due_at)
ORDER BY next_attempt_at LIMIT sqlc.arg(lim);

-- name: CIRecoveryMove :exec
UPDATE publishing_ci_recovery SET state= @state, released= @released, dispatched= @dispatched, handoff=sqlc.narg(handoff),
 payload= @payload, command_id=sqlc.narg(command_id), command_until=sqlc.narg(command_until), updated_at=now()
WHERE id= @id;

-- name: CIRecoverySourceRevoked :one
SELECT EXISTS (SELECT 1 FROM run_token WHERE execution_id=$1
 AND (revoked_at IS NOT NULL OR director_access_revoked_at IS NOT NULL));
