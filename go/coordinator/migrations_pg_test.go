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
