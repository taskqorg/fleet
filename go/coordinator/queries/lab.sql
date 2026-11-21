-- Lab reads and writes besides the experiment state machine (lab_*.go; experiments.sql holds the machine).

-- name: LabTrialAssessmentsSet :exec
-- A trial run's linked project assessments (lab_assessment.go).
UPDATE execution SET data=jsonb_set(data,'{projectAssessments}',sqlc.arg(assessments)::jsonb) WHERE id=sqlc.arg(id);

-- name: LabApprovedExamples :many
-- A project's approved stored examples, oldest first (lab_compare.go).
SELECT id FROM artifact WHERE role='example' AND state='stored' AND approved_at IS NOT NULL AND project_id=$1 ORDER BY created_at,id;

-- name: LabVariantsSet :exec
UPDATE experiment SET variants=sqlc.arg(variants)::jsonb WHERE id=sqlc.arg(id);

-- name: LabVariantsSetRunning :exec
-- A loop's next iteration's variants, only while it runs.
UPDATE experiment SET variants=sqlc.arg(variants)::jsonb WHERE id=sqlc.arg(id) AND state='running';

-- name: LabSuffixRuns :many
-- The runs a recovery request created, as records (lab_retry.go).
SELECT (data || jsonb_build_object('id',id,'experimentId',experiment_id,'kind',kind))::jsonb AS record FROM execution WHERE request_id=$1;

-- name: LabHostJobInProject :one
SELECT EXISTS(SELECT 1 FROM execution WHERE id=sqlc.arg(id) AND project_id=sqlc.narg(project_id) AND kind='host_job')::boolean AS owned;

-- name: LabScoreInsert :exec
-- One score of an experiment's trials (lab_score.go).
INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value,source_execution_id,question_id,rationale,replaces_score_id)
VALUES(sqlc.arg(id),sqlc.arg(experiment_id),sqlc.arg(project_id),sqlc.arg(trial_ids)::text[],sqlc.arg(evaluator),sqlc.arg(evaluator_version),sqlc.arg(form),sqlc.arg(value)::jsonb,
  sqlc.narg(source_execution_id),sqlc.narg(question_id),sqlc.narg(rationale),sqlc.narg(replaces_score_id));

-- name: LabExampleFileFind :one
-- A stored example file by content, of the project or of no project, oldest first (lab.go exampleFileBytes).
SELECT id FROM artifact WHERE role='example-file' AND scope='lab' AND state='stored' AND content_hash=sqlc.arg(content_hash)
  AND coalesce(project_id,'') IN ('', sqlc.arg(project)::text) ORDER BY created_at LIMIT 1;

-- name: LabExampleFileCount :one
SELECT count(*)::int FROM artifact WHERE role='example-file' AND scope='lab' AND state='stored' AND content_hash=sqlc.arg(content_hash)
  AND coalesce(project_id,'')=sqlc.arg(project)::text;

-- name: LabExampleFileInsert :exec
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,role,state)
VALUES(sqlc.arg(id),sqlc.narg(project_id),'lab',sqlc.arg(content_hash),sqlc.arg(size),sqlc.arg(media_type),sqlc.arg(name),'example-file','stored');

-- name: LabExampleByName :one
-- A live example of the project (or of no project) by name (lab.go labExamplesAdd).
SELECT id,content_hash,approved_at FROM artifact WHERE role='example' AND state<>'deleted' AND name=sqlc.arg(name) AND coalesce(project_id,'')=sqlc.arg(project)::text;

-- name: LabPurgedCount :one
SELECT count(*)::int FROM purged_hash WHERE project_id=sqlc.arg(project_id) AND scope='lab' AND content_hash=sqlc.arg(content_hash);

-- name: LabExampleInsert :many
-- A new example; nothing when one of that name exists meanwhile.
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,role,state,approved_at)
VALUES(sqlc.arg(id),sqlc.narg(project_id),'lab',sqlc.arg(content_hash),sqlc.arg(size),'application/json',sqlc.arg(name),'example','stored',sqlc.narg(approved_at))
ON CONFLICT (coalesce(project_id,''),name) WHERE role='example' AND state<>'deleted' DO NOTHING RETURNING id;

-- name: LabExampleApprove :exec
UPDATE artifact SET approved_at=now() WHERE id=sqlc.arg(id);

-- name: LabExampleApproveOpen :exec
-- An example a rating approved, unless it is approved already.
UPDATE artifact SET approved_at=now() WHERE id=sqlc.arg(id) AND role='example' AND approved_at IS NULL;

-- name: LabExamplesList :many
-- The stored examples by name; approved true or false narrows them (lab.go labExamplesList).
SELECT id,project_id,name,content_hash,approved_at,created_at FROM artifact WHERE role='example' AND state='stored'
  AND (sqlc.narg(approved)::boolean IS NULL OR (approved_at IS NOT NULL)=sqlc.narg(approved)::boolean) ORDER BY name;

-- name: LabExample :one
SELECT id,approved_at,project_id,content_hash FROM artifact WHERE id=sqlc.arg(id) AND role='example';

-- name: LabArtifactHash :one
SELECT content_hash FROM artifact WHERE id=sqlc.arg(id);

-- name: LabScoresCurrent :many
-- An experiment's scores no correction replaced, in order (lab.go scoresCurrent).
SELECT * FROM score s WHERE s.experiment_id=sqlc.arg(experiment_id) AND NOT EXISTS (SELECT 1 FROM score r WHERE r.replaces_score_id=s.id) ORDER BY seq;

-- name: LabScore :one
SELECT * FROM score WHERE id=sqlc.arg(id);

-- name: LabScoreReplacedCount :one
SELECT count(*)::int FROM score WHERE replaces_score_id=sqlc.arg(id);

-- name: LabExperiment :one
SELECT * FROM experiment WHERE id=sqlc.arg(id);

-- name: LabExperimentForUpdate :one
-- The experiment row, locked for this transaction (a record change reads and writes it once).
SELECT * FROM experiment WHERE id=sqlc.arg(id) FOR UPDATE;

-- name: LabExperimentForShare :one
-- The experiment row, shared-locked before its requester's person rows (lab.go labRequester).
SELECT * FROM experiment WHERE id=sqlc.arg(id) FOR SHARE;

-- name: LabExperiments :many
SELECT * FROM experiment ORDER BY seq DESC;

-- name: LabExperimentInsert :exec
INSERT INTO experiment(id,project_id,kind,pipeline_id,examples,variants,repeats,evaluators,primary_evaluator,budgets,goal,hypothesis,decision_rule,lab_group,strategy,requested_by)
VALUES(sqlc.arg(id),sqlc.arg(project_id),sqlc.arg(kind),sqlc.arg(pipeline_id),sqlc.arg(examples)::jsonb,sqlc.arg(variants)::jsonb,sqlc.arg(repeats),sqlc.arg(evaluators)::jsonb,
  sqlc.narg(primary_evaluator),sqlc.arg(budgets)::jsonb,sqlc.narg(goal),sqlc.narg(hypothesis),sqlc.narg(decision_rule),sqlc.narg(lab_group),sqlc.arg(strategy)::jsonb,sqlc.arg(requested_by));

-- name: LabExperimentRecordSet :exec
-- One record field of an experiment (an amendment; field is the column name).
UPDATE experiment SET
  goal=CASE WHEN sqlc.arg(field)::text='goal' THEN sqlc.narg(value)::text ELSE goal END,
  hypothesis=CASE WHEN sqlc.arg(field)::text='hypothesis' THEN sqlc.narg(value)::text ELSE hypothesis END,
  decision_rule=CASE WHEN sqlc.arg(field)::text='decision_rule' THEN sqlc.narg(value)::text ELSE decision_rule END,
  lab_group=CASE WHEN sqlc.arg(field)::text='lab_group' THEN sqlc.narg(value)::text ELSE lab_group END,
  primary_evaluator=CASE WHEN sqlc.arg(field)::text='primary_evaluator' THEN sqlc.narg(value)::text ELSE primary_evaluator END
WHERE id=sqlc.arg(id);

-- name: LabNoteInsert :exec
INSERT INTO experiment_note(id,experiment_id,project_id,kind,field,old,new,text,author)
VALUES(sqlc.arg(id),sqlc.arg(experiment_id),sqlc.arg(project_id),sqlc.arg(kind),sqlc.narg(field),sqlc.arg(old)::jsonb,sqlc.arg(new)::jsonb,sqlc.arg(text),sqlc.arg(author));

-- name: LabNotes :many
SELECT id,kind,field,old,new,text,author,at FROM experiment_note WHERE experiment_id=sqlc.arg(experiment_id) ORDER BY seq;

-- name: LabExperimentTouch :exec
-- A note changes the experiment as its readers see it: the row's change reaches the feed.
UPDATE experiment SET id=id WHERE id=sqlc.arg(id);

-- name: LabExperimentDecide :exec
UPDATE experiment SET decision=sqlc.arg(decision),decision_reason=sqlc.narg(decision_reason),decided_by=sqlc.arg(decided_by),decided_at=now() WHERE id=sqlc.arg(id);

-- name: LabExperimentStopReasonClear :exec
UPDATE experiment SET stop_reason=NULL WHERE id=sqlc.arg(id);

-- name: LabExperimentBudgetsSet :exec
UPDATE experiment SET budgets=sqlc.arg(budgets)::jsonb WHERE id=sqlc.arg(id);

-- name: LabTrialSpend :one
-- An experiment's trial runs and their wall time so far (lab.go expSpend).
SELECT count(*) FILTER (WHERE variant IS NOT NULL)::int AS trials,
  coalesce(sum(extract(epoch FROM coalesce(ended_at,now())-started_at)*1000) FILTER (WHERE started_at IS NOT NULL),0)::float8 AS wall_ms
FROM execution WHERE experiment_id=sqlc.arg(experiment_id) AND kind='pipeline_run';

-- name: LabToolSpend :one
SELECT count(*)::int AS calls,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits,
  count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int AS unmeasured
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=sqlc.arg(experiment_id) AND e.kind='tool_run';

-- name: LabRunIDs :many
-- An experiment's pipeline runs in order (lab.go expRuns; pipelines.go runRows).
SELECT id,variant,problem FROM execution WHERE experiment_id=sqlc.arg(experiment_id) AND kind='pipeline_run' AND data ? 'pipelineId' ORDER BY seq;

-- name: LabStepAgents :many
-- The agent steps of an experiment's runs (lab.go expGet).
SELECT coalesce(data->>'pipelineRunId','')::text AS run,host_id,harness,harness_version,model FROM execution
WHERE experiment_id=sqlc.arg(experiment_id) AND data ? 'pipelineStepId' AND data ? 'agentConfig';

-- name: LabRunCredits :many
SELECT coalesce(e.data->>'pipelineRunId',split_part(e.parent_execution_id,':step:',1),'')::text AS run,count(*)::int AS calls,
  coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits,
  count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int AS unmeasured
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=sqlc.arg(experiment_id) AND e.kind='tool_run' GROUP BY 1;

-- name: LabRunUsage :many
-- Each tool call's provider usage, by run (fleet-analytics.md: usage x the latest price entry in force at the call); a call
-- without a run has run ''.
SELECT coalesce(e.data->>'pipelineRunId',split_part(e.parent_execution_id,':step:',1),'')::text AS run,
  (coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool',''))::text AS subject,coalesce(t.connection_cost->>'unit','')::text AS unit,
  (coalesce(t.connection_cost->>'amount',t.connection_cost->>(t.connection_cost->>'unit')) IS NOT NULL)::boolean AS has_amount,
  coalesce(coalesce(t.connection_cost->>'amount',t.connection_cost->>(t.connection_cost->>'unit'))::float8,0)::float8 AS amount,
  coalesce(to_timestamp((e.data->>'startedAt')::bigint/1000.0),e.started_at,now())::timestamptz AS at
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=sqlc.arg(experiment_id) AND e.kind='tool_run';

-- name: LabExecutionDefinitions :one
-- A step execution's definition pins, in the trial's project (lab.go labSelectedEvidence).
SELECT definitions FROM execution WHERE id=sqlc.arg(id) AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id);

-- name: LabRating :one
SELECT id,kind::text AS kind,options,state::text AS state,answer,text,data FROM question WHERE id=sqlc.arg(id);

-- name: LabRatingLockState :one
SELECT state::text AS state FROM question WHERE id=sqlc.arg(id) AND kind='rating' FOR UPDATE;

-- name: LabRatingIDs :many
SELECT id FROM question WHERE kind='rating' AND (sqlc.narg(experiment_id)::text IS NULL OR options->>'experiment_id'=sqlc.narg(experiment_id)::text)
  AND (sqlc.narg(state)::text IS NULL OR state::text=sqlc.narg(state)::text) ORDER BY seq;

-- name: LabExampleRatingCount :one
SELECT count(*)::int FROM question WHERE kind='rating' AND options->>'example_artifact_id'=sqlc.arg(artifact_id)::text;

-- name: LabTrialsLineage :one
-- Non-reused leaf executions per trial ID, traversing strictly downward via indexed lookups (lab.go expGet, lab_leaderboard.go).
WITH RECURSIVE
 trials AS MATERIALIZED (
  SELECT DISTINCT t.trial_id FROM unnest(sqlc.arg(trial_ids)::text[]) AS t(trial_id)
 ),
 trial_steps AS MATERIALIZED (
  SELECT
   p.run_id AS trial_id,
   (p.run_id || ':step:' || p.step_index::text) AS step_exec_id,
   p.ref_mode,
   p.ref_execution_id,
   p.native_execution_id
  FROM pipeline_step p
  JOIN trials t ON t.trial_id = p.run_id
 ),
 reused_steps AS MATERIALIZED (
  SELECT trial_id, step_exec_id
  FROM trial_steps
  WHERE ref_mode = 'reused'
 ),
 seed AS (
  SELECT t.trial_id, c.id AS exec_id, c.kind
  FROM trials t
  JOIN execution c ON c.data ? 'pipelineRunId' AND c.data->>'pipelineRunId' = t.trial_id
  LEFT JOIN reused_steps rs_self ON rs_self.trial_id = t.trial_id AND rs_self.step_exec_id = c.id
  LEFT JOIN reused_steps rs_par ON rs_par.trial_id = t.trial_id AND rs_par.step_exec_id = c.parent_execution_id
  WHERE rs_self.trial_id IS NULL AND rs_par.trial_id IS NULL
  UNION ALL
  SELECT t.trial_id, c.id AS exec_id, c.kind
  FROM trials t
  JOIN execution c ON c.parent_execution_id = t.trial_id
  LEFT JOIN reused_steps rs ON rs.trial_id = t.trial_id AND rs.step_exec_id = c.id
  WHERE rs.trial_id IS NULL
  UNION ALL
  SELECT s.trial_id, c.id AS exec_id, c.kind
  FROM trial_steps s
  JOIN execution c ON c.parent_execution_id = s.step_exec_id
  WHERE s.ref_mode IS DISTINCT FROM 'reused'
  UNION ALL
  SELECT s.trial_id, c.id AS exec_id, c.kind
  FROM trial_steps s
  JOIN execution c ON c.parent_execution_id = s.ref_execution_id
  WHERE s.ref_mode IS DISTINCT FROM 'reused' AND s.ref_execution_id IS NOT NULL
  UNION ALL
  SELECT s.trial_id, c.id AS exec_id, c.kind
  FROM trial_steps s
  JOIN execution c ON c.id = s.native_execution_id
  WHERE s.ref_mode IS DISTINCT FROM 'reused' AND s.native_execution_id IS NOT NULL
  UNION ALL
  SELECT t.trial_id, c.id AS exec_id, c.kind
  FROM trials t
  JOIN session s ON s.pipeline_run_id = t.trial_id
  JOIN execution c ON c.session_id = s.id
  LEFT JOIN reused_steps rs_self ON rs_self.trial_id = t.trial_id AND rs_self.step_exec_id = c.id
  LEFT JOIN reused_steps rs_par ON rs_par.trial_id = t.trial_id AND rs_par.step_exec_id = c.parent_execution_id
  WHERE rs_self.trial_id IS NULL AND rs_par.trial_id IS NULL
 ),
 tree AS (
  SELECT trial_id, exec_id, kind FROM seed
  UNION
  SELECT tr.trial_id, ch.exec_id, ch.kind
  FROM tree tr
  CROSS JOIN LATERAL (
   SELECT c.id AS exec_id, c.kind
   FROM execution c
   WHERE c.parent_execution_id = tr.exec_id
   UNION ALL
   SELECT c.id AS exec_id, c.kind
   FROM session s
   JOIN execution c ON c.session_id = s.id
   WHERE s.parent_execution_id = tr.exec_id
  ) ch
  LEFT JOIN reused_steps rs ON rs.trial_id = tr.trial_id AND rs.step_exec_id = ch.exec_id
  WHERE tr.kind <> 'tool_run'
    AND rs.trial_id IS NULL
 ),
 scoped_leaves AS (
  SELECT 'trial'::text AS scope, tr.trial_id AS scope_key, tr.exec_id AS leaf_id
  FROM tree tr
  WHERE tr.kind NOT IN ('pipeline_run', 'pipeline_step')
    AND (tr.kind <> 'child_agent' OR EXISTS (SELECT 1 FROM execution_totals tot WHERE tot.execution_id = tr.exec_id))
 )
SELECT
 coalesce(array_agg(scope ORDER BY scope, scope_key, leaf_id), '{}')::text[] AS scopes,
 coalesce(array_agg(scope_key ORDER BY scope, scope_key, leaf_id), '{}')::text[] AS scope_keys,
 coalesce(array_agg(leaf_id ORDER BY scope, scope_key, leaf_id), '{}')::text[] AS leaf_ids
FROM scoped_leaves;

-- name: LabLeafSpend :many
-- Precomputed spend and token totals for leaf executions (lab_leaderboard.go).
SELECT
 e.id AS execution_id,
 e.kind::text AS kind,
 (s.execution_id IS NOT NULL)::boolean AS has_spend,
 coalesce(s.stale, true)::boolean AS stale,
 s.cost_currency,
 s.cost_basis,
 s.cost_value,
 coalesce(s.cost_unpriced, 0)::bigint AS cost_unpriced,
 s.payload,
 t.tokens_input,
 t.tokens_output,
 t.tokens_cache_read,
 t.tokens_cache_write
FROM execution e
LEFT JOIN execution_spend s ON s.execution_id = e.id
LEFT JOIN execution_totals t ON t.execution_id = e.id
WHERE e.id = ANY(sqlc.arg(leaf_ids)::text[])
  AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
  AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL);

-- name: LabLeafMeta :many
-- Resolved model and profile metadata for leaf executions (lab_leaderboard.go).
SELECT
 e.id,
 e.kind::text AS kind,
 coalesce(nullif(coalesce(tot.model, e.model), ''), '')::text AS model,
 coalesce(nullif(coalesce(e.profile_id, e.data->'agentConfig'->>'id'), ''), '')::text AS profile_id
FROM execution e
LEFT JOIN execution_totals tot ON tot.execution_id = e.id
WHERE e.id = ANY(sqlc.arg(leaf_ids)::text[])
ORDER BY e.seq;

-- name: LabLeaderboardTrials :many
-- Confirmed succeeded and failed trials within the last days, visible under caller RLS (lab_leaderboard.go).
SELECT
 e.id,
 e.experiment_id,
 e.project_id,
 e.variant,
 e.state::text AS state,
 e.pipeline_id,
 e.config_ref,
 e.started_at,
 e.ended_at,
 coalesce(e.ended_at, e.started_at, e.updated_at)::timestamptz AS trial_at,
 coalesce(e.data->'cell'->>'example', '')::text AS cell_example,
 coalesce(e.data->'example'->>'use_case_id', '')::text AS example_use_case_id,
 coalesce(CASE WHEN (e.data->>'durationMs') ~ '^([0-9]+(\.[0-9]*)?|\.[0-9]+)$' THEN (e.data->>'durationMs')::float8 END, -1)::float8 AS data_duration_ms
FROM execution e
JOIN experiment x ON x.id = e.experiment_id
WHERE e.kind = 'pipeline_run'
  AND e.variant IS NOT NULL
  AND e.experiment_id IS NOT NULL
  AND e.state IN ('succeeded', 'failed')
  AND e.observation = 'confirmed'
  AND (sqlc.narg(project_id)::text IS NULL OR e.project_id = sqlc.narg(project_id)::text)
  AND coalesce(e.ended_at, e.started_at, e.updated_at) >= now() - make_interval(days => sqlc.arg(days)::int)
ORDER BY coalesce(e.ended_at, e.started_at, e.updated_at) DESC, e.seq DESC;

-- name: LabExperimentsByIDs :many
SELECT * FROM experiment WHERE id = ANY(sqlc.arg(ids)::text[]);

-- name: LabScoresByExperiments :many
SELECT * FROM score s
WHERE s.experiment_id = ANY(sqlc.arg(experiment_ids)::text[])
  AND NOT EXISTS (SELECT 1 FROM score r WHERE r.replaces_score_id = s.id)
ORDER BY s.seq;
