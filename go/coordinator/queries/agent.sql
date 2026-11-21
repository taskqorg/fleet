-- Agent session tools (agent.go): the reads of handoff, transition, ask/reply, worker_done, execution_status,
-- task attach and focus that are not record reads.

-- name: AgentTranscriptArtifacts :many
-- The artifacts of a session's executions, in creation order (a handoff's results).
SELECT a.execution_id, a.name, a.content_hash FROM artifact a
WHERE a.execution_id IN (SELECT e.id FROM execution e WHERE e.session_id = @session_id::text)
ORDER BY a.seq;

-- name: AgentExecutionLink :one
-- An execution's task and project columns.
SELECT task_id, project_id FROM execution WHERE id = @id;

-- name: AgentReadinessData :many
-- The data of an execution's readiness events, in order.
SELECT (payload->'data')::jsonb AS data FROM event WHERE NOT delta AND execution_id = @execution_id::text AND type='readiness' ORDER BY seq;

-- name: AgentSessionHandoffs :many
-- A session's handoffs in order (session_get); the caller leaves out their text.
SELECT id, project_id, state, payload FROM handoff WHERE session_id = @session_id ORDER BY seq;

-- name: AgentTaskProject :one
SELECT id, project_id FROM task WHERE id = @id;

-- name: AgentTaskHeld :one
SELECT EXISTS(SELECT 1 FROM task WHERE id = @id AND pause_reason='held');

-- name: AgentSessionOtherTask :one
-- An execution of the session already bound to a task other than @task_id.
SELECT EXISTS(SELECT 1 FROM execution WHERE session_id = @session_id::text AND task_id IS NOT NULL AND task_id <> @task_id::text);

-- name: AgentSessionActive :one
-- Work in flight for a session: an open execution of it, of a delegation it made or of a pipeline run it started, or a
-- queued or steering message.
SELECT EXISTS (
 SELECT 1 FROM execution e WHERE e.state IN ('preparing','running','waiting')
 AND (e.session_id = @session_id::text OR EXISTS (SELECT 1 FROM execution p WHERE p.session_id = @session_id::text AND (
  (e.delegation IS NOT NULL AND e.parent_execution_id=p.id) OR
  (p.kind='pipeline_run' AND e.data->>'pipelineRunId'=p.id)
 )))
) OR EXISTS (
 SELECT 1 FROM queued_message WHERE session_id = @session_id::text AND state IN ('queued','steering')
) AS active;

-- name: AgentProjectLive :one
SELECT EXISTS(SELECT 1 FROM definition WHERE kind='project' AND id = @id AND retired_at IS NULL);

-- name: AgentSessionPayload :one
-- A session's payload: the record fields without a column (session_resume).
SELECT payload FROM session WHERE id = @id;

-- name: QueuedMessages :many
SELECT * FROM queued_message
WHERE (sqlc.narg(session_id)::text IS NULL OR session_id = sqlc.narg(session_id))
 AND (sqlc.narg(request_id)::text IS NULL OR request_id = sqlc.narg(request_id))
 AND (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state))
 AND (sqlc.narg(steer_command_id)::text IS NULL OR steer_command_id = sqlc.narg(steer_command_id))
ORDER BY seq;

-- name: QueuedMessagePut :exec
INSERT INTO queued_message(id, session_id, project_id, task_id, request_id, text, state, execution_id, steer_command_id, sent_by, source, created_at, payload)
VALUES (@id, @session_id, sqlc.narg(project_id), sqlc.narg(task_id), sqlc.narg(request_id), sqlc.narg(text), sqlc.narg(state), sqlc.narg(execution_id), sqlc.narg(steer_command_id), sqlc.narg(sent_by), sqlc.narg(source), sqlc.narg(created_at), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 session_id=excluded.session_id, project_id=excluded.project_id, task_id=excluded.task_id, request_id=excluded.request_id,
 text=excluded.text, state=excluded.state, execution_id=excluded.execution_id, steer_command_id=excluded.steer_command_id,
 sent_by=excluded.sent_by, source=excluded.source, created_at=excluded.created_at;

-- name: HandoffByID :one
SELECT * FROM handoff WHERE id=$1;

-- name: HandoffsByState :many
SELECT * FROM handoff WHERE state = @state::text ORDER BY seq;

-- name: HandoffPut :exec
INSERT INTO handoff(id, session_id, project_id, state, payload)
VALUES (@id, @session_id, sqlc.narg(project_id), sqlc.narg(state), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 session_id=excluded.session_id, project_id=excluded.project_id, state=excluded.state;

-- name: SessionForks :many
SELECT * FROM session_fork
WHERE source_session_id = @source_session_id
 AND (sqlc.narg(request_id)::text IS NULL OR request_id = sqlc.narg(request_id))
ORDER BY seq;

-- name: SessionForkPut :exec
INSERT INTO session_fork(id, session_id, source_session_id, project_id, request_id, payload)
VALUES (@id, @session_id, @source_session_id, sqlc.narg(project_id), sqlc.narg(request_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 session_id=excluded.session_id, source_session_id=excluded.source_session_id, project_id=excluded.project_id, request_id=excluded.request_id;

-- name: SessionTransitions :many
SELECT * FROM session_transition WHERE session_id = @session_id ORDER BY seq;

-- name: SessionTransitionPut :exec
INSERT INTO session_transition(id, session_id, project_id, payload)
VALUES (@id, @session_id, sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 session_id=excluded.session_id, project_id=excluded.project_id;

-- name: WorkerDonePut :exec
INSERT INTO worker_done(id, execution_id, project_id, payload)
VALUES (@id, sqlc.narg(execution_id), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 execution_id=excluded.execution_id, project_id=excluded.project_id;
