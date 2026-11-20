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
-- A session's payload: the record fields without a column (convTables "session").
SELECT payload FROM session WHERE id = @id;
