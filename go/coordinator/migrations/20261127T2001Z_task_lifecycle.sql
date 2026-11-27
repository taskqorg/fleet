CREATE TABLE IF NOT EXISTS task_wake (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind IN ('task','reply','blocker')),
 source_id text NOT NULL,
 task_id text REFERENCES task(id) ON DELETE CASCADE,
 project_id text,
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 last_error text,
 disposition text,
 cursor_id text
);
CREATE INDEX IF NOT EXISTS task_wake_due ON task_wake(next_attempt_at,id) WHERE disposition IS NULL;
CREATE INDEX IF NOT EXISTS task_wake_task ON task_wake(task_id) WHERE disposition IS NULL;
CREATE INDEX IF NOT EXISTS task_dependency_reverse ON task_dependency(depends_on_task_id,task_id) WHERE kind='blocks';

CREATE TABLE IF NOT EXISTS task_delivery_result (
 task_id text PRIMARY KEY REFERENCES task(id) ON DELETE CASCADE,
 project_id text,
 proof jsonb NOT NULL,
 verification_key text NOT NULL,
 stage_since timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS task_recovery (
 task_id text PRIMARY KEY REFERENCES task(id) ON DELETE CASCADE,
 project_id text,
 deadline timestamptz NOT NULL,
 generation integer NOT NULL DEFAULT 1 CHECK (generation > 0),
 turns integer NOT NULL DEFAULT 0 CHECK (turns BETWEEN 0 AND 2),
 execution_id text,
 progress text[] NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX IF NOT EXISTS task_recovery_execution ON task_recovery(execution_id) WHERE execution_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS task_delivery_verification (
 verification_key text PRIMARY KEY,
 task_id text NOT NULL REFERENCES task(id) ON DELETE CASCADE,
 project_id text,
 execution_id text NOT NULL UNIQUE,
 deadline timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS task_delivery_verification_task ON task_delivery_verification(task_id);
