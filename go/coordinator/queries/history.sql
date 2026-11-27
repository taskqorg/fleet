-- name: HistoryObjectTitle :one
-- The readable name of the one object a history_view request narrows to (its scope chip): a task's code and title, a
-- conversation's title, an experiment's lab group, a bot's code, a host's id.
SELECT coalesce(
  (SELECT coalesce(t.code || ' ', '') || t.title FROM task t WHERE t.id = sqlc.narg(task_id)::text),
  (SELECT coalesce(s.title, s.code) FROM session s WHERE s.id = sqlc.narg(session_id)::text),
  (SELECT coalesce(x.title, x.lab_group, x.goal, x.kind::text) FROM experiment x WHERE x.id = sqlc.narg(experiment_id)::text),
  (SELECT coalesce(b.code, b.id) FROM bot b WHERE b.id = sqlc.narg(bot_id)::text),
  sqlc.narg(host_id)::text,
  '')::text AS title;

-- name: HistoryBotNames :many
-- Current bot names for resolving raw bot UUID actors in history entries (OR-38).
SELECT b.id::text AS id, r.name::text AS name
FROM bot b
JOIN bot_revision r ON r.bot_id = b.id AND r.revision = b.revision
WHERE b.id = ANY(@ids::text[]);
