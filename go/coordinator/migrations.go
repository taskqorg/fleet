package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// Migrations (docs/designs/fleet-standard.md, S1 "Migrations"): one file per migration in migrations/, named
// <UTC time>_<slug>.sql (20261126T0315Z_history.sql). Its id is the name without .sql; schema_migration records each
// applied id. Two branches that each add a migration add two different files: neither git nor the runner sees a clash,
// and no number is taken in advance. schema.sql holds the numbered migrations written before (ids "1" to "121"), which
// run first, in number order; the files run after them, in name order.
//
// The runner applies every id the database has not recorded yet, so a migration that lands after a later-named one was
// already applied still runs. Migrations of parallel branches are independent by construction (neither branch saw the
// other's); a migration that builds on another file is named after it, which a fresh database (every CI run) checks.

//go:embed schema.sql
var schemaSQL string

//go:embed migrations
var migrationFS embed.FS

// migrationFileName: <yyyymmdd>T<hhmm>Z_<slug>.sql, the UTC time it was written and a lower-case slug.
var migrationFileName = regexp.MustCompile(`^[0-9]{8}T[0-9]{4}Z_[a-z0-9_]+\.sql$`)

var migrationSplit = regexp.MustCompile(`(?m)^-- migration (\d+)`)

type migration struct {
	id   string
	body string
}

// migrations lists every migration in the order a fresh database applies them: the numbered sections of schema.sql,
// then the files of migrations/ by name.
func migrations() ([]migration, error) {
	var out []migration
	text := schemaSQL
	idx := migrationSplit.FindAllStringSubmatchIndex(text, -1)
	for i, m := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, migration{id: text[m[2]:m[3]], body: text[m[0]:end]})
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		if !migrationFileName.MatchString(base) {
			return nil, errors.New("migrations/" + base + ": name it <yyyymmdd>T<hhmm>Z_<slug>.sql (UTC, lower-case slug)")
		}
		b, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{id: strings.TrimSuffix(base, ".sql"), body: string(b)})
	}
	return out, nil
}

// migrationsText: every migration's SQL in apply order, for checks that read the schema source.
func migrationsText() string {
	ms, err := migrations()
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	for _, m := range ms {
		b.WriteString(m.body)
		b.WriteString("\n")
	}
	return b.String()
}

// migrationIDs: the ids this build carries, one per line (`fleet-coordinator migrations`). fleet-update dumps the
// database before a release only when one of them is missing from schema_migration (docs/designs/fleet-self-update.md,
// step 8).
func migrationIDs() (string, error) {
	ms, err := migrations()
	if err != nil {
		return "", err
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.id
	}
	return strings.Join(ids, "\n"), nil
}

// schemaMigrationTable creates schema_migration keyed by the migration id, or converts the earlier table keyed by the
// number (version int) in place: each number becomes its id ("120"). version stays as a column generated from a
// numeric id, so an older coordinator's check (WHERE version = N) and an older updater's max(version) still read it.
const schemaMigrationTable = `
CREATE TABLE IF NOT EXISTS schema_migration(
 id text PRIMARY KEY,
 applied_at timestamptz NOT NULL DEFAULT now(),
 version int GENERATED ALWAYS AS (CASE WHEN id ~ '^[0-9]+$' THEN id::int END) STORED
);
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM information_schema.columns
   WHERE table_schema = current_schema() AND table_name = 'schema_migration' AND column_name = 'id') THEN
  ALTER TABLE schema_migration ADD COLUMN id text;
  UPDATE schema_migration SET id = version::text;
  ALTER TABLE schema_migration DROP CONSTRAINT schema_migration_pkey;
  ALTER TABLE schema_migration ALTER COLUMN id SET NOT NULL, ADD PRIMARY KEY (id);
  ALTER TABLE schema_migration DROP COLUMN version;
  ALTER TABLE schema_migration ADD COLUMN version int GENERATED ALWAYS AS (CASE WHEN id ~ '^[0-9]+$' THEN id::int END) STORED;
 END IF;
END $$;`

// migrate applies the migrations as the owner role: each once, in order, in one transaction that takes the migration
// lock before it checks schema_migration, so concurrent starts never run one twice. Then applySchemaFiles brings every
// function, trigger and policy to its one current definition in schema/*.sql.
// Migration 6's JavaScript document move is a no-op on an empty database and is not ported.
func migrate(ctx context.Context, url string) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	if err := applyMigrations(ctx, url, ms); err != nil {
		return err
	}
	return applySchemaFiles(ctx, url)
}

// applyMigrations applies the migrations schema_migration does not list yet, in the order given.
func applyMigrations(ctx context.Context, url string, ms []migration) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if err := migrateLocked(ctx, conn, func(_ *db.Queries, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, schemaMigrationTable, pgx.QueryExecModeSimpleProtocol)
		return err
	}); err != nil {
		return errors.New("schema_migration: " + err.Error())
	}
	for _, m := range ms {
		err := migrateLocked(ctx, conn, func(q *db.Queries, tx pgx.Tx) error {
			// Decided under the lock: another coordinator may have applied this migration while this one waited for it.
			if applied, err := q.MigrationApplied(ctx, m.id); err != nil || applied {
				return err
			}
			// Simple protocol: the whole migration runs as one multi-statement query inside the transaction.
			if _, err := tx.Exec(ctx, m.body, pgx.QueryExecModeSimpleProtocol); err != nil {
				return err
			}
			return q.MigrationRecord(ctx, m.id)
		})
		if err != nil {
			return errors.New("migration " + m.id + ": " + err.Error())
		}
	}
	return nil
}
