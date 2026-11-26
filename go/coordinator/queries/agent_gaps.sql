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
  x.host_id, x.harness, coalesce(x.config_ref->>'profileId', x.data->>'profileId')::text AS profile_id, x.session_id, x.task_id, x.experiment_id
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
