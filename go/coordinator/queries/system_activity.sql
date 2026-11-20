-- System activity (system_activity.go): the legacy rows of the coordinator's own reads (migration 69) and their cleanup.

-- name: SystemReadsLegacyLeft :one
SELECT EXISTS(SELECT 1 FROM execution WHERE system_actor IS NOT NULL AND data @> '{"legacySystemRead": true}')::boolean AS left;

-- name: SystemReadsLegacyBatch :many
-- Up to 1000 legacy rows no spool line names.
SELECT id FROM execution
 WHERE system_actor IS NOT NULL AND data @> '{"legacySystemRead": true}' AND NOT id = ANY(sqlc.arg(keep)::text[]) LIMIT 1000;

-- name: SystemReadsTotalsDelete :exec
DELETE FROM execution_totals WHERE execution_id = ANY(sqlc.arg(ids)::text[]);

-- name: SystemReadsDelete :exec
DELETE FROM execution WHERE id = ANY(sqlc.arg(ids)::text[]);
