CREATE TABLE IF NOT EXISTS publishing_ci_recovery (
 id text PRIMARY KEY,
 project_id text NOT NULL,
 round_key text NOT NULL,
 released boolean NOT NULL DEFAULT false,
 repo text NOT NULL,
 number bigint NOT NULL,
 head_sha text NOT NULL,
 run_number bigint NOT NULL,
 original_attempt bigint NOT NULL,
 source_execution_id text NOT NULL,
 command_id text,
 command_until timestamptz,
 state text NOT NULL,
 dispatched boolean NOT NULL DEFAULT false,
 handoff text,
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 attempts int NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text,
 tombstoned_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS publishing_ci_recovery_round ON publishing_ci_recovery(round_key) WHERE NOT released;
CREATE INDEX IF NOT EXISTS publishing_ci_recovery_due ON publishing_ci_recovery(next_attempt_at)
 WHERE state IN ('queued', 'rate_limited', 'dispatch_unknown', 'dispatched', 'observing', 'handoff_pending');
CREATE INDEX IF NOT EXISTS publishing_ci_recovery_project ON publishing_ci_recovery(project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS publishing_ci_request (
 id text PRIMARY KEY,
 project_id text NOT NULL,
 recovery_id text NOT NULL REFERENCES publishing_ci_recovery(id) ON DELETE CASCADE,
 payload jsonb NOT NULL,
 tombstoned_at timestamptz
);
