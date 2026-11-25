-- Home view (home_view): the cause of each failed session's newest failed run, over execution_session.

-- name: HomeSessionFailures :many
-- The newest failed or cancelled conversation run with a problem of each session in ids (tool calls and system runs
-- are not the conversation's runs).
SELECT DISTINCT ON (e.session_id) e.session_id::text AS session_id, e.problem::text AS problem, e.host_id,
 coalesce(e.ended_at, e.last_seen_at, e.started_at) AS at
FROM execution e
WHERE e.session_id = ANY(@ids::text[]) AND e.kind <> 'tool_run' AND e.system_actor IS NULL
 AND e.state IN ('failed', 'cancelled') AND e.problem IS NOT NULL
ORDER BY e.session_id, e.seq DESC;
