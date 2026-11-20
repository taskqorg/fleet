-- API connections, connection keys and run tokens (connections.go, key_states.go; migration 78). key_states.go alone
-- moves a key's state after the key is added.

-- name: ConnectionKeyStateOf :one
SELECT state FROM connection_key WHERE id=$1 FOR UPDATE;

-- name: ConnectionKeyMove :exec
UPDATE connection_key SET state= @state, updated_at=now() WHERE id= @id;

-- name: DeleteAPIConnection :exec
-- A deleted connection with its keys (foreign key) and its history (audit rows); an audit row records the delete.
WITH c AS (DELETE FROM api_connection WHERE id= @id::text)
DELETE FROM audit_log WHERE target= @id::text;

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

-- name: ConnHasActiveKey :one
-- Whether connection name has an active key.
SELECT EXISTS (SELECT 1 FROM connection_key WHERE connection_id= @name::text AND state='active');

-- name: ConnTakesCalls :one
-- Whether connection name takes calls: its intake is all and it has an active key.
SELECT EXISTS (SELECT 1 FROM api_connection c WHERE c.id='connection:' || @name::text AND c.payload->>'intake'='all'
 AND EXISTS (SELECT 1 FROM connection_key k WHERE k.connection_id= @name::text AND k.state='active'));


-- name: ConnKeyRequestByRequest :one
-- A project's key request by its request id, as the project's questions show it (projects.go projectQuestionJSON).
SELECT text, (jsonb_build_object('id',id,'projectId',project_id,'requestId',request_id,'text',text,'state',CASE WHEN state='open' THEN 'waiting' ELSE state::text END,
 'createdAt',(extract(epoch FROM asked_at)*1000)::bigint,'actor',asked_by) || CASE WHEN state='answered' THEN jsonb_build_object('answer',answer,'answeredAt',(extract(epoch FROM answered_at)*1000)::bigint,'answeredBy',answered_by) ELSE '{}' END)::jsonb AS question
 FROM question WHERE kind='secret' AND project_id= @project_id::text AND request_id= @request_id::text LIMIT 1;

-- name: ConnExecutionSession :one
SELECT session_id FROM execution WHERE id=$1;

-- name: ConnOpenKeyRequests :many
-- The open key requests of a connection.
SELECT id FROM question WHERE kind='secret' AND state='open' AND options->>'connection'= @connection::text ORDER BY seq;

-- name: ConnKeyRequest :one
-- A key request: its state, answer and the connection it names ('' when it names none).
SELECT state::text AS state, coalesce(answer,'')::text AS answer,
 (CASE WHEN jsonb_typeof(options->'connection')='string' THEN options->>'connection' ELSE '' END)::text AS connection
 FROM question WHERE id=$1 AND kind='secret';

-- name: ConnCallPhases :many
-- Call rows already recorded with their transfer phase ('' for a one-line call; upload-unaccounted: a completed upload
-- whose usage is not accounted yet).
SELECT id, (CASE WHEN data->>'phase'='completed' AND data->'transfer'->>'direction'='upload' AND NOT data ? 'uploadAccounted'
 THEN 'upload-unaccounted' ELSE coalesce(data->>'phase','') END)::text AS phase FROM execution WHERE id = ANY(@ids::text[]);

-- name: ConnCallParent :one
-- The lineage a call row takes from the execution it was made for.
SELECT task_id, session_id, directed_by, pipeline_id, pipeline_revision, step_id, profile_id, profile_revision FROM execution WHERE id=$1;

-- name: ConnPersonExists :one
SELECT EXISTS (SELECT 1 FROM person WHERE email=$1);

-- name: ConnCallProblem :exec
-- A failed call's problem, unless one is recorded.
UPDATE execution SET problem= @problem::text WHERE id= @id::text AND problem IS NULL;

-- name: ConnUsageKeyCount :one
-- Other call rows that already counted a usage key.
SELECT count(*) FROM execution WHERE kind='tool_run' AND data->>'usageKey'= @usage_key::text AND id<> @id::text;

-- name: ConnSetUsageKey :exec
UPDATE execution SET data=jsonb_set(data,'{usageKey}',to_jsonb(@usage_key::text)) WHERE id= @id::text;

-- name: ConnTransferComplete :execrows
-- A transfer's completion over its started row (or an upload's completed row not yet accounted): problem, the patch
-- and its end.
UPDATE execution SET problem= sqlc.narg(problem)::text, data=data|| @patch::jsonb,
 ended_at=started_at+make_interval(secs => sqlc.arg(ms)::bigint/1000.0) WHERE id= @id::text AND
 (data->>'phase'='started' OR (@upload::boolean AND data->>'phase'='completed' AND NOT data ? 'uploadAccounted'));

-- name: ConnTransferTotals :exec
-- A completed transfer's measured wall and active time.
UPDATE execution_totals SET wall_ms= @ms::bigint,wall_ms_basis='measured',active_ms= @ms::bigint,active_ms_basis='measured',computed_at=now()
 WHERE execution_id= @id::text AND EXISTS (SELECT 1 FROM execution e WHERE e.id= @id::text AND e.started_at IS NOT NULL);

-- name: ConnExecutionExists :one
SELECT EXISTS (SELECT 1 FROM execution WHERE id=$1);

-- name: ConnRunState :one
-- The state of an agent run or host job; '' when there is none.
SELECT coalesce((SELECT state::text FROM execution WHERE id=$1 AND kind IN ('agent_run','host_job')),'')::text;

-- name: ConnKeyRecordedSince :one
-- The cost recorded for a key's calls since a time (ms).
SELECT coalesce(sum((t.connection_cost->>'amount')::numeric),0)::float8 FROM execution e JOIN execution_totals t ON t.execution_id=e.id
 WHERE e.kind='tool_run' AND e.data->>'keyFingerprint'= @fingerprint::text AND (e.data->>'startedAt')::bigint>= @since::bigint;
