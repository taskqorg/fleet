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
