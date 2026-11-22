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
