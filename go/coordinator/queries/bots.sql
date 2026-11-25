-- name: BotGet :one
SELECT b.id, b.code, b.project_id, b.director, b.revision, b.memory_revision,
       cv.id AS conversation_id,
       b.paused, b.paused_at, b.paused_by, b.resumed_at,
       s.name, s.purpose, s.profile_id, s.instructions, s.soul,
       s.created_at AS revision_created_at,
       coalesce(m.memory, '')::text AS memory,
       ar.id AS active_run_id,
       lr.started_at AS last_run_started_at, lr.updated_at AS last_run_at, lr.state AS last_run_state,
       av.id AS avatar_artifact_id, av.media_type AS avatar_media_type, b.avatar_still_artifact_id, b.avatar_glow,
       (coalesce(ar.state = 'waiting', false) OR EXISTS (
         SELECT 1 FROM question q JOIN session qs ON qs.id = q.session_id WHERE q.state = 'open' AND qs.bot_id = b.id
       ))::boolean AS needs_you
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN artifact av ON av.id = b.avatar_artifact_id AND av.state = 'stored'
LEFT JOIN bot_memory_revision m ON m.bot_id = b.id AND m.revision = b.memory_revision
LEFT JOIN session cv ON cv.id = (
  SELECT cs.id FROM session cs WHERE cs.bot_id = b.id AND cs.subject IS NULL AND cs.tombstoned_at IS NULL
  ORDER BY cs.archived_at NULLS FIRST, cs.seq DESC LIMIT 1
)
LEFT JOIN execution ar ON ar.id = (
  SELECT e.id FROM execution e JOIN session rs ON rs.id = e.session_id AND rs.bot_id = e.bot_id
  WHERE e.bot_id = b.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL AND e.state IN ('preparing', 'running', 'waiting')
  ORDER BY e.seq DESC LIMIT 1
)
LEFT JOIN execution lr ON lr.id = (
  SELECT e.id FROM execution e JOIN session rs ON rs.id = e.session_id AND rs.bot_id = e.bot_id
  WHERE e.bot_id = b.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL
  ORDER BY e.seq DESC LIMIT 1
)
WHERE b.id = $1;

-- name: BotList :many
SELECT b.id, b.code, b.project_id, b.revision,
       cv.id AS conversation_id,
       b.paused, s.name, s.purpose,
       coalesce(ar.id, '')::text AS active_run_id, coalesce(lr.state::text, '')::text AS last_run_state, coalesce(lr.started_at, lr.updated_at, 'epoch'::timestamptz)::timestamptz AS last_run_at,
       av.id AS avatar_artifact_id, av.media_type AS avatar_media_type, b.avatar_still_artifact_id, b.avatar_glow,
       (coalesce(ar.state = 'waiting', false) OR EXISTS (
         SELECT 1 FROM question q JOIN session qs ON qs.id = q.session_id WHERE q.state = 'open' AND qs.bot_id = b.id
       ))::boolean AS needs_you
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN artifact av ON av.id = b.avatar_artifact_id AND av.state = 'stored'
LEFT JOIN session cv ON cv.id = (
  SELECT cs.id FROM session cs WHERE cs.bot_id = b.id AND cs.subject IS NULL AND cs.tombstoned_at IS NULL
  ORDER BY cs.archived_at NULLS FIRST, cs.seq DESC LIMIT 1
)
-- Each bot's active and latest run read in place (LATERAL over execution_bot_seq): joining by an id subquery made the
-- planner hash every run of every bot through the row policies (prod db_explain 2026-11-26, bot_run then). LATERAL
-- columns read as not null to sqlc, so the missing ones come as '' and the epoch.
LEFT JOIN LATERAL (
  SELECT e.id, e.state FROM execution e JOIN session rs ON rs.id = e.session_id AND rs.bot_id = e.bot_id
  WHERE e.bot_id = b.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL AND e.state IN ('preparing', 'running', 'waiting')
  ORDER BY e.seq DESC LIMIT 1
) ar ON true
LEFT JOIN LATERAL (
  SELECT e.state, e.started_at, e.updated_at FROM execution e JOIN session rs ON rs.id = e.session_id AND rs.bot_id = e.bot_id
  WHERE e.bot_id = b.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL
  ORDER BY e.seq DESC LIMIT 1
) lr ON true
WHERE (sqlc.narg(project_id)::text IS NULL OR b.project_id = sqlc.narg(project_id))
  AND (coalesce(cardinality(sqlc.arg(project_ids)::text[]), 0) = 0 OR b.project_id = ANY (sqlc.arg(project_ids)))
  AND NOT (b.project_id = ANY (coalesce(sqlc.arg(exclude_project_ids)::text[], ARRAY[]::text[])))
ORDER BY lower(s.name), b.id;

-- name: BotCreate :execrows
INSERT INTO bot(id, project_id, director, revision)
VALUES ($1, $2, $3, 1)
ON CONFLICT (id) DO NOTHING;

-- name: BotSettingsRevisionInsert :exec
INSERT INTO bot_revision(bot_id, revision, name, purpose, profile_id, instructions, soul)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: BotSettingsAdvance :execrows
UPDATE bot SET revision = revision + 1 WHERE id = $1 AND revision = $2;

-- name: BotMemoryRevisionInsert :exec
INSERT INTO bot_memory_revision(bot_id, revision, memory) VALUES ($1, $2, $3);

-- name: BotMemoryAdvance :execrows
UPDATE bot SET memory_revision = memory_revision + 1 WHERE id = $1 AND memory_revision = $2;

-- name: BotSetPaused :exec
UPDATE bot
SET paused = $2,
    paused_at = CASE WHEN $2 THEN now() ELSE paused_at END,
    paused_by = CASE WHEN $2 THEN $3 ELSE paused_by END,
    resumed_at = CASE WHEN NOT $2 THEN now() ELSE resumed_at END
WHERE id = $1;

-- name: BotRunsList :many
-- A bot's runs: the agent runs of its conversations (fleet-conversations.md, migration step 4), newest first. The
-- wake's cause and quiet verdict are in the run's botContext (no cause: a message); who sent it in its first message.
SELECT e.id, e.bot_id::text AS bot_id, coalesce(e.project_id, '')::text AS project_id, e.session_id,
       coalesce((e.data->'botContext'->>'botRevision')::bigint, 0)::bigint AS bot_revision,
       coalesce((e.data->'botContext'->>'memoryRevision')::bigint, 0)::bigint AS memory_revision,
       coalesce(e.data->'botContext'->>'cause', 'message')::text AS cause,
       (CASE WHEN e.state = 'preparing' THEN 'queued' ELSE e.state::text END)::text AS state,
       e.observation::text AS observation,
       coalesce((e.data->'botContext'->>'quiet')::boolean, false)::boolean AS quiet,
       coalesce(nullif(e.data->>'error', ''), nullif(e.problem, ''), e.data->>'stopDetail', '')::text AS reason,
       coalesce(m.sent_by, '')::text AS sent_by, coalesce(m.source, '')::text AS source, e.host_id, e.problem,
       tr.id AS trigger_id, coalesce(tr.name, '')::text AS trigger_name, coalesce(tr.revision, 0)::int AS trigger_revision,
       coalesce(to_timestamp((e.data->>'createdAt')::float8 / 1000), e.started_at, e.updated_at)::timestamptz AS created_at,
       e.started_at, e.ended_at, e.seq
FROM execution e
JOIN session s ON s.id = e.session_id AND s.bot_id = e.bot_id
LEFT JOIN LATERAL (SELECT m.sent_by, m.source, m.payload FROM message m WHERE m.execution_id = e.id ORDER BY m.seq LIMIT 1) m ON true
LEFT JOIN trigger tr ON tr.id = m.payload->>'triggerId'
WHERE e.bot_id IS NOT NULL AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL
  AND (sqlc.narg(id)::text IS NULL OR e.id = sqlc.narg(id))
  AND (sqlc.narg(bot_id)::text IS NULL OR e.bot_id = sqlc.narg(bot_id))
  AND (sqlc.narg(trigger_id)::text IS NULL OR m.payload->>'triggerId' = sqlc.narg(trigger_id))
  AND (sqlc.narg(project_id)::text IS NULL OR e.project_id = sqlc.narg(project_id))
  AND (coalesce(cardinality(sqlc.arg(project_ids)::text[]), 0) = 0 OR e.project_id = ANY (sqlc.arg(project_ids)))
  AND NOT (coalesce(e.project_id, '') = ANY (coalesce(sqlc.arg(exclude_project_ids)::text[], ARRAY[]::text[])))
  AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN e.state = 'preparing' THEN 'queued' ELSE e.state::text END) = sqlc.narg(state))
  AND (sqlc.narg(cause)::text IS NULL OR coalesce(e.data->'botContext'->>'cause', 'message') = sqlc.narg(cause))
  AND (sqlc.narg(observation)::text IS NULL OR e.observation::text = sqlc.narg(observation))
  AND (sqlc.narg(quiet)::boolean IS NULL OR coalesce((e.data->'botContext'->>'quiet')::boolean, false) = sqlc.narg(quiet))
  AND (sqlc.narg(after_seq)::bigint IS NULL OR (e.seq, e.id) < (sqlc.narg(after_seq)::bigint, sqlc.narg(after_id)::text))
ORDER BY e.seq DESC, e.id DESC
LIMIT sqlc.arg(lim)::int;

-- name: BotRunByRequest :one
-- The run a bot wake with this request id (its dedup key) started in one of the bot's conversations.
SELECT m.execution_id::text FROM message m JOIN session s ON s.id = m.session_id
WHERE s.bot_id = $1 AND m.request_id = $2 AND m.execution_id IS NOT NULL
LIMIT 1;

-- name: BotRunActive :one
-- The bot's newest run that has not ended, in any of its conversations.
SELECT e.id FROM execution e JOIN session s ON s.id = e.session_id AND s.bot_id = e.bot_id
WHERE e.bot_id = $1 AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL AND e.state IN ('preparing', 'running', 'waiting')
ORDER BY e.seq DESC LIMIT 1;

-- name: BotRunSetQuiet :exec
-- A wake's verdict once its run succeeded: quiet when it changed nothing (BotExecutionMutated).
UPDATE execution SET data = jsonb_set(data, '{botContext,quiet}', to_jsonb(@quiet::boolean))
WHERE id = @id AND data ? 'botContext';

-- name: BotExecutionMutated :one
SELECT (
  EXISTS (SELECT 1 FROM session s WHERE s.created_by_execution_id = @execution_id::text AND s.tombstoned_at IS NULL)
  OR EXISTS (SELECT 1 FROM question qu WHERE qu.execution_id = @execution_id::text AND qu.tombstoned_at IS NULL)
  OR EXISTS (SELECT 1 FROM audit_log al WHERE al.actor = 'execution:' || @execution_id::text)
  OR EXISTS (SELECT 1 FROM change ch WHERE ch.actor = 'execution:' || @execution_id::text AND ch.kind IN ('task', 'question', 'session', 'bot', 'board', 'delivery'))
)::boolean AS mutated;

-- name: BotOwnExecutions :many
SELECT e.id
FROM execution e
WHERE e.session_id = $1
   OR e.session_id IN (SELECT s.id FROM session s WHERE s.created_by_session_id = $1);

-- name: BotLastSessionDispatch :one
SELECT id,
       coalesce(project_id, data->'dispatch'->>'projectId', '')::text AS project_id,
       coalesce(host_id, '')::text AS host_id,
       coalesce(harness, data->'dispatch'->>'harness', '')::text AS harness,
       coalesce(profile_id, data->'agentConfig'->>'id', '')::text AS profile_id,
       coalesce(data->'dispatch'->>'workspaceKey', '')::text AS workspace_key,
       coalesce(data->'dispatch'->>'workingDirectory', data->'workspaceSnapshot'->>'workingDirectory', '')::text AS working_directory,
       coalesce(data->'workspaceSnapshot'->'project', '{}'::jsonb)::jsonb AS workspace_project,
       coalesce(data->'effectiveAccount'->>'binding', '')::text AS account_binding
FROM execution
WHERE session_id = $1 AND id <> $2 AND tombstoned_at IS NULL AND kind = 'agent_run' AND host_id IS NOT NULL
ORDER BY seq DESC
LIMIT 1;

-- name: BotTaskUpsert :exec
INSERT INTO bot_task(bot_id, task_id, project_id, session_id, execution_id, link_kind, created_by)
VALUES ($1, $2, $3, sqlc.narg(session_id), sqlc.narg(execution_id), $4, $5)
ON CONFLICT (bot_id, task_id) DO UPDATE SET
  project_id = excluded.project_id,
  session_id = coalesce(excluded.session_id, bot_task.session_id),
  execution_id = coalesce(excluded.execution_id, bot_task.execution_id),
  link_kind = CASE WHEN bot_task.tombstoned_at IS NULL AND bot_task.link_kind = 'created' THEN bot_task.link_kind ELSE excluded.link_kind END,
  tombstoned_at = NULL,
  updated_at = now();

-- name: BotTaskGetByTask :one
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.session_id, bt.execution_id,
       bt.link_kind, bt.created_by, bt.created_at
FROM bot_task bt
JOIN bot b ON b.id = bt.bot_id AND b.tombstoned_at IS NULL
JOIN bot_revision br ON br.bot_id = b.id AND br.revision = b.revision
WHERE bt.task_id = $1 AND bt.tombstoned_at IS NULL
ORDER BY bt.seq DESC
LIMIT 1;

-- name: BotTaskGet :one
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.session_id, bt.execution_id,
       bt.link_kind, bt.created_by, bt.created_at, bt.seq,
       t.code AS task_code, t.title,
       t.state::text AS state, t.wait_reason, t.pause_reason, t.priority,
       t.accepted_at, t.accepted_by, t.updated_at,
       v.open_questions, v.execution_count, v.executions_by_state
FROM bot_task bt
JOIN bot b ON b.id = bt.bot_id AND b.tombstoned_at IS NULL
JOIN bot_revision br ON br.bot_id = b.id AND br.revision = b.revision
JOIN task t ON t.id = bt.task_id AND t.tombstoned_at IS NULL
JOIN task_view v ON v.task_id = t.id
WHERE bt.bot_id = $1 AND bt.task_id = $2 AND bt.tombstoned_at IS NULL;

-- name: BotTasksList :many
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.session_id, bt.execution_id,
       bt.link_kind, bt.created_by, bt.created_at, bt.seq,
       t.code AS task_code, t.title,
       t.state::text AS state, t.wait_reason, t.pause_reason, t.priority,
       t.accepted_at, t.accepted_by, t.updated_at,
       v.open_questions, v.execution_count, v.executions_by_state
FROM bot_task bt
JOIN bot b ON b.id = bt.bot_id AND b.tombstoned_at IS NULL
JOIN bot_revision br ON br.bot_id = b.id AND br.revision = b.revision
JOIN task t ON t.id = bt.task_id AND t.tombstoned_at IS NULL
JOIN task_view v ON v.task_id = t.id
WHERE bt.bot_id = @bot_id::text
  AND bt.tombstoned_at IS NULL
  AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN t.state = 'paused' AND t.pause_reason = 'held' THEN 'held' ELSE t.state::text END) = sqlc.narg(state))
  AND (sqlc.narg(after_seq)::bigint IS NULL OR (bt.seq, bt.task_id) < (sqlc.narg(after_seq)::bigint, sqlc.narg(after_task_id)::text))
ORDER BY bt.seq DESC, bt.task_id DESC
LIMIT sqlc.arg(lim)::int;

-- name: BotTasksByRunIDs :many
SELECT execution_id::text AS execution_id, task_id
FROM bot_task
WHERE execution_id = ANY(@run_ids::text[]) AND tombstoned_at IS NULL
ORDER BY seq;

-- name: BotAvatarSet :execrows
-- The bot's avatar (bot_avatar_set, bot_avatar_clear): NULL artifact clears it back to the letter.
UPDATE bot SET avatar_artifact_id = sqlc.narg(avatar_artifact_id), avatar_still_artifact_id = sqlc.narg(avatar_still_artifact_id),
       avatar_glow = sqlc.arg(avatar_glow)
WHERE id = sqlc.arg(id) AND tombstoned_at IS NULL;

-- name: BotConversationBySubject :one
-- A bot's conversation for one subject (fleet-conversations.md#rules), an open one before an archived one; no
-- subject: its newest conversation without one (person messages and direct runs).
SELECT id FROM session
WHERE bot_id = @bot_id AND subject IS NOT DISTINCT FROM sqlc.narg(subject)::text AND tombstoned_at IS NULL
ORDER BY archived_at NULLS FIRST, seq DESC
LIMIT 1;

-- name: BotRenameConversations :exec
-- A renamed bot's conversations without a subject that still carry its name.
UPDATE session SET title = @title::text WHERE bot_id = @bot_id::text AND subject IS NULL AND title_by = 'bot' AND tombstoned_at IS NULL;

-- name: SessionSetSubject :exec
UPDATE session SET subject = $2 WHERE id = $1;

-- name: BotSessionIDs :many
-- Every conversation of a bot: its Home and one per subject.
SELECT id FROM session WHERE bot_id = $1 AND tombstoned_at IS NULL;

-- name: BotTriggerWakeQueued :one
-- A message of this trigger still queued in one of the bot's conversations.
SELECT EXISTS (SELECT 1 FROM queued_message qm JOIN session s ON s.id = qm.session_id
 WHERE s.bot_id = $1 AND qm.state = 'queued' AND qm.payload->>'triggerId' = $2::text);
