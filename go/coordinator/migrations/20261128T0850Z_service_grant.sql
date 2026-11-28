CREATE TABLE IF NOT EXISTS service_grant (
 name text PRIMARY KEY,
 project_id text NOT NULL,
 request_id text NOT NULL,
 question_id text UNIQUE REFERENCES question(id) ON DELETE SET NULL,
 asked_by text NOT NULL,
 approved_at timestamptz,
 declined_at timestamptz,
 account_id text,
 enrollment_reference text,
 application_id text,
 policy_id text UNIQUE,
 token_id text UNIQUE,
 client_id text UNIQUE,
 adopted_at timestamptz,
 paused boolean NOT NULL DEFAULT false,
 checked_at timestamptz,
 provider_check text,
 binding_check text,
 expires_at timestamptz,
 provider_last_seen_at timestamptz,
 check_sequence bigint NOT NULL DEFAULT 0,
 check_started_at timestamptz,
 attempts int NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text
);
CREATE INDEX IF NOT EXISTS service_grant_due ON service_grant(next_attempt_at) WHERE approved_at IS NOT NULL AND token_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS service_grant_renewal (
 grant_name text NOT NULL REFERENCES service_grant(name) ON DELETE CASCADE,
 request_id text NOT NULL,
 requested_by text NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT now(),
 baseline_expires_at timestamptz,
 baseline_secret_version int,
 dispatched_at timestamptz,
 acknowledged_expiry timestamptz,
 verified_expiry timestamptz,
 completed_at timestamptz,
 result text,
 detail text,
 PRIMARY KEY(grant_name, request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS service_grant_one_renewal ON service_grant_renewal(grant_name) WHERE completed_at IS NULL;
CREATE INDEX IF NOT EXISTS service_grant_renewal_latest ON service_grant_renewal(grant_name, requested_at DESC);
