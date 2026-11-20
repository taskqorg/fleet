-- Pipeline runs on the work kit (pipeline_work.go, docs/designs/fleet-standard.md S6): one pipeline_work row per run
-- the kit still has to move; the retry columns are the kit's (work_kit.go).

-- name: PipelineWorkDue :many
SELECT run_id,attempts FROM pipeline_work WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz ORDER BY next_attempt_at,run_id LIMIT sqlc.arg(lim);

-- name: PipelineWorkEnsure :execrows
-- A running run has its row; nothing when it has one.
INSERT INTO pipeline_work(run_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: PipelineWorkDelete :exec
DELETE FROM pipeline_work WHERE run_id=$1;

-- name: PipelineRunLock :one
-- The run row, locked for this step's transaction.
SELECT id FROM execution WHERE id=$1 FOR UPDATE;

-- name: PipelineRunOfExecution :one
-- The pipeline run an agent or script step's own execution belongs to; '' for any other execution.
SELECT coalesce(data->>'pipelineRunId','')::text AS run_id FROM execution WHERE id=$1;

-- name: LabWorkDue :many
-- The due experiments, and a running experiment without its row (one set running outside the lab queue), first.
SELECT experiment_id,attempts FROM (
 SELECT e.id AS experiment_id,0 AS attempts,NULL::timestamptz AS due FROM experiment e WHERE e.state='running' AND NOT EXISTS(SELECT 1 FROM lab_work w WHERE w.experiment_id=e.id)
 UNION ALL SELECT experiment_id,attempts,next_attempt_at FROM lab_work WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz) d
ORDER BY due NULLS FIRST,experiment_id LIMIT sqlc.arg(lim);

-- name: LabWorkEnsure :execrows
INSERT INTO lab_work(experiment_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: LabWorkDelete :exec
DELETE FROM lab_work WHERE experiment_id=$1;

-- name: LabExperimentState :one
SELECT state FROM experiment WHERE id=$1;
