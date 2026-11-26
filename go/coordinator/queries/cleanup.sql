-- Delete and purge (cleanup.go).

-- name: ReferenceKeys :many
-- Every foreign-key column of the schema, its parent table and whether its row goes with the parent (ON DELETE CASCADE).
SELECT c.conrelid::regclass::text AS child, a.attname::text AS col, c.confrelid::regclass::text AS parent,
  c.confdeltype = 'c' AS cascades
FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
ORDER BY 1, 2;

-- The root of a delete as the caller sees it: gone false asks whether it is there (a project not deleted), gone true
-- whether it is already deleted (with deleted rows shown).

-- name: DeleteRootProject :one
SELECT EXISTS (SELECT 1 FROM definition WHERE kind = 'project' AND id = @id AND (deleted_at IS NOT NULL) = @gone::bool);

-- name: DeleteRootTask :one
SELECT EXISTS (SELECT 1 FROM task WHERE id = @id AND (NOT @gone::bool OR tombstoned_at IS NOT NULL));

-- name: DeleteRootSession :one
SELECT EXISTS (SELECT 1 FROM session WHERE id = @id AND (NOT @gone::bool OR tombstoned_at IS NOT NULL));

-- name: DeleteRootExperiment :one
SELECT EXISTS (SELECT 1 FROM experiment WHERE id = @id AND (NOT @gone::bool OR tombstoned_at IS NOT NULL));

-- name: CleanupStoredBytes :one
SELECT coalesce(sum(size), 0)::bigint FROM artifact WHERE id = ANY (@ids::text[]) AND state = 'stored';

-- name: CleanupOpenSessionOfTasks :one
-- An open session working for one of the tasks, outside the cascade's sessions.
SELECT id FROM session WHERE task_id = ANY (@tasks::text[]) AND archived_at IS NULL AND tombstoned_at IS NULL
  AND NOT id = ANY (@sessions::text[]) AND (project_id IS NULL OR NOT project_id = ANY (fleet_deleted_projects())) LIMIT 1;

-- name: CleanupExecutionKept :one
-- The first execution of the cascade that live work outside it keeps: a live task's run, a live experiment's trial, a
-- child of an unfinished parent run.
SELECT (CASE
  WHEN t.id IS NOT NULL THEN 'execution.task_id by task ' || t.id
  WHEN x.id IS NOT NULL THEN 'execution.experiment_id by experiment ' || x.id
  ELSE 'execution.parent_execution_id by execution ' || p.id END)::text AS reason
FROM execution e
LEFT JOIN task t ON t.id = e.task_id AND t.tombstoned_at IS NULL AND NOT t.id = ANY (@tasks::text[])
  AND (t.project_id IS NULL OR NOT t.project_id = ANY (fleet_deleted_projects()))
LEFT JOIN experiment x ON x.id = e.experiment_id AND x.tombstoned_at IS NULL AND NOT x.id = ANY (@experiments::text[])
  AND NOT x.project_id = ANY (fleet_deleted_projects())
LEFT JOIN execution p ON p.id = e.parent_execution_id AND p.tombstoned_at IS NULL AND NOT p.id = ANY (@executions::text[])
  AND p.state IN ('preparing', 'running', 'waiting') AND (p.project_id IS NULL OR NOT p.project_id = ANY (fleet_deleted_projects()))
WHERE e.id = ANY (@executions::text[]) AND (t.id IS NOT NULL OR x.id IS NOT NULL OR p.id IS NOT NULL) LIMIT 1;

-- name: CleanupRunningCount :one
-- The executions the confirm stops (agent runs and host jobs, execKinds) or cancels (pipeline runs not ended: running,
-- or preparing while their inputs by reference are read).
SELECT count(*) FROM execution WHERE id = ANY (@ids::text[])
  AND ((kind IN ('agent_run', 'host_job') AND state IN ('preparing', 'running', 'waiting')) OR (kind = 'pipeline_run' AND state IN ('preparing', 'running')));

-- name: CleanupRunningPipelineRuns :many
SELECT id FROM execution WHERE id = ANY (@ids::text[]) AND kind = 'pipeline_run' AND state IN ('preparing', 'running') ORDER BY seq;

-- name: CleanupLiveRuns :many
SELECT id FROM execution WHERE id = ANY (@ids::text[]) AND kind IN ('agent_run', 'host_job') AND state IN ('preparing', 'running', 'waiting') ORDER BY seq;

-- name: CleanupStopDetail :exec
UPDATE execution SET data = data || '{"stopDetail":"deleted"}' WHERE id = @id;

-- name: CleanupRevokeRunTokens :execrows
UPDATE run_token SET revoked_at = @at, updated_at = now() WHERE execution_id = ANY (@ids::text[]) AND revoked_at IS NULL;

-- name: CleanupQueuedMessages :exec
-- Messages still waiting for a turn never start one (host receipts see deleted rows and promote queued messages).
UPDATE queued_message SET state = 'deleted', updated_at = now() WHERE session_id = ANY (@sessions::text[]) AND state IN ('queued', 'steering');

-- name: CleanupBudgets :exec
DELETE FROM budget WHERE (scope = @scope AND scope_id = @scope_id) OR (scope = 'experiment' AND scope_id = ANY (@experiments::text[]));

-- name: CleanupProjectHead :one
SELECT r.content FROM definition d JOIN definition_revision r ON r.kind = d.kind AND r.id = d.id AND r.revision = d.head
WHERE d.kind = 'project' AND d.id = @id;

-- name: CleanupProjectDeleted :exec
UPDATE definition SET deleted_at = now(), retired_at = coalesce(retired_at, now()) WHERE kind = 'project' AND id = @id;

-- name: CleanupProjectPurged :exec
UPDATE definition SET purged_at = now() WHERE kind = 'project' AND id = @id;

-- name: PurgeRootProjects :many
SELECT id FROM definition WHERE kind = 'project' AND deleted_at IS NOT NULL AND purged_at IS NULL ORDER BY deleted_at, id;

-- name: PurgeRootExperiments :many
SELECT id FROM experiment WHERE tombstoned_at IS NOT NULL AND NOT project_id = ANY (fleet_deleted_projects()) ORDER BY tombstoned_at, id;

-- name: PurgeRootTasks :many
SELECT id FROM task WHERE tombstoned_at IS NOT NULL AND (project_id IS NULL OR NOT project_id = ANY (fleet_deleted_projects()))
ORDER BY tombstoned_at, id;

-- name: PurgeRootSessions :many
-- A trial step's session goes with its experiment (its pipeline run is a trial), not as a root of its own.
SELECT id FROM session d WHERE tombstoned_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.id = d.pipeline_run_id AND r.experiment_id IS NOT NULL)
  AND (project_id IS NULL OR NOT project_id = ANY (fleet_deleted_projects())) ORDER BY tombstoned_at, id;

-- name: CleanupCascadeObjects :many
-- The store objects of the cascade's artifacts (an upload in progress: its staging key and multipart upload).
SELECT DISTINCT c.store_id, a.project_id, a.scope, a.content_hash, c.staging_key, c.upload_id
FROM artifact_copy c JOIN artifact a ON a.id = c.artifact_id WHERE a.id = ANY (@artifacts::text[]) AND c.state <> 'deleted';

-- name: CleanupPendingCommands :many
-- The unfinished commands of executions and whether their host is gone: removed, or unknown (neither the default
-- host nor a host record nor an observation), as knownHost decides.
SELECT c.id, c.kind, c.host_id AS host,
  (h.state = 'removed' AND h.id IS NOT NULL
   OR (h.id IS NULL AND c.host_id IS DISTINCT FROM @default_host::text
    AND NOT EXISTS (SELECT 1 FROM host_observation o WHERE o.id = c.host_id)))::bool AS dead
FROM command c LEFT JOIN host h ON h.host_id = c.host_id
WHERE c.execution_id = ANY (@executions::text[]) AND c.status = 'pending' ORDER BY c.seq;

-- name: TxSetPurge :exec
SELECT set_config('fleet.purge', 'on', true);

-- name: PurgeTaskAdd :exec
INSERT INTO purge_task(id, store_id, payload) VALUES (@id, sqlc.narg(store_id), @payload::jsonb);

-- name: PurgeTasks :many
SELECT id, store_id, payload FROM purge_task ORDER BY seq;

-- name: PurgeTaskCount :one
SELECT count(*) FROM purge_task;

-- name: PurgeTaskDelete :exec
DELETE FROM purge_task WHERE id = @id;

-- name: PurgeTaskUpdate :exec
-- An update, never an insert: a task another purge finished meanwhile stays gone.
UPDATE purge_task SET payload = @payload::jsonb WHERE id = @id;

-- name: CleanupObjectUsed :one
-- A copy on the store still maps to the content: an upload or a write deduplicated onto it since the purge.
SELECT EXISTS (SELECT 1 FROM artifact_copy c JOIN artifact a ON a.id = c.artifact_id WHERE c.store_id = @store_id AND c.state <> 'deleted'
  AND a.project_id IS NOT DISTINCT FROM sqlc.narg(project_id) AND a.scope = @scope AND a.content_hash = @content_hash);
