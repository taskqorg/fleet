-- Agent gaps (docs/designs/fleet-telemetry.md#10-agent-gaps): one row per failed agent tool call or gateway call that a rule of
-- config/agent-gaps.json names, written at ingest (agent_gaps.go). execution_id is the agent run that lacked something
-- (for a gateway call, the run that made it; a call without one keeps its own tool_run row); rules is the digest of the
-- rule table that classified it.
CREATE TABLE IF NOT EXISTS agent_gap(
 id text PRIMARY KEY,
 execution_id text NOT NULL REFERENCES execution(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 source text NOT NULL CHECK (source IN ('agent', 'gateway')),
 at timestamptz NOT NULL,
 class text NOT NULL,
 family text NOT NULL,
 tool text NOT NULL DEFAULT '',
 line text NOT NULL DEFAULT '',
 rules text NOT NULL,
 tombstoned_at timestamptz NULL
);
CREATE INDEX IF NOT EXISTS agent_gap_at ON agent_gap(at);
CREATE INDEX IF NOT EXISTS agent_gap_execution ON agent_gap(execution_id);
-- One row per completed classification of a window: the rules digest it used and how many days it covered.
CREATE TABLE IF NOT EXISTS agent_gap_pass(
 rules text PRIMARY KEY,
 days integer NOT NULL,
 at timestamptz NOT NULL DEFAULT now()
);
