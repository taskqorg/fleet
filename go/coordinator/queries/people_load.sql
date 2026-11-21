-- The people file's projection (main.go loadPeople) and the host credential check (main.go hostFromSecret).

-- name: PeopleRevokeInitialMembers :exec
-- A project's initial member who left the file loses it.
UPDATE definition SET initial_member_revoked_at = now(), access_version = access_version + 1
WHERE kind = 'project' AND initial_member IS NOT NULL AND initial_member_revoked_at IS NULL AND NOT (initial_member = ANY (@emails::text[]));

-- name: PeopleRevokeSessionDirectors :exec
UPDATE session SET director_access_revoked_at = now()
WHERE project_id IS NULL AND directed_by IS NOT NULL AND director_access_revoked_at IS NULL AND NOT (directed_by = ANY (@emails::text[]));

-- name: PeopleRevokeExecutionDirectors :exec
UPDATE execution SET data = data || jsonb_build_object('directorAccessRevokedAt', extract(epoch FROM now()) * 1000)
WHERE project_id IS NULL AND session_id IS NULL AND directed_by IS NOT NULL AND NOT (data ? 'directorAccessRevokedAt')
  AND NOT (directed_by = ANY (@emails::text[]));

-- name: PeopleRevokeRunTokens :exec
UPDATE run_token SET revoked_at = coalesce(revoked_at, now()), director_access_revoked_at = now(), updated_at = now()
WHERE directed_by IS NOT NULL AND director_access_revoked_at IS NULL AND NOT (directed_by = ANY (@emails::text[]));

-- name: PeopleMarkRemovedRequesters :exec
UPDATE experiment SET requester_removed_at = now()
WHERE requester_removed_at IS NULL AND requested_by IN (SELECT email FROM person WHERE NOT (email = ANY (@emails::text[])));

-- name: PeopleClear :exec
DELETE FROM person;

-- name: PersonInsert :exec
INSERT INTO person(email, name, roles, owner) VALUES (@email, @name, @roles::text[], @owner);

-- name: PersonGrantInsert :exec
INSERT INTO person_grant(email, object_kind, object_id) VALUES (@email, @object_kind, @object_id) ON CONFLICT DO NOTHING;

-- name: PeopleChanged :exec
SELECT fleet_enqueue_wake('fleet_change', '');

-- name: HostRecordBySecret :one
-- The host record (as its record reads, queries/records.sql) whose secret matches, while its holder is a person.
SELECT (h.payload || jsonb_strip_nulls(jsonb_build_object('id', h.id, 'hostId', h.host_id, 'holder', h.holder,
  'state', nullif(h.state, 'active'), 'intake', h.intake, 'secretHash', h.secret_hash, 'removedSecretHash', h.removed_secret_hash,
  'projectId', h.project_id)))::jsonb AS record
FROM host h JOIN person p ON p.email = h.holder WHERE h.secret_hash = @secret_hash;
