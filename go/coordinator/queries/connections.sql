-- API connections, connection keys and run tokens (connections.go, key_states.go; migration 78). key_states.go alone
-- moves a key's state after the key is added.

-- name: ConnectionKeyStateOf :one
SELECT state FROM connection_key WHERE id=$1 FOR UPDATE;

-- name: ConnectionKeyMove :exec
UPDATE connection_key SET state= @state, updated_at=now() WHERE id= @id;

-- name: DeleteAPIConnection :exec
-- A deleted connection with its keys (foreign key) and its history (setting-change rows); an audit row records the delete.
WITH c AS (DELETE FROM api_connection WHERE id= @id::text)
DELETE FROM document WHERE kind='setting-change' AND data->>'target'= @id::text;

-- name: RevokeExecutionTokens :execrows
-- The open tokens of an execution.
UPDATE run_token SET revoked_at=to_timestamp(sqlc.arg(at)::float8/1000), updated_at=now() WHERE execution_id= @execution_id::text AND revoked_at IS NULL;

-- name: RevokeStepTokensBefore :exec
-- At start no api step call of an earlier process is in flight.
UPDATE run_token SET revoked_at=to_timestamp(sqlc.arg(at)::float8/1000), updated_at=now()
 WHERE step AND revoked_at IS NULL AND coalesce(created_at, 'epoch') < to_timestamp(sqlc.arg(started)::float8/1000);

-- name: OpenRunTokenExecutions :many
-- The executions of open run tokens that are neither a step's nor a person's, in token order.
SELECT execution_id::text FROM run_token WHERE revoked_at IS NULL AND step IS NULL AND personal IS NULL AND execution_id IS NOT NULL
 GROUP BY execution_id ORDER BY min(seq);
