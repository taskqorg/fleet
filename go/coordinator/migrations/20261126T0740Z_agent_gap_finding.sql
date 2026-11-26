-- Agent gap findings (docs/designs/fleet-telemetry.md#10-agent-gaps): a class above the finding threshold, its problem
-- question (cause gap:<class>), the PM conversation investigate started, the pull request that changed the setting,
-- the change point (when that release ran on every host) and the before/after verdict (agent_gaps.go, agent_gap_findings.go).
CREATE TABLE IF NOT EXISTS agent_gap_finding(
 class text PRIMARY KEY,
 project_id text NULL,
 state text NOT NULL CHECK (state IN ('open', 'investigating', 'dismissed', 'linked', 'watching', 'fixed', 'still-failing')),
 question_id text NULL,
 session_id text NULL,
 pr bigint NULL,
 pr_url text NULL,
 release text NULL,
 change_at timestamptz NULL,
 before_count integer NULL,
 after_count integer NULL,
 before_runs integer NULL,
 after_runs integer NULL,
 verdict_at timestamptz NULL,
 note text NOT NULL DEFAULT '',
 opened_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
