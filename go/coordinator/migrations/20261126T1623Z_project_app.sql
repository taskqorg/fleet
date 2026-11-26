-- Project apps (docs/designs/fleet-project-apps.md): a project's own application calls its pipelines with a key.
-- project_app is project-scoped; project_app_key holds only key hashes and is read before any scope is known (like
-- host secrets), so it carries the app's project and approver itself. apps_edge is the one Cloudflare edge row.
CREATE TABLE IF NOT EXISTS project_app(
 id text PRIMARY KEY,
 project_id text NOT NULL,
 name text NOT NULL,
 state text NOT NULL CHECK (state IN ('asked', 'active', 'declined', 'revoked')),
 request_id text NOT NULL,
 asked_by text NOT NULL,
 approved_by text NULL,
 question_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 last_used_at timestamptz NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz NULL,
 UNIQUE (project_id, request_id)
);
CREATE INDEX IF NOT EXISTS project_app_project ON project_app(project_id, created_at);
CREATE INDEX IF NOT EXISTS project_app_question ON project_app(question_id) WHERE question_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_app_key(
 id text PRIMARY KEY,
 app_id text NOT NULL REFERENCES project_app(id) ON DELETE CASCADE,
 project_id text NOT NULL,
 directed_by text NOT NULL,
 sha256 text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 taken_at timestamptz NULL,
 last_used_at timestamptz NULL,
 retired_at timestamptz NULL
);
CREATE INDEX IF NOT EXISTS project_app_key_app ON project_app_key(app_id, created_at);

CREATE TABLE IF NOT EXISTS apps_edge(
 id text PRIMARY KEY CHECK (id = 'edge'),
 state text NOT NULL CHECK (state IN ('missing', 'asked', 'working', 'ready', 'failed')),
 request_id text NULL,
 question_id text NULL,
 rotate boolean NOT NULL DEFAULT false,
 payload jsonb NOT NULL DEFAULT '{}',
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- The apps-edge queue's pending Cloudflare work (work kit columns), one row while the work is due.
CREATE TABLE IF NOT EXISTS apps_edge_work(
 id text PRIMARY KEY CHECK (id = 'edge'),
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NULL
);

ALTER TABLE execution ADD COLUMN IF NOT EXISTS app_id text NULL;
CREATE INDEX IF NOT EXISTS execution_app ON execution(app_id, started_at) WHERE app_id IS NOT NULL;
