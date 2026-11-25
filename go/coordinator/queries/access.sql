-- Project resource access (docs/designs/fleet-project-access.md): servers, grants and the provisioner's work.

-- name: AccessServerGet :one
SELECT id, address, via, host_key, enrolled_at, payload FROM access_server WHERE id = $1 AND deleted_at IS NULL;

-- name: AccessServerList :many
SELECT id, address, via, host_key, enrolled_at, payload FROM access_server WHERE deleted_at IS NULL ORDER BY id;

-- name: AccessServerInsert :exec
INSERT INTO access_server (id, address, via, host_key) VALUES (@id, @address, @via, @host_key);

-- name: AccessServerChecked :exec
-- The last check (payload.checked); a success sets enrolled_at once.
UPDATE access_server SET payload = jsonb_set(payload, '{checked}', @checked::jsonb),
 enrolled_at = CASE WHEN @ok::boolean THEN coalesce(enrolled_at, clock_timestamp()) ELSE enrolled_at END,
 updated_at = clock_timestamp()
WHERE id = @id;

-- name: AccessGrantInsert :exec
INSERT INTO access_grant (id, project_id, server_id, kind, level, state, request_id, asked_by, question_id, payload)
VALUES (@id, @project_id, @server_id, @kind, @level, 'asked', @request_id, @asked_by, @question_id, @payload);

-- name: AccessGrantByRequest :one
SELECT id, project_id, server_id, kind, level, state, request_id, asked_by, question_id, credential, expires_at, payload, created_at, updated_at
FROM access_grant WHERE project_id = @project_id AND request_id = @request_id AND deleted_at IS NULL;

-- name: AccessGrantGet :one
SELECT id, project_id, server_id, kind, level, state, request_id, asked_by, question_id, credential, expires_at, payload, created_at, updated_at
FROM access_grant WHERE id = $1 AND deleted_at IS NULL;

-- name: AccessGrantByQuestion :one
SELECT id FROM access_grant WHERE question_id = $1 AND deleted_at IS NULL;

-- name: AccessGrantList :many
-- Grants visible to the caller (row-level security), of one project or of all.
SELECT id, project_id, server_id, kind, level, state, request_id, asked_by, question_id, credential, expires_at, payload, created_at, updated_at
FROM access_grant WHERE deleted_at IS NULL AND (@project_id::text = '' OR project_id = @project_id::text)
ORDER BY project_id, created_at;

-- name: AccessGrantLiveForUser :many
-- The other live grants of one project on one server: they share the OS user, so a revoke keeps it while any is live.
SELECT id, kind, level, payload FROM access_grant
WHERE project_id = @project_id AND server_id = @server_id AND id <> @id AND deleted_at IS NULL
 AND state IN ('provisioning', 'ready', 'failed');

-- name: AccessGrantLockState :one
SELECT state, project_id FROM access_grant WHERE id = $1 FOR UPDATE;

-- name: AccessGrantMove :exec
UPDATE access_grant SET state = @state, updated_at = clock_timestamp() WHERE id = @id;

-- name: AccessGrantSetQuestion :exec
UPDATE access_grant SET question_id = @question_id, payload = @payload, updated_at = clock_timestamp() WHERE id = @id;

-- name: AccessGrantSetCredential :exec
UPDATE access_grant SET credential = @credential, updated_at = clock_timestamp() WHERE id = @id;

-- name: AccessGrantNote :exec
-- One field of payload: the last probe or the last error.
UPDATE access_grant SET payload = jsonb_set(payload, ARRAY[@field::text], @value::jsonb), updated_at = clock_timestamp() WHERE id = @id;

-- name: AccessWorkPut :exec
-- Queue an operation for a grant; a newer operation replaces a pending one (kitSchedule makes it due at once).
INSERT INTO access_work (grant_id, op) VALUES (@grant_id, @op)
ON CONFLICT (grant_id) DO UPDATE SET op = excluded.op;

-- name: AccessWorkDue :many
SELECT grant_id, op, attempts FROM access_work WHERE next_attempt_at <= @now ORDER BY next_attempt_at LIMIT @lim;

-- name: AccessWorkGet :one
SELECT grant_id, op, attempts FROM access_work WHERE grant_id = $1;

-- name: AccessWorkDelete :exec
DELETE FROM access_work WHERE grant_id = @grant_id AND op = @op;

-- name: AccessGrantByConnection :one
-- The ready grant a project access connection serves (payload.connection names it).
SELECT id, project_id, server_id, kind, level, state, request_id, asked_by, question_id, credential, expires_at, payload, created_at, updated_at
FROM access_grant WHERE payload->>'connection' = @connection::text AND state = 'ready' AND deleted_at IS NULL
ORDER BY updated_at DESC LIMIT 1;
