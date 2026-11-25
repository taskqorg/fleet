-- Lab's comparison recomputes only when Lab data changes (lab_leaderboard.go, labLeaderboardVersion), not every 30 s.
-- lab_trial also keeps each trial's state, end, observation and variant, and when any of its facts last changed;
-- score gets the index its "not replaced" anti join needs (LabScoresByExperiments: 76 ms of nested scans, prod).
ALTER TABLE lab_trial ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS ended_at timestamptz NULL,
 ADD COLUMN IF NOT EXISTS observation text NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS variant text NULL,
 ADD COLUMN IF NOT EXISTS changed_at timestamptz NOT NULL DEFAULT now();
SELECT set_config('fleet.project_ids', '*', true);
UPDATE lab_trial t SET state = e.state::text, ended_at = e.ended_at, observation = e.observation::text, variant = e.variant
FROM execution e WHERE e.id = t.execution_id;
CREATE INDEX IF NOT EXISTS score_replaces ON score (replaces_score_id) WHERE replaces_score_id IS NOT NULL;
