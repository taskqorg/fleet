-- Project apps (docs/designs/fleet-project-apps.md).

-- name: ProjectAppInsert :exec
INSERT INTO project_app (id, project_id, name, state, request_id, asked_by, payload)
VALUES (@id, @project_id, @name, 'asked', @request_id, @asked_by, @payload);

-- name: ProjectAppGet :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE id = $1 AND deleted_at IS NULL;

-- name: ProjectAppLock :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ProjectAppByRequest :one
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE project_id = @project_id AND request_id = @request_id AND deleted_at IS NULL;

-- name: ProjectAppByQuestion :one
SELECT id FROM project_app WHERE question_id = $1;

-- name: ProjectAppsOfProject :many
SELECT id, project_id, name, state, request_id, asked_by, approved_by, question_id, payload, last_used_at, created_at, updated_at
FROM project_app WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at;

-- name: ProjectAppIDTaken :one
-- App ids are unique in Fleet, across projects (SECURITY DEFINER: the caller sees only its projects).
SELECT fleet_project_app_id_taken($1)::boolean;

-- name: ProjectAppSetQuestion :exec
UPDATE project_app SET question_id = @question_id, payload = @payload, name = @name, updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppSetState :exec
UPDATE project_app SET state = @state, approved_by = coalesce(sqlc.narg(approved_by), approved_by), updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppSetPayload :exec
UPDATE project_app SET payload = @payload, updated_at = clock_timestamp() WHERE id = @id;

-- name: ProjectAppKeyInsert :exec
INSERT INTO project_app_key (id, app_id, project_id, directed_by, sha256) VALUES (@id, @app_id, @project_id, @directed_by, @sha256);

-- name: ProjectAppKeys :many
SELECT id, app_id, created_at, taken_at, last_used_at, retired_at FROM project_app_key WHERE app_id = ANY(@app_ids::text[]) ORDER BY created_at;

-- name: ProjectAppKeyNewestUntaken :one
SELECT id FROM project_app_key WHERE app_id = $1 AND taken_at IS NULL AND retired_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: ProjectAppKeyTaken :one
SELECT taken_at FROM project_app_key WHERE app_id = $1 AND retired_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: ProjectAppKeyMarkTaken :exec
UPDATE project_app_key SET taken_at = clock_timestamp() WHERE id = $1 AND taken_at IS NULL;

-- name: ProjectAppKeysRetire :exec
UPDATE project_app_key SET retired_at = clock_timestamp() WHERE app_id = $1 AND retired_at IS NULL;

-- name: ProjectAppKeysRetireBefore :exec
-- A rotation's grace ends at the new key's first use: the app's older live keys are retired.
UPDATE project_app_key SET retired_at = clock_timestamp() WHERE app_id = @app_id AND retired_at IS NULL AND created_at < @created_at;

-- name: ProjectAppKeyBySecret :one
-- The live key a request presents (read before any scope is known, like a host secret).
SELECT k.id, k.app_id, k.project_id, k.directed_by, k.created_at, k.last_used_at FROM project_app_key k
WHERE k.sha256 = $1 AND k.retired_at IS NULL;

-- name: ProjectAppKeyUsed :exec
UPDATE project_app_key SET last_used_at = clock_timestamp() WHERE id = $1;

-- name: ProjectAppUsed :exec
UPDATE project_app SET last_used_at = clock_timestamp() WHERE id = $1;

-- name: AppsEdgeGet :one
SELECT e.id, e.state, e.request_id, e.question_id, e.rotate, e.payload, e.updated_at, w.last_error FROM apps_edge e LEFT JOIN apps_edge_work w ON w.id = e.id WHERE e.id = 'edge';

-- name: AppsEdgeLock :one
SELECT id, state, request_id, question_id, rotate, payload, updated_at FROM apps_edge WHERE id = 'edge' FOR UPDATE;

-- name: AppsEdgeUpsert :exec
INSERT INTO apps_edge (id, state, request_id, question_id, rotate, payload) VALUES ('edge', @state, @request_id, @question_id, @rotate, @payload)
ON CONFLICT (id) DO UPDATE SET state = excluded.state, request_id = excluded.request_id, question_id = excluded.question_id,
 rotate = excluded.rotate, payload = excluded.payload, updated_at = clock_timestamp();

-- name: AppsEdgeSet :exec
UPDATE apps_edge SET state = @state, payload = @payload, rotate = @rotate, updated_at = clock_timestamp() WHERE id = 'edge';

-- name: AppsEdgeQueue :exec
UPDATE apps_edge SET state = 'working', updated_at = clock_timestamp() WHERE id = 'edge';

-- name: AppsEdgeWorkPut :exec
INSERT INTO apps_edge_work (id) VALUES ('edge') ON CONFLICT (id) DO NOTHING;

-- name: AppsEdgeWorkDelete :exec
DELETE FROM apps_edge_work WHERE id = 'edge';

-- name: AppsEdgeWorkDue :many
SELECT id, attempts FROM apps_edge_work WHERE next_attempt_at <= @now LIMIT @lim;

-- name: AppsEdgeByQuestion :one
SELECT id FROM apps_edge WHERE question_id = $1;

-- name: ExecutionSetApp :exec
-- The app a pipeline run belongs to; its steps and leaves take it from their parent (fleet_inherit_app).
UPDATE execution SET app_id = @app_id WHERE id = @id AND app_id IS DISTINCT FROM @app_id;

-- name: ProjectAppPayload :one
SELECT payload FROM project_app WHERE id = $1 AND state = 'active' AND deleted_at IS NULL;
