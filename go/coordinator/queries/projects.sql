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

-- name: ProjectRecommendation :many
-- The method a project selected (recommendation_set), as the catalogue reads it.
SELECT jsonb_build_object('id', id, 'projectId', project_id, 'methodId', method_id, 'selectedAt', (extract(epoch FROM selected_at) * 1000)::bigint, 'actor', actor)::jsonb
FROM project_recommendation WHERE project_id = @project_id::text;

-- name: ProjectRecommendationSet :exec
INSERT INTO project_recommendation(id, project_id, method_id, actor) VALUES (@id::text, @project_id::text, @method_id::text, @actor::text)
ON CONFLICT (id) DO UPDATE SET method_id=excluded.method_id, selected_at=now(), actor=excluded.actor, updated_at=now();
