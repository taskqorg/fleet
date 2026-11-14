package main

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

// Requirement: committed supported writes invalidate authorization snapshots before returning.
// Risk: concurrent reads publish stale grants or request mutation poisons the shared cache.
func TestAccessCacheCommitRollbackAndConcurrentRead(t *testing.T) {
	e := openTestCoordinator(t, false)
	p, err := e.app.resolveAccess(e.ctx, testOwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "request-local mutation"
	p2, err := e.app.resolveAccess(e.ctx, testOwnerEmail)
	if err != nil || p2.Name == p.Name {
		t.Fatalf("cache aliases request: %v", err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.appStore.Tx(e.ctx, nil, true, "", func(tx pgx.Tx) error {
			if _, err := tx.Exec(e.ctx, "UPDATE person SET name='changed' WHERE email=$1", testOwnerEmail); err != nil {
				return err
			}
			close(entered)
			<-finish
			return nil
		})
	}()
	<-entered
	during, err := e.app.resolveAccess(e.ctx, testOwnerEmail)
	if err != nil || during.Name == "changed" {
		t.Fatalf("uncommitted access visible: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := e.app.resolveAccess(e.ctx, testOwnerEmail)
	if err != nil || after.Name != "changed" {
		t.Fatalf("stale access after commit: %+v %v", after, err)
	}
	aborted := errors.New("rollback")
	err = e.appStore.Tx(e.ctx, nil, true, "", func(tx pgx.Tx) error {
		if _, err := tx.Exec(e.ctx, "DELETE FROM person WHERE email=$1", testOwnerEmail); err != nil {
			return err
		}
		return aborted
	})
	if !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	restored, err := e.app.resolveAccess(e.ctx, testOwnerEmail)
	if err != nil || restored.Name != "changed" {
		t.Fatalf("rollback altered access: %v", err)
	}
}

// Requirement: host lifecycle and credentials use the same committed cache invalidation boundary.
// Risk: a deleted host or rotated secret remains authorized, or removed people regain stale access.
func TestAuthCacheHostRotationDeletionAndPersonRegrant(t *testing.T) {
	e := openTestCoordinator(t, false)
	secret := e.registerHost(t, "cache-host")
	if h, err := e.app.hostFromSecret(e.ctx, secret); err != nil || h == nil {
		t.Fatalf("initial host: %v", err)
	}
	rotated := "rotated-test-secret"
	write := func(sql string, args ...any) {
		t.Helper()
		if err := e.appStore.Tx(e.ctx, nil, true, NOLOCK, func(tx pgx.Tx) error { _, err := tx.Exec(e.ctx, sql, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	write("UPDATE document SET data=jsonb_set(data,'{secretHash}',to_jsonb($1::text)) WHERE kind='host' AND (id='cache-host' OR id='host:cache-host')", secretHash(rotated))
	if h, err := e.app.hostFromSecret(e.ctx, secret); err != nil || h != nil {
		t.Fatalf("old credential accepted: %v", err)
	}
	if h, err := e.app.hostFromSecret(e.ctx, rotated); err != nil || h == nil {
		t.Fatalf("rotated host: %v", err)
	}
	write("DELETE FROM document WHERE kind='host' AND (id='cache-host' OR id='host:cache-host')")
	if h, err := e.app.hostFromSecret(e.ctx, rotated); err != nil || h != nil {
		t.Fatalf("deleted host accepted: %v", err)
	}
	if _, err := e.app.resolveAccess(e.ctx, "outsider@trial.test"); err != nil {
		t.Fatal(err)
	}
	write("DELETE FROM person WHERE email='outsider@trial.test'")
	if _, err := e.app.resolveAccess(e.ctx, "outsider@trial.test"); err == nil {
		t.Fatal("removed person accepted")
	}
	write("INSERT INTO person(email,name,roles,owner) VALUES('outsider@trial.test','Regranted',ARRAY['operator'],false)")
	p, err := e.app.resolveAccess(e.ctx, "outsider@trial.test")
	if err != nil || p.Name != "Regranted" {
		t.Fatalf("regrant stale: %+v %v", p, err)
	}
}
