-- The wake dispatcher (wake_outbox.go): claim, NOTIFY and delete in one transaction.

-- name: WakeClaim :many
SELECT wake_id::text AS id, channel, payload FROM wake_outbox
ORDER BY created_at, wake_id LIMIT sqlc.arg(batch) FOR UPDATE SKIP LOCKED;

-- name: WakeNotify :exec
SELECT count(pg_notify(a.channel, b.payload))
FROM unnest(@channels::text[]) WITH ORDINALITY AS a(channel, n)
JOIN unnest(@payloads::text[]) WITH ORDINALITY AS b(payload, n) USING (n);

-- name: WakeDelete :exec
DELETE FROM wake_outbox WHERE wake_id = ANY (CAST(@ids::text[] AS uuid[]));

-- name: WakeOldest :one
SELECT created_at FROM wake_outbox ORDER BY created_at, wake_id LIMIT 1;
