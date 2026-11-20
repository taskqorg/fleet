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
