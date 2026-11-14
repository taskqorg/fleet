package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"
)

// Requirement: shared viewer tabs reuse RLS batches only in the current authorization generation.
// Risk: a previously filtered batch leaks permissions after revocation.
func TestSharedFeedTabsAndRevocation(t *testing.T) {
	e := openTestCoordinator(t, false)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	b := &Broadcast{subs: map[chan struct{}]bool{}, feedWake: make(chan struct{}, 1)}
	go b.runFeed(ctx, e.appStore)
	b.signalFeed()
	deadline := time.After(5 * time.Second)
	for {
		b.mu.Lock()
		ready := b.feedReady
		b.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("feed initialization timeout")
		case <-time.After(time.Millisecond):
		}
	}
	p, err := e.app.resolveAccess(ctx, testOwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	cursor := b.feedHead
	b.mu.Unlock()
	first := b.readBatch(ctx, e.app, p, cursor)
	if first.err != nil {
		t.Fatal(first.err)
	}
	second := b.readBatch(ctx, e.app, p, cursor)
	if second.err != nil {
		t.Fatal(second.err)
	}
	b.mu.Lock()
	cached := len(b.filtered)
	b.mu.Unlock()
	if cached != 1 {
		t.Fatalf("same-viewer tabs made %d filtered batches", cached)
	}
	outsider, err := e.app.resolveAccess(ctx, "outsider@trial.test")
	if err != nil {
		t.Fatal(err)
	}
	var tabs sync.WaitGroup
	before := b.rlsQueries.Load()
	for i := 0; i < 200; i++ {
		tabs.Add(1)
		go func(i int) {
			defer tabs.Done()
			viewer := p
			if i%2 == 1 {
				viewer = outsider
			}
			if got := b.readBatch(ctx, e.app, viewer, cursor); got.err != nil {
				t.Errorf("tab read: %v", got.err)
			}
		}(i)
	}
	tabs.Wait()
	queries := b.rlsQueries.Load() - before
	if queries != 1 {
		t.Fatalf("200 tabs, two identities, one already cached: got %d additional RLS transactions", queries)
	}
	t.Logf("200 tabs, 2 identities: total RLS batches=%d, shared since queries=%d", b.rlsQueries.Load(), b.sinceQueries.Load())

	if err := e.appStore.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE person SET roles=ARRAY['viewer']::text[],owner=false WHERE email=$1", p.Email)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	revoked := b.readBatch(ctx, e.app, p, cursor)
	if revoked.err != nil || !revoked.resync || len(revoked.rows) != 0 {
		t.Fatalf("stale batch reused after revoke: %+v", revoked)
	}
}

// Requirement: the shared XID barrier retries held rows without a second notification and expires old cursors.
// Risk: a transaction ending silently loses committed updates, or pruning silently skips reconnect history.
func TestSharedFeedHeldWaitAndPruning(t *testing.T) {
	e := openTestCoordinator(t, false)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	b := &Broadcast{subs: map[chan struct{}]bool{}, feedWake: make(chan struct{}, 1)}
	go b.runFeed(ctx, e.appStore)
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("feed condition timeout")
			}
			time.Sleep(time.Millisecond)
		}
	}
	b.signalFeed()
	wait(func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.feedReady })
	blocker, err := e.ownerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, "SELECT pg_current_xact_id()"); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := e.appStore.Tx(ctx, nil, true, NOLOCK, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "INSERT INTO change(kind,id,op) VALUES('task','held-shared','I') RETURNING seq").Scan(&seq)
	}); err != nil {
		t.Fatal(err)
	}
	b.signalFeed()
	wait(func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.feedHeld })
	b.mu.Lock()
	for _, row := range b.ring {
		if row.Seq == seq {
			t.Fatal("held transaction crossed XID barrier")
		}
	}
	b.mu.Unlock()
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// No NOTIFY is sent by rollback: the one shared heldWait timer must deliver it.
	wait(func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, row := range b.ring {
			if row.Seq == seq {
				return true
			}
		}
		return false
	})
	if err := e.appStore.Tx(ctx, nil, true, NOLOCK, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE change SET at=now()-interval '25 hours' WHERE seq=$1", seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.pruneChanges(ctx, e.appStore); err != nil {
		t.Fatal(err)
	}
	p, err := e.app.resolveAccess(ctx, testOwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	result := b.readBatch(ctx, e.app, p, seq)
	if result.err != nil || !result.resync {
		t.Fatalf("expired cursor did not resync: %+v", result)
	}
}
