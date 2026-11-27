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
-- The visible run states of open execution tokens, in token order. A missing run has an empty state.
WITH open AS MATERIALIZED (
  SELECT execution_id, min(seq) AS first_seq FROM run_token
  WHERE revoked_at IS NULL AND step IS NULL AND personal IS NULL AND execution_id IS NOT NULL
  GROUP BY execution_id
)
SELECT o.execution_id::text AS execution_id,
       coalesce((SELECT e.state::text FROM execution e WHERE e.id = o.execution_id AND e.kind IN ('agent_run','host_job')), '')::text AS state
FROM open o ORDER BY o.first_seq;

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
UPDATE execution SET problem= @problem::text WHERE id= @id::text AND (problem IS NULL OR problem='unknown:unreported');

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

-- name: ConnKeyRecordedSince :one
-- The cost recorded for a key's calls since a time (ms).
SELECT coalesce(sum((t.connection_cost->>'amount')::numeric),0)::float8 FROM execution e JOIN execution_totals t ON t.execution_id=e.id
 WHERE e.kind='tool_run' AND e.data->>'keyFingerprint'= @fingerprint::text AND (e.data->>'startedAt')::bigint>= @since::bigint;

-- name: APIConnectionByID :one
SELECT * FROM api_connection WHERE id=$1;

-- name: APIConnectionByIDLocked :one
SELECT * FROM api_connection WHERE id=$1 FOR UPDATE;

-- name: APIConnections :many
SELECT * FROM api_connection ORDER BY seq;

-- name: APIConnectionPut :exec
INSERT INTO api_connection(id, name, provider, holder, project_id, payload)
VALUES (@id, @name, sqlc.narg(provider), sqlc.narg(holder), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 name=excluded.name, provider=excluded.provider, holder=excluded.holder, project_id=excluded.project_id;

-- name: ConnectionKeyByID :one
SELECT * FROM connection_key WHERE id=$1;

-- name: ConnectionKeys :many
SELECT * FROM connection_key
WHERE (sqlc.narg(connection_id)::text IS NULL OR connection_id = sqlc.narg(connection_id)) ORDER BY seq;

-- name: ConnectionKeyPut :exec
-- Writes a connection key without touching state (new rows start active; key_states.go alone moves state).
INSERT INTO connection_key(id, connection_id, fingerprint, added_at, project_id, payload)
VALUES (@id, @connection_id, sqlc.narg(fingerprint), sqlc.narg(added_at), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 connection_id=excluded.connection_id, fingerprint=excluded.fingerprint, added_at=excluded.added_at, project_id=excluded.project_id;

-- name: RunTokenByID :one
SELECT * FROM run_token WHERE id=$1;

-- name: RunTokens :many
SELECT * FROM run_token ORDER BY seq;

-- name: RunTokenPut :exec
INSERT INTO run_token(id, sha256, execution_id, pipeline_run_id, personal, step, directed_by, created_at, revoked_at, director_access_revoked_at, project_id, payload)
VALUES (@id, sqlc.narg(sha256), sqlc.narg(execution_id), sqlc.narg(pipeline_run_id), sqlc.narg(personal), sqlc.narg(step), sqlc.narg(directed_by), sqlc.narg(created_at), sqlc.narg(revoked_at), sqlc.narg(director_access_revoked_at), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 sha256=excluded.sha256, execution_id=excluded.execution_id, pipeline_run_id=excluded.pipeline_run_id, personal=excluded.personal,
 step=excluded.step, directed_by=excluded.directed_by, created_at=excluded.created_at, revoked_at=excluded.revoked_at,
 director_access_revoked_at=excluded.director_access_revoked_at, project_id=excluded.project_id;

-- name: AccessLastCalls :many
-- Per connection, its latest call and its latest call that succeeded (access_status). The calls are chosen in
-- execution_spend by its connection column (index execution_spend_connection_last): under row level security a JSON
-- path is never an index condition (jsonb_object_field_text is not leakproof), so choosing them in execution by
-- data->>'connection' scanned every execution twice per connection (10 s on prod, 2026-11-26). Text and timestamps
-- are leakproof, so each lateral is one backward walk of that connection's rows; the call itself is a primary key read,
-- under the same visibility: execution_spend's policies mirror execution's (directed_spend and directed_execution
-- admit the same projectless runs). A call without a start time has no execution_spend row and is not shown.
SELECT c.name::text AS connection,
 coalesce(e.id, '')::text AS last_id, coalesce(e.state::text, '')::text AS last_state, coalesce(e.problem, '')::text AS last_problem,
 coalesce(e.data->>'tool', '')::text AS last_tool, coalesce((e.data->>'startedAt')::bigint, 0)::bigint AS last_started_at,
 coalesce((k.data->>'startedAt')::bigint, 0)::bigint AS ok_started_at
FROM unnest(@names::text[]) AS c(name)
LEFT JOIN LATERAL (
 SELECT s.execution_id FROM execution_spend s
 WHERE s.connection = c.name AND s.kind = 'tool_run'
 ORDER BY s.started_at DESC, s.execution_id COLLATE "C" DESC LIMIT 1) l ON true
LEFT JOIN LATERAL (
 SELECT s.execution_id FROM execution_spend s
 WHERE s.connection = c.name AND s.kind = 'tool_run' AND s.state = 'succeeded'
 ORDER BY s.started_at DESC, s.execution_id COLLATE "C" DESC LIMIT 1) o ON true
LEFT JOIN execution e ON e.id = l.execution_id
LEFT JOIN execution k ON k.id = o.execution_id;

-- name: AccessConnectionChecked :exec
-- A check's call is a tool_run, which publishes no change; this one change on the connection lets open access_status
-- reads show the check's outcome at once.
WITH published AS (
 INSERT INTO change (kind, id, op, holder) VALUES ('api-connection', @id::text, 'U', sqlc.narg(holder)::text)
 RETURNING id
)
SELECT fleet_enqueue_wake('fleet_change', '') WHERE EXISTS (SELECT 1 FROM published);
