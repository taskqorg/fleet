-- Navigation counters (nav_counts.go).

-- name: NavResultsCount :one
-- The Results counter: results created since, not tombstoned, outside the ignored projects ('-' is no project); in_focus
-- counts those in the focus (every one without a focus).
SELECT (count(*) FILTER (WHERE cardinality(sqlc.arg(focus)::text[])=0 OR coalesce(project_id,'-')=ANY(sqlc.arg(focus)::text[])))::bigint AS in_focus,
 count(*)::bigint AS total
FROM artifact WHERE tombstoned_at IS NULL AND created_at>=sqlc.arg(since) AND NOT coalesce(project_id,'-')=ANY(sqlc.arg(ignored)::text[]);
