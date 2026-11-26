-- Agent gaps of source dispatch (docs/designs/fleet-telemetry.md#10-agent-gaps): a queued agent run that only a changed
-- setting lets start (dispatch.go, settingsRefuse), one row per execution and cause kind. Host diagnosis reports
-- (docs/designs/fleet-system-management.md#hosts, host_diagnose): the latest report each host sent in its poll, and
-- when one was last asked for.
ALTER TABLE agent_gap DROP CONSTRAINT IF EXISTS agent_gap_source_check;
ALTER TABLE agent_gap ADD CONSTRAINT agent_gap_source_check CHECK (source IN ('agent', 'gateway', 'dispatch'));
CREATE TABLE IF NOT EXISTS host_diagnosis(
 host_id text PRIMARY KEY,
 requested_at timestamptz NULL,
 reported_at timestamptz NULL,
 report jsonb NULL
);
