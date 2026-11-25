-- Session view (session_view): one conversation screen as summaries, over the execution_thread and message_thread
-- indexes (migration 109). No stored snapshot, dispatch or configuration column is read.

-- name: SessionViewExecutions :many
-- The newest turns of a session below before (NULL: from the newest), newest first. The page is chosen on
-- execution_session_turns (migration 111) before data is read, so only the page's rows are detoasted.
WITH page AS (
 SELECT x.id FROM execution x
 WHERE x.session_id = @session_id AND x.kind IN ('agent_run', 'host_job')
  AND (sqlc.narg(before)::bigint IS NULL OR x.seq < sqlc.narg(before)::bigint)
 ORDER BY x.seq DESC LIMIT @lim
)
SELECT e.id, e.seq, e.kind::text AS kind, e.state::text AS state, e.thread, e.started_at, e.ended_at,
 split_part(coalesce(e.problem, e.data->>'error', e.data->>'errorMessage', ''), E'\n', 1)::text AS error,
 e.harness, e.model, e.host_id, coalesce(e.data->>'source', e.data->>'wakeSource', '')::text AS source
FROM page JOIN execution e ON e.id = page.id
ORDER BY e.seq DESC;

-- name: SessionViewExecutionCount :one
SELECT count(*) FROM execution WHERE session_id = @session_id AND kind IN ('agent_run', 'host_job');

-- name: SessionViewThreads :many
-- Each work-item thread of a session with its turns, last activity, whether a turn is queued or running, and the
-- turns since the viewer last opened it (person_visit page thread:<session>:<thread>).
WITH runs AS (
 SELECT e.thread, count(*) AS turns, max(coalesce(e.ended_at, e.started_at)) AS last_at,
  bool_or(e.state IN ('preparing', 'running', 'waiting')) AS running
 FROM execution e WHERE e.session_id = @session_id AND e.kind = 'agent_run' GROUP BY e.thread
), msgs AS (
 SELECT m.thread, max(m.updated_at) AS last_at FROM message m WHERE m.session_id = @session_id GROUP BY m.thread
), queued AS (
 SELECT coalesce(qm.payload->>'thread', '') AS thread, count(*) AS n FROM queued_message qm WHERE qm.session_id = @session_id AND qm.execution_id IS NULL GROUP BY 1
), keys AS (
 SELECT thread FROM runs UNION SELECT thread FROM msgs UNION SELECT thread FROM queued
)
SELECT k.thread::text AS thread, coalesce(r.turns, 0)::bigint AS turns,
 greatest(r.last_at, m.last_at)::timestamptz AS last_at,
 (coalesce(r.running, false) OR coalesce(q.n, 0) > 0)::boolean AS active,
 (SELECT count(*) FROM execution u WHERE u.session_id = @session_id AND u.thread = k.thread AND u.kind = 'agent_run'
   AND u.started_at > coalesce((SELECT v.visited_at FROM person_visit v WHERE v.email = @email AND v.page = 'thread:' || @session_id || ':' || k.thread), '-infinity'))::bigint AS unread
FROM keys k LEFT JOIN runs r ON r.thread = k.thread LEFT JOIN msgs m ON m.thread = k.thread LEFT JOIN queued q ON q.thread = k.thread
ORDER BY 3 DESC NULLS LAST;

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
  (SELECT coalesce(tr.name, au.name) FROM bot_run br LEFT JOIN trigger tr ON tr.id = br.trigger_id LEFT JOIN bot_automation au ON au.id = br.automation_id
   WHERE br.execution_id = e.id ORDER BY br.seq DESC LIMIT 1),
  (SELECT tr.name FROM message m JOIN trigger tr ON tr.id = m.payload->>'triggerId' WHERE m.execution_id = e.id ORDER BY m.seq LIMIT 1),
  nullif(left(split_part(btrim((SELECT m.text FROM message m WHERE m.execution_id = e.id ORDER BY m.seq LIMIT 1)), E'\n', 1), 120), ''),
  CASE e.kind WHEN 'host_job' THEN 'Host job' ELSE 'Run' END || coalesce(' on ' || e.host_id, '') || coalesce(' · ' || nullif(left(split_part(e.problem, E'\n', 1), 80), ''), ''))::text,
 e.state::text,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = e.task_id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text
FROM execution e LEFT JOIN task t ON t.id = e.task_id WHERE e.id = ANY (@execution_ids::text[]);

-- name: SessionViewOpenQuestions :many
-- Open questions of the given sessions, oldest first.
SELECT x.id, coalesce(x.session_id, '')::text AS session_id, x.kind::text AS kind, x.text, coalesce(x.options, '[]')::jsonb AS options, x.execution_id
FROM question x WHERE x.session_id = ANY (@session_ids::text[]) AND x.state = 'open' ORDER BY x.asked_at;

-- name: SessionViewTitle :one
-- The head's title (set or generated, else the first message's start) and the project's current code.
SELECT coalesce(nullif(s.title, ''), nullif(left(regexp_replace((SELECT m.text FROM message m WHERE m.session_id = s.id ORDER BY m.seq LIMIT 1), '\s+', ' ', 'g'), 200), ''), 'New conversation')::text AS title,
 coalesce((SELECT c.code FROM entity_code c WHERE c.entity_id = s.project_id AND c.superseded_by IS NULL ORDER BY c.issued_at DESC LIMIT 1), '')::text AS project_code
FROM session s WHERE s.id = @id;

-- name: ToolCallBlocks :one
-- The normalized blocks of an execution's event at seq (tool_output) over event_execution, visible under the caller's RLS.
SELECT coalesce(ev.payload->'data'->'fleetNormalized'->'blocks', '[]')::jsonb AS blocks FROM event ev WHERE ev.execution_id = @execution_id AND ev.seq = @seq;
