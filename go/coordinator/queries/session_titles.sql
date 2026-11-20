-- Conversation titles (session_titles.go, docs/designs/fleet-conversations.md, Titles): the title columns of a session
-- are written only here (session_rename and the title pass); a session put keeps them (sessions.go, convTables).

-- name: TitleCandidates :many
-- Unarchived untitled sessions with a first message and a reply (a run that ended or started before @wait_before),
-- not host command sessions, not tried since @tried_before nor @tries times; newest first.
SELECT s.id, s.project_id, m.text AS first_message, r.id AS reply_execution
FROM session s
JOIN LATERAL (
 SELECT e.id FROM execution e WHERE e.session_id=s.id AND e.kind='agent_run'
  AND e.project_id IS NOT DISTINCT FROM s.project_id
  AND EXISTS (
   SELECT 1 FROM event d
    WHERE d.execution_id=e.id
     AND (d.project_id IS NULL OR d.project_id IS NOT DISTINCT FROM s.project_id)
     AND jsonb_path_exists(d.payload, '$.data.fleetNormalized.blocks[*] ? (@.kind == "text" && (!exists(@.role) || @.role == "agent") && @.markdown like_regex "[^[:space:]]")')
  )
  AND (e.ended_at IS NOT NULL OR e.started_at < @wait_before::timestamptz)
 ORDER BY e.seq LIMIT 1
) r ON true
JOIN LATERAL (
 SELECT x.text, x.project_id FROM message x WHERE x.session_id=s.id ORDER BY x.seq LIMIT 1
) m ON m.project_id IS NOT DISTINCT FROM s.project_id
WHERE s.title IS NULL AND s.archived_at IS NULL AND s.title_unknown_call IS NULL AND coalesce(s.actor,'')<>'program-worker'
 AND coalesce(s.title_attempt_at,'-infinity') < @tried_before::timestamptz AND s.title_attempts < @tries::int
ORDER BY s.seq DESC LIMIT @batch::int;

-- name: TitleReadScope :exec
-- A candidate's reply is read with its project and projectless rows visible, never a different explicit project.
SELECT set_config('fleet.project_ids', @scope::text, true);

-- name: TitleClaim :execrows
-- Reserve the attempt before the call: a crash or a lost result leaves it visible and cannot cause another call.
UPDATE session SET title_unknown_call= @attempt::text
WHERE id= @id AND title IS NULL AND title_unknown_call IS NULL AND archived_at IS NULL
 AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text
 AND coalesce(title_attempt_at,'-infinity') < @tried_before::timestamptz AND title_attempts < @tries::int;

-- name: TitleUnknown :exec
-- The call's outcome is unknown: the reservation names the call and stays.
UPDATE session SET title_unknown_call= @call::text
WHERE id= @id AND title IS NULL AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND title_unknown_call= @attempt::text;

-- name: TitleFailed :exec
UPDATE session SET title_unknown_call=NULL, title_attempt_at= @at::timestamptz, title_attempts=title_attempts+ @used::int
WHERE id= @id AND title IS NULL AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND coalesce(title_unknown_call,'')= @attempt::text;

-- name: TitleGenerated :exec
-- updated_at stays: a title is no conversation activity, so a backfill does not reorder the list. A rename during the
-- call keeps its title.
UPDATE session SET title= @title::text, title_by='auto', title_call=sqlc.narg(title_call)::text, title_attempt_at=NULL, title_attempts=0, title_unknown_call=NULL
WHERE id= @id AND title IS NULL AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND title_unknown_call= @attempt::text;

-- name: TitleRename :execrows
UPDATE session SET title= @title::text, title_by= @title_by::text, title_call=NULL, title_attempt_at=NULL, title_attempts=0, title_unknown_call=NULL
WHERE id= @id;
