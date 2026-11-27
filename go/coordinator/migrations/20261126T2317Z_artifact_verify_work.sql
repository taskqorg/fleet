-- Work accepted from older store hosts that report a digest without a reservation key.
CREATE TABLE IF NOT EXISTS artifact_verify_work(
 id text PRIMARY KEY,
 artifact_id text NOT NULL,
 store_id text NOT NULL,
 staging_key text NOT NULL,
 digest text NOT NULL,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 last_error text,
 UNIQUE(artifact_id, store_id, staging_key, digest),
 FOREIGN KEY(artifact_id, store_id) REFERENCES artifact_copy(artifact_id, store_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS artifact_verify_work_due ON artifact_verify_work(next_attempt_at, id);
