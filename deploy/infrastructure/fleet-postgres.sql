-- Fleet roles and database (design FD1, tight D6). Run once by root through the existing route:
--   sudo -u postgres psql -v ON_ERROR_STOP=1 -f fleet-postgres.sql
-- Idempotent. No passwords: both roles log in only by local peer authentication.
--
-- Peer authentication (edit by root, then reload PostgreSQL):
--   pg_ident.conf:  fleet  fleet  fleet
--                   fleet  fleet  fleet_app
--   pg_hba.conf (before any broader local line):
--     local  fleet  fleet,fleet_app  peer  map=fleet
-- The Unix user fleet may then connect as fleet (migrations) or fleet_app (coordinator).

SELECT 'CREATE ROLE fleet LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'fleet') \gexec
SELECT 'CREATE ROLE fleet_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'fleet_app') \gexec
SELECT 'CREATE DATABASE fleet OWNER fleet'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'fleet') \gexec

REVOKE ALL ON DATABASE fleet FROM PUBLIC;
GRANT CONNECT ON DATABASE fleet TO fleet_app;

\connect fleet
-- In PostgreSQL 15+ schema public belongs to the database owner (fleet).
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO fleet_app;
-- fleet owns every table it creates in migrations; fleet_app gets row access only, never ownership.
ALTER DEFAULT PRIVILEGES FOR ROLE fleet IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO fleet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE fleet IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO fleet_app;
