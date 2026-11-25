-- Host update history (docs/designs/fleet-host-updates.md, "What is recorded"; host_update_history): one row per change
-- of the update result a host reports on its poll (fleet-update's update-state.json last; PDS18 merges its coordinator
-- and executor parts), never rewritten. payload holds the component versions the release pins for the host's target
-- ({"components": {"cli/codex": "0.169.0"}}) and those it changed against the host's last switched release ("brought").
-- The poll writes it in the host observation's transaction (host_poll.go); visible as the host's observation is.
CREATE TABLE IF NOT EXISTS host_update (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 host_id text NOT NULL REFERENCES host(host_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 holder text NULL,
 project_id text NULL,
 version text NOT NULL,
 update_state text NOT NULL,
 reason text NOT NULL DEFAULT '',
 at timestamptz NOT NULL,
 payload jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS host_update_host ON host_update (host_id, seq DESC);
