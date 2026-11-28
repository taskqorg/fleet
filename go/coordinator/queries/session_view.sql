-- Session view (session_view): one conversation screen as summaries. No stored snapshot, dispatch or configuration column is read.

-- name: SessionViewExecutions :many
-- The newest turns of a session below before (NULL: from the newest), newest first. The page is chosen on
-- execution_session_turns (migration 111) before data is read, so only the page's rows are detoasted.
WITH page AS (
 SELECT x.id FROM execution x
 WHERE x.session_id = @session_id AND x.kind IN ('agent_run', 'host_job')
  AND (sqlc.narg(before)::bigint IS NULL OR x.seq < sqlc.narg(before)::bigint)
 ORDER BY x.seq DESC LIMIT @lim
)
SELECT e.id, e.seq, e.kind::text AS kind, e.state::text AS state, e.started_at, e.ended_at,
 split_part(coalesce(e.problem, e.data->>'error', e.data->>'errorMessage', ''), E'\n', 1)::text AS error,
 e.harness, e.model, e.host_id, coalesce(e.data->>'source', e.data->>'wakeSource', '')::text AS source
FROM page JOIN execution e ON e.id = page.id
ORDER BY e.seq DESC;

-- name: SessionViewExecutionCount :one
SELECT count(*) FROM execution WHERE session_id = @session_id AND kind IN ('agent_run', 'host_job');

-- name: SessionViewLastMessage :one
SELECT m.id, coalesce(m.text, '')::text AS text, coalesce(m.payload->'attachments', '[]')::jsonb AS attachments
FROM message m WHERE m.session_id = @session_id AND m.tombstoned_at IS NULL ORDER BY m.seq DESC LIMIT 1;

-- name: SessionViewThreadItems :many
-- The work items threads name (task:<id>, question:<id>, execution:<id>): title, current code and state, visible under
-- the caller's RLS. A run is named by its task (code and title), else by the trigger or automation that woke it (its
-- wake prompt is not a name; the thread's lastActivityAt gives the time), else by its first message line, else by its
-- kind, host and problem ("Host job on owner-mac · preparation failed: ...").
SELECT ('task:' || t.id)::text AS thread, t.title::text AS title, t.state::text AS state,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = t.id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text AS code
FROM task t WHERE t.id = ANY (@task_ids::text[])
UNION ALL
SELECT ('question:' || x.id)::text, x.text::text, x.state::text,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = x.id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text
FROM question x WHERE x.id = ANY (@question_ids::text[])
UNION ALL
SELECT ('execution:' || e.id)::text,
 coalesce(nullif(t.title, ''),
  (SELECT tr.name FROM message m JOIN trigger tr ON tr.id = m.payload->>'triggerId' WHERE m.execution_id = e.id ORDER BY m.seq LIMIT 1),
  nullif(left(split_part(btrim((SELECT m.text FROM message m WHERE m.execution_id = e.id ORDER BY m.seq LIMIT 1)), E'\n', 1), 120), ''),
  CASE e.kind WHEN 'host_job' THEN 'Host job' ELSE 'Run' END || coalesce(' on ' || e.host_id, '') || coalesce(' · ' || nullif(left(split_part(e.problem, E'\n', 1), 80), ''), ''))::text,
 e.state::text,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = e.task_id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text
FROM execution e LEFT JOIN task t ON t.id = e.task_id WHERE e.id = ANY (@execution_ids::text[]);

-- name: SessionViewOpenQuestions :many
-- Open questions of the given sessions, oldest first.
SELECT x.id, coalesce(x.session_id, '')::text AS session_id, x.kind::text AS kind, x.text, coalesce(x.options, '[]')::jsonb AS options, x.execution_id, (x.data->'artifactIds')::jsonb AS artifact_ids
FROM question x WHERE x.session_id = ANY (@session_ids::text[]) AND x.state = 'open' ORDER BY x.asked_at;

-- name: SessionViewTitle :one
-- The head's title (set or generated, else the first message's start) and the project's current code.
SELECT coalesce(nullif(s.title, ''), nullif(left(regexp_replace((SELECT m.text FROM message m WHERE m.session_id = s.id ORDER BY m.seq LIMIT 1), '\s+', ' ', 'g'), 200), ''), 'New conversation')::text AS title,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = s.project_id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text AS project_code
FROM session s WHERE s.id = @id;

-- name: ToolCallBlocks :one
-- The normalized blocks of an execution's event at seq (tool_output) over event_execution, visible under the caller's RLS.
SELECT coalesce(ev.payload->'data'->'fleetNormalized'->'blocks', '[]')::jsonb AS blocks FROM event ev WHERE ev.execution_id = @execution_id AND ev.seq = @seq;
