-- Host commands (commands.go, docs/designs/fleet-standard.md S5): commandTransition alone moves a command's status.

-- name: CommandUpsert :exec
-- A new command starts with its status; a put of an existing one changes everything but its status and stop detail.
INSERT INTO command(id, execution_id, session_id, host_id, project_id, kind, status, stop_detail, handed_out, traceparent, payload)
VALUES (@id, @execution_id, @session_id, @host_id, @project_id, @kind, @status, @stop_detail, @handed_out, @traceparent, @payload)
ON CONFLICT (id) DO UPDATE SET execution_id=excluded.execution_id, session_id=excluded.session_id, host_id=excluded.host_id,
 project_id=excluded.project_id, kind=excluded.kind, handed_out=excluded.handed_out, traceparent=excluded.traceparent,
 payload=excluded.payload, updated_at=now();

-- name: CommandStatusOf :one
SELECT status FROM command WHERE id=$1 FOR UPDATE;

-- name: CommandMove :exec
UPDATE command SET status = @status, stop_detail=coalesce(sqlc.narg(stop_detail), stop_detail), updated_at=now() WHERE id = @id;

-- name: CommandsStopOpen :many
-- Stop the open commands of an execution, a host or one command (each filter optional; at least one is given): those
-- in one of the given statuses, with optional kinds. Returns the stopped ids.
UPDATE command SET status='stopped', stop_detail=coalesce(sqlc.narg(stop_detail), stop_detail), updated_at=now()
WHERE status = ANY (CAST(sqlc.arg(statuses)::text[] AS command_status[]))
 AND (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id))
 AND (sqlc.narg(host_id)::text IS NULL OR host_id=sqlc.narg(host_id))
 AND (sqlc.narg(id)::text IS NULL OR id=sqlc.narg(id))
 AND (sqlc.narg(kinds)::text[] IS NULL OR kind = ANY (sqlc.narg(kinds)::text[]))
RETURNING id;

-- name: CommandHandOut :exec
-- The coordinator's delivery fence: the command reached a poll reply.
UPDATE command SET handed_out=true, updated_at=now() WHERE id=$1 AND handed_out=false;

-- name: CommandsPending :many
SELECT id FROM command WHERE host_id=$1 AND status='pending' ORDER BY seq;

-- name: CommandsPendingCount :one
SELECT count(*)::int AS n, coalesce(bool_or(e.data ? 'pipelineRunId' AND c.kind = ANY (@kinds::text[])), false)::boolean AS pipeline
FROM command c LEFT JOIN execution e ON e.id=c.execution_id WHERE c.host_id = @host_id AND c.status='pending';

-- name: CommandsPendingByHost :many
SELECT host_id::text AS host_id, count(*)::int AS n FROM command WHERE status='pending' AND host_id IS NOT NULL GROUP BY host_id;
