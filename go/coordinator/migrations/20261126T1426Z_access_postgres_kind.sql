-- Project access kind postgres (docs/designs/fleet-project-access.md#model): a grant on a PostgreSQL database, as Fleet
-- reads its own production database (#fleets-own-database).
ALTER TABLE access_grant DROP CONSTRAINT IF EXISTS access_grant_kind_check;
ALTER TABLE access_grant ADD CONSTRAINT access_grant_kind_check CHECK (kind IN ('mysql', 'postgres', 'files'));
