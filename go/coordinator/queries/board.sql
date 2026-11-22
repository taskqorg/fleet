-- Swarm work board: durable work_plan and board_message queries (board.go).

-- name: BoardTaskProject :one
SELECT project_id FROM task WHERE id = $1;

-- name: BoardSessionProject :one
SELECT project_id FROM session WHERE id = $1;

-- name: BoardPipelineRunProject :one
SELECT project_id FROM execution WHERE id = $1 AND kind = 'pipeline_run';

-- name: BoardWorkPlanScopeProject :one
SELECT project_id FROM work_plan WHERE scope_kind = $1 AND scope_id = $2;

-- name: BoardAnyMessageProject :one
SELECT project_id FROM board_message WHERE scope_kind = $1 AND scope_id = $2 ORDER BY seq ASC LIMIT 1;

-- name: BoardArtifactProject :one
SELECT project_id FROM artifact WHERE id = $1;

-- name: BoardSessionExists :one
SELECT EXISTS(SELECT 1 FROM session WHERE id = $1);

-- name: BoardExecutionInfo :one
SELECT id, session_id, project_id FROM execution WHERE id = $1;

-- name: BoardPipelineRunPrimarySession :one
SELECT session_id
FROM execution
WHERE parent_execution_id = @run_id::text
  AND session_id IS NOT NULL
  AND session_id <> ''
ORDER BY
  (EXISTS (SELECT 1 FROM work_plan wp WHERE wp.scope_kind = 'session' AND wp.scope_id = execution.session_id)) DESC,
  (EXISTS (SELECT 1 FROM board_message bm WHERE bm.scope_kind = 'session' AND bm.scope_id = execution.session_id)) DESC,
  seq ASC,
  id ASC
LIMIT 1;

-- name: BoardScopeIsTerminal :one
SELECT CASE
  WHEN @scope_kind::text = 'task' THEN
    EXISTS(SELECT 1 FROM task WHERE id = @scope_id::text AND state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
  WHEN @scope_kind::text = 'pipeline_run' THEN
    EXISTS(SELECT 1 FROM execution WHERE id = @scope_id::text AND state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
  ELSE false
END;

-- name: BoardSessionParentTerminal :one
WITH RECURSIVE chain(id, task_id, pipeline_run_id, parent_execution_id, created_by_execution_id, created_by_session_id, source_session_id, depth) AS (
  SELECT id, task_id, pipeline_run_id, parent_execution_id, created_by_execution_id, created_by_session_id, source_session_id, 0
  FROM session
  WHERE id = @session_id::text
  UNION ALL
  SELECT s.id, s.task_id, s.pipeline_run_id, s.parent_execution_id, s.created_by_execution_id, s.created_by_session_id, s.source_session_id, c.depth + 1
  FROM session s
  JOIN chain c ON s.id = COALESCE(NULLIF(c.created_by_session_id, ''), NULLIF(c.source_session_id, ''))
  WHERE c.depth < 10
)
SELECT EXISTS (
  SELECT 1
  FROM chain c
  WHERE (
    c.task_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM task t
      WHERE t.id = c.task_id
        AND t.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped')
    )
  ) OR (
    c.pipeline_run_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM execution r
      WHERE r.id = c.pipeline_run_id
        AND r.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped')
    )
  ) OR (
    c.parent_execution_id IS NOT NULL AND EXISTS (
      SELECT 1
      FROM execution pe
      LEFT JOIN execution pr ON pr.id = pe.parent_execution_id
      WHERE pe.id = c.parent_execution_id
        AND (
          (pe.kind = 'pipeline_run' AND pe.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
          OR (pe.kind = 'pipeline_step' AND pr.kind = 'pipeline_run' AND pr.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
        )
    )
  ) OR EXISTS (
    SELECT 1
    FROM execution ps
    JOIN execution pr ON pr.id = ps.parent_execution_id
    WHERE ps.session_id = c.id
      AND ps.kind = 'pipeline_step'
      AND pr.kind = 'pipeline_run'
      AND pr.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped')
  ) OR (
    c.created_by_execution_id IS NOT NULL AND EXISTS (
      SELECT 1
      FROM execution ce
      LEFT JOIN execution pr ON pr.id = COALESCE(NULLIF(ce.data->>'pipelineRunId', ''), ce.parent_execution_id)
      WHERE ce.id = c.created_by_execution_id
        AND (
          (ce.kind = 'pipeline_run' AND ce.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
          OR (pr.kind = 'pipeline_run' AND pr.state::text IN ('succeeded', 'completed', 'failed', 'cancelled', 'stopped'))
        )
    )
  )
);

-- name: WorkPlanGet :one
SELECT * FROM work_plan
WHERE scope_kind = @scope_kind::text AND scope_id = @scope_id::text;

-- name: WorkPlanUpsert :one
INSERT INTO work_plan (id, project_id, scope_kind, scope_id, version, title, goal, strategy, payload)
VALUES (@id, @project_id, @scope_kind, @scope_id, 1, @title, @goal, @strategy, @payload)
ON CONFLICT (scope_kind, scope_id) DO UPDATE SET
 title = excluded.title,
 goal = excluded.goal,
 strategy = excluded.strategy,
 payload = excluded.payload,
 version = work_plan.version + 1,
 updated_at = now()
WHERE work_plan.project_id = excluded.project_id
  AND work_plan.tombstoned_at IS NULL
RETURNING *;

-- name: BoardNextSeq :one
SELECT (COALESCE(MAX(seq), 0) + 1)::bigint AS next_seq
FROM board_message
WHERE scope_kind = @scope_kind::text AND scope_id = @scope_id::text;

-- name: BoardHeadSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint AS head_seq
FROM board_message
WHERE scope_kind = @scope_kind::text AND scope_id = @scope_id::text;

-- name: BoardMessageInsert :one
INSERT INTO board_message (
 id, project_id, scope_kind, scope_id, seq, thread_id,
 author_member, author_role, author_session_id, author_execution_id,
 kind, mentions, text, artifact_id, payload
)
VALUES (
 @id, @project_id, @scope_kind, @scope_id, @seq, @thread_id,
 @author_member, @author_role, sqlc.narg(author_session_id), sqlc.narg(author_execution_id),
 @kind, @mentions::text[], @text, sqlc.narg(artifact_id), @payload::jsonb
)
RETURNING *;

-- name: BoardMessagesRead :many
SELECT * FROM board_message
WHERE scope_kind = @scope_kind::text
  AND scope_id = @scope_id::text
  AND seq > @since::bigint
  AND (sqlc.narg(thread_id)::text IS NULL OR thread_id = sqlc.narg(thread_id)::text)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(mention)::text IS NULL OR sqlc.narg(mention)::text = ANY(mentions))
  AND (sqlc.narg(author_member)::text IS NULL OR author_member = sqlc.narg(author_member)::text)
ORDER BY seq ASC
LIMIT @lim::int;

-- name: BoardScopeSummaryCounts :one
SELECT
 COUNT(*)::bigint AS messages_total,
 COALESCE(MAX(seq), 0)::bigint AS head_seq,
 COUNT(*) FILTER (WHERE kind = 'blocker')::bigint AS blockers_count,
 COUNT(*) FILTER (WHERE kind = 'screenshot')::bigint AS screenshots_count
FROM board_message
WHERE scope_kind = @scope_kind::text AND scope_id = @scope_id::text;

-- name: BoardMemberPosts :many
SELECT
 author_member,
 MAX(seq)::bigint AS last_post_seq,
 COALESCE((ARRAY_AGG(author_role ORDER BY seq DESC))[1], 'worker')::text AS author_role,
 COALESCE((ARRAY_AGG(thread_id ORDER BY seq DESC))[1], '')::text AS thread_id,
 COALESCE((ARRAY_AGG(author_session_id ORDER BY seq DESC) FILTER (WHERE author_session_id IS NOT NULL))[1], '')::text AS author_session_id,
 COALESCE((ARRAY_AGG(author_execution_id ORDER BY seq DESC) FILTER (WHERE author_execution_id IS NOT NULL))[1], '')::text AS author_execution_id,
 MIN(seq)::bigint AS first_seq
FROM board_message
WHERE scope_kind = @scope_kind::text AND scope_id = @scope_id::text
GROUP BY author_member
ORDER BY MIN(seq) ASC;

-- name: BoardRosterSessions :many
SELECT id, code, host_id, payload, archived_at
FROM session
WHERE id = ANY(@session_ids::text[]);

-- name: BoardRosterSessionExecutions :many
SELECT
 e.id,
 e.session_id,
 e.state::text AS state,
 e.harness,
 COALESCE(NULLIF(t.model, ''), e.model) AS model,
 e.host_id,
 e.started_at,
 e.ended_at,
 e.seq,
 t.wall_ms,
 t.tokens_input,
 t.tokens_output,
 (sp.execution_id IS NOT NULL)::boolean AS has_spend,
 sp.cost_currency,
 sp.cost_basis,
 sp.cost_value,
 COALESCE(sp.cost_unpriced, 0)::bigint AS cost_unpriced,
 sp.payload AS spend_payload
FROM execution e
LEFT JOIN execution_totals t ON t.execution_id = e.id
LEFT JOIN execution_spend sp ON sp.execution_id = e.id
WHERE e.session_id = ANY(@session_ids::text[])
  AND e.kind IN ('agent_run', 'host_job', 'child_agent')
ORDER BY e.session_id, e.seq DESC;

-- name: BoardRosterExecutionsByIDs :many
SELECT
 e.id,
 e.session_id,
 e.state::text AS state,
 e.harness,
 COALESCE(NULLIF(t.model, ''), e.model) AS model,
 e.host_id,
 e.started_at,
 e.ended_at,
 e.seq,
 t.wall_ms,
 t.tokens_input,
 t.tokens_output,
 (sp.execution_id IS NOT NULL)::boolean AS has_spend,
 sp.cost_currency,
 sp.cost_basis,
 sp.cost_value,
 COALESCE(sp.cost_unpriced, 0)::bigint AS cost_unpriced,
 sp.payload AS spend_payload
FROM execution e
LEFT JOIN execution_totals t ON t.execution_id = e.id
LEFT JOIN execution_spend sp ON sp.execution_id = e.id
WHERE e.id = ANY(@execution_ids::text[])
ORDER BY e.seq DESC;
