package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// Requirement: a task change wakes a trigger only when it changed the task's state, pause or wait; an edit that kept the
// state (title, link, dependency) does not. Risk (2026-11-26): 23 of HAL0's 55 task wakes repeated "task X now
// in_progress" for edits that changed nothing it watches.
func TestTaskWatchStateOnlyStateChanges(t *testing.T) {
	sockDir, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", name, sockDir, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SET fleet.project_ids = '*'"); err != nil {
		t.Fatal(err)
	}
	seqsAfter := func(sql string) []int64 {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, "SELECT seq FROM change WHERE kind='task' AND id='t1' AND xid=pg_current_xact_id()")
		if err != nil {
			t.Fatal(err)
		}
		seqs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return seqs
	}
	q := db.New(conn)
	// A write can also produce derived rows for the same task in the same transaction.
	for _, c := range []struct {
		sql  string
		want int
	}{
		{"INSERT INTO task(id, title) VALUES ('t1', 'one')", 1},
		{"UPDATE task SET title='two' WHERE id='t1'", 0},
		{"UPDATE task SET state='in_progress' WHERE id='t1'", 1},
		{"UPDATE task SET title='three' WHERE id='t1'", 0},
	} {
		changed := 0
		for _, seq := range seqsAfter(c.sql) {
			r, err := q.TaskWatchState(ctx, db.TaskWatchStateParams{Seq: seq, ID: "t1"})
			if err != nil {
				t.Fatal(err)
			}
			if r.StateChanged {
				changed++
			}
		}
		if changed != c.want {
			t.Errorf("%s: %d state changes, want %d", c.sql, changed, c.want)
		}
	}
}
