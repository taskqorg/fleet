-- Bugsink error tracking (bugsink.go, docs/designs/fleet-error-tracking.md).

-- name: BugsinkFleetProject :one
-- The live Fleet project whose head revision's settings.errors.bugsink lists a Bugsink project name.
SELECT d.id FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
 WHERE d.kind='project' AND d.retired_at IS NULL AND jsonb_typeof(r.content::jsonb #> '{settings,errors,bugsink}')='array'
 AND (r.content::jsonb #> '{settings,errors,bugsink}') ? @name::text ORDER BY d.id LIMIT 1;

-- name: BugsinkIgnore :one
-- A live Fleet project's settings.errors.ignore: the title substrings of Bugsink issues that ask nothing.
SELECT coalesce(r.content::jsonb #> '{settings,errors,ignore}', '[]'::jsonb)::jsonb AS ignore FROM definition d
 JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
 WHERE d.kind='project' AND d.id=sqlc.arg(project_id)::text AND d.retired_at IS NULL;

-- name: BugsinkHeldProblems :many
-- The open problems of Bugsink issues, with their project ('' for none).
SELECT cause::text AS cause, coalesce(project_id,'')::text AS project FROM problem WHERE cause LIKE @prefix::text || '%' AND state = 'open' ORDER BY seq;
