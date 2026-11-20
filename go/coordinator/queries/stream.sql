-- Live change feed (stream.go, stream_shared.go; docs/designs/fleet-operator-app.md, sections 3-4). The horizon subquery
-- (stream.go, since) is repeated in every query that reads up to it: the lowest xid of the statement's snapshot that
-- may still commit change rows in this database.

-- name: StreamChangeXid :one
SELECT xid::text FROM change WHERE seq=$1;

-- name: StreamChangesFirst :many
-- Visible changes from the start of the log, in (xid, seq) order.
SELECT seq, kind, id, task_id, session_id, host_id, op, project_id, actor, setting_target FROM change
 WHERE xid < (SELECT coalesce(min(x), pg_snapshot_xmax(pg_current_snapshot())) FROM pg_snapshot_xip(pg_current_snapshot()) x
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity a WHERE a.backend_xid = xid(x) AND a.datid IS DISTINCT FROM (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())))
 ORDER BY xid, seq LIMIT @lim::int;

-- name: StreamChangesAfter :many
-- Visible changes after the change at (xid, seq), in (xid, seq) order.
SELECT seq, kind, id, task_id, session_id, host_id, op, project_id, actor, setting_target FROM change
 WHERE xid < (SELECT coalesce(min(x), pg_snapshot_xmax(pg_current_snapshot())) FROM pg_snapshot_xip(pg_current_snapshot()) x
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity a WHERE a.backend_xid = xid(x) AND a.datid IS DISTINCT FROM (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())))
 AND (xid, seq) > (CAST(@xid::text AS xid8), @seq::bigint)
 ORDER BY xid, seq LIMIT @lim::int;

-- name: StreamChangesHeld :one
-- Committed change rows still wait behind a running transaction of this database.
SELECT EXISTS(SELECT 1 FROM change WHERE xid >= (SELECT coalesce(min(x), pg_snapshot_xmax(pg_current_snapshot())) FROM pg_snapshot_xip(pg_current_snapshot()) x
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity a WHERE a.backend_xid = xid(x) AND a.datid IS DISTINCT FROM (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())))) AS held;

-- name: StreamHead :one
-- The last visible change's seq; 0 when there is none.
SELECT coalesce((SELECT seq FROM change
 WHERE xid < (SELECT coalesce(min(x), pg_snapshot_xmax(pg_current_snapshot())) FROM pg_snapshot_xip(pg_current_snapshot()) x
 WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity a WHERE a.backend_xid = xid(x) AND a.datid IS DISTINCT FROM (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())))
 ORDER BY xid DESC, seq DESC LIMIT 1), 0)::bigint AS seq;

-- name: StreamVisibleSeqs :many
-- Which of these changes the viewer's scope (RLS) shows.
SELECT seq FROM change WHERE seq = ANY(@seqs::bigint[]);

-- name: StreamPruneChanges :exec
-- Expire the reconnect log: up to 10000 changes older than 24 hours.
DELETE FROM change WHERE seq IN (SELECT seq FROM change WHERE at < now()-interval '24 hours' ORDER BY at LIMIT 10000);
