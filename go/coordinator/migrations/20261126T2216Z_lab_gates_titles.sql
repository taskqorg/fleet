-- Lab decisions go to whoever must make them (docs/designs/fleet-lab.md, Experiment record). A gate is a pass/fail
-- experiment (host qualification, smoke) that decides itself at close: passed or failed, never a question. title is
-- the record's human name ("antigravity 1.5.18 pin · PR #1175"); requested_by_execution is the agent run that created
-- the experiment, whose bot or session decides it.
ALTER TABLE experiment ADD COLUMN IF NOT EXISTS gate boolean NOT NULL DEFAULT false, ADD COLUMN IF NOT EXISTS title text NULL, ADD COLUMN IF NOT EXISTS requested_by_execution text NULL;
ALTER TABLE experiment DROP CONSTRAINT IF EXISTS experiment_decision;
ALTER TABLE experiment ADD CONSTRAINT experiment_decision CHECK (decision IS NULL OR decision IN ('keep baseline', 'inconclusive', 'pending', 'passed', 'failed') OR decision LIKE 'adopt %');
-- The agent CLI pin qualifications so far: gates, titled by their candidates and pull request.
UPDATE experiment e SET gate = true, title = (
  SELECT string_agg(DISTINCT (v->'overrides'->'candidate'->>'name') || ' ' || (v->'overrides'->'candidate'->'asset'->>'version'), ', ')
  FROM jsonb_array_elements(e.variants) v WHERE v->'overrides'->'candidate'->>'name' IS NOT NULL
) || CASE WHEN (SELECT count(DISTINCT v->'overrides'->'candidate'->>'name') FROM jsonb_array_elements(e.variants) v) > 1 THEN ' pins' ELSE ' pin' END
  || coalesce(' · PR #' || substring(e.goal FROM '/pull/([0-9]+)'), '')
WHERE e.pipeline_id = 'pins-qualify';
-- Their history rows so far are named by the new title too.
UPDATE activity a SET title = x.title FROM experiment x
WHERE a.experiment_id = x.id AND x.title IS NOT NULL AND a.kind = 'lab' AND a.title = coalesce(x.lab_group, x.goal, x.kind::text);
UPDATE activity a SET title = x.title || ' trial' FROM experiment x
WHERE a.experiment_id = x.id AND x.title IS NOT NULL AND a.kind <> 'lab' AND a.title = coalesce(x.lab_group, x.goal, x.kind::text) || ' trial';
