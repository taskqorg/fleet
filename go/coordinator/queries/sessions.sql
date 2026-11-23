-- Sessions and their agent tool receipts (sessions.go, agent_receipts.go, agents.go).

-- name: ReceiptOf :one
-- An agent tool request identity the executor journaled, committed with its mutation (agent_receipts.go).
SELECT execution_id, directing_email, operation, payload_sha, result, authority FROM agent_tool_receipt WHERE id=$1;

-- name: ReceiptInsert :exec
INSERT INTO agent_tool_receipt(id, execution_id, directing_email, operation, payload_sha, result, authority)
VALUES (@id, @execution_id, @directing_email, @operation, @payload_sha, @result, @authority);

-- name: AgentsCompatibilityRuns :many
-- The latest execution in 30 days of each (harness, version, model, effort) combination run from agent definition @id
-- (agents.go agentCompatibility); @defs is [{"kind":"agent","id":@id}]. effort and revision are their JSON values (NULL: absent).
SELECT DISTINCT ON (coalesce(e.harness,'codex'),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''))
  coalesce(e.harness,'codex')::text AS harness, e.harness_version, coalesce(e.model,'')::text AS model, (e.data->'effort')::jsonb AS effort,
  e.state::text AS state, e.problem, e.id, coalesce(e.ended_at,e.started_at,e.updated_at)::text AS at, (d->'revision')::jsonb AS revision
FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
WHERE e.definitions @> @defs::jsonb AND d->>'kind'='agent' AND d->>'id'= @id::text AND coalesce(e.started_at,e.updated_at) >= now() - interval '30 days'
ORDER BY coalesce(e.harness,'codex'),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''),e.seq DESC;

-- name: SessionCodeAndState :one
SELECT d.code, (SELECT state FROM fleet_conversation_state(d.id))::text AS state FROM session d WHERE d.id = @id;

-- name: TaskSessionIDs :many
-- The sessions a task's executions ran in (deliveredTeardown).
SELECT DISTINCT session_id::text FROM execution WHERE task_id = @task_id AND session_id IS NOT NULL;

-- name: SessionDelegations :many
-- The delegations a session takes part in, as parent or child (sessionGet).
SELECT delegation FROM execution WHERE delegation IS NOT NULL
  AND (delegation->>'parentSessionId' = @session_id::text OR delegation->>'childSessionId' = @session_id::text) ORDER BY seq;

-- name: ExecutionDelegations :many
-- The delegations an execution takes part in, as parent, child or continuation.
SELECT delegation FROM execution WHERE delegation IS NOT NULL
  AND (delegation->>'parentExecutionId' = @execution_id::text OR delegation->>'childExecutionId' = @execution_id::text
   OR delegation->>'continuationExecutionId' = @execution_id::text) ORDER BY seq;

-- name: SessionByID :one
SELECT * FROM session WHERE id=$1;

-- name: SessionByIDLocked :one
SELECT * FROM session WHERE id=$1 FOR UPDATE;

-- name: SessionsByStartRequest :many
SELECT * FROM session WHERE payload->>'startRequest' = @start_request::text ORDER BY seq;

-- name: SessionPut :exec
INSERT INTO session(id, project_id, task_id, host_id, actor, directed_by, director_access_revoked_at,
 parent_execution_id, created_by_execution_id, created_by_session_id, source_session_id, pipeline_run_id,
 created_at, archived_at, title, title_by, title_call, title_attempt_at, title_attempts, title_unknown_call, payload)
VALUES (@id, sqlc.narg(project_id), sqlc.narg(task_id), sqlc.narg(host_id), sqlc.narg(actor), sqlc.narg(directed_by),
 sqlc.narg(director_access_revoked_at), sqlc.narg(parent_execution_id), sqlc.narg(created_by_execution_id),
 sqlc.narg(created_by_session_id), sqlc.narg(source_session_id), sqlc.narg(pipeline_run_id),
 sqlc.narg(created_at), sqlc.narg(archived_at), sqlc.narg(title), sqlc.narg(title_by), sqlc.narg(title_call),
 sqlc.narg(title_attempt_at), @title_attempts, sqlc.narg(title_unknown_call), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 project_id=excluded.project_id,
 task_id=excluded.task_id,
 host_id=excluded.host_id,
 actor=excluded.actor,
 directed_by=excluded.directed_by,
 director_access_revoked_at=excluded.director_access_revoked_at,
 parent_execution_id=excluded.parent_execution_id,
 created_by_execution_id=excluded.created_by_execution_id,
 created_by_session_id=excluded.created_by_session_id,
 source_session_id=excluded.source_session_id,
 pipeline_run_id=excluded.pipeline_run_id,
 created_at=excluded.created_at,
 archived_at=excluded.archived_at,
 title=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title ELSE NULL END,
 title_by=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_by ELSE NULL END,
 title_call=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_call ELSE NULL END,
 title_attempt_at=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_attempt_at ELSE NULL END,
 title_attempts=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_attempts ELSE 0 END,
 title_unknown_call=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_unknown_call ELSE NULL END;

-- name: Messages :many
SELECT * FROM message
WHERE session_id = @session_id
 AND (sqlc.narg(request_id)::text IS NULL OR request_id = sqlc.narg(request_id))
ORDER BY seq;

-- name: MessagePut :exec
INSERT INTO message(id, session_id, execution_id, project_id, request_id, text, sent_by, source, payload)
VALUES (@id, @session_id, sqlc.narg(execution_id), sqlc.narg(project_id), sqlc.narg(request_id), sqlc.narg(text), sqlc.narg(sent_by), sqlc.narg(source), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 session_id=excluded.session_id, execution_id=excluded.execution_id, project_id=excluded.project_id,
 request_id=excluded.request_id, text=excluded.text, sent_by=excluded.sent_by, source=excluded.source;
