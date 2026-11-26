package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Requirement: a database migrated before migration files existed (schema_migration keyed by version int, every numbered
// section applied) converts in place, runs none of its numbered sections again, then runs the files it lacks, also one
// named before a file it already ran (two branches landing in either order). Risk: the first start after the release
// reruns migration 1 and fails, or skips a file that landed late.
func TestMigrationConvertsNumberedDatabase(t *testing.T) {
	sock, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	url := testPGURL("fleet", name, sock, port)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	// The state of a database the numbered runner migrated: version int PRIMARY KEY, one row per section.
	var numbered []string
	for _, m := range ms {
		if !migrationFileName.MatchString(m.id + ".sql") {
			numbered = append(numbered, m.id)
		}
	}
	if _, err := conn.Exec(ctx, `DROP TABLE schema_migration;
		CREATE TABLE schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_migration(version) VALUES (`+strings.Join(numbered, "),(")+`)`); err != nil {
		t.Fatal(err)
	}
	later := migration{id: "20991231T2359Z_later", body: "CREATE TABLE migration_probe_later(x int);"}
	if err := applyMigrations(ctx, url, append(ms, later)); err != nil {
		t.Fatal(err)
	}
	// A branch whose file is named earlier lands after the later one ran.
	earlier := migration{id: "20991231T2358Z_earlier", body: "CREATE TABLE migration_probe_earlier(x int);"}
	if err := applyMigrations(ctx, url, append(ms, earlier, later)); err != nil {
		t.Fatal(err)
	}
	var n, maxVersion int
	if err := conn.QueryRow(ctx, `SELECT count(*), max(version) FROM schema_migration
		WHERE to_regclass('migration_probe_later') IS NOT NULL AND to_regclass('migration_probe_earlier') IS NOT NULL`).Scan(&n, &maxVersion); err != nil {
		t.Fatal(err)
	}
	if n != len(ms)+2 || maxVersion != latestNumbered(t) {
		t.Fatalf("schema_migration: %d rows (want %d), max(version) %d", n, len(ms)+2, maxVersion)
	}
	// An older coordinator's check still reads the numbers.
	var applied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migration WHERE version = 1)`).Scan(&applied); err != nil || !applied {
		t.Fatalf("version 1 applied = %v, %v", applied, err)
	}
}

func latestNumbered(t *testing.T) int {
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range ms {
		var v int
		if _, err := fmt.Sscanf(m.id, "%d", &v); err == nil && fmt.Sprint(v) == m.id {
			n = v
		}
	}
	return n
}

// Requirement: a data migration sees every row of a table with FORCE ROW LEVEL SECURITY, whichever project holds it,
// without setting the project scope itself. Risk: the owner role is subject to the table's policies, so the migration's
// UPDATE or DELETE matches no row and nothing fails (20261126T1249Z_unpurge_dam_pages left dam's purge marks in place).
func TestMigrationSeesRowsUnderForcedRLS(t *testing.T) {
	sock, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	url := testPGURL("fleet", name, sock, port)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var forced bool
	if err := conn.QueryRow(ctx, `SELECT relforcerowsecurity FROM pg_class WHERE oid = 'purged_hash'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("purged_hash FORCE ROW LEVEL SECURITY = %v, %v: pick another forced table for this check", forced, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('fleet.project_ids', '*', true);
		INSERT INTO purged_hash(project_id, scope, content_hash, reason) VALUES
		 ('dam', 'prod', 'probe-update', 'before'), ('other', 'prod', 'probe-update', 'before'), ('dam', 'prod', 'probe-delete', 'before')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	probe := migration{id: "20991231T2357Z_forced_rls_probe", body: `UPDATE purged_hash SET reason = 'after' WHERE content_hash = 'probe-update';
DELETE FROM purged_hash WHERE content_hash = 'probe-delete';`}
	if err := applyMigrations(ctx, url, append(ms, probe)); err != nil {
		t.Fatal(err)
	}
	var updated, deletedLeft int
	if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('fleet.project_ids', '*', true)`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE content_hash = 'probe-update' AND reason = 'after'),
			count(*) FILTER (WHERE content_hash = 'probe-delete') FROM purged_hash`).Scan(&updated, &deletedLeft)
	}); err != nil {
		t.Fatal(err)
	}
	if updated != 2 || deletedLeft != 0 {
		t.Fatalf("migration under FORCE RLS: %d rows updated (want 2), %d rows left to delete (want 0)", updated, deletedLeft)
	}
}
