-- A Lab trial's leaderboard facts (lab_leaderboard: Lab's comparison, lab_view): the example of its cell, the use case
-- of its example and its duration, which lived only in the run's data. A pipeline run's data carries its snapshots
-- (about 320 kB a run), so reading three small fields detoasted all of it: LabLeaderboardTrials took 287 ms for 421
-- trials under RLS (db_explain, prod 2026-11-26). Kept by the trigger lab_trial_execution (schema/) in the same
-- transaction as the run's writes; visible when its run is (schema/policies.sql).
CREATE TABLE IF NOT EXISTS lab_trial (
 execution_id text PRIMARY KEY REFERENCES execution(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 cell_example text NOT NULL DEFAULT '',
 example_use_case_id text NOT NULL DEFAULT '',
 duration_ms double precision NOT NULL DEFAULT -1
);
-- The backfill reads every run (the owner is under FORCE ROW LEVEL SECURITY too).
SELECT set_config('fleet.project_ids', '*', true);
INSERT INTO lab_trial (execution_id, cell_example, example_use_case_id, duration_ms)
SELECT e.id, coalesce(e.data->'cell'->>'example', ''), coalesce(e.data->'example'->>'use_case_id', ''),
 coalesce(CASE WHEN (e.data->>'durationMs') ~ '^([0-9]+(\.[0-9]*)?|\.[0-9]+)$' THEN (e.data->>'durationMs')::float8 END, -1)
FROM execution e WHERE e.kind = 'pipeline_run' AND e.experiment_id IS NOT NULL
ON CONFLICT (execution_id) DO NOTHING;
