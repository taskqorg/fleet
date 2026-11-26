-- Agent gaps (agent_gaps.go, docs/designs/fleet-telemetry.md#10-agent-gaps): classified failures of agent tool calls and gateway calls.

-- name: AgentGapPut :exec
-- One failure's class; a repeated ingest or a reclassification writes the same id again.
INSERT INTO agent_gap(id, execution_id, project_id, source, at, class, family, tool, line, rules)
VALUES (@id::text, @execution_id::text, sqlc.narg(project_id)::text, @source::text, @at::timestamptz, @class::text, @family::text, @tool::text, @line::text, @rules::text)
ON CONFLICT (id) DO UPDATE SET class = EXCLUDED.class, family = EXCLUDED.family, tool = EXCLUDED.tool, line = EXCLUDED.line, rules = EXCLUDED.rules;

-- name: AgentGapsClear :execrows
-- The agent rows of executions about to be classified again (reclassification); gateway rows go by their window.
DELETE FROM agent_gap WHERE source = 'agent' AND execution_id = ANY(@ids::text[]);

-- name: AgentGapsClearGateway :execrows
-- The gateway rows of a window about to be classified again.
DELETE FROM agent_gap WHERE source = 'gateway' AND at >= @since::timestamptz;

-- name: AgentGapRows :many
-- The failures of a window with the run they belong to, newest first (the reply groups them).
SELECT g.id, g.execution_id, g.project_id, g.source, g.at, g.class, g.family, g.tool, g.line, g.rules,
  x.host_id, x.harness, coalesce(x.config_ref->>'profileId', x.data->>'profileId', '')::text AS profile_id, x.session_id, x.task_id, x.experiment_id
FROM agent_gap g JOIN execution x ON x.id = g.execution_id
WHERE g.at >= @since::timestamptz AND g.at < @until::timestamptz
ORDER BY g.at DESC, g.id
LIMIT 50001;

-- name: AgentGapPassDone :one
-- Whether a classification with these rules covered at least days days.
SELECT EXISTS (SELECT 1 FROM agent_gap_pass WHERE rules = @rules::text AND days >= @days::int)::bool;

-- name: AgentGapPassPut :exec
INSERT INTO agent_gap_pass(rules, days) VALUES (@rules::text, @days::int)
ON CONFLICT (rules) DO UPDATE SET days = greatest(agent_gap_pass.days, EXCLUDED.days), at = now();

-- name: AgentGapRuns :many
-- Agent runs active since since, in id order after after (a reclassification's page).
SELECT id, project_id, host_id, kind::text AS kind FROM execution
WHERE kind = 'agent_run' AND coalesce(ended_at, updated_at) >= @since::timestamptz AND id > @after::text
ORDER BY id LIMIT @lim::int;

-- name: AgentGapFailedEvents :many
-- The stored events of one execution that hold a failed tool block (fleetNormalized.blocks).
SELECT id, at, payload FROM event
WHERE execution_id = @execution_id::text AND NOT delta
  AND jsonb_path_exists(payload, '$.data.fleetNormalized.blocks[*] ? (@.kind == "tool" && @.state == "failed")')
ORDER BY seq;

-- name: AgentGapFailedCalls :many
-- Failed gateway calls since since: the tool_run rows spool ingest wrote (connection_ingest.go).
SELECT id, parent_execution_id, project_id, started_at,
  coalesce(data->>'tool', '')::text AS tool, coalesce(data->>'connection', '')::text AS connection,
  coalesce(data->>'errorCode', '')::text AS error_code, coalesce(data->>'outcome', '')::text AS outcome
FROM execution
WHERE kind = 'tool_run' AND state = 'failed' AND coalesce(started_at, updated_at) >= @since::timestamptz;

-- name: AgentGapRunExists :one
SELECT EXISTS (SELECT 1 FROM execution WHERE id = @id::text)::bool;

-- name: AgentGapFindings :many
-- Every finding (agent_gap_findings.go): the pass reads them all; the read joins them to their classes.
SELECT class, state, question_id, session_id, pr, pr_url, release, change_at, before_count, after_count, before_runs, after_runs,
  verdict_at, note, opened_at, updated_at
FROM agent_gap_finding ORDER BY class;

-- name: AgentGapFinding :one
SELECT class, state, question_id, session_id, pr, pr_url, release, change_at, before_count, after_count, before_runs, after_runs,
  verdict_at, note, opened_at, updated_at
FROM agent_gap_finding WHERE class = @class::text;

-- name: AgentGapFindingPut :exec
-- The whole finding as agentGapFindingTransition decided it; project_id is the Fleet project, whose members and PM
-- conversations see it (a fix goes into Fleet's settings).
INSERT INTO agent_gap_finding(class, project_id, state, question_id, session_id, pr, pr_url, release, change_at, before_count, after_count,
  before_runs, after_runs, verdict_at, note, opened_at, updated_at)
VALUES (@class::text, @project_id::text, @state::text, sqlc.narg(question_id)::text, sqlc.narg(session_id)::text, sqlc.narg(pr)::bigint, sqlc.narg(pr_url)::text,
  sqlc.narg(release)::text, sqlc.narg(change_at)::timestamptz, sqlc.narg(before_count)::int, sqlc.narg(after_count)::int,
  sqlc.narg(before_runs)::int, sqlc.narg(after_runs)::int, sqlc.narg(verdict_at)::timestamptz, @note::text, @opened_at::timestamptz, now())
ON CONFLICT (class) DO UPDATE SET state = EXCLUDED.state, question_id = EXCLUDED.question_id, session_id = EXCLUDED.session_id,
  pr = EXCLUDED.pr, pr_url = EXCLUDED.pr_url, release = EXCLUDED.release, change_at = EXCLUDED.change_at,
  before_count = EXCLUDED.before_count, after_count = EXCLUDED.after_count, before_runs = EXCLUDED.before_runs,
  after_runs = EXCLUDED.after_runs, verdict_at = EXCLUDED.verdict_at, note = EXCLUDED.note, opened_at = EXCLUDED.opened_at, updated_at = now();

-- name: AgentGapFindingDelete :exec
DELETE FROM agent_gap_finding WHERE class = @class::text;

-- name: AgentGapClassCount :one
-- Failures of one class in [since, until) (the watch's before and after windows).
SELECT count(*)::int FROM agent_gap WHERE class = @class::text AND at >= @since::timestamptz AND at < @until::timestamptz;

-- name: AgentGapRunCount :one
-- Agent runs started in [since, until): the denominator of a class's rate.
SELECT count(*)::int FROM execution WHERE kind = 'agent_run' AND started_at >= @since::timestamptz AND started_at < @until::timestamptz;

-- name: AgentGapQuestion :one
-- The answered question an answer hook reads (agentGapAnswerAfter).
SELECT id, coalesce(answer, '')::text AS answer, coalesce(data->>'source', '')::text AS source, coalesce(data->>'class', '')::text AS class
FROM question WHERE id = @id::text;
