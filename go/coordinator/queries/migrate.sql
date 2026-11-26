-- The migration steps of migrations.go (applyMigrations) and store.go (applySchemaFiles), each in one transaction under the migration lock.

-- name: MigrationApplied :one
SELECT EXISTS(SELECT 1 FROM schema_migration WHERE id = @id::text);

-- name: MigrationRecord :exec
INSERT INTO schema_migration(id) VALUES (@id::text);

-- name: SchemaFileHash :one
SELECT sha256 FROM schema_file WHERE name = @name;

-- name: SchemaFileRecord :exec
INSERT INTO schema_file(name, sha256) VALUES (@name, @sha256)
ON CONFLICT (name) DO UPDATE SET sha256 = excluded.sha256, applied_at = now();

-- name: MigrationScope :exec
-- Every migration sees every row: all projects and tombstoned rows (FORCE ROW LEVEL SECURITY applies to the owner too).
SELECT set_config('fleet.project_ids', '*', true), set_config('fleet.deleted', 'on', true);
