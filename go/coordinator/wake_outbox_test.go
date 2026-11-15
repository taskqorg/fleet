package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Requirement: durable hints retain canonical identities across failed claims, backend loss
// and concurrent delivery; risk: lost source wakeup, transient precommit delivery or RLS leak.
func TestWakeOutboxPostgres(t *testing.T) {
	url := os.Getenv("FLEET_WAKE_TEST_URL")
	if url == "" {
		t.Skip("isolated wake-outbox fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := openStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{store: st}
	sql := func(s string) {
		t.Helper()
		if e := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return exec(q, s) }); e != nil {
			t.Fatal(e)
		}
	}
	scalar := func(s string) string {
		t.Helper()
		var v string
		if e := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(q.ctx(), s).Scan(&v) }); e != nil {
			t.Fatal(e)
		}
		return v
	}
	sql(`DELETE FROM wake_outbox; INSERT INTO execution(id,kind,state,project_id) VALUES('wake-source','agent_run','running','WAKE')`)
	sourceXID := scalar(`SELECT xid::text FROM change WHERE id='wake-source' ORDER BY seq DESC LIMIT 1`)
	if got := scalar(`SELECT count(DISTINCT source_xid)::text FROM wake_outbox`); got != "1" {
		t.Fatal("source identity", got)
	}
	if got := scalar(`SELECT min(source_xid::text) FROM wake_outbox`); got != sourceXID {
		t.Fatal("wake replaced source XID", got, sourceXID)
	}
	if got := scalar(`SELECT count(*)::text FROM resource_work WHERE execution_id='wake-source'`); got != "1" {
		t.Fatal("source capture absent", got)
	}
	var scoped string
	err = st.Tx(ctx, &Scope{ProjectIDs: "WAKE", Person: "owner@trial.test"}, false, NOLOCK, func(tx pgx.Tx) error { return tx.QueryRow(ctx, `SELECT count(*)::text FROM wake_outbox`).Scan(&scoped) })
	if err != nil || scoped != "0" {
		t.Fatal("wake rows leak under scoped RLS", scoped, err)
	}
	err = st.Tx(ctx, &Scope{ProjectIDs: "WAKE"}, true, NOLOCK, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT fleet_enqueue_wake('fleet_change','')`); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT current_setting('fleet.project_ids')`).Scan(&scoped)
	})
	if err != nil || scoped != "WAKE" {
		t.Fatal("enqueue leaked maintenance scope", scoped, err)
	}
	err = st.Tx(ctx, &Scope{ProjectIDs: "WAKE"}, true, NOLOCK, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT fleet_enqueue_wake('untrusted','payload')`)
		return e
	})
	if err == nil {
		t.Fatal("unknown channel accepted")
	}
	before := scalar(`SELECT jsonb_agg(to_jsonb(w) ORDER BY wake_id)::text FROM wake_outbox w`)
	rollback := errors.New("fixture source rollback")
	err = runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
		if e := exec(q, `SELECT fleet_enqueue_wake('fleet_change','')`); e != nil {
			return e
		}
		return rollback
	})
	if !errors.Is(err, rollback) || scalar(`SELECT jsonb_agg(to_jsonb(w) ORDER BY wake_id)::text FROM wake_outbox w`) != before {
		t.Fatal("source rollback changed intents", err)
	}
	listener, e := pgx.Connect(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close(context.Background())
	for _, channel := range []string{"fleet_change", "fleet_resource_work", "fleet_resource_summary"} {
		if _, e = listener.Exec(ctx, "LISTEN "+channel); e != nil {
			t.Fatal(e)
		}
	}
	notifications := make(chan *pgconn.Notification, 100)
	go func() {
		for {
			n, e := listener.WaitForNotification(ctx)
			if e != nil {
				return
			}
			select {
			case notifications <- n:
			case <-ctx.Done():
				return
			}
		}
	}()
	assertQuiet := func() {
		t.Helper()
		select {
		case n := <-notifications:
			t.Fatalf("uncommitted notification: %+v", n)
		case <-time.After(100 * time.Millisecond):
		}
	}
	sql(`INSERT INTO wake_fixture_fault VALUES('error')`)
	if _, err = wakeOutboxPass(ctx, a); err == nil || !strings.Contains(err.Error(), "fixture wake deletion failure") {
		t.Fatal("dispatch SQL error not propagated", err)
	}
	if scalar(`SELECT jsonb_agg(to_jsonb(w) ORDER BY wake_id)::text FROM wake_outbox w`) != before {
		t.Fatal("failed claim lost immutable intents")
	}
	assertQuiet()
	sql(`DELETE FROM wake_fixture_fault; INSERT INTO wake_fixture_fault VALUES('gate')`)
	gate, e := pgx.Connect(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer gate.Close(context.Background())
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('fixture:wake-delete',0))`); e != nil {
		t.Fatal(e)
	}
	defer gate.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('fixture:wake-delete',0))`)
	done := make(chan error, 1)
	go func() { _, e := wakeOutboxPass(ctx, a); done <- e }()
	blocked := func() int32 {
		t.Helper()
		var pid int32
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if e = gate.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND usename=current_user AND wait_event='advisory' AND query LIKE '%wake_outbox%' AND pid<>pg_backend_pid() LIMIT 1),0)`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if pid != 0 {
				return pid
			}
			if time.Now().After(deadline) {
				t.Fatal("dispatcher did not reach actual locked deletion")
			}
		}
	}
	backend := blocked()
	var killed bool
	if e = gate.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backend).Scan(&killed); e != nil || !killed {
		t.Fatal("kill dispatcher backend", killed, e)
	}
	if e = <-done; e == nil {
		t.Fatal("terminated dispatcher committed")
	}
	if scalar(`SELECT jsonb_agg(to_jsonb(w) ORDER BY wake_id)::text FROM wake_outbox w`) != before {
		t.Fatal("backend loss lost immutable intents")
	}
	assertQuiet()
	// A second dispatcher skips the first one's claims; a later enqueue is not in those claims.
	go func() { _, e := wakeOutboxPass(ctx, a); done <- e }()
	_ = blocked()
	if n, e := wakeOutboxPass(ctx, a); e != nil || n != 0 {
		t.Fatal("concurrent dispatcher stole locked claims", n, e)
	}
	sql(`SELECT fleet_enqueue_wake('fleet_resource_summary','later-project')`)
	later := scalar(`SELECT wake_id::text FROM wake_outbox WHERE payload='later-project'`)
	if _, e = gate.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('fixture:wake-delete',0))`); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if got := scalar(`SELECT count(*)::text FROM wake_outbox WHERE wake_id='` + later + `'`); got != "1" {
		t.Fatal("older claim erased concurrent enqueue", got)
	}
	if got := scalar(`SELECT xid::text FROM change WHERE id='wake-source' ORDER BY seq DESC LIMIT 1`); got != sourceXID {
		t.Fatal("dispatcher rewrote source visibility XID", got, sourceXID)
	}
	if got := scalar(`SELECT count(*)::text FROM resource_work WHERE execution_id='wake-source'`); got != "1" {
		t.Fatal("wake drain erased source work", got)
	}
	sql(`DELETE FROM wake_fixture_fault`)
	if n, e := wakeOutboxPass(ctx, a); e != nil || n != 1 {
		t.Fatal("later enqueue did not drain", n, e)
	}
	// Coalescing is per claim and preserves exact channels/payloads, including Unicode.
	sql(`SELECT fleet_enqueue_wake('fleet_resource_summary','precise-☃');SELECT fleet_enqueue_wake('fleet_resource_summary','precise-☃')`)
	if n, e := wakeOutboxPass(ctx, a); e != nil || n != 2 {
		t.Fatal("duplicate intents", n, e)
	}
	saw := map[string]bool{}
	for deadline := time.Now().Add(5 * time.Second); !saw["fleet_resource_summary/precise-☃"] || !saw["fleet_resource_summary/later-project"] || !saw["fleet_change/"] || !saw["fleet_resource_work/"]; {
		select {
		case n := <-notifications:
			saw[n.Channel+"/"+n.Payload] = true
		case <-time.After(time.Until(deadline)):
			t.Fatal("missing committed channel/payload", saw)
		}
	}
	if n, e := wakeOutboxPass(ctx, a); e != nil || n != 0 {
		t.Fatal("empty recovery pass", n, e)
	}
	sql(`SELECT fleet_enqueue_wake('fleet_change','')`)
	if n, e := wakeOutboxPass(ctx, a); e != nil || n != 1 {
		t.Fatal("enqueue after empty boundary lost", n, e)
	}
	if got := scalar(`SELECT count(*)::text FROM wake_outbox`); got != "0" {
		t.Fatal("intents did not drain", got)
	}
	// Retain a source identity for the real process restart/recovery check in the outer fixture.
	sql(`SELECT fleet_enqueue_wake('fleet_resource_summary','restart-owned')`)
	t.Logf("actual backend %d terminated: intents retained; concurrent claims/late enqueue/source XID/RLS/rollback/payload proofs passed", backend)
}
