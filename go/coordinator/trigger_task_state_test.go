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
	seqAfter := func(sql string) int64 {
		t.Helper()
		var s int64
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, "SELECT max(seq) FROM change WHERE kind='task' AND id='t1'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	}
	q := db.New(conn)
	// Each step checks the change it wrote against the row as a trigger pass right after it would read it.
	for _, c := range []struct {
		sql  string
		want bool
	}{
		{"INSERT INTO task(id, title) VALUES ('t1', 'one')", true},
		{"UPDATE task SET title='two' WHERE id='t1'", false},
		{"UPDATE task SET state='in_progress' WHERE id='t1'", true},
		{"UPDATE task SET title='three' WHERE id='t1'", false},
	} {
		r, err := q.TaskWatchState(ctx, db.TaskWatchStateParams{Seq: seqAfter(c.sql), ID: "t1"})
		if err != nil {
			t.Fatal(err)
		}
		if r.StateChanged != c.want {
			t.Errorf("%s: state changed %v, want %v", c.sql, r.StateChanged, c.want)
		}
	}
}
