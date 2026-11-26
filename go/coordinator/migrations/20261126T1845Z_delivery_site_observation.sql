CREATE TABLE IF NOT EXISTS delivery_site_observation (
  first_seen timestamptz PRIMARY KEY,
  last_seen timestamptz NOT NULL,
  covered_from timestamptz,
  coordinator_version text NOT NULL,
  ui_version text NOT NULL,
  project_id text NOT NULL,
  CHECK (first_seen <= last_seen),
  CHECK (covered_from <= first_seen)
);

-- T-FLT-86: the old collector stopped storing version sightings when workflow_runs_day was removed.
-- NULL covered_from marks the lost interval. The grant audit records its removal and restoration.
-- Existing exact observations are still usable.
INSERT INTO delivery_site_observation(first_seen, last_seen, coordinator_version, ui_version, project_id)
VALUES ('2026-11-25T17:58:04Z', '2026-11-26T04:19:20Z', '', '', 'fleet')
ON CONFLICT (first_seen) DO NOTHING;
