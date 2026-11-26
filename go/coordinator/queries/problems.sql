-- Problems (problems.go, docs/designs/fleet-system-management.md#problems-anomalies-with-a-bot-owner): problems.go
-- alone writes these rows.

-- name: ProblemOpenByCause :one
SELECT id, misses FROM problem WHERE cause = $1 AND state = 'open' FOR UPDATE;

-- name: ProblemRecentClosed :one
-- The newest closed row of cause closed after since: a returning cause reopens it.
SELECT id FROM problem WHERE cause = @cause AND state = 'closed' AND closed_at > @since::timestamptz
ORDER BY closed_at DESC LIMIT 1 FOR UPDATE;

-- name: ProblemInsert :exec
INSERT INTO problem(id, cause, source, rule, project_id, text, data, owner_bot_id)
VALUES (@id, @cause, @source, @rule, sqlc.narg(project_id), @text, @data::jsonb, sqlc.narg(owner_bot_id));

-- name: ProblemReopen :exec
UPDATE problem SET state = 'open', phase = 'new', phase_at = now(), closed_at = NULL, close_reason = NULL, misses = 0,
 count = count + 1, seen_at = now(), reopened = reopened + 1, text = @text, data = @data::jsonb,
 owner_bot_id = coalesce(owner_bot_id, sqlc.narg(owner_bot_id))
WHERE id = @id;

-- name: ProblemSeen :exec
-- A raise of an open cause: one more occurrence; text and data refreshed when given.
UPDATE problem SET count = count + 1, seen_at = now(), misses = 0,
 text = coalesce(sqlc.narg(text), text), data = coalesce(sqlc.narg(data)::jsonb, data)
WHERE id = @id;

-- name: ProblemPresent :exec
-- A sync source still sees the cause: its miss count clears; text refreshed when given.
UPDATE problem SET misses = 0, seen_at = now(), text = coalesce(sqlc.narg(text), text) WHERE id = @id;

-- name: ProblemMissed :exec
UPDATE problem SET misses = @misses WHERE id = @id;

-- name: ProblemsClose :exec
UPDATE problem SET state = 'closed', closed_at = now(), close_reason = @reason::text
WHERE id = ANY(@ids::text[]) AND state = 'open';

-- name: ProblemsOpenOf :many
-- Open problems holding one of causes, a cause starting with prefix, or a cause matching pattern.
SELECT id, cause FROM problem WHERE state = 'open'
 AND (cause = ANY(sqlc.narg(causes)::text[]) OR starts_with(cause, sqlc.narg(prefix)::text) OR cause ~ sqlc.narg(pattern)::text)
ORDER BY id FOR UPDATE;

-- name: ProblemsOpenOfSource :many
SELECT id, cause, misses FROM problem WHERE state = 'open' AND source = @source::text AND cause LIKE 'problem:%' ORDER BY id;

-- name: ProblemIsOpen :one
SELECT EXISTS(SELECT 1 FROM problem WHERE cause = $1 AND state = 'open');

-- name: ProblemsList :many
SELECT * FROM problem
WHERE (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
 AND (sqlc.narg(phase)::text IS NULL OR phase = sqlc.narg(phase)::text)
 AND (sqlc.narg(owner_bot_id)::text IS NULL OR owner_bot_id = sqlc.narg(owner_bot_id)::text)
 AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id)::text)
 AND (sqlc.narg(before)::bigint IS NULL OR seq < sqlc.narg(before)::bigint)
ORDER BY seq DESC LIMIT @lim;

-- name: ProblemGet :one
SELECT * FROM problem WHERE id = $1;

-- name: ProblemBySeq :one
SELECT * FROM problem WHERE seq = $1;

-- name: ProblemOpenGet :one
SELECT * FROM problem WHERE cause = $1 AND state = 'open';

-- name: ProblemUpdate :exec
-- The owner's update: phase, note, work; a phase change resets the quiet wake count.
UPDATE problem SET phase = coalesce(sqlc.narg(phase)::text, phase),
 phase_at = CASE WHEN sqlc.narg(phase)::text IS NOT NULL AND sqlc.narg(phase)::text <> phase THEN now() ELSE phase_at END,
 note = coalesce(sqlc.narg(note)::text, note), work = coalesce(sqlc.narg(work)::text, work),
 quiet_wakes = CASE WHEN sqlc.narg(phase)::text IS NOT NULL OR sqlc.narg(note)::text IS NOT NULL OR sqlc.narg(work)::text IS NOT NULL THEN 0 ELSE quiet_wakes END,
 question_id = CASE WHEN @clear_question::bool THEN NULL ELSE coalesce(sqlc.narg(question_id)::text, question_id) END
WHERE id = @id;

-- name: ProblemsSummary :many
-- Open problems by owner and phase, for Home's pulse line.
SELECT coalesce(owner_bot_id, '')::text AS owner, phase, count(*)::int AS n, max(seq)::bigint AS newest
FROM problem WHERE state = 'open' GROUP BY 1, 2;

-- name: ProblemsOwnedIDs :many
SELECT id FROM problem WHERE state = 'open' AND owner_bot_id = @bot::text;

-- name: ProblemsSetOwner :exec
-- Problems without an owner get one (a duty saved after they opened).
UPDATE problem SET owner_bot_id = @bot::text WHERE state = 'open' AND owner_bot_id IS NULL
 AND (CASE WHEN @system::bool THEN project_id IS NULL OR NOT (project_id = ANY(@claimed::text[])) ELSE false END
  OR project_id = ANY(@projects::text[]));

-- name: ProblemQuietWake :exec
-- The owner ran on these problems without changing them: one more quiet wake; stuck after stuck_wakes.
UPDATE problem SET quiet_wakes = quiet_wakes + 1,
 phase = CASE WHEN quiet_wakes + 1 >= @stuck_wakes::int AND phase NOT IN ('escalated', 'watching') THEN 'stuck' ELSE phase END,
 phase_at = CASE WHEN quiet_wakes + 1 >= @stuck_wakes::int AND phase NOT IN ('escalated', 'watching', 'stuck') THEN now() ELSE phase_at END
WHERE id = ANY(@ids::text[]) AND state = 'open';

-- name: ProblemsStale :exec
-- Working for longer than stuck_hours: stuck.
UPDATE problem SET phase = 'stuck', phase_at = now()
WHERE state = 'open' AND phase = 'working' AND phase_at < now() - make_interval(hours => @stuck_hours::int);

-- name: ProblemStallExists :one
SELECT EXISTS(SELECT 1 FROM problem WHERE state = 'open' AND cause ~ @pattern::text);

-- name: ProblemDutyHealth :many
-- Each unpaused duty bot's health for the watchdog: enabled triggers that wake on problems and failed their last pass, its
-- oldest new problem, and its last run.
SELECT b.id, coalesce(s.name, '')::text AS name,
 (SELECT count(*) FROM trigger t WHERE t.bot_id = b.id AND t.enabled AND 'problem' = ANY(t.if_changed) AND coalesce(t.last_error, '') <> '')::int AS failing,
 (SELECT min(p.opened_at) FROM problem p WHERE p.owner_bot_id = b.id AND p.state = 'open' AND p.phase = 'new')::timestamptz AS oldest_new,
 (SELECT max(e.started_at) FROM execution e WHERE e.bot_id = b.id AND e.kind = 'agent_run')::timestamptz AS last_run,
 (SELECT count(*) FROM problem p WHERE p.owner_bot_id = b.id AND p.state = 'open')::int AS waiting
FROM bot b JOIN bot_revision s ON s.bot_id = b.id AND s.revision = b.revision
WHERE b.tombstoned_at IS NULL AND NOT b.paused AND s.duty ? 'problems' ORDER BY b.id;

-- name: ProblemQuestionOpen :one
SELECT EXISTS(SELECT 1 FROM question WHERE id = $1 AND state = 'open');
