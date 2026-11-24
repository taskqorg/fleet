-- The personal Lab operations set the authenticated actor and review capability for the table RLS.

-- name: PersonalLabDraftScope :exec
SELECT set_config('fleet.lab_author', sqlc.arg(author)::text, true),
 set_config('fleet.lab_review', CASE WHEN sqlc.arg(review)::boolean THEN 'true' ELSE 'false' END, true);

-- name: PersonalLabDrafts :many
SELECT * FROM personal_lab_draft
ORDER BY updated_at DESC, id;

-- name: PersonalLabDraftSave :one
INSERT INTO personal_lab_draft(id, author, project, pipeline, variant, pipeline_revision)
VALUES (@id, @author, @project, @pipeline, @variant, sqlc.narg(pipeline_revision))
ON CONFLICT (id) DO UPDATE SET project = excluded.project, pipeline = excluded.pipeline,
 variant = excluded.variant, pipeline_revision = excluded.pipeline_revision, updated_at = now()
WHERE personal_lab_draft.author = excluded.author AND personal_lab_draft.pipeline_revision IS NULL
RETURNING *;

-- name: PersonalLabDraftWithdraw :execrows
DELETE FROM personal_lab_draft WHERE id = @id AND author = @author AND promoted_method IS NULL;

-- name: PersonalLabDraftReview :one
SELECT * FROM personal_lab_draft
WHERE id = @id AND pipeline_revision IS NOT NULL AND fleet_visible(project)
FOR UPDATE;

-- name: PersonalLabDraftPromoted :exec
UPDATE personal_lab_draft SET promoted_method = @promoted_method, updated_at = now() WHERE id = @id;
