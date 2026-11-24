-- name: BotAutomationGet :one
SELECT * FROM bot_automation WHERE bot_id = $1 AND id = $2;

-- name: BotAutomationList :many
SELECT sqlc.embed(a),
       coalesce(CASE WHEN e.state::text = 'preparing' THEN 'queued' ELSE e.state::text END, r.state, '')::text AS last_state,
       coalesce(e.observation::text, r.observation, '')::text AS last_observation,
       coalesce(e.problem, r.reason, '')::text AS last_reason,
       coalesce(r.created_at, 'epoch'::timestamptz) AS last_run_at
FROM bot_automation a
LEFT JOIN LATERAL (
  SELECT br.state, br.observation, br.reason, br.created_at, br.execution_id
  FROM bot_run br
  WHERE br.automation_id = a.id AND br.tombstoned_at IS NULL
  ORDER BY br.seq DESC LIMIT 1
) r ON true
LEFT JOIN execution e ON e.id = r.execution_id
WHERE a.bot_id = $1
ORDER BY lower(a.name), a.id;

-- name: BotAutomationTriggers :many
SELECT a.*
FROM bot_automation a
JOIN bot b ON b.id = a.bot_id
WHERE a.enabled AND a.kind <> 'manual' AND b.tombstoned_at IS NULL
ORDER BY a.bot_id, a.id;

-- name: BotAutomationCreate :execrows
INSERT INTO bot_automation(id, bot_id, project_id, revision, name, instructions, kind, schedule_time,
                          schedule_zone, event, enabled, effective_since, event_cursor)
VALUES ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO NOTHING;

-- name: BotAutomationUpdate :execrows
UPDATE bot_automation
SET revision = revision + 1, name = @name, instructions = @instructions, kind = @kind,
    schedule_time = @schedule_time, schedule_zone = @schedule_zone, event = @event,
    enabled = @enabled, effective_since = @effective_since, event_cursor = @event_cursor
WHERE id = @id AND bot_id = @bot_id AND revision = @revision;

-- name: BotAutomationScheduleAdvance :exec
UPDATE bot_automation SET last_schedule_date = $2 WHERE id = $1;

-- name: BotAutomationEventAdvance :exec
UPDATE bot_automation SET event_cursor = $2 WHERE id = $1;
