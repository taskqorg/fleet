-- Artifacts, their copies and the stores that hold them (artifacts.go, artifact_routes.go, artifact_states.go;
-- docs/designs/fleet-data-foundation.md §3).

-- name: ArtStores :many
SELECT * FROM store ORDER BY id;

-- name: ArtStoreHost :one
SELECT host_id FROM store WHERE id=$1;

-- name: ArtStoreUpsert :exec
-- Only the own host replaces it: a store of another host is unchanged.
INSERT INTO store(id,host_id,zone,bucket,endpoint,local_endpoint,capacity_bytes,used_bytes,requires_logon,state,observed_at,holder)
VALUES(@id,@host_id,sqlc.narg(zone),@bucket,@endpoint,sqlc.narg(local_endpoint),sqlc.narg(capacity_bytes),sqlc.narg(used_bytes),@requires_logon,'reachable',now(),sqlc.narg(holder))
ON CONFLICT(id) DO UPDATE SET zone=excluded.zone,bucket=excluded.bucket,endpoint=excluded.endpoint,local_endpoint=excluded.local_endpoint,
 capacity_bytes=excluded.capacity_bytes,used_bytes=excluded.used_bytes,requires_logon=excluded.requires_logon,state='reachable',
 observed_at=now(),registered_at=now(),holder=excluded.holder
WHERE store.host_id=excluded.host_id;

-- name: ArtStoreObserve :exec
-- The report of a host of one store: full when the capacity left falls below its warning; nothing written when nothing
-- changed (observed_at only every 30 seconds).
UPDATE store SET
 state=CASE WHEN NOT sqlc.arg(reachable)::boolean THEN 'unreachable' WHEN free_space_warning_bytes IS NOT NULL AND coalesce(sqlc.narg(capacity)::bigint,capacity_bytes) < free_space_warning_bytes THEN 'full' ELSE 'reachable' END,
 capacity_bytes=coalesce(sqlc.narg(capacity)::bigint,capacity_bytes),
 used_bytes=coalesce(sqlc.narg(used)::bigint,used_bytes),
 observed_at=CASE WHEN sqlc.arg(reachable)::boolean THEN now() ELSE observed_at END
WHERE id= @id AND host_id= @host_id AND (
 state IS DISTINCT FROM CASE WHEN NOT sqlc.arg(reachable)::boolean THEN 'unreachable' WHEN free_space_warning_bytes IS NOT NULL AND coalesce(sqlc.narg(capacity)::bigint,capacity_bytes) < free_space_warning_bytes THEN 'full' ELSE 'reachable' END
 OR capacity_bytes IS DISTINCT FROM coalesce(sqlc.narg(capacity)::bigint,capacity_bytes)
 OR used_bytes IS DISTINCT FROM coalesce(sqlc.narg(used)::bigint,used_bytes)
 OR (sqlc.arg(reachable)::boolean AND (observed_at IS NULL OR observed_at < now() - interval '30 seconds')));

-- name: ArtStoreLockState :one
SELECT state, capacity_bytes FROM store WHERE id=$1 FOR UPDATE;

-- name: ArtStoreMove :exec
UPDATE store SET
 free_space_warning_bytes=CASE WHEN sqlc.arg(set_warning)::boolean THEN sqlc.narg(bytes)::bigint ELSE free_space_warning_bytes END,
 state= @state
WHERE id= @id;

-- name: ArtStoresSweep :exec
-- Stores a host has not reported for unseen_ms are unreachable.
UPDATE store SET state='unreachable' WHERE state<>'unreachable' AND (observed_at IS NULL OR observed_at < now() - sqlc.arg(unseen_ms)::bigint * interval '1 millisecond');

-- name: ArtByID :one
-- A deleted artifact is unknown, whoever asks.
SELECT * FROM artifact WHERE (id=$1 OR legacy_id=$1) AND tombstoned_at IS NULL ORDER BY (id=$1) DESC LIMIT 1;

-- name: ArtReservation :one
SELECT state, execution_id FROM artifact WHERE id=$1;

-- name: ArtLiveCopyStore :one
SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying','verified') ORDER BY state LIMIT 1;

-- name: ArtPurged :one
SELECT EXISTS (SELECT 1 FROM purged_hash WHERE project_id=$1 AND scope=$2 AND content_hash=$3);

-- name: ArtVerifiedContent :one
-- A verified copy of this content on the store.
SELECT EXISTS (SELECT 1 FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id
 WHERE a.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND a.scope= @scope AND a.content_hash= @content_hash AND c.store_id= @store_id AND c.state='verified');

-- name: ArtInsert :exec
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,required,role,derived_from_artifact_id,previous_artifact_id,state)
VALUES(@id,sqlc.narg(project_id),@scope,@content_hash,@size,@media_type,@name,sqlc.narg(execution_id),sqlc.narg(task_id),@required,@role,sqlc.narg(derived_from_artifact_id),sqlc.narg(previous_artifact_id),@state);

-- name: ArtInsertStored :exec
-- An object the coordinator wrote itself for an execution.
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state)
VALUES(@id,sqlc.narg(project_id),@scope,@content_hash,@size,@media_type,@name,@execution_id,'stored');

-- name: ArtCopyStateForUpdate :one
SELECT state FROM artifact_copy WHERE artifact_id=$1 AND store_id=$2 FOR UPDATE;

-- name: ArtCopyInsertVerified :exec
INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES(@artifact_id,@store_id,sqlc.narg(project_id),'verified',now());

-- name: ArtCopyInsertPending :exec
INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at)
VALUES(@artifact_id,@store_id,sqlc.narg(project_id),'pending',@upload_id,@staging_key,now()+ sqlc.arg(lease_ms)::bigint * interval '1 millisecond');

-- name: ArtCopyWorking :one
-- The copy of the given store, else the one in progress.
SELECT store_id,state,upload_id,staging_key FROM artifact_copy
WHERE artifact_id= @artifact_id AND (store_id=sqlc.narg(store_id)::text OR sqlc.narg(store_id)::text IS NULL AND state IN ('pending','copying')) LIMIT 1;

-- name: ArtCopyLease :exec
-- A lease renewal; clear_upload ends the multipart upload, keep_later never shortens a lease.
UPDATE artifact_copy SET upload_id=CASE WHEN sqlc.arg(clear_upload)::boolean THEN NULL ELSE upload_id END,
 lease_expires_at=CASE WHEN sqlc.arg(keep_later)::boolean THEN greatest(lease_expires_at,now()+ sqlc.arg(lease_ms)::bigint * interval '1 millisecond') ELSE now()+ sqlc.arg(lease_ms)::bigint * interval '1 millisecond' END
WHERE artifact_id= @artifact_id AND store_id= @store_id;

-- name: ArtVerifyQueued :one
SELECT EXISTS (SELECT 1 FROM command WHERE kind='artifact-verify' AND host_id= @host_id AND status IN ('pending','accepted','started')
 AND payload->'payload'->>'artifactId'= sqlc.arg(artifact_id)::text AND payload->'payload'->>'storeId'= sqlc.arg(store_id)::text);

-- name: ArtCopyReject :exec
-- A failed copy retries after 1 minute doubling, up to the retry window.
UPDATE artifact_copy SET state='failed',last_error= @why,upload_id=NULL,lease_expires_at=NULL,attempts=attempts+1,
 next_attempt_at=now()+least(sqlc.arg(retry_window_ms)::float8,60000*power(2,least(attempts,30))) * interval '1 millisecond'
WHERE artifact_id= @artifact_id AND store_id= @store_id;

-- name: ArtFailUnstored :exec
UPDATE artifact SET state='failed',last_error= @why
WHERE id= @id AND state='pending' AND NOT EXISTS (SELECT 1 FROM artifact_copy WHERE artifact_id= @id AND state='verified');

-- name: ArtCopiesInProgress :many
SELECT store_id,upload_id,staging_key FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying');

-- name: ArtCopyVerified :exec
UPDATE artifact_copy SET state='verified',verified_at=now(),upload_id=NULL,staging_key=NULL,lease_expires_at=NULL,last_error=NULL
WHERE artifact_id=$1 AND store_id=$2;

-- name: ArtStoredNow :exec
UPDATE artifact SET state='stored',last_error=NULL WHERE id=$1 AND state='pending';

-- name: ArtDeclareRequired :exec
-- The required declaration: on the execution record and on its artifacts of that name.
UPDATE execution SET data=jsonb_set(data,'{requiredArtifacts}',(SELECT coalesce(jsonb_agg(DISTINCT v),'[]')
 FROM jsonb_array_elements_text(coalesce(data->'requiredArtifacts','[]')||to_jsonb(sqlc.arg(name)::text)) v)) WHERE id= @execution_id;

-- name: ArtMarkRequired :exec
UPDATE artifact SET required=true WHERE execution_id= @execution_id AND name= @name;

-- name: ArtReadableStores :many
-- Stores with a verified copy; a store with a purge pending serves nothing.
SELECT c.store_id FROM artifact_copy c WHERE c.artifact_id=$1 AND c.state='verified'
 AND c.store_id NOT IN (SELECT p.store_id FROM artifact_copy p WHERE p.state='purge_pending');

-- name: ArtCopyStates :many
SELECT store_id,state FROM artifact_copy WHERE artifact_id=$1 ORDER BY store_id;

-- name: ArtList :many
-- artifact_list: every filter optional; newest first (the seq index serves a page's limit), the caller reverses an
-- unpaged list.
SELECT * FROM artifact
WHERE tombstoned_at IS NULL
 AND (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id)::text)
 AND (sqlc.narg(task_id)::text IS NULL OR task_id=sqlc.narg(task_id)::text)
 AND (sqlc.narg(project_id)::text IS NULL OR project_id=sqlc.narg(project_id)::text)
 AND (sqlc.narg(media_type)::text IS NULL OR media_type=sqlc.narg(media_type)::text)
 AND (sqlc.narg(media_like)::text IS NULL OR media_type LIKE sqlc.narg(media_like)::text)
 AND (sqlc.narg(project_ids)::text[] IS NULL OR project_id=ANY(sqlc.narg(project_ids)::text[]) OR sqlc.arg(project_ids_null)::boolean AND project_id IS NULL)
 AND (sqlc.narg(exclude_ids)::text[] IS NULL OR NOT coalesce(project_id=ANY(sqlc.narg(exclude_ids)::text[]),false))
 AND (NOT sqlc.arg(exclude_null)::boolean OR project_id IS NOT NULL)
 AND (sqlc.narg(states)::text[] IS NULL OR state=ANY(sqlc.narg(states)::text[]))
 AND (sqlc.narg(since)::timestamptz IS NULL OR created_at>=sqlc.narg(since)::timestamptz)
 AND (sqlc.narg(words)::text[] IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(sqlc.narg(words)::text[]) w WHERE NOT lower(name) LIKE w))
 AND (sqlc.narg(project_like)::text IS NULL OR lower(coalesce(project_id,'')) LIKE sqlc.narg(project_like)::text)
 AND (sqlc.narg(search_state)::text IS NULL OR state=sqlc.narg(search_state)::text)
 AND NOT sqlc.arg(never)::boolean
 AND (NOT sqlc.arg(no_examples)::boolean OR role<>'example')
 AND (sqlc.narg(before)::bigint IS NULL OR seq<sqlc.narg(before)::bigint)
ORDER BY seq DESC
LIMIT sqlc.narg(row_limit)::bigint;

-- name: ArtStateCounts :many
-- Counts per state matching the artifact_list filters without states, before or limit.
SELECT state, count(*)::bigint AS n FROM artifact
WHERE tombstoned_at IS NULL
 AND (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id)::text)
 AND (sqlc.narg(task_id)::text IS NULL OR task_id=sqlc.narg(task_id)::text)
 AND (sqlc.narg(project_id)::text IS NULL OR project_id=sqlc.narg(project_id)::text)
 AND (sqlc.narg(media_type)::text IS NULL OR media_type=sqlc.narg(media_type)::text)
 AND (sqlc.narg(media_like)::text IS NULL OR media_type LIKE sqlc.narg(media_like)::text)
 AND (sqlc.narg(project_ids)::text[] IS NULL OR project_id=ANY(sqlc.narg(project_ids)::text[]) OR sqlc.arg(project_ids_null)::boolean AND project_id IS NULL)
 AND (sqlc.narg(exclude_ids)::text[] IS NULL OR NOT coalesce(project_id=ANY(sqlc.narg(exclude_ids)::text[]),false))
 AND (NOT sqlc.arg(exclude_null)::boolean OR project_id IS NOT NULL)
 AND (sqlc.narg(since)::timestamptz IS NULL OR created_at>=sqlc.narg(since)::timestamptz)
 AND (sqlc.narg(words)::text[] IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(sqlc.narg(words)::text[]) w WHERE NOT lower(name) LIKE w))
 AND (sqlc.narg(project_like)::text IS NULL OR lower(coalesce(project_id,'')) LIKE sqlc.narg(project_like)::text)
 AND (sqlc.narg(search_state)::text IS NULL OR state=sqlc.narg(search_state)::text)
 AND NOT sqlc.arg(never)::boolean
 AND (NOT sqlc.arg(no_examples)::boolean OR role<>'example')
GROUP BY state;

-- name: ArtTaskCodes :many
SELECT id,code FROM task WHERE id=ANY(sqlc.arg(ids)::text[]);

-- name: ArtOfContent :many
-- The artifacts of one content hash, in a project and scope when given (a purge).
SELECT * FROM artifact WHERE content_hash= @content_hash
 AND (sqlc.narg(project_id)::text IS NULL OR project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text)
 AND (sqlc.narg(scope)::text IS NULL OR scope=sqlc.narg(scope)::text) ORDER BY seq;

-- name: ArtPurgedInsert :exec
INSERT INTO purged_hash(project_id,scope,content_hash,reason) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING;

-- name: ArtCopiesLive :many
SELECT store_id,staging_key FROM artifact_copy WHERE artifact_id=$1 AND state<>'deleted';

-- name: ArtCopySetState :exec
UPDATE artifact_copy SET state= @state WHERE artifact_id= @artifact_id AND store_id= @store_id;

-- name: ArtMarkDeleted :exec
UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by= @deleted_by WHERE id= @id;

-- name: ArtLeasesExpired :many
SELECT artifact_id,store_id,upload_id,staging_key FROM artifact_copy WHERE state IN ('pending','copying') AND lease_expires_at < now();

-- name: ArtExecution :one
-- The execution an artifact belongs to: its host, project, task, experiment and declared required names.
SELECT id, coalesce(host_id,'')::text AS host_id, project_id, task_id, experiment_id,
 coalesce(data->'requiredArtifacts','[]'::jsonb)::jsonb AS required_artifacts
FROM execution WHERE id=$1;

-- name: ArtTaskArtifactRules :one
SELECT coalesce(completion_requirements->'artifacts','[]'::jsonb)::jsonb AS artifacts FROM task WHERE id=$1;

-- name: ArtOfExecution :many
-- An execution's originals, oldest first; only stored ones when stored_only.
SELECT * FROM artifact WHERE execution_id= @execution_id AND role='original'
 AND (CASE WHEN sqlc.arg(stored_only)::boolean THEN state='stored' ELSE state<>'deleted' END) ORDER BY seq;

-- name: ArtCommandOf :one
-- A command as a host reads it: its kind, status, host and payload.
SELECT kind, status::text AS status, coalesce(host_id,payload->>'hostId','')::text AS host_id,
 coalesce(payload->'payload','{}'::jsonb)::jsonb AS payload FROM command WHERE id=$1;

-- name: ArtStartPayloads :many
-- The start and script commands of an execution on a host.
SELECT coalesce(payload->'payload','{}'::jsonb)::jsonb AS payload FROM command
WHERE execution_id= @execution_id AND host_id= @host_id AND kind IN ('start','script') ORDER BY seq;

-- name: ArtLimitReadings :many
-- The latest limit reading per subscription and period.
SELECT DISTINCT ON (x.account_id, r.value->>'period') x.account_id::text AS account_id, x.host_id, r.value::jsonb AS reading, d.updated_at
FROM event d JOIN execution x ON x.id=d.execution_id
 CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.payload->'data'->'readings','[]'::jsonb)) AS r(value)
WHERE NOT d.delta AND d.type='limit_reading' AND x.account_id IS NOT NULL
ORDER BY x.account_id, r.value->>'period', d.seq DESC;

-- name: ArtStoredAll :many
-- Every stored artifact, for the placement pass (placement.go).
SELECT * FROM artifact WHERE state='stored' ORDER BY seq;

-- name: ArtWorkFacts :many
-- The work facts of a results page (controls/work-facts.md): per run, its harness and its task's executions so far.
SELECT e.id,e.harness,coalesce(d.executions,0)::bigint AS task_attempts FROM execution e LEFT JOIN task_derived d ON d.task_id=e.task_id
WHERE e.id=ANY(@ids::text[]);

-- name: ArtTaskAttempts :many
-- Executions so far of the tasks of a results page whose rows have no run.
SELECT d.task_id,d.executions::bigint AS task_attempts FROM task_derived d WHERE d.task_id=ANY(@ids::text[]);
