-- Hosts and their observations (hosts.go, host_remove.go, host_states.go; migration 78). host_states.go alone moves a
-- host's state after the host is registered.

-- name: HostStateOf :one
SELECT state FROM host WHERE id=$1 FOR UPDATE;

-- name: HostMove :exec
UPDATE host SET state= @state, updated_at=now() WHERE id= @id;

-- name: RemovedHostIDs :many
SELECT host_id FROM host WHERE state='removed' ORDER BY seq;

-- name: HostRemovedByHostID :one
SELECT EXISTS (SELECT 1 FROM host WHERE host_id=$1 AND state='removed');

-- name: HostKnown :one
-- A host record or an observation names the host.
SELECT EXISTS (SELECT 1 FROM host WHERE host_id= @host_id::text UNION ALL SELECT 1 FROM host_observation WHERE id= @host_id::text);

-- name: HostRemovedSecretCount :one
SELECT count(*) FROM host WHERE removed_secret_hash=$1;

-- name: HostAutoUpdate :one
SELECT (payload->>'autoUpdate')::text FROM host WHERE id=$1;

-- name: DispatchableHostIDs :many
-- Hosts that take work: not removed, intake not paused.
SELECT host_id FROM host WHERE state<>'removed' AND coalesce(intake,'all')<>'paused' ORDER BY seq;

-- name: PurgeableHosts :many
-- Removed hosts and the first table that still names each (a removed host stays while any row names it).
SELECT h.host_id, (CASE
 WHEN EXISTS (SELECT 1 FROM execution e WHERE e.host_id=h.host_id) THEN 'execution.host_id'
 WHEN EXISTS (SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE s.host_id=h.host_id AND c.state<>'deleted') THEN 'artifact_copy on its store'
 WHEN EXISTS (SELECT 1 FROM route_decision r WHERE r.host_id=h.host_id) THEN 'route_decision.host_id'
 WHEN EXISTS (SELECT 1 FROM session x WHERE x.host_id=h.host_id) THEN 'session.host_id'
 WHEN EXISTS (SELECT 1 FROM command x WHERE x.host_id=h.host_id) THEN 'command.host_id' ELSE '' END)::text AS referenced
 FROM host h WHERE h.state='removed' ORDER BY h.seq;

-- name: HostRemoveCounts :one
SELECT
 (SELECT count(*) FROM execution WHERE host_id= @host_id::text AND state IN ('preparing','running','waiting'))::bigint AS running,
 (SELECT count(*) FROM session WHERE host_id= @host_id::text AND archived_at IS NULL)::bigint AS sessions,
 (SELECT count(DISTINCT c.artifact_id) FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE s.host_id= @host_id::text AND c.state='verified'
  AND NOT EXISTS (SELECT 1 FROM artifact_copy o JOIN store t ON t.id=o.store_id WHERE o.artifact_id=c.artifact_id AND o.state='verified' AND t.host_id<> @host_id::text))::bigint AS lonely;

-- name: DeleteDeletedCopiesOfHost :exec
-- Only deleted copies (the ones hosts_remove dropped) still name a purged host's stores; they go with them.
DELETE FROM artifact_copy WHERE state='deleted' AND store_id IN (SELECT id FROM store WHERE host_id=$1);

-- name: DeleteStoresOfHost :exec
DELETE FROM store WHERE host_id=$1;

-- name: DeleteHost :exec
-- A purged host: its record and its observation.
WITH h AS (DELETE FROM host WHERE host_id= @host_id::text) DELETE FROM host_observation WHERE id= @host_id::text;

-- name: ObservationHasCapability :one
SELECT EXISTS (SELECT 1 FROM host_observation WHERE id= @host_id::text AND payload->'capabilities' ? @capability::text);

-- name: ObservationHasProblems :one
SELECT EXISTS (SELECT 1 FROM host_observation WHERE id=$1 AND jsonb_array_length(coalesce(payload->'problems','[]'))>0);

-- name: ClearObservationPollError :exec
UPDATE host_observation SET payload=payload-'pollError', updated_at=now() WHERE id=$1 AND payload ? 'pollError';

-- name: DeleteHostSettings :exec
DELETE FROM host_setting WHERE host_id=$1;

-- name: OpenExecutionsOnHost :many
SELECT id FROM execution WHERE host_id=$1 AND state IN ('preparing','running','waiting') ORDER BY seq;

-- name: FailArtifactsOnlyOnHost :exec
-- Artifacts whose only verified copy was on the host's stores are lost (data foundation tombstone rules).
UPDATE artifact a SET state='failed', last_error='host-removed' WHERE a.state='stored'
 AND EXISTS (SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE c.artifact_id=a.id AND c.state='verified' AND s.host_id= @host_id::text)
 AND NOT EXISTS (SELECT 1 FROM artifact_copy o JOIN store t ON t.id=o.store_id WHERE o.artifact_id=a.id AND o.state='verified' AND t.host_id<> @host_id::text);

-- name: DeleteCopiesOnHost :exec
UPDATE artifact_copy c SET state='deleted', last_error='host-removed' FROM store s WHERE s.id=c.store_id AND s.host_id=$1 AND c.state<>'deleted';

-- name: StoresOfHostUnreachable :exec
UPDATE store SET state='unreachable' WHERE host_id=$1;

-- name: AddressQuestion :exec
UPDATE question SET addressed_to= @holder::text WHERE id= @id::text;

-- name: RunningByHostHarness :many
SELECT host_id::text AS host_id, coalesce(harness,'')::text AS harness, count(*)::int AS n FROM execution
 WHERE state IN ('preparing','running') AND host_id IS NOT NULL GROUP BY 1,2;

-- name: PendingCommandsByHost :many
SELECT host_id::text AS host_id, count(*)::int AS n FROM command WHERE status='pending' AND host_id IS NOT NULL GROUP BY 1;

-- name: RouteDecisionsOf :many
SELECT execution_id, at, host_id, candidates, winner FROM route_decision WHERE execution_id=$1 ORDER BY at;

-- name: HostRecords :many
-- Every host record, removed ones too, in record order; payload holds the fields that are not columns (typed_records.go).
SELECT host_id, holder, state, intake, secret_hash, removed_secret_hash, project_id, payload FROM host ORDER BY seq;

-- name: HostRecordOf :one
SELECT host_id, holder, state, intake, secret_hash, removed_secret_hash, project_id, payload FROM host WHERE id=$1;

-- name: HostRegister :exec
-- A registered host: a new row starts active; a removed one's row is written afresh (its state moves in host_states.go).
INSERT INTO host(id, host_id, holder, intake, secret_hash, payload) VALUES(@id, @host_id, @holder, @intake, @secret_hash, @payload)
 ON CONFLICT(id) DO UPDATE SET host_id=excluded.host_id, holder=excluded.holder, intake=excluded.intake, secret_hash=excluded.secret_hash,
 removed_secret_hash=NULL, project_id=NULL, payload=excluded.payload, updated_at=now();

-- name: HostPatch :exec
-- hosts_update: intake when given, payload fields merged.
UPDATE host SET intake=coalesce(sqlc.narg(intake)::text, intake), payload=payload || @patch::jsonb, updated_at=now() WHERE id= @id;

-- name: HostSetSecret :exec
UPDATE host SET secret_hash= @secret_hash, updated_at=now() WHERE id= @id;

-- name: HostTombstone :exec
-- hosts_remove: the secret hash moves to removed_secret_hash, harnesses go, removedAt is kept (host_remove.go).
UPDATE host SET removed_secret_hash=secret_hash, secret_hash=NULL, payload=(payload - 'harnesses') || jsonb_build_object('removedAt', @removed_at::bigint),
 updated_at=now() WHERE id= @id;

-- name: HostObservations :many
-- Every host observation in record order; payload holds what the host reported (host_poll.go).
SELECT id, holder, last_seen_at, payload FROM host_observation ORDER BY seq;

-- name: HostLastSeen :many
-- Non-removed hosts and any unremoved observed hosts with their last_seen_at, for nav_counts offline check.
SELECT h.host_id AS id, o.last_seen_at
FROM host h
LEFT JOIN host_observation o ON o.id = h.host_id
WHERE h.state <> 'removed'
UNION ALL
SELECT o.id, o.last_seen_at
FROM host_observation o
WHERE NOT EXISTS (SELECT 1 FROM host h WHERE h.host_id = o.id);

-- name: HostObservationOf :one
SELECT id, holder, last_seen_at, project_id, payload FROM host_observation WHERE id=$1;

-- name: HostObservationPut :exec
INSERT INTO host_observation(id, holder, last_seen_at, project_id, payload)
VALUES (@id, sqlc.narg(holder), sqlc.narg(last_seen_at), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 holder=excluded.holder, last_seen_at=excluded.last_seen_at, project_id=excluded.project_id;
