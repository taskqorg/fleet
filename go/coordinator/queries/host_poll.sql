-- Host polls, their health and receipts (host_poll.go, host_poll_health.go, host_receipts.go): the host's observation, its
-- recorded poll failure and the execution row locks a poll and a receipt take.

-- name: HostPollErrorOf :one
-- The observation's recorded poll failure (JSON null when none); no row when the host was never observed.
SELECT coalesce(payload->'pollError', 'null'::jsonb)::jsonb AS poll_error FROM host_observation WHERE id=$1;

-- name: HostPollErrorSet :exec
UPDATE host_observation SET payload=payload || jsonb_build_object('pollError', @poll_error::jsonb), updated_at=now() WHERE id= @id::text;

-- name: HostPollLockExecution :one
-- The execution row lock a poll shares with stop, taken only when free (a busy row is retried by the next poll).
SELECT EXISTS(SELECT 1 FROM execution WHERE id=$1 FOR UPDATE SKIP LOCKED);

-- name: HostPollClearWaitReason :exec
UPDATE execution SET wait_reason=NULL WHERE id=$1;

-- name: HostReceiptSessions :many
-- The sessions a receipt's execution writes to: its own and the one that created it (host_receipts.go).
SELECT DISTINCT sid::text FROM (
 SELECT session_id AS sid FROM execution WHERE id= @execution_id::text
 UNION SELECT s.created_by_session_id FROM execution e JOIN session s ON s.id=e.session_id WHERE e.id= @execution_id::text
) x WHERE coalesce(sid,'')<>'';

-- name: HostReceiptLockExecution :exec
-- executionStop's row lock: an overlapping started or accepted receipt cannot overwrite a durable stop request.
SELECT 1 FROM execution WHERE id=$1 FOR UPDATE;
