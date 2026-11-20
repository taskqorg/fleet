-- People (people.go, person_settings.go): holders of the objects a holder rule names, the settings audit and the
-- person-setting rows (migration 78: id person-setting:<email>, email a column, payload the other record fields).

-- name: PeopleAccountHolder :one
SELECT coalesce(holder, '')::text AS holder FROM managed_account WHERE id=$1;

-- name: PeopleConnectionHolder :one
SELECT coalesce(holder, '')::text AS holder FROM api_connection WHERE id=$1;

-- name: PersonSettingPayload :one
SELECT payload FROM person_setting WHERE id=$1;

-- name: PersonSettingPut :exec
-- One row per person, no project; payload holds the settings fields under their record names.
INSERT INTO person_setting(id, email, project_id, payload) VALUES(@id::text, @email::text, NULL, @payload::jsonb)
 ON CONFLICT(id) DO UPDATE SET email=excluded.email, project_id=NULL, payload=excluded.payload, updated_at=now();
