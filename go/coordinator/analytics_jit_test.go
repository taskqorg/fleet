package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"reflect"
	"testing"
)

// Requirement: resource lineage preserves accounting and transaction settings;
// risk: native task creation times out compiling JIT or leaks settings after failure.
func TestTaskResourceJITPostgres(t *testing.T) {
	url := os.Getenv("FLEET_JIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("run through tests/go/analytics-jit.test.ts isolated PostgreSQL harness")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var baseline any
	for _, mode := range []string{"on", "off"} {
		if _, err = c.Exec(ctx, "SELECT set_config('jit',$1,false)", mode); err != nil {
			t.Fatal(err)
		}
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		q := backgroundRequest(ctx, nil, tx)
		got, err := taskResourceRollup(q, "jit-target")
		if err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		var actual string
		if err = tx.QueryRow(ctx, "SHOW jit").Scan(&actual); err != nil || actual != mode {
			t.Fatalf("restore %s: %s %v", mode, actual, err)
		}
		if baseline == nil {
			baseline = got
		} else if !reflect.DeepEqual(baseline, got) {
			t.Fatal("JIT changes accounting")
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		// Cancellation must propagate to the enclosing rollback; reuse this connection.
		tx, err = c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = taskResourceRollup(backgroundRequest(canceled, nil, tx), "jit-target")
		if err == nil {
			t.Fatal("canceled query succeeded")
		}
		tx.Rollback(ctx)
		if err = c.QueryRow(ctx, "SHOW jit").Scan(&actual); err != nil || actual != mode {
			t.Fatalf("rollback %s: %s %v", mode, actual, err)
		}
	}
}
