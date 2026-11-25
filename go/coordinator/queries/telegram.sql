-- The Telegram channel (telegram.go; migration 107; docs/designs/fleet-telegram.md).

-- name: TelegramUpdateNext :one
-- The offset of the next getUpdates: one past the highest update the journal holds (0: none yet).
SELECT coalesce(max(update_id) + 1, 0)::bigint FROM telegram_update;

-- name: TelegramUpdateAdd :execrows
INSERT INTO telegram_update(id, update_id, payload, chat_id, message_id, from_id)
VALUES (@id, @update_id, @payload, sqlc.narg(chat_id), sqlc.narg(message_id), sqlc.narg(from_id))
ON CONFLICT DO NOTHING;

-- name: TelegramUpdateDue :many
SELECT id, attempts FROM telegram_update WHERE outcome = '' AND next_attempt_at <= sqlc.arg(now)::pg_catalog.timestamptz
ORDER BY update_id LIMIT sqlc.arg(lim);

-- name: TelegramUpdateGet :one
SELECT * FROM telegram_update WHERE id = $1;

-- name: TelegramUpdateDone :exec
UPDATE telegram_update SET outcome = @outcome, work_id = sqlc.narg(work_id) WHERE id = @id AND outcome IN ('', 'asked');

-- name: TelegramUpdateByMessage :one
-- The routed human message a Telegram reply points at.
SELECT * FROM telegram_update WHERE chat_id = @chat_id AND message_id = @message_id AND work_id IS NOT NULL
ORDER BY update_id DESC LIMIT 1;

-- name: TelegramUpdatePending :one
SELECT count(*) FROM telegram_update WHERE outcome = '';

-- name: TelegramUnknownSenders :many
-- Telegram users who wrote to the bot without a linked person, newest first (telegram_people).
SELECT from_id::bigint AS telegram_user_id,
       (array_agg(coalesce(payload->'message'->'from'->>'username', '') ORDER BY update_id DESC))[1]::text AS username,
       (array_agg(coalesce(payload->'message'->'from'->>'first_name', '') ORDER BY update_id DESC))[1]::text AS name,
       max(received_at)::timestamptz AS last_at, count(*)::bigint AS messages
FROM telegram_update WHERE outcome = 'unknown-sender' AND from_id IS NOT NULL
  AND from_id NOT IN (SELECT telegram_user_id FROM telegram_person)
GROUP BY from_id ORDER BY max(received_at) DESC LIMIT 50;

-- name: TelegramPersonByUser :one
SELECT * FROM telegram_person WHERE telegram_user_id = $1;

-- name: TelegramPersonGet :one
SELECT * FROM telegram_person WHERE email = $1;

-- name: TelegramPersonList :many
SELECT * FROM telegram_person ORDER BY email;

-- name: TelegramPersonUnlinkUser :exec
-- A Telegram user belongs to one person: linking it to another person unlinks it here first.
DELETE FROM telegram_person WHERE telegram_user_id = @telegram_user_id AND email <> @email;

-- name: TelegramPersonLink :exec
INSERT INTO telegram_person(email, telegram_user_id, username, language)
VALUES (@email, @telegram_user_id, @username, @language)
ON CONFLICT (email) DO UPDATE SET telegram_user_id = EXCLUDED.telegram_user_id, username = EXCLUDED.username,
  language = CASE WHEN EXCLUDED.language <> '' THEN EXCLUDED.language ELSE telegram_person.language END,
  home_session_id = CASE WHEN telegram_person.telegram_user_id = EXCLUDED.telegram_user_id THEN telegram_person.home_session_id END,
  linked_at = CASE WHEN telegram_person.telegram_user_id = EXCLUDED.telegram_user_id THEN telegram_person.linked_at ELSE clock_timestamp() END;

-- name: TelegramPersonSetLanguage :execrows
UPDATE telegram_person SET language = @language WHERE email = @email;

-- name: TelegramPersonSetHome :exec
UPDATE telegram_person SET home_session_id = sqlc.narg(session_id) WHERE email = @email;

-- name: TelegramPersonDelete :execrows
DELETE FROM telegram_person WHERE email = $1;

-- name: TelegramLinkAdd :exec
INSERT INTO telegram_link(code, email) VALUES (@code, @email);

-- name: TelegramLinkTake :one
-- A /start code links once, within a day of its making.
UPDATE telegram_link SET used_at = clock_timestamp()
WHERE code = $1 AND used_at IS NULL AND created_at > clock_timestamp() - interval '1 day'
RETURNING email;

-- name: TelegramRoomUpsert :one
INSERT INTO telegram_room(id, chat_id, thread_id, kind, title)
VALUES (@id, @chat_id, @thread_id, @kind, @title)
ON CONFLICT (chat_id, thread_id) DO UPDATE SET seen_at = clock_timestamp(), kind = EXCLUDED.kind,
  title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE telegram_room.title END
RETURNING *;

-- name: TelegramRoomGet :one
SELECT * FROM telegram_room WHERE id = $1;

-- name: TelegramRoomList :many
SELECT * FROM telegram_room ORDER BY seen_at DESC;

-- name: TelegramRoomSetHint :execrows
UPDATE telegram_room SET project_hint = @project_hint WHERE id = @id;

-- name: TelegramWorkAdd :exec
INSERT INTO telegram_work(id, room_id, session_id, title, created_by) VALUES (@id, @room_id, @session_id, @title, @created_by)
ON CONFLICT DO NOTHING;

-- name: TelegramWorkGet :one
SELECT * FROM telegram_work WHERE id = $1;

-- name: TelegramWorkBySession :one
SELECT * FROM telegram_work WHERE room_id = @room_id AND session_id = @session_id ORDER BY created_at DESC LIMIT 1;

-- name: TelegramWorkOpen :many
-- A room's open work items, most recent first: what the conductor chooses among.
SELECT * FROM telegram_work WHERE room_id = $1 AND closed_at IS NULL ORDER BY last_at DESC LIMIT 8;

-- name: TelegramWorkList :many
SELECT * FROM telegram_work WHERE closed_at IS NULL ORDER BY last_at DESC LIMIT 500;

-- name: TelegramWorkTouch :exec
UPDATE telegram_work SET last_at = clock_timestamp() WHERE id = $1;

-- name: TelegramWorkClose :execrows
UPDATE telegram_work SET closed_at = clock_timestamp() WHERE id = $1 AND closed_at IS NULL;

-- name: TelegramTurnAdd :exec
INSERT INTO telegram_turn(id, session_id, request_id, work_id, chat_id, thread_id, message_id, email)
VALUES (@id, @session_id, @request_id, sqlc.narg(work_id), @chat_id, @thread_id, @message_id, @email)
ON CONFLICT DO NOTHING;

-- name: TelegramTurnDue :many
SELECT id, attempts FROM telegram_turn WHERE next_attempt_at <= sqlc.arg(now)::pg_catalog.timestamptz
ORDER BY next_attempt_at, id LIMIT sqlc.arg(lim);

-- name: TelegramTurnGet :one
SELECT * FROM telegram_turn WHERE id = $1;

-- name: TelegramTurnTyped :exec
UPDATE telegram_turn SET typing_at = clock_timestamp() WHERE id = $1;

-- name: TelegramTurnsInRoom :many
-- Messages of one chat (and topic) still waiting for their reply: /stop asks their runs to stop.
SELECT * FROM telegram_turn WHERE chat_id = @chat_id AND thread_id = @thread_id ORDER BY id LIMIT 50;

-- name: TelegramTurnDelete :exec
DELETE FROM telegram_turn WHERE id = $1;

-- name: TelegramTurnPending :one
SELECT count(*) FROM telegram_turn;

-- name: TelegramOutboxAdd :exec
INSERT INTO telegram_outbox(id, chat_id, thread_id, kind, reply_to, edit_message_id, text, payload, work_id, session_id)
VALUES (@id, @chat_id, @thread_id, @kind, sqlc.narg(reply_to), sqlc.narg(edit_message_id), @text, sqlc.narg(payload),
        sqlc.narg(work_id), sqlc.narg(session_id))
ON CONFLICT DO NOTHING;

-- name: TelegramOutboxDue :many
-- The oldest pending item of each chat when it is due: one chat's messages leave one at a time, in order.
SELECT o.id, o.attempts FROM (
  SELECT DISTINCT ON (chat_id) id, attempts, next_attempt_at FROM telegram_outbox WHERE state = 'pending'
  ORDER BY chat_id, created_at, id
) o WHERE o.next_attempt_at <= sqlc.arg(now)::pg_catalog.timestamptz ORDER BY o.next_attempt_at LIMIT sqlc.arg(lim);

-- name: TelegramOutboxGet :one
SELECT * FROM telegram_outbox WHERE id = $1;

-- name: TelegramOutboxSettle :exec
UPDATE telegram_outbox SET state = @state, telegram_message_id = sqlc.narg(telegram_message_id), sent_at = clock_timestamp(),
  error = sqlc.narg(error)
WHERE id = @id AND state = 'pending';

-- name: TelegramOutboxBySent :one
-- The Fleet message a Telegram reply points at.
SELECT * FROM telegram_outbox WHERE chat_id = @chat_id AND telegram_message_id = @telegram_message_id LIMIT 1;

-- name: TelegramOutboxPending :one
SELECT count(*) FROM telegram_outbox WHERE state = 'pending';

-- name: TelegramOutboxMarkRichRefused :exec
-- Telegram refused the item as a rich message: its next send is HTML, with text its first HTML part.
UPDATE telegram_outbox SET error = 'rich-refused', text = @text, payload = sqlc.narg(payload) WHERE id = @id AND state = 'pending';

-- name: TelegramOutboxAddPart :exec
-- A further HTML part of a refused rich message: it follows the item in the chat's order (created_at, id).
INSERT INTO telegram_outbox(id, chat_id, thread_id, kind, text, payload, work_id, session_id, created_at, error)
SELECT @part_id::text, o.chat_id, o.thread_id, o.kind, @text::text, sqlc.narg(payload)::jsonb, o.work_id, o.session_id, o.created_at, 'rich-refused'
FROM telegram_outbox o WHERE o.id = @id
ON CONFLICT DO NOTHING;

-- name: TelegramOutboxMarkPlain :exec
-- Telegram refused the item's formatting: its next send goes as plain text.
UPDATE telegram_outbox SET error = 'html-refused' WHERE id = $1 AND state = 'pending';

-- name: TelegramWorkStatusMessage :one
-- A work item's status message: the "New: <title>" notice that opened it, edited in place afterwards.
SELECT * FROM telegram_outbox WHERE work_id = $1 AND id LIKE 'notice:newwork:%' ORDER BY created_at LIMIT 1;

-- name: TelegramTurnOthers :one
-- Messages of a work item still waiting for their reply, besides this one: its status stays "working" until the last.
SELECT count(*) FROM telegram_turn WHERE work_id = @work_id AND id <> @id;

-- name: TelegramWorkLock :exec
-- Serialises the reply steps of one work item, so the last one to end sees the others gone and writes the status.
SELECT id FROM telegram_work WHERE id = $1 FOR UPDATE;

-- name: TelegramSessionRun :one
-- Where a work item's conversation stands: its newest run, its open questions and its recent runs (for delivery).
SELECT coalesce((SELECT e.state::text FROM execution e WHERE e.session_id = @session_id ORDER BY e.seq DESC LIMIT 1), '')::text AS run_state,
  (SELECT coalesce(e.ended_at, e.started_at, e.updated_at) FROM execution e WHERE e.session_id = @session_id ORDER BY e.seq DESC LIMIT 1)::timestamptz AS run_at,
  (SELECT count(*) FROM question q WHERE q.session_id = @session_id AND q.state = 'open')::bigint AS open_questions,
  coalesce((SELECT array_agg(r.id ORDER BY r.seq DESC) FROM (SELECT e.id, e.seq FROM execution e WHERE e.session_id = @session_id ORDER BY e.seq DESC LIMIT 20) r), '{}')::text[] AS execution_ids;

-- name: TelegramWorkActive :many
-- Open work items with activity in the last week: the milestone pass looks at their state.
SELECT * FROM telegram_work WHERE closed_at IS NULL AND last_at > clock_timestamp() - interval '7 days' ORDER BY last_at DESC LIMIT 200;

-- name: TelegramQuestionsToPush :many
-- Open questions of a work item's or a home conversation not yet sent to Telegram, with where they go.
SELECT q.id, q.kind::text AS kind, q.text, q.options, q.session_id::text AS session_id, w.id AS work_id, r.chat_id, r.thread_id,
  ''::text AS email
FROM question q
JOIN telegram_work w ON w.session_id = q.session_id AND w.closed_at IS NULL
JOIN telegram_room r ON r.id = w.room_id
WHERE q.state = 'open' AND q.asked_at > clock_timestamp() - interval '2 days'
  AND NOT EXISTS (SELECT 1 FROM telegram_outbox o WHERE o.id = 'q:' || q.id)
UNION ALL
SELECT q.id, q.kind::text, q.text, q.options, q.session_id::text, ''::text, p.telegram_user_id, 0::bigint, p.email
FROM question q
JOIN telegram_person p ON p.home_session_id = q.session_id
WHERE q.state = 'open' AND q.asked_at > clock_timestamp() - interval '2 days'
  AND NOT EXISTS (SELECT 1 FROM telegram_outbox o WHERE o.id = 'q:' || q.id)
LIMIT 50;

-- name: TelegramQuestionsEnded :many
-- Questions sent to Telegram that were answered or withdrawn since, whose message does not say so yet.
SELECT q.id, q.state::text AS state, coalesce(q.answer, '')::text AS answer, coalesce(q.answered_by, '')::text AS answered_by, q.text,
  o.chat_id, o.telegram_message_id
FROM telegram_outbox o JOIN question q ON o.id = 'q:' || q.id
WHERE o.state = 'sent' AND o.telegram_message_id IS NOT NULL AND q.state <> 'open'
  AND NOT EXISTS (SELECT 1 FROM telegram_outbox e WHERE e.id = 'qend:' || q.id)
LIMIT 50;

-- name: TelegramQuestionMessage :one
-- The question a Fleet message in a chat asks (a Telegram reply to it answers the question).
-- Ids: q:<question> (the push) and qn:<question>:<update> (a /needs listing).
SELECT split_part(id, ':', 2)::text AS question_id FROM telegram_outbox WHERE chat_id = @chat_id AND telegram_message_id = @telegram_message_id
  AND (id LIKE 'q:%' OR id LIKE 'qn:%') LIMIT 1;

-- name: TelegramQuestionGet :one
SELECT id, kind::text AS kind, state::text AS state, text, options, session_id, project_id FROM question WHERE id = $1;

-- name: TelegramTaskByRef :one
-- A task by its code (FLT-12) or id, for /status and the milestone pass.
SELECT id, code, title, state::text AS state, wait_reason, project_id FROM task WHERE id = $1 OR code = upper($1) LIMIT 1;

-- name: TelegramOpenQuestions :many
-- Open questions of a group room's work items, or (private) of the person's home conversation and the work items they
-- opened, oldest first (/needs).
SELECT q.id, q.kind::text AS kind, q.text, q.session_id FROM question q
WHERE q.state = 'open' AND q.session_id IN (
  SELECT w.session_id FROM telegram_work w WHERE w.closed_at IS NULL
    AND ((NOT @private::boolean AND w.room_id = @room_id) OR (@private::boolean AND w.created_by = @email))
  UNION SELECT p.home_session_id FROM telegram_person p WHERE @private::boolean AND p.email = @email AND p.home_session_id IS NOT NULL)
ORDER BY q.asked_at LIMIT 10;

-- name: TelegramOverlaps :many
-- Other conversations running on one of these tasks now: a work item's room hears of them once (FLT-36).
SELECT DISTINCT ON (e.session_id) e.session_id::text AS session_id, coalesce(s.title, '')::text AS title, coalesce(e.host_id, '')::text AS host_id,
  e.task_id::text AS task_id
FROM execution e JOIN session s ON s.id = e.session_id
WHERE e.task_id = ANY(@task_ids::text[]) AND e.session_id <> @session_id AND e.state IN ('preparing', 'running', 'waiting')
  AND s.archived_at IS NULL
  -- The work item's own workers are no overlap: sessions it started, directly or one level down.
  AND coalesce(s.created_by_session_id, '') <> @session_id::text
  AND coalesce(s.created_by_session_id, '') NOT IN (SELECT c.id FROM session c WHERE c.created_by_session_id = @session_id::text)
  AND NOT EXISTS (SELECT 1 FROM session w WHERE w.id = @session_id::text AND w.created_by_session_id = e.session_id)
ORDER BY e.session_id, e.seq DESC LIMIT 5;

-- name: TelegramWorkJoin :execrows
-- A work item moves to another conversation (join): later messages go there.
UPDATE telegram_work SET session_id = @session_id, last_at = clock_timestamp() WHERE id = @id AND closed_at IS NULL;

-- name: TelegramDeliveryHealth :one
-- The outbox in the last day: sends refused or of unknown outcome, and the newest refusal (telegram_status.delivery).
SELECT count(*) FILTER (WHERE state = 'sent')::bigint AS sent, count(*) FILTER (WHERE state = 'failed')::bigint AS failed,
  count(*) FILTER (WHERE state = 'unknown')::bigint AS unknown,
  coalesce((SELECT o.error FROM telegram_outbox o WHERE o.state = 'failed' AND o.error <> 'no status message' ORDER BY o.sent_at DESC LIMIT 1), '')::text AS last_error,
  (SELECT max(o.sent_at) FROM telegram_outbox o WHERE o.state = 'sent')::timestamptz AS last_sent_at
FROM telegram_outbox WHERE created_at > clock_timestamp() - interval '1 day';

-- name: TelegramIntakeHealth :one
-- The intake in the last day: updates routed, from unknown senders, and failed deliveries.
SELECT count(*)::bigint AS updates, count(*) FILTER (WHERE outcome = 'unknown-sender')::bigint AS strangers,
  count(*) FILTER (WHERE outcome = 'failed')::bigint AS failed, max(received_at)::timestamptz AS last_at
FROM telegram_update WHERE received_at > clock_timestamp() - interval '1 day';

-- name: TelegramSessionTitle :one
-- A conversation's own short title (set as it works); a work item shows it once there is one.
SELECT coalesce(title, '')::text FROM session WHERE id = $1;

-- name: TelegramNeedsAll :many
-- Every open question, newest first, for a person's /needs in a private chat (filtered by their projects in Go).
SELECT id, kind::text AS kind, text, session_id, coalesce(project_id, '')::text AS project_id FROM question
WHERE state = 'open' ORDER BY asked_at DESC LIMIT 100;

-- name: TelegramAuthorRecentWork :one
-- The work item the author's last routed message in this room went to, within the window: a follow-up of theirs
-- goes there without a model (owner-tester F19). Per author, so two people's threads never mix (pilotq L14).
SELECT u.work_id::text AS work_id, u.received_at FROM telegram_update u JOIN telegram_work w ON w.id = u.work_id
WHERE u.chat_id = @chat_id AND u.from_id = @from_id AND u.outcome IN ('delivered', 'status') AND w.room_id = @room_id
  AND w.closed_at IS NULL AND u.received_at > @since::timestamptz AND u.id <> @id
ORDER BY u.update_id DESC LIMIT 1;

-- name: TelegramWorkLastReply :one
-- The newest answer Fleet sent for a work item, for the conductor's context.
SELECT text, created_at FROM telegram_outbox WHERE work_id = $1 AND id LIKE 'reply:%' ORDER BY created_at DESC LIMIT 1;

-- name: TelegramHistoryPut :exec
-- One imported message of a room (telegram_history_import); a repeat of the same message replaces it.
INSERT INTO telegram_history (id, room_id, message_id, at, author, text) VALUES (@id, @room_id, @message_id, @at, @author, @text)
ON CONFLICT (id) DO UPDATE SET at = EXCLUDED.at, author = EXCLUDED.author, text = EXCLUDED.text;

-- name: TelegramRoomHistory :many
-- A room's newest messages, newest first: imported history, people's delivered messages (the journal, except the one
-- being routed) and Fleet's sent replies. The context a new work item of a group room starts with (telegram_history).
SELECT h.at AS at, h.author AS author, h.text AS text, 'import'::text AS source FROM telegram_history h WHERE h.room_id = @room_id
  -- An export may also hold messages Fleet saw live: those come from the journal and the outbox, once.
  AND NOT EXISTS (SELECT 1 FROM telegram_update u2 WHERE u2.chat_id = @chat_id AND u2.message_id = h.message_id)
  AND NOT EXISTS (SELECT 1 FROM telegram_outbox o2 WHERE o2.chat_id = @chat_id AND o2.telegram_message_id = h.message_id)
UNION ALL
SELECT u.received_at, coalesce(nullif(trim(coalesce(u.payload->'message'->'from'->>'first_name', '') || ' ' ||
    coalesce(u.payload->'message'->'from'->>'last_name', '')), ''), 'someone'),
  coalesce(nullif(u.payload->'message'->>'text', ''), u.payload->'message'->>'caption', ''), 'person'
FROM telegram_update u
WHERE u.chat_id = @chat_id AND u.outcome = 'delivered' AND u.id <> @except_id
  AND (CASE WHEN (u.payload->'message'->>'is_topic_message')::boolean THEN (u.payload->'message'->>'message_thread_id')::bigint ELSE 0 END) = @thread_id::bigint
UNION ALL
SELECT o.sent_at, 'Fleet', o.text, 'fleet' FROM telegram_outbox o
WHERE o.chat_id = @chat_id AND o.thread_id = @thread_id::bigint AND o.state = 'sent' AND o.kind = 'text' AND o.id LIKE 'reply:%' AND o.sent_at IS NOT NULL
ORDER BY 1 DESC LIMIT @lim;
