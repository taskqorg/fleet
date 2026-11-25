-- Host update history (host_updates.go, docs/designs/fleet-host-updates.md "What is recorded"): one row per change of
-- the update result a host reports, written by the poll, read newest first a page at a time.

-- name: HostUpdateLast :one
-- The host's newest row: the poll compares the reported result with it.
SELECT version, update_state, reason FROM host_update WHERE host_id=$1 ORDER BY seq DESC LIMIT 1;

-- name: HostUpdateLastSwitch :one
-- The release the host last switched to (state ok).
SELECT version FROM host_update WHERE host_id=$1 AND update_state='ok' ORDER BY seq DESC LIMIT 1;

-- name: HostUpdateLastPins :one
-- The payload of the newest switch whose component versions are known: what a new release's components compare with.
SELECT payload FROM host_update WHERE host_id=$1 AND update_state='ok' AND payload ? 'components' ORDER BY seq DESC LIMIT 1;

-- name: HostUpdateInsert :exec
INSERT INTO host_update(host_id, holder, version, update_state, reason, at, payload)
VALUES (@host_id, sqlc.narg(holder), @version, @update_state, @reason, @at, @payload);

-- name: HostUpdatePage :many
-- One page of a host's history, newest first: rows below the cursor seq (0: from the newest).
SELECT seq, version, update_state, reason, at, payload FROM host_update
WHERE host_id= @host_id::text AND (@before::bigint = 0 OR seq < @before::bigint)
ORDER BY seq DESC LIMIT @lim::int;
