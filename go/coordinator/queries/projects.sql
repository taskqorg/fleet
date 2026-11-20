-- Project operations (projects.go): project codes, project questions, pipeline history and the prepared-read gate.
-- A project question (question_ask, or a key request) is an operator decision or secret question with the asker's request id.

-- name: ProjectsCurrentCodes :many
SELECT project_id, code FROM project_code WHERE current;

-- name: ProjectsDecisionByRequest :many
-- A project's operator decision by the asker's request id (question_ask is idempotent on it).
SELECT * FROM question WHERE kind='operator_decision' AND project_id=sqlc.arg(project_id)::text AND request_id=sqlc.arg(request_id)::text;

-- name: ProjectsQuestionByID :many
SELECT * FROM question WHERE id=sqlc.arg(id) AND kind IN ('operator_decision','secret') AND request_id IS NOT NULL;

-- name: ProjectsQuestionHistory :many
SELECT * FROM question WHERE kind IN ('operator_decision','secret') AND request_id IS NOT NULL AND project_id=sqlc.arg(project_id)::text ORDER BY seq;

-- name: ProjectsPipelineRunIDs :many
-- A project's pipeline runs (runRows in pipelines.go: a run record was written for it), oldest first.
SELECT id FROM execution WHERE kind='pipeline_run' AND data ? 'pipelineId' AND data->>'projectId'=sqlc.arg(project_id)::text ORDER BY seq;

-- name: ProjectsLiveDefinition :one
SELECT EXISTS(SELECT 1 FROM definition WHERE kind='project' AND id=sqlc.arg(id) AND retired_at IS NULL);
