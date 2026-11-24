-- name: BotGet :one
SELECT b.id, b.project_id, b.director, b.revision, b.memory_revision,
       s.name, s.purpose, s.profile_id, s.instructions, s.soul, s.schedule_time, s.schedule_zone, s.wake_on_needs_you,
       coalesce(m.memory, '') AS memory
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
LEFT JOIN bot_memory_revision m ON m.bot_id = b.id AND m.revision = b.memory_revision
WHERE b.id = $1;

-- name: BotList :many
SELECT b.id, b.project_id, b.revision, s.name, s.purpose
FROM bot b
JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
WHERE (sqlc.narg(project_id)::text IS NULL OR b.project_id = sqlc.narg(project_id))
  AND (coalesce(cardinality(sqlc.arg(project_ids)::text[]), 0) = 0 OR b.project_id = ANY (sqlc.arg(project_ids)))
  AND NOT (b.project_id = ANY (coalesce(sqlc.arg(exclude_project_ids)::text[], ARRAY[]::text[])))
ORDER BY lower(s.name), b.id;

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
