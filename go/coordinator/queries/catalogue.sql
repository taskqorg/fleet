-- Catalogue evidence (catalogue_evidence.go): the experiments that may assess a method.

-- name: CatalogueExperiments :many
-- A project's launched experiments, newest first.
SELECT id, state, stop_reason, created_at, pipeline_id, primary_evaluator, examples, variants, repeats FROM experiment
 WHERE project_id=$1 AND state <> 'draft' ORDER BY created_at DESC, id DESC;

-- name: CatalogueSourceExperiment :many
SELECT id, state, stop_reason, created_at, pipeline_id, primary_evaluator, examples, variants, repeats FROM experiment
 WHERE id=$1 AND project_id=$2;
