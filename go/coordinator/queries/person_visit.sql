-- Last page visits (person_visit.go; migration 99): one row per person and page.

-- name: PersonVisitGet :one
SELECT visited_at FROM person_visit WHERE email = @email::text AND page = @page::text;

-- name: PersonVisitRecord :one
-- Records this visit and answers the one before it (NULL: none). The old row is read in the same statement.
WITH old AS (SELECT visited_at FROM person_visit WHERE email = @email::text AND page = @page::text),
up AS (INSERT INTO person_visit(email, page, visited_at) VALUES (@email::text, @page::text, @at::timestamptz)
 ON CONFLICT (email, page) DO UPDATE SET visited_at = excluded.visited_at)
SELECT (SELECT visited_at FROM old)::timestamptz AS previous_at;
