package main

import (
	"context"
	"fmt"
	"time"
)

const streamRingLimit = 4096

type streamBatch struct {
	rows               []change
	next               int64
	resync, held, more bool
	err                error
}
type streamFlight struct {
	done  chan struct{}
	batch streamBatch
}

// runFeed owns the global cursor. The ring never advances past the XID barrier.
func (b *Broadcast) runFeed(ctx context.Context, s *Store) {
	var cursor int64
	initialized := false
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.feedWake:
			t := time.NewTimer(75 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		case <-timer:
		}
		timer = nil
		for {
			var batch streamBatch
			err := s.Read(ctx, nil, func(q *Request) error {
				if !initialized {
					var err error
					cursor, err = head(ctx, q.tx)
					if err != nil {
						return err
					}
					initialized = true
				}
				var err error
				b.sinceQueries.Add(1)
				batch.rows, batch.next, batch.resync, batch.held, err = since(ctx, q.tx, cursor, 1000)
				return err
			})
			if err != nil {
				timer = time.After(time.Second)
				break
			}
			b.mu.Lock()
			if !b.feedReady || batch.resync {
				b.ring = nil
				b.ringBase = cursor
			}
			b.feedReady = true
			if batch.resync {
				b.ringBase = batch.next
			}
			b.ring = append(b.ring, batch.rows...)
			if len(b.ring) > streamRingLimit {
				drop := len(b.ring) - streamRingLimit
				b.ringBase = b.ring[drop-1].Seq
				b.ring = append([]change(nil), b.ring[drop:]...)
			}
			b.feedHead = batch.next
			b.feedHeld = batch.held
			b.feedEpoch++
			b.filtered = make(map[string]*streamFlight)
			b.mu.Unlock()
			cursor = batch.next
			b.wake()
			if batch.held {
				timer = time.After(heldWait)
			}
			if len(batch.rows) < 1000 {
				break
			}
		}
	}
}

// readBatch shares database RLS work across tabs with identical scope, generation,
// cursor and feed epoch. No mutex is held while acquiring a connection or querying.
func (b *Broadcast) readBatch(ctx context.Context, a *App, p *Person, cursor int64) streamBatch {
	s := a.store
	s.authMu.Lock()
	generation, writing := s.authGeneration, s.authWrites != 0
	s.authMu.Unlock()
	sc := scopeOf(p)
	ids, person := "*", ""
	if sc != nil {
		ids, person = sc.ProjectIDs, sc.Person
	}
	if p != nil {
		person = p.Email
	}
	version := ""
	if p != nil {
		version = grantsVersion(p)
	}
	b.mu.Lock()
	epoch := b.feedEpoch
	key := fmt.Sprintf("%q/%q/%q/%d/%d/%d", ids, person, version, generation, cursor, epoch)
	if !writing {
		if f := b.filtered[key]; f != nil {
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return streamBatch{err: ctx.Err()}
			case <-f.done:
				return f.batch
			}
		}
	}
	f := &streamFlight{done: make(chan struct{})}
	if !writing {
		if len(b.filtered) >= 128 {
			b.filtered = make(map[string]*streamFlight)
		}
		if b.filtered == nil {
			b.filtered = make(map[string]*streamFlight)
		}
		b.filtered[key] = f
	}
	available := b.feedReady
	start := -1
	if cursor == b.ringBase {
		start = 0
	} else {
		for i := range b.ring {
			if b.ring[i].Seq == cursor {
				start = i + 1
				break
			}
		}
	}
	rows := []change(nil)
	next := b.feedHead
	if available && start >= 0 {
		rows = append(rows, b.ring[start:min(len(b.ring), start+1000)]...)
		if len(rows) > 0 {
			next = rows[len(rows)-1].Seq
		}
	}
	b.mu.Unlock()
	batch := streamBatch{next: next, more: len(rows) == 1000}
	b.rlsQueries.Add(1)
	batch.err = s.Read(ctx, scopedRequest(ctx, a, p), func(q *Request) error {
		if p != nil {
			current, err := resolveAccessTx(ctx, q.tx, p.Email)
			if err != nil {
				return err
			}
			if grantsVersion(current) != version {
				batch.resync = true
				return nil
			}
		}
		if !available || start < 0 {
			var err error
			batch.rows, batch.next, batch.resync, batch.held, err = since(ctx, q.tx, cursor, 1000)
			batch.more = len(batch.rows) == 1000
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		seqs := make([]int64, len(rows))
		for i, c := range rows {
			seqs[i] = c.Seq
		}
		visible, err := q.db().StreamVisibleSeqs(ctx, seqs)
		if err != nil {
			return err
		}
		allowed := make(map[int64]bool, len(visible))
		for _, seq := range visible {
			allowed[seq] = true
		}
		// Preserve global XID order; hidden rows and their payload never leave this method.
		for _, c := range rows {
			if allowed[c.Seq] {
				batch.rows = append(batch.rows, c)
			}
		}
		return nil
	})
	if batch.held {
		b.signalFeed()
		batch.held = false
	}
	f.batch = batch
	close(f.done)
	// An active write or failed query must never seed a reusable authorization result.
	s.authMu.Lock()
	stale := s.authGeneration != generation || s.authWrites != 0
	s.authMu.Unlock()
	if stale || batch.err != nil {
		b.mu.Lock()
		if b.filtered[key] == f {
			delete(b.filtered, key)
		}
		b.mu.Unlock()
	}
	return batch
}

// pruneChanges only expires the reconnect log, never durable work or transcripts; every minute (listen), then the feed
// reads again.
func (b *Broadcast) pruneChanges(ctx context.Context, s *Store) error {
	err := s.Write(ctx, nil, nil, func(q *Request) error { return q.db().StreamPruneChanges(ctx) })
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.feedReady = false
	b.ring = nil
	b.filtered = nil
	b.mu.Unlock()
	b.signalFeed()
	return nil
}
