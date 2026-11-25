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

-- name: TelegramOutboxMarkPlain :exec
-- Telegram refused the item's formatting: its next send goes as plain text.
UPDATE telegram_outbox SET error = 'html-refused' WHERE id = $1 AND state = 'pending';

-- name: TelegramWorkStatusMessage :one
-- A work item's status message: the "New: <title>" notice that opened it, edited in place afterwards.
SELECT * FROM telegram_outbox WHERE work_id = $1 AND id LIKE 'notice:newwork:%' ORDER BY created_at LIMIT 1;

-- name: TelegramTurnOthers :one
-- Messages of a work item still waiting for their reply, besides this one: its status stays "working" until the last.
SELECT count(*) FROM telegram_turn WHERE work_id = @work_id AND id <> @id;
