-- Fleet Postgres schema. Each "-- migration N" section runs once, in order, inside one transaction,
-- as the owner role fleet; fleet_app gets row access through the default privileges in deploy/infrastructure/fleet-postgres.sql.
-- schema_migration(version, applied_at) is created by store.migrate() before the first section.

-- migration 1: legacy records as documents (design Phase 1, item 10)
CREATE TABLE document(
 kind text NOT NULL,
 id text PRIMARY KEY,
 data jsonb NOT NULL,
 project_id text NULL,
 seq bigserial NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX document_kind_seq ON document(kind, seq);
CREATE INDEX document_project ON document(project_id) WHERE project_id IS NOT NULL;

-- migration 2: per-request reads filter documents by jsonb containment (store.all with a filter)
CREATE INDEX document_data ON document USING gin (data jsonb_path_ops);
