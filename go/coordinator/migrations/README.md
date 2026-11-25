# Migrations

One file per Postgres migration, run once by the coordinator at start ([migrations.go](../migrations.go)).

- **Name:** `<yyyymmdd>T<hhmm>Z_<slug>.sql`, the UTC time you write it and a lower-case slug, for example `20261126T0315Z_history_activity.sql` (`date -u +%Y%m%dT%H%MZ`). The name is the migration's id in `schema_migration`; never rename or edit a file once it is on `main`.
- **No numbers:** parallel branches add different files, so they never clash in git or in the runner, and nobody takes "the next free number".
- **Order:** a fresh database runs the numbered sections of [schema.sql](../schema.sql) (1 to 121, written before this folder), then these files by name. A database that already ran a later-named file still runs an earlier-named one that lands after it. A migration that builds on another file's tables must be named after it; rename yours to the current time when you rebase onto one it uses. CI migrates a fresh database on every run, so a wrong order fails there.
- **Content:** what a `schema.sql` section held: plain SQL, run in one transaction as the owner role `fleet`, statements split at a semicolon that ends a line. Functions, triggers and policies are defined in [schema/](../schema/) instead.
- **After adding one:** regenerate the sqlc schema (`cd go && FLEET_UPDATE_SQLC_SCHEMA=1 go test ./coordinator -run TestMigrationSqlcSchema`, then `pnpm run go:sqlc`) as before.
