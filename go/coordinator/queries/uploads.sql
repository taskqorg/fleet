-- Operator uploads (uploads.go): the upload row (migration 82). The fields a write guards or a reference names are
-- columns, payload the rest (uploadPayload).

-- name: UploadInsert :exec
INSERT INTO upload(id, person, session_id, project_id, received, payload) VALUES (@id, @person, sqlc.narg(session_id), sqlc.narg(project_id), 0, @payload::jsonb);

-- name: UploadGet :one
SELECT id, person, session_id, project_id, received, multipart, artifact_id, payload FROM upload WHERE id = @id;

-- name: UploadSave :execrows
-- The record, only while it still has the received count it was read with and no artifact yet.
UPDATE upload SET received = @received, multipart = sqlc.narg(multipart), artifact_id = sqlc.narg(artifact_id), payload = @payload::jsonb, updated_at = now()
WHERE id = @id AND received = @was_received AND artifact_id IS NULL;

-- name: UploadMultipartSet :execrows
UPDATE upload SET multipart = @multipart, updated_at = now() WHERE id = @id AND multipart IS NULL;

-- name: UploadsStale :many
SELECT id, person, session_id, project_id, received, multipart, artifact_id, payload FROM upload
WHERE updated_at < now() - make_interval(secs => @age_seconds::float8);

-- name: UploadDrop :exec
DELETE FROM upload WHERE id = @id AND updated_at < now() - make_interval(secs => @age_seconds::float8);

-- name: UploadContentPurged :one
-- As artifactReserve: purged content is never stored again.
SELECT EXISTS (SELECT 1 FROM purged_hash WHERE project_id = @project_key AND scope = 'prod' AND content_hash = @content_hash);

-- name: UploadArtifactInsert :exec
INSERT INTO artifact(id, project_id, scope, content_hash, size, media_type, name, session_id, role, state)
VALUES (@id, sqlc.narg(project_id), 'prod', @content_hash, @size, @media_type, @name, sqlc.narg(session_id), 'attachment', 'stored');

-- name: UploadCopyInsert :exec
INSERT INTO artifact_copy(artifact_id, store_id, project_id, state, verified_at) VALUES (@artifact_id, @store_id, sqlc.narg(project_id), 'verified', now());

-- name: UploadSessionProject :one
-- The project of the session an upload attaches to (its record's projectId); no row when the caller does not see it.
SELECT project_id, (payload->'projectId')::jsonb AS payload_project FROM session WHERE id = @id;

-- name: UploadFinishWorkDue :many
-- The work kit's due background finishes (upload_work.go).
SELECT upload_id, attempts FROM upload_finish_work WHERE next_attempt_at <= sqlc.arg(now)::pg_catalog.timestamptz ORDER BY next_attempt_at, upload_id LIMIT sqlc.arg(lim);

-- name: UploadFinishWorkAdd :exec
INSERT INTO upload_finish_work(upload_id) VALUES (@upload_id) ON CONFLICT DO NOTHING;

-- name: UploadFinishing :one
-- The upload's finish runs in the background (its work row is there).
SELECT EXISTS (SELECT 1 FROM upload_finish_work WHERE upload_id = @upload_id);

-- name: UploadFinishWorkDelete :exec
DELETE FROM upload_finish_work WHERE upload_id = @upload_id;
