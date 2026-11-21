-- Execution events (host_events.go, event_batch.go, docs/designs/fleet-standard.md S2): an event and a stream delta share
-- one table, told apart by delta. The record a reader sees is payload with the columns under their record names
-- (eventRecordOf); a write splits a record the same way (eventColumnsOf), so its record (records.go recEvent) reads alike.

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

-- Transport conflicts and their audit (migration 82): a record is the row as transport_inspect shows it.

-- name: EventsTransportConflict :one
SELECT (jsonb_build_object('id', id, 'executionId', coalesce(execution_id, ''), 'hostId', coalesce(host_id, ''), 'originalEventId', coalesce(original_event_id, ''),
 'originalSequence', coalesce(original_sequence, 0), 'frameSha256', coalesce(frame_sha256, ''), 'original', payload, 'collisionEventId', coalesce(collision_event_id, ''),
 'supported', supported, 'state', state, 'observedAt', coalesce(observed_at, 0))
 || jsonb_strip_nulls(jsonb_build_object('reservedSequence', reserved_sequence, 'authorizedAt', authorized_at, 'deliveredAt', delivered_at)))::jsonb FROM transport_conflict WHERE id=$1;

-- name: EventsTransportConflicts :many
SELECT (jsonb_build_object('id', id, 'executionId', coalesce(execution_id, ''), 'hostId', coalesce(host_id, ''), 'originalEventId', coalesce(original_event_id, ''),
 'originalSequence', coalesce(original_sequence, 0), 'frameSha256', coalesce(frame_sha256, ''), 'original', payload, 'collisionEventId', coalesce(collision_event_id, ''),
 'supported', supported, 'state', state, 'observedAt', coalesce(observed_at, 0))
 || jsonb_strip_nulls(jsonb_build_object('reservedSequence', reserved_sequence, 'authorizedAt', authorized_at, 'deliveredAt', delivered_at)))::jsonb FROM transport_conflict WHERE execution_id= @execution_id::text ORDER BY seq;

-- name: EventsTransportAudits :many
-- The transport audit of an execution, or with one request id (each filter optional).
SELECT (jsonb_build_object('id', id, 'executionId', coalesce(execution_id, ''), 'conflictId', coalesce(conflict_id, ''), 'actor', actor, 'operation', operation,
 'at', coalesce(at, 0), 'nativeEffectReplayed', native_effect_replayed) || jsonb_strip_nulls(jsonb_build_object('requestId', request_id, 'signature', signature)))::jsonb FROM transport_audit
WHERE (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id))
 AND (sqlc.narg(request_id)::text IS NULL OR request_id=sqlc.narg(request_id))
ORDER BY seq;

-- name: EventsTransportConflictPut :exec
-- A transport conflict record (no project: it belongs to an execution's transport).
INSERT INTO transport_conflict(id, execution_id, host_id, original_event_id, original_sequence, frame_sha256, payload, collision_event_id, supported, state,
 observed_at, reserved_sequence, authorized_at, delivered_at)
SELECT d->>'id', nullif(d->>'executionId', ''), nullif(d->>'hostId', ''), nullif(d->>'originalEventId', ''), (d->>'originalSequence')::bigint, nullif(d->>'frameSha256', ''),
 d->'original', nullif(d->>'collisionEventId', ''), coalesce((d->>'supported')::boolean, false), coalesce(d->>'state', ''), (d->>'observedAt')::bigint,
 (d->>'reservedSequence')::float8, (d->>'authorizedAt')::bigint, (d->>'deliveredAt')::bigint
FROM (SELECT @data::jsonb AS d) x
ON CONFLICT (id) DO UPDATE SET execution_id=excluded.execution_id, host_id=excluded.host_id, original_event_id=excluded.original_event_id,
 original_sequence=excluded.original_sequence, frame_sha256=excluded.frame_sha256, payload=excluded.payload, collision_event_id=excluded.collision_event_id,
 supported=excluded.supported, state=excluded.state, observed_at=excluded.observed_at, reserved_sequence=excluded.reserved_sequence,
 authorized_at=excluded.authorized_at, delivered_at=excluded.delivered_at, updated_at=now();

-- name: EventsTransportAuditPut :exec
-- A transport audit record (kept: it outlives the execution).
INSERT INTO transport_audit(id, execution_id, conflict_id, request_id, signature, actor, operation, at, native_effect_replayed)
SELECT d->>'id', nullif(d->>'executionId', ''), nullif(d->>'conflictId', ''), nullif(d->>'requestId', ''), nullif(d->>'signature', ''), coalesce(d->>'actor', ''),
 coalesce(d->>'operation', ''), (d->>'at')::bigint, coalesce((d->>'nativeEffectReplayed')::boolean, false)
FROM (SELECT @data::jsonb AS d) x
ON CONFLICT (id) DO NOTHING;

-- name: EventsCountOfExecution :one
-- How many events and deltas an execution has stored.
SELECT count(*)::bigint AS n FROM event WHERE execution_id=$1;
