-- A project app's run callback (docs/designs/fleet-project-apps.md, Calling Fleet): one row per run started with
-- notify, on the work kit's app-callback queue. It waits at next_attempt_at 'infinity' until the run ends, is due then,
-- retries with the kit's backoff, and ends with delivered_at; it goes with the run's execution row.
CREATE TABLE IF NOT EXISTS app_callback(
 run_id text PRIMARY KEY REFERENCES execution(id) ON DELETE CASCADE,
 app_id text NOT NULL REFERENCES project_app(id) ON DELETE CASCADE,
 url text NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT 'infinity',
 last_error text NULL,
 delivered_at timestamptz NULL
);
CREATE INDEX IF NOT EXISTS app_callback_due ON app_callback(next_attempt_at) WHERE delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS app_callback_app ON app_callback(app_id);
