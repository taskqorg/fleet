-- Inputs by reference staged after pipeline_run answers (docs/designs/fleet-project-apps.md#inputs-by-reference): one
-- row per ref input of a preparing run, on the work kit's input-refs queue. The run's record names the ref as asked
-- (inputRefs); the row goes once its file is stored or the run ends, and with the run's execution row.
CREATE TABLE IF NOT EXISTS pipeline_input_ref(
 id text PRIMARY KEY,
 run_id text NOT NULL REFERENCES execution(id) ON DELETE CASCADE,
 path text NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NULL,
 UNIQUE (run_id, path)
);
CREATE INDEX IF NOT EXISTS pipeline_input_ref_due ON pipeline_input_ref(next_attempt_at);
