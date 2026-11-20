-- The experiment state machine (experiment_states.go, docs/designs/fleet-standard.md S5).

-- name: ExperimentLockState :one
SELECT state FROM experiment WHERE id=$1 FOR UPDATE;

-- name: ExperimentMove :exec
-- One move; a given stop reason replaces the recorded one (the experiment_record trigger stamps launched_at and decision).
UPDATE experiment SET state=sqlc.arg(state),stop_reason=coalesce(sqlc.narg(stop_reason),stop_reason) WHERE id=sqlc.arg(id);

-- name: ExperimentsStop :many
-- Stops the running or paused experiments of ids with reason; the ids stopped.
UPDATE experiment SET state='stopped',stop_reason=sqlc.arg(reason)::text WHERE id=ANY(sqlc.arg(ids)::text[]) AND state IN ('running','paused') RETURNING id;

-- name: ExperimentOpenBudgetQuestions :many
SELECT id FROM question WHERE state='open' AND kind='operator_decision' AND data->>'experimentId'=ANY(sqlc.arg(experiment_ids)::text[]) ORDER BY id;
