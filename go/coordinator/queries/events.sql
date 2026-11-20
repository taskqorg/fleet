-- Execution events (host_events.go, event_batch.go, docs/designs/fleet-standard.md S2): an event and a stream delta share
-- one table, told apart by delta. The record a reader sees is payload with the columns under their record names
-- (eventRecordOf); a write splits a record the same way (eventColumnsOf), so the generic record API (records.go) reads it alike.

-- name: EventsByIdentity :many
-- The stored events and deltas of an execution at one of @seqs or with one of @ids (a host batch's snapshot).
SELECT * FROM event WHERE execution_id= @execution_id AND (event_seq = ANY (@seqs::bigint[]) OR id = ANY (@ids::text[]));

-- name: EventsClosedByIdentity :many
-- The compact identities of closed deltas at one of @seqs or with one of @ids.
SELECT id, event_seq, digest, event_type FROM closed_delta_identity
WHERE execution_id= @execution_id AND (event_seq = ANY (@seqs::bigint[]) OR id = ANY (@ids::text[]));

-- name: EventsClosedDeltas :many
-- The deltas of an execution whose block a stored complete event closes.
SELECT d.* FROM event d WHERE d.delta AND d.execution_id= @execution_id AND EXISTS (
 SELECT 1 FROM event c WHERE NOT c.delta AND c.execution_id= @execution_id
  AND c.payload->'data'->'fleetNormalized'->'closes' ? (d.payload->'data'->'fleetNormalized'->>'deltaOf'));

-- name: EventsClosedIdentityInsert :exec
INSERT INTO closed_delta_identity(id, execution_id, event_seq, digest, event_type)
SELECT unnest(@ids::text[]), @execution_id::text, unnest(@seqs::bigint[]), unnest(@digests::text[]), unnest(@types::text[])
ON CONFLICT (id) DO NOTHING;

-- name: EventsDeltasDelete :exec
DELETE FROM event WHERE delta AND id = ANY (@ids::text[]);

-- name: EventsInsert :exec
-- A host's new events in one statement; an event already stored stays as it is. @rows: a JSON array of eventColumns
-- (event_batch.go), at in epoch milliseconds.
INSERT INTO event(id, delta, execution_id, session_id, project_id, event_seq, type, at, payload)
SELECT r->>'id', (r->>'delta')::boolean, r->>'executionId', r->>'sessionId', r->>'projectId', (r->>'seq')::bigint, r->>'type',
 to_timestamp((r->>'at')::float8/1000), r->'payload'
FROM jsonb_array_elements(@rows::jsonb) r
ON CONFLICT (id) DO NOTHING;

-- name: EventsOfExecution :many
-- An execution's events (not deltas) in sequence order, without its ended marker (GET /api/executions/{id}/events).
SELECT * FROM event WHERE NOT delta AND execution_id=$1 AND type IS DISTINCT FROM 'execution.ended' ORDER BY event_seq, seq;

-- name: EventsAllOfExecution :many
-- An execution's events and deltas in write order.
SELECT * FROM event WHERE execution_id=$1 ORDER BY seq;

-- name: EventByID :one
SELECT * FROM event WHERE id=$1;

-- name: EventsAtSeq :many
-- The events and deltas of an execution at sequence @seq.
SELECT * FROM event WHERE execution_id= @execution_id AND event_seq= @seq ORDER BY seq;

-- name: EventsExecution :one
-- The execution a host batch belongs to: its record remainder and state (an agent run or host job).
SELECT data, state::text AS state FROM execution WHERE id=$1 AND kind IN ('agent_run','host_job');

-- name: EventsExecutionProblem :exec
-- The first cause wins: a later event does not replace the problem an execution already records.
UPDATE execution SET problem= @problem WHERE id= @id AND problem IS NULL;

-- name: EventsTransportConflict :one
SELECT data FROM document WHERE kind='transport-conflict' AND id=$1;

-- name: EventsTransportConflicts :many
SELECT data FROM document WHERE kind='transport-conflict' AND data->>'executionId'= @execution_id::text ORDER BY seq;

-- name: EventsTransportAudits :many
-- The transport audit of an execution, or with one request id (each filter optional).
SELECT data FROM document WHERE kind='transport-audit'
 AND (sqlc.narg(execution_id)::text IS NULL OR data->>'executionId'=sqlc.narg(execution_id))
 AND (sqlc.narg(request_id)::text IS NULL OR data->>'requestId'=sqlc.narg(request_id))
ORDER BY seq;

-- name: EventsTransportPut :exec
-- A transport conflict or audit record (no project: they belong to an execution's transport).
INSERT INTO document(kind, id, data, project_id) VALUES (@kind, @id, @data, NULL)
ON CONFLICT (id) DO UPDATE SET data=excluded.data, project_id=excluded.project_id, updated_at=now() WHERE document.kind=excluded.kind;

-- name: EventsCountOfExecution :one
-- How many events and deltas an execution has stored.
SELECT count(*)::bigint AS n FROM event WHERE execution_id=$1;
