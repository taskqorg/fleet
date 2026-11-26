package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Requirement: the dam unpurge migration removes the purge marks the 2026-11-26 cleanup left on project dam and keeps
// the project-less ones. Risk: purged_hash has FORCE ROW LEVEL SECURITY, so a migration without the project scope
// (20261126T1249Z) deletes nothing and dam keeps refusing its own page images ("Content was purged").
func TestMigrationUnpurgeDamPagesSeesRows(t *testing.T) {
	sock, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testPGURL("fleet", name, sock, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{}
	for _, m := range ms {
		body[m.id] = m.body
	}
	run := func(sql string) {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	reason := "owner decision 2026-11-26: pipeline host-job files saved without a project between #1108 and #1169 (DAM page images, prompt output)"
	run(fmt.Sprintf(`SELECT set_config('fleet.project_ids', '*', true);
		INSERT INTO purged_hash(project_id, scope, content_hash, reason) VALUES ('_', 'prod', 'h1', '%[1]s'), ('dam', 'prod', 'h1', '%[1]s'), ('dam', 'prod', 'h2', 'other reason')`, reason))
	count := func(project string) int {
		t.Helper()
		var n int
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('fleet.project_ids', '*', true)`); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM purged_hash WHERE project_id=$1 AND reason=$2`, project, reason).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	run(body["20261126T1249Z_unpurge_dam_pages"])
	if count("dam") != 1 {
		t.Fatal("run outside the runner (as before MigrationScope), the first migration was expected to see no row under row level security")
	}
	run(body["20261126T1410Z_unpurge_dam_pages_rls"])
	if count("dam") != 0 || count("_") != 1 {
		t.Fatalf("dam marks %d (want 0), project-less marks %d (want 1)", count("dam"), count("_"))
	}
}
