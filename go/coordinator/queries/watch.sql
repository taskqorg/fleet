-- Watches (watch.go; migration 78): a session's wake subscription and the reads of one watch pass.

-- name: WatchIDs :many
SELECT id FROM watch ORDER BY seq;

-- name: WatchSetError :exec
UPDATE watch SET payload = payload || jsonb_build_object('lastError', @error::text) WHERE id = @id::text;

-- name: DeleteWatch :exec
DELETE FROM watch WHERE id=$1;

-- name: TaskCancelled :one
SELECT EXISTS (SELECT 1 FROM task WHERE id=$1 AND state='cancelled');

-- name: WatchWakeQueued :one
-- A watch or timer wake of the session still queued.
SELECT EXISTS (SELECT 1 FROM queued_message WHERE session_id=$1 AND state='queued' AND (request_id LIKE 'watch:%' OR request_id LIKE 'timer:%'));

-- name: SessionExecutionIDs :many
SELECT id FROM execution WHERE session_id=$1;

-- name: ChildSessionIDs :many
SELECT id FROM session WHERE created_by_session_id=$1;

-- name: TaskWatchState :one
SELECT state::text AS state, pause_reason FROM task WHERE id=$1;

-- name: OpenQuestionTask :one
SELECT coalesce(task_id, '')::text AS task_id FROM question WHERE id=$1 AND state='open';

-- name: ExecutionWatchState :one
SELECT (CASE WHEN state='failed' THEN 'failed' ELSE 'unknown' END)::text AS s FROM execution WHERE id=$1 AND (state='failed' OR observation='unconfirmed');

-- name: WatchRecordOf :one
-- A watch row: session and project are columns, payload the other record fields (typed_records.go).
SELECT session_id, project_id, payload FROM watch WHERE id=$1;

-- name: WatchPut :exec
INSERT INTO watch(id, session_id, project_id, payload) VALUES(@id::text, @session_id::text, sqlc.narg(project_id)::text, @payload::jsonb)
 ON CONFLICT(id) DO UPDATE SET session_id=excluded.session_id, project_id=excluded.project_id, payload=excluded.payload, updated_at=now();

-- name: WatchSession :one
-- The session fields a watch reads: its project, task, director and payload (deliveryId, focus).
SELECT project_id, task_id, directed_by, payload FROM session WHERE id=$1;
