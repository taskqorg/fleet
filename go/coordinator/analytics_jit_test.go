package main

import (
	"context"
	"fleet/contract"
	"github.com/jackc/pgx/v5"
	"os"
	"reflect"
	"testing"
	"time"
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
	for _, rollup := range []func(*Request, string) (contract.ScopedResourceRollup, error){taskResourceRollup, sessionResourceRollup} {
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
			got, err := rollup(q, "jit-target")
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
			// Hold execution only in this isolated test database. Wait for the actual
			// lineage SELECT to block, proving set_config has already succeeded.
			blocker, e := pgx.Connect(ctx, url)
			if e != nil {
				t.Fatal(e)
			}
			lock, e := blocker.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = lock.Exec(ctx, "LOCK TABLE execution IN ACCESS EXCLUSIVE MODE"); e != nil {
				t.Fatal(e)
			}
			tx, e = c.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			running, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { _, e := rollup(backgroundRequest(running, nil, tx), "jit-target"); done <- e }()
			deadline := time.Now().Add(5 * time.Second)
			observed := false
			for time.Now().Before(deadline) {
				if _, e = blocker.Exec(ctx, "SELECT pg_stat_clear_snapshot()"); e != nil {
					stop()
					t.Fatal(e)
				}
				if e = blocker.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND query LIKE 'WITH RECURSIVE%')", c.PgConn().PID()).Scan(&observed); e != nil {
					stop()
					t.Fatal(e)
				}
				if observed {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if observed {
				_, e = blocker.Exec(ctx, "SELECT pg_cancel_backend($1)", c.PgConn().PID())
				if e != nil {
					stop()
					t.Fatal(e)
				}
			} else {
				stop()
			}
			e = <-done
			stop()
			tx.Rollback(ctx)
			lock.Rollback(ctx)
			blocker.Close(ctx)
			if !observed || e == nil {
				t.Fatalf("lineage cancellation: blocked=%v error=%v", observed, e)
			}
			if e = c.QueryRow(ctx, "SHOW jit").Scan(&actual); e != nil || actual != mode {
				t.Fatalf("mid-query rollback %s: %s %v", mode, actual, e)
			}
			// Cancellation must propagate to the enclosing rollback; reuse this connection.
			tx, err = c.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = rollup(backgroundRequest(canceled, nil, tx), "jit-target")
			if err == nil {
				t.Fatal("canceled query succeeded")
			}
			tx.Rollback(ctx)
			if err = c.QueryRow(ctx, "SHOW jit").Scan(&actual); err != nil || actual != mode {
				t.Fatalf("rollback %s: %s %v", mode, actual, err)
			}
		}
	}
}
