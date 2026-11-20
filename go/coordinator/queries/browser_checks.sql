-- The people file re-read (browser_checks.go): the open question about a broken file and the rows of the last valid load.

-- name: BrowserPeopleQuestionOpen :one
SELECT EXISTS (SELECT 1 FROM question WHERE state='open' AND data->>'peopleFile'='true');

-- name: BrowserPersonRows :many
SELECT email, name, roles, owner FROM person;

-- name: BrowserPersonGrants :many
SELECT email, object_kind, object_id FROM person_grant;
