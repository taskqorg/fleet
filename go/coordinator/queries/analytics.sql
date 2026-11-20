-- Analytics (analytics.go, docs/designs/fleet-analytics.md): the fixed statements. The grouped queries and rollups
-- compose their SQL from the requested dimensions and stay in analytics.go.

-- name: AnalyticsWindowLeafCount :one
-- Every leaf execution in [from, to), past the caller's row scope (CA-05).
SELECT fleet_window_leaf_count(sqlc.arg(win_from)::pg_catalog.timestamptz, sqlc.arg(win_to)::pg_catalog.timestamptz)::bigint AS n;

-- name: AnalyticsTaskHiddenLineage :one
SELECT fleet_task_hidden_lineage_count(sqlc.arg(task_id)::text)::bigint AS n;

-- name: AnalyticsSessionHiddenLineage :one
SELECT fleet_session_hidden_lineage_count(sqlc.arg(session_id)::text)::bigint AS n;

-- name: AnalyticsJIT :one
SELECT current_setting('jit')::text AS jit;

-- name: AnalyticsSetJIT :exec
SELECT set_config('jit', sqlc.arg(jit)::text, true);
