-- name: BotGet :one
SELECT b.id, b.project_id, b.director, b.revision, b.memory_revision,
       hs.id AS home_session_id,
       b.paused, b.paused_at, b.paused_by, b.resumed_at,
       s.name, s.purpose, s.profile_id, s.instructions, s.soul, s.schedule_time, s.schedule_zone, s.wake_on_needs_you,
       s.created_at AS revision_created_at,
       coalesce(m.memory, '')::text AS memory,
       ar.id AS active_run_id, ar.execution_id AS active_execution_id,
       lr.created_at AS last_run_at, lr.state AS last_run_state
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN bot_memory_revision m ON m.bot_id = b.id AND m.revision = b.memory_revision
LEFT JOIN session hs ON hs.id = b.home_session_id AND hs.tombstoned_at IS NULL
LEFT JOIN bot_run ar ON ar.id = (
  SELECT r.id FROM bot_run r
  WHERE r.bot_id = b.id AND r.tombstoned_at IS NULL AND r.execution_id IS NOT NULL AND r.state IN ('queued', 'running', 'waiting')
  ORDER BY r.seq DESC LIMIT 1
)
LEFT JOIN bot_run lr ON lr.id = (
  SELECT r.id FROM bot_run r
  WHERE r.bot_id = b.id AND r.tombstoned_at IS NULL
  ORDER BY r.seq DESC LIMIT 1
)
WHERE b.id = $1;

-- name: BotList :many
SELECT b.id, b.project_id, b.revision,
       hs.id AS home_session_id,
       b.paused, s.name, s.purpose,
       ar.id AS active_run_id, lr.state AS last_run_state, lr.created_at AS last_run_at
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN session hs ON hs.id = b.home_session_id AND hs.tombstoned_at IS NULL
LEFT JOIN bot_run ar ON ar.id = (
  SELECT r.id FROM bot_run r
  WHERE r.bot_id = b.id AND r.tombstoned_at IS NULL AND r.execution_id IS NOT NULL AND r.state IN ('queued', 'running', 'waiting')
  ORDER BY r.seq DESC LIMIT 1
)
LEFT JOIN bot_run lr ON lr.id = (
  SELECT r.id FROM bot_run r
  WHERE r.bot_id = b.id AND r.tombstoned_at IS NULL
  ORDER BY r.seq DESC LIMIT 1
)
WHERE (sqlc.narg(project_id)::text IS NULL OR b.project_id = sqlc.narg(project_id))
  AND (coalesce(cardinality(sqlc.arg(project_ids)::text[]), 0) = 0 OR b.project_id = ANY (sqlc.arg(project_ids)))
  AND NOT (b.project_id = ANY (coalesce(sqlc.arg(exclude_project_ids)::text[], ARRAY[]::text[])))
ORDER BY lower(s.name), b.id;

-- name: BotListForTriggers :many
SELECT b.id, b.project_id, b.director, b.revision, b.memory_revision,
       b.home_session_id, b.paused, b.paused_at, b.resumed_at,
       s.name, s.purpose, s.profile_id, s.instructions, s.soul, s.schedule_time, s.schedule_zone, s.wake_on_needs_you,
       s.created_at AS revision_created_at,
       coalesce(m.memory, '') AS memory
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN bot_memory_revision m ON m.bot_id = b.id AND m.revision = b.memory_revision
WHERE b.tombstoned_at IS NULL
  AND (s.schedule_time <> '' OR s.wake_on_needs_you)
ORDER BY b.id;

-- name: BotCreate :execrows
INSERT INTO bot(id, project_id, director, revision)
VALUES ($1, $2, $3, 1)
ON CONFLICT (id) DO NOTHING;

-- name: BotSettingsRevisionInsert :exec
INSERT INTO bot_revision(bot_id, revision, name, purpose, profile_id, instructions, soul, schedule_time, schedule_zone, wake_on_needs_you)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: BotSettingsAdvance :execrows
UPDATE bot SET revision = revision + 1 WHERE id = $1 AND revision = $2;

-- name: BotMemoryRevisionInsert :exec
INSERT INTO bot_memory_revision(bot_id, revision, memory) VALUES ($1, $2, $3);

-- name: BotMemoryAdvance :execrows
UPDATE bot SET memory_revision = memory_revision + 1 WHERE id = $1 AND memory_revision = $2;

-- name: BotSetHomeSession :exec
UPDATE bot SET home_session_id = $2 WHERE id = $1;

-- name: BotSetPaused :exec
UPDATE bot
SET paused = $2,
    paused_at = CASE WHEN $2 THEN now() ELSE paused_at END,
    paused_by = CASE WHEN $2 THEN $3 ELSE paused_by END,
    resumed_at = CASE WHEN NOT $2 THEN now() ELSE resumed_at END
WHERE id = $1;

-- name: BotByHomeSession :one
SELECT b.id
FROM bot b
WHERE b.home_session_id = $1 AND b.tombstoned_at IS NULL
LIMIT 1;

-- name: BotWakeCursorGet :one
SELECT bot_id, project_id, needs_you_change_seq, last_schedule_key, last_schedule_at, updated_at
FROM bot_wake_cursor
WHERE bot_id = $1;

-- name: BotWakeCursorUpsert :exec
INSERT INTO bot_wake_cursor(bot_id, project_id, needs_you_change_seq, last_schedule_key, last_schedule_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (bot_id) DO UPDATE SET
  project_id = excluded.project_id,
  needs_you_change_seq = excluded.needs_you_change_seq,
  last_schedule_key = excluded.last_schedule_key,
  last_schedule_at = excluded.last_schedule_at,
  updated_at = now();

-- name: BotRunInsert :execrows
INSERT INTO bot_run(id, bot_id, project_id, bot_revision, memory_revision, session_id, execution_id, queued_message_id, cause, dedup_key, state, observation, quiet, reason, sent_by, source, payload, automation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT DO NOTHING;

-- name: BotRunGet :one
SELECT br.id, br.bot_id, br.project_id, br.bot_revision, br.memory_revision, br.session_id, br.execution_id, br.queued_message_id,
       br.cause, br.dedup_key,
       (CASE WHEN e.id IS NOT NULL THEN (CASE WHEN e.state::text = 'preparing' THEN 'queued' ELSE e.state::text END) ELSE br.state END)::text AS state,
       (CASE WHEN e.id IS NOT NULL THEN e.observation::text ELSE br.observation END)::text AS observation,
       br.quiet, br.reason, br.sent_by, br.source, e.host_id, e.problem,
       br.created_at, coalesce(e.started_at, br.started_at) AS started_at, coalesce(e.ended_at, br.ended_at) AS ended_at,
       br.payload, br.seq
FROM bot_run br
LEFT JOIN execution e ON e.id = br.execution_id
WHERE br.id = $1;

-- name: BotRunByDedup :one
SELECT id FROM bot_run
WHERE bot_id = $1 AND dedup_key = $2 AND tombstoned_at IS NULL
LIMIT 1;

-- name: BotHandledDedupPayloads :many
SELECT dedup_key, payload
FROM bot_run
WHERE bot_id = $1 AND tombstoned_at IS NULL AND cause = 'needs_you'
  AND automation_id IS NOT DISTINCT FROM sqlc.narg(automation_id)::text
  AND (state <> 'skipped' OR reason = 'paused');

-- name: BotRunByExecution :one
SELECT id, bot_id, state, observation, cause, quiet, reason, started_at, ended_at
FROM bot_run
WHERE execution_id = $1 AND tombstoned_at IS NULL
FOR UPDATE;

-- name: BotRunByQueuedMessage :one
SELECT id, bot_id, state, observation, cause, quiet, reason
FROM bot_run
WHERE queued_message_id = $1 AND tombstoned_at IS NULL
FOR UPDATE;

-- name: BotRunActiveForBot :one
SELECT br.id, br.execution_id, br.session_id, br.queued_message_id, br.state, br.observation
FROM bot_run br
LEFT JOIN execution e ON e.id = br.execution_id
WHERE br.bot_id = $1 AND br.tombstoned_at IS NULL
  AND (
    (br.execution_id IS NOT NULL AND coalesce(e.state::text, br.state) IN ('preparing', 'queued', 'running', 'waiting'))
    OR (br.execution_id IS NULL AND br.state IN ('queued', 'running', 'waiting') AND br.queued_message_id IS NULL)
  )
ORDER BY br.seq DESC
LIMIT 1;

-- name: BotRunLockState :one
SELECT state FROM bot_run WHERE id = $1 FOR UPDATE;

-- name: BotRunMove :exec
UPDATE bot_run
SET state = @state,
    observation = coalesce(sqlc.narg(observation), observation),
    quiet = CASE WHEN @set_quiet::boolean THEN @quiet::boolean ELSE quiet END,
    reason = CASE WHEN @set_reason::boolean THEN @reason::text ELSE reason END,
    started_at = CASE WHEN @set_started::boolean THEN coalesce(started_at, sqlc.narg(started_at)) ELSE started_at END,
    ended_at = CASE WHEN @set_ended::boolean THEN coalesce(ended_at, sqlc.narg(ended_at), now()) ELSE ended_at END
WHERE id = @id;

-- name: BotRunSyncFields :exec
UPDATE bot_run
SET observation = coalesce(sqlc.narg(observation), observation),
    quiet = CASE WHEN @set_quiet::boolean THEN @quiet::boolean ELSE quiet END,
    reason = CASE WHEN @set_reason::boolean THEN @reason::text ELSE reason END,
    started_at = CASE WHEN @set_started::boolean THEN coalesce(started_at, sqlc.narg(started_at)) ELSE started_at END,
    ended_at = CASE WHEN @set_ended::boolean THEN coalesce(ended_at, sqlc.narg(ended_at)) ELSE ended_at END
WHERE id = @id;

-- name: BotRunAttachExecution :exec
UPDATE bot_run
SET execution_id = $2,
    bot_revision = $3,
    memory_revision = $4,
    project_id = $5
WHERE id = $1;

-- name: BotRunsList :many
SELECT br.id, br.bot_id, br.project_id, br.bot_revision, br.memory_revision, br.session_id, br.execution_id, br.queued_message_id,
       br.cause, br.dedup_key,
       (CASE WHEN e.id IS NOT NULL THEN (CASE WHEN e.state::text = 'preparing' THEN 'queued' ELSE e.state::text END) ELSE br.state END)::text AS state,
       (CASE WHEN e.id IS NOT NULL THEN e.observation::text ELSE br.observation END)::text AS observation,
       br.quiet, br.reason, br.sent_by, br.source, e.host_id, e.problem,
       br.created_at, coalesce(e.started_at, br.started_at) AS started_at, coalesce(e.ended_at, br.ended_at) AS ended_at,
       br.payload, br.seq
FROM bot_run br
LEFT JOIN execution e ON e.id = br.execution_id
WHERE br.tombstoned_at IS NULL
  AND (sqlc.narg(bot_id)::text IS NULL OR br.bot_id = sqlc.narg(bot_id))
  AND (sqlc.narg(automation_id)::text IS NULL OR br.automation_id = sqlc.narg(automation_id))
  AND (sqlc.narg(project_id)::text IS NULL OR br.project_id = sqlc.narg(project_id))
  AND (coalesce(cardinality(sqlc.arg(project_ids)::text[]), 0) = 0 OR br.project_id = ANY (sqlc.arg(project_ids)))
  AND NOT (br.project_id = ANY (coalesce(sqlc.arg(exclude_project_ids)::text[], ARRAY[]::text[])))
  AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN e.id IS NOT NULL THEN (CASE WHEN e.state::text = 'preparing' THEN 'queued' ELSE e.state::text END) ELSE br.state END) = sqlc.narg(state))
  AND (sqlc.narg(cause)::text IS NULL OR br.cause = sqlc.narg(cause))
  AND (sqlc.narg(observation)::text IS NULL OR (CASE WHEN e.id IS NOT NULL THEN e.observation::text ELSE br.observation END) = sqlc.narg(observation))
  AND (sqlc.narg(quiet)::boolean IS NULL OR br.quiet = sqlc.narg(quiet))
  AND (sqlc.narg(after_seq)::bigint IS NULL OR (br.seq, br.id) < (sqlc.narg(after_seq)::bigint, sqlc.narg(after_id)::text))
ORDER BY br.seq DESC, br.id DESC
LIMIT sqlc.arg(lim)::int;

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

-- name: BotNeedsYouQuestions :many
SELECT id, code, project_id, task_id, execution_id, session_id, text, asked_by
FROM question
WHERE project_id = @project_id::text
  AND state = 'open'
  AND (sqlc.narg(enabled_since)::timestamptz IS NULL OR asked_at >= sqlc.narg(enabled_since))
  AND tombstoned_at IS NULL
  AND (sqlc.narg(ids)::text[] IS NULL OR id = ANY(sqlc.narg(ids)::text[]))
ORDER BY seq;

-- name: BotNeedsYouExecutions :many
SELECT id, project_id, task_id, session_id, state::text AS state, problem
FROM execution
WHERE project_id = @project_id::text
  AND tombstoned_at IS NULL
  AND problem IS NOT NULL
  AND (sqlc.narg(enabled_since)::timestamptz IS NULL OR updated_at >= sqlc.narg(enabled_since))
  AND problem NOT IN ('unknown:abandoned', 'cancelled')
  AND kind <> 'tool_run'
  AND NOT EXISTS (SELECT 1 FROM problem_dismissal x WHERE x.execution_id = execution.id)
  AND NOT (state IN ('succeeded', 'failed', 'cancelled') AND (problem LIKE 'unregistered host %' OR problem LIKE 'unregistered account %' OR problem LIKE 'no registered environment of project %'))
  AND NOT (kind = 'pipeline_step' AND EXISTS (SELECT 1 FROM execution r WHERE r.kind = 'pipeline_run' AND r.problem IS NOT NULL AND r.id = coalesce(execution.parent_execution_id, split_part(execution.id, ':step:', 1))))
  AND EXISTS (SELECT 1 FROM definition d WHERE d.kind = 'project' AND d.id = execution.project_id)
  AND (sqlc.narg(ids)::text[] IS NULL OR id = ANY(sqlc.narg(ids)::text[]))
ORDER BY seq;

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

-- name: BotRunIDByExecution :one
SELECT id
FROM bot_run
WHERE execution_id = $1 AND tombstoned_at IS NULL
LIMIT 1;

-- name: BotTaskUpsert :exec
INSERT INTO bot_task(bot_id, task_id, project_id, bot_run_id, session_id, execution_id, link_kind, created_by)
VALUES ($1, $2, $3, sqlc.narg(bot_run_id), sqlc.narg(session_id), sqlc.narg(execution_id), $4, $5)
ON CONFLICT (bot_id, task_id) DO UPDATE SET
  project_id = excluded.project_id,
  bot_run_id = coalesce(excluded.bot_run_id, bot_task.bot_run_id),
  session_id = coalesce(excluded.session_id, bot_task.session_id),
  execution_id = coalesce(excluded.execution_id, bot_task.execution_id),
  link_kind = CASE WHEN bot_task.tombstoned_at IS NULL AND bot_task.link_kind = 'created' THEN bot_task.link_kind ELSE excluded.link_kind END,
  tombstoned_at = NULL,
  updated_at = now();

-- name: BotTaskGetByTask :one
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.bot_run_id, bt.session_id, bt.execution_id,
       bt.link_kind, bt.created_by, bt.created_at
FROM bot_task bt
JOIN bot b ON b.id = bt.bot_id AND b.tombstoned_at IS NULL
JOIN bot_revision br ON br.bot_id = b.id AND br.revision = b.revision
WHERE bt.task_id = $1 AND bt.tombstoned_at IS NULL
ORDER BY bt.seq DESC
LIMIT 1;

-- name: BotTaskGet :one
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.bot_run_id, bt.session_id, bt.execution_id,
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
SELECT bt.bot_id, br.name AS bot_name, bt.task_id, bt.project_id, bt.bot_run_id, bt.session_id, bt.execution_id,
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
SELECT bot_run_id, task_id
FROM bot_task
WHERE bot_run_id = ANY(@run_ids::text[]) AND tombstoned_at IS NULL
ORDER BY seq;
