CREATE TABLE IF NOT EXISTS youtrack_binding (
 id text PRIMARY KEY,
 project_id text NOT NULL,
 bot_id text NOT NULL REFERENCES bot(id) ON DELETE CASCADE,
 instance text NOT NULL,
 remote_project_id text NOT NULL,
 remote_project_key text NOT NULL,
 issue_ids text[] NOT NULL,
 bot_user_id text NOT NULL,
 reviewer_user_id text NOT NULL,
 reviewer_person text NOT NULL,
 token_sha256 text NOT NULL,
 started_ms bigint NOT NULL,
 cursor_ms bigint NOT NULL,
 revision integer NOT NULL DEFAULT 1,
 disconnected boolean NOT NULL DEFAULT false,
 tombstoned_at timestamptz,
 UNIQUE (id, project_id)
);
CREATE INDEX IF NOT EXISTS youtrack_binding_selector ON youtrack_binding(instance, remote_project_id);

CREATE TABLE IF NOT EXISTS youtrack_task (
 instance text NOT NULL,
 issue_id text NOT NULL,
 binding_id text NOT NULL,
 project_id text NOT NULL,
 task_id text UNIQUE REFERENCES task(id) ON DELETE SET NULL,
 decision_ms bigint NOT NULL,
 assignment_ms bigint NOT NULL,
 assignment_authorized boolean NOT NULL,
 issue_updated_ms bigint NOT NULL,
 tombstoned_at timestamptz,
 PRIMARY KEY (instance, issue_id),
 UNIQUE (binding_id, issue_id),
 FOREIGN KEY (binding_id, project_id) REFERENCES youtrack_binding(id, project_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS youtrack_receipt (
 binding_id text NOT NULL,
 issue_id text NOT NULL,
 remote_event text NOT NULL,
 project_id text NOT NULL,
 observed_ms bigint NOT NULL,
 woke boolean NOT NULL,
 tombstoned_at timestamptz,
 PRIMARY KEY (binding_id, issue_id, remote_event),
 FOREIGN KEY (binding_id, project_id) REFERENCES youtrack_binding(id, project_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS youtrack_comment_work (
 id text PRIMARY KEY,
 binding_id text NOT NULL,
 issue_id text NOT NULL,
 project_id text NOT NULL,
 milestone text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('link', 'question', 'result')),
 comment_id text,
 provider_created_ms bigint,
 sent_at timestamptz,
 confirmed_at timestamptz,
 skip_reason text,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text,
 tombstoned_at timestamptz,
 UNIQUE (binding_id, issue_id, milestone),
 FOREIGN KEY (binding_id, issue_id) REFERENCES youtrack_task(binding_id, issue_id) ON DELETE CASCADE,
 FOREIGN KEY (binding_id, project_id) REFERENCES youtrack_binding(id, project_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS youtrack_comment_due ON youtrack_comment_work(next_attempt_at, id) WHERE confirmed_at IS NULL AND skip_reason IS NULL;

CREATE TABLE IF NOT EXISTS youtrack_inbox (
 id text PRIMARY KEY,
 binding_id text NOT NULL,
 project_id text NOT NULL,
 issue_id text NOT NULL,
 signal text NOT NULL CHECK (signal IN ('assignment', 'comment')),
 revision_ms bigint NOT NULL,
 binding_revision integer NOT NULL,
 event_ref text NOT NULL,
 disposition text,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text,
 tombstoned_at timestamptz,
 FOREIGN KEY (binding_id, project_id) REFERENCES youtrack_binding(id, project_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS youtrack_inbox_due ON youtrack_inbox(next_attempt_at, id) WHERE disposition IS NULL;
