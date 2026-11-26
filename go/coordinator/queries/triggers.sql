-- Triggers (triggers.go; migration 106; docs/designs/fleet-triggers.md): timed and change wakes of a session or a bot.

-- name: TriggerGet :one
SELECT * FROM trigger WHERE id = $1;

-- name: TriggerList :many
-- The triggers of one target with the last wake each one caused (bot targets only): the run of its newest message in the
-- bot's conversations, or its newest skipped wake (trigger_log), whichever is later.
SELECT sqlc.embed(t),
       coalesce(r.state, '')::text AS last_state,
       coalesce(r.observation, '')::text AS last_observation,
       coalesce(r.reason, '')::text AS last_reason,
       coalesce(r.at, 'epoch'::timestamptz)::timestamptz AS last_run_at
FROM trigger t
LEFT JOIN LATERAL (
  SELECT x.state, x.observation, x.reason, x.at FROM (
    (SELECT (CASE WHEN e.state = 'preparing' THEN 'queued' ELSE e.state::text END) AS state, e.observation::text AS observation,
            coalesce(e.problem, '') AS reason, coalesce(e.started_at, e.updated_at) AS at
     FROM session s JOIN message m ON m.session_id = s.id JOIN execution e ON e.id = m.execution_id
     WHERE s.bot_id = t.bot_id AND m.payload->>'triggerId' = t.id
     ORDER BY m.seq DESC LIMIT 1)
    UNION ALL
    (SELECT 'skipped', 'confirmed', l.reason, l.at FROM trigger_log l WHERE l.trigger_id = t.id ORDER BY l.at DESC LIMIT 1)
  ) x ORDER BY x.at DESC LIMIT 1
) r ON t.bot_id IS NOT NULL
WHERE (sqlc.narg(session_id)::text IS NOT NULL AND t.session_id = sqlc.narg(session_id))
   OR (sqlc.narg(bot_id)::text IS NOT NULL AND t.bot_id = sqlc.narg(bot_id))
ORDER BY lower(t.name), t.id;

-- name: TriggerDue :many
-- Enabled timed triggers whose target is live; the pass decides from the time and zone which are due.
SELECT t.*
FROM trigger t
LEFT JOIN session s ON s.id = t.session_id
LEFT JOIN bot b ON b.id = t.bot_id
WHERE t.enabled AND t.when_kind <> 'manual'
  AND (t.session_id IS NULL OR s.tombstoned_at IS NULL)
  AND (t.bot_id IS NULL OR b.tombstoned_at IS NULL)
ORDER BY t.id;

-- name: TriggerCreate :execrows
INSERT INTO trigger(id, session_id, bot_id, project_id, revision, name, preset_id, text, when_kind, every_ms, daily_time,
                    daily_zone, if_changed, enabled, effective_since, change_cursor, next_at)
VALUES (@id, sqlc.narg(session_id), sqlc.narg(bot_id), sqlc.narg(project_id), 1, @name, @preset_id, @text, @when_kind,
        sqlc.narg(every_ms), @daily_time, @daily_zone, @if_changed::text[], @enabled, @effective_since, @change_cursor,
        sqlc.narg(next_at))
ON CONFLICT (id) DO NOTHING;

-- name: TriggerUpdate :execrows
UPDATE trigger
SET revision = revision + 1, name = @name, preset_id = @preset_id, text = @text, when_kind = @when_kind,
    every_ms = sqlc.narg(every_ms), daily_time = @daily_time, daily_zone = @daily_zone, if_changed = @if_changed::text[],
    enabled = @enabled, effective_since = @effective_since, change_cursor = @change_cursor, next_at = sqlc.narg(next_at)
WHERE id = @id AND revision = @revision;

-- name: TriggerDelete :execrows
DELETE FROM trigger WHERE id = @id AND revision = @revision;

-- name: TriggerDeleteIDs :exec
DELETE FROM trigger WHERE id = ANY(@ids::text[]);

-- name: TriggerState :exec
-- A pass's result: the state columns only, so a save in between keeps its settings (the revision is not touched).
UPDATE trigger
SET change_cursor = @change_cursor, next_at = sqlc.narg(next_at), last_slot = @last_slot, fires = @fires,
    last_fire_at = sqlc.narg(last_fire_at), quiet_checks = @quiet_checks, quiet_since = sqlc.narg(quiet_since),
    last_error = sqlc.narg(last_error), limits_signaled = @limits_signaled::text[]
WHERE id = @id;

-- name: TriggerSetError :exec
UPDATE trigger SET last_error = @error::text WHERE id = @id::text;

-- name: TriggerShareCursor :exec
-- After a fire the target has seen every change its change triggers watch up to the cursor: they all start from there.
UPDATE trigger
SET change_cursor = @change_cursor
WHERE id <> @id
  AND ((sqlc.narg(session_id)::text IS NOT NULL AND session_id = sqlc.narg(session_id))
    OR (sqlc.narg(bot_id)::text IS NOT NULL AND bot_id = sqlc.narg(bot_id)))
  AND cardinality(if_changed) > 0 AND change_cursor < @change_cursor;

-- name: TriggerWakeQueued :one
-- A trigger message still queued for the session: when it starts, the session reads its focus anyway, and the next
-- fire after it lists every change since.
SELECT EXISTS (SELECT 1 FROM queued_message WHERE session_id = $1 AND state = 'queued' AND payload ? 'triggerId');

-- name: TriggerLogSkip :exec
-- A bot wake of this trigger that did not run (the bot paused): the trigger's log, not a run.
INSERT INTO trigger_log(trigger_id, request_id, project_id, reason) VALUES (@trigger_id, @request_id, sqlc.narg(project_id), @reason)
ON CONFLICT (trigger_id, request_id) DO NOTHING;

-- name: TriggerSubjectScope :one
-- A bot's conversation for one subject (a problem, a question): its bot and subject; a session trigger on it sees
-- only the tasks it works (bot_task with its session), not the whole project (triggerChanges).
SELECT coalesce(bot_id, '')::text AS bot_id, coalesce(subject, '')::text AS subject FROM session WHERE id = $1;

-- name: TriggerSubjectTasks :many
SELECT task_id FROM bot_task WHERE session_id = $1 AND tombstoned_at IS NULL;
