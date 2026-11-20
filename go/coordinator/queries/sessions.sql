-- Sessions and their agent tool receipts (sessions.go, agent_receipts.go, agents.go).

-- name: ReceiptOf :one
-- An agent tool request identity the executor journaled, committed with its mutation (agent_receipts.go).
SELECT execution_id, directing_email, operation, payload_sha, result, authority FROM agent_tool_receipt WHERE id=$1;

-- name: ReceiptInsert :exec
INSERT INTO agent_tool_receipt(id, execution_id, directing_email, operation, payload_sha, result, authority)
VALUES (@id, @execution_id, @directing_email, @operation, @payload_sha, @result, @authority);

-- name: AgentsCompatibilityRuns :many
-- The latest execution in 30 days of each (harness, version, model, effort) combination run from agent definition @id
-- (agents.go agentCompatibility); @defs is [{"kind":"agent","id":@id}]. effort and revision are their JSON values (NULL: absent).
SELECT DISTINCT ON (coalesce(e.harness,'codex'),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''))
  coalesce(e.harness,'codex')::text AS harness, e.harness_version, coalesce(e.model,'')::text AS model, (e.data->'effort')::jsonb AS effort,
  e.state::text AS state, e.problem, e.id, coalesce(e.ended_at,e.started_at,e.updated_at)::text AS at, (d->'revision')::jsonb AS revision
FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
WHERE e.definitions @> @defs::jsonb AND d->>'kind'='agent' AND d->>'id'= @id::text AND coalesce(e.started_at,e.updated_at) >= now() - interval '30 days'
ORDER BY coalesce(e.harness,'codex'),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''),e.seq DESC;
