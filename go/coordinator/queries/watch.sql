-- The reads of a trigger's change check (watch.go, triggers.go).

-- name: TaskCancelled :one
SELECT EXISTS (SELECT 1 FROM task WHERE id=$1 AND state='cancelled');

-- name: SessionExecutionIDs :many
SELECT id FROM execution WHERE session_id=$1;

-- name: ChildSessionIDs :many
SELECT id FROM session WHERE created_by_session_id=$1;

-- name: TaskWatchState :one
SELECT state::text AS state, pause_reason FROM task WHERE id=$1;

-- name: OpenQuestionTask :one
SELECT coalesce(task_id, '')::text AS task_id FROM question WHERE id=$1 AND state='open';

-- name: ExecutionWatchState :one
-- A failed or unconfirmed run, as the Needs you list counts it: not dismissed, not cancelled or abandoned, and not a
-- pipeline step whose run failed already (the run is the one entry).
SELECT (CASE WHEN state='failed' THEN 'failed' ELSE 'unknown' END)::text AS s FROM execution WHERE execution.id=$1 AND (state='failed' OR observation='unconfirmed')
 AND kind <> 'tool_run' AND coalesce(problem, '') NOT IN ('unknown:abandoned', 'cancelled')
 AND NOT EXISTS (SELECT 1 FROM problem_dismissal x WHERE x.execution_id = execution.id)
 AND NOT (kind = 'pipeline_step' AND EXISTS (SELECT 1 FROM execution r WHERE r.kind = 'pipeline_run' AND r.problem IS NOT NULL
   AND r.id = coalesce(execution.parent_execution_id, split_part(execution.id, ':step:', 1))));

-- name: WatchSession :one
-- The session fields a watch reads: its project, task, director and payload (deliveryId, focus).
SELECT project_id, task_id, directed_by, payload FROM session WHERE id=$1;
