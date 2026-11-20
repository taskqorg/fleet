-- The migration steps of store.go (migrateSQL, applySchemaFiles), each in one transaction under the migration lock.

-- name: MigrationLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(@lock::text, 0));

-- name: MigrationApplied :one
SELECT EXISTS(SELECT 1 FROM schema_migration WHERE version = @version::int);

-- name: MigrationRecord :exec
INSERT INTO schema_migration(version) VALUES (@version::int);

-- name: SchemaFileHash :one
SELECT sha256 FROM schema_file WHERE name = @name;

-- name: SchemaFileRecord :exec
INSERT INTO schema_file(name, sha256) VALUES (@name, @sha256)
ON CONFLICT (name) DO UPDATE SET sha256 = excluded.sha256, applied_at = now();
