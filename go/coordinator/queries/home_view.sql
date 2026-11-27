-- Home view (home_view): failed sessions, bot ownership, and escalated/unowned problems.

-- name: HomeFailedSessions :many
-- For each failed session in ids, whether a bot owns it (directly, via bot_task, via site-agents, or via parent
-- session) and the newest failed or cancelled conversation run with a problem (tool calls and system runs excluded).
SELECT s.id::text AS session_id,
 coalesce(
  s.bot_id IS NOT NULL
  OR s.directed_by = 'site-agents'
  OR EXISTS (SELECT 1 FROM bot_task bt WHERE bt.task_id = s.task_id AND bt.link_kind IN ('created', 'managed') AND bt.tombstoned_at IS NULL)
  OR EXISTS (
   SELECT 1 FROM session ps
   LEFT JOIN bot_task pbt ON pbt.task_id = ps.task_id AND pbt.link_kind IN ('created', 'managed') AND pbt.tombstoned_at IS NULL
   WHERE ps.id = s.created_by_session_id AND (ps.bot_id IS NOT NULL OR ps.directed_by = 'site-agents' OR pbt.bot_id IS NOT NULL)
  ),
  false
 )::boolean AS bot_owned,
 coalesce(e.problem::text, '')::text AS problem,
 e.host_id,
 coalesce(e.ended_at, e.last_seen_at, e.started_at) AS at
FROM session s
LEFT JOIN LATERAL (
 SELECT ex.problem, ex.host_id, ex.ended_at, ex.last_seen_at, ex.started_at
 FROM execution ex
 WHERE ex.session_id = s.id AND ex.kind <> 'tool_run' AND ex.system_actor IS NULL
  AND ex.state IN ('failed', 'cancelled') AND ex.problem IS NOT NULL
 ORDER BY ex.seq DESC LIMIT 1
) e ON true
WHERE s.id = ANY(@ids::text[]);

-- name: HomeBotOwnedExecutions :many
-- Executions in ids that belong to host jobs, non-session pipeline runs/steps, a bot directly, or a bot-owned task/session.
SELECT e.id::text
FROM execution e
LEFT JOIN session s ON s.id = e.session_id
LEFT JOIN session ps ON ps.id = s.created_by_session_id
WHERE e.id = ANY(@ids::text[])
 AND (
  e.kind IN ('host_job', 'pipeline_step')
  OR (e.kind = 'pipeline_run' AND e.session_id IS NULL AND e.task_id IS NULL)
  OR e.bot_id IS NOT NULL
  OR s.bot_id IS NOT NULL
  OR s.directed_by = 'site-agents'
  OR EXISTS (SELECT 1 FROM bot_task bt WHERE bt.task_id = coalesce(e.task_id, s.task_id) AND bt.link_kind IN ('created', 'managed') AND bt.tombstoned_at IS NULL)
  OR ps.bot_id IS NOT NULL
  OR ps.directed_by = 'site-agents'
  OR EXISTS (SELECT 1 FROM bot_task pbt WHERE pbt.task_id = ps.task_id AND pbt.link_kind IN ('created', 'managed') AND pbt.tombstoned_at IS NULL)
 );

-- name: HomeProblemsSummary :many
-- Open escalated or unowned problems by owner and phase, for Home's pulse line.
SELECT coalesce(owner_bot_id, '')::text AS owner, phase, count(*)::int AS n, max(seq)::bigint AS newest
FROM problem WHERE state = 'open' AND (owner_bot_id IS NULL OR phase = 'escalated') GROUP BY 1, 2;

-- name: HomeProblemsList :many
-- Open escalated or unowned problems for Home's pulse line and blockers.
SELECT * FROM problem WHERE state = 'open' AND (owner_bot_id IS NULL OR phase = 'escalated')
ORDER BY seq DESC LIMIT @lim;
