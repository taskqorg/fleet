package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// Live change feed (docs/designs/fleet-operator-app.md §3, SL8). One LISTEN fleet_change connection per process wakes every open
// stream; each stream reads in its own short snapshot in its viewer's scope, so it never holds a transaction between wakes.
type Broadcast struct {
	mu                  sync.Mutex
	subs                map[chan struct{}]bool
	closed              bool
	ctx                 context.Context
	cancel              context.CancelFunc
	done                chan struct{}
	feedWake            chan struct{}
	ring                []change
	ringBase, feedHead  int64
	feedReady, feedHeld bool
	feedEpoch           uint64
	filtered            map[string]*streamFlight
	sinceQueries        atomic.Int64
	rlsQueries          atomic.Int64
}

// heldWait: the next look when committed changes wait behind an older running transaction of this database (visible).
const heldWait = 250 * time.Millisecond

func listen(parent context.Context, url string, store *Store) *Broadcast {
	ctx, cancel := context.WithCancel(parent)
	b := &Broadcast{subs: map[chan struct{}]bool{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), feedWake: make(chan struct{}, 1)}
	bg := backgroundTracker(parent)
	if bg.tryAdd() {
		go func() {
			defer bg.done()
			b.runFeed(ctx, store)
		}()
	}
	every(ctx, "change-prune", time.Minute, func(ctx context.Context) error { return b.pruneChanges(ctx, store) })
	if bg.tryAdd() {
		go func() {
			defer bg.done()
			defer close(b.done)
			for ctx.Err() == nil {
				conn, err := pgx.Connect(ctx, url)
				if err == nil {
					if _, err = conn.Exec(ctx, "LISTEN fleet_change"); err == nil {
						b.signalFeed() // anything committed while not listening
						for err == nil {
							_, err = conn.WaitForNotification(ctx)
							if err == nil {
								b.signalFeed()
							}
						}
					}
					c, ccancel := context.WithTimeout(context.Background(), time.Second)
					_ = conn.Close(c)
					ccancel()
				}
				if ctx.Err() == nil {
					log.Printf("listen: %v; again in 1 s", err)
					t := time.NewTimer(time.Second)
					select {
					case <-ctx.Done():
						t.Stop()
						return
					case <-t.C:
					}
				}
			}
		}()
	} else {
		close(b.done)
	}
	return b
}

func (b *Broadcast) wake() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (b *Broadcast) join() chan struct{} {
	c := make(chan struct{}, 1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(c)
		return c
	}
	b.subs[c] = true
	b.mu.Unlock()
	return c
}

func (b *Broadcast) leave(c chan struct{}) { b.mu.Lock(); delete(b.subs, c); b.mu.Unlock() }

func (b *Broadcast) close() {
	b.mu.Lock()
	b.closed = true
	for c := range b.subs {
		close(c)
	}
	b.subs = map[chan struct{}]bool{}
	b.mu.Unlock()
	b.cancel()
}

// change: one change row; the JSON is contract.Change (contracts/ops-sessions.schema.json), the other fields stay in the coordinator.
type change struct {
	contract.Change
	ProjectID *string `json:"-"` // watch only (fleet-pm-agent.md §5); not in the stream contract
	Actor     *string `json:"-"`
}

// The horizon (queries/stream.sql): the lowest xid of the statement's snapshot that may still commit change rows
// (docs/designs/fleet-operator-app.md §4). A running transaction of this database may still commit rows under its lower
// xid, so the cursor must not pass it. pg_snapshot_xmin alone spans the whole server: a transaction in another database
// (pg_dump, another service's session, another test file's database) would hold the stream back. So a running xid is
// left out when pg_stat_activity shows its backend in another database; a backend never changes database, so that xid
// can never write here. An xid that has ended since the snapshot, a prepared transaction or an unlisted backend still
// counts (the next look passes it). Uncorrelated, so one InitPlan per statement, an index bound on change_order;
// pg_stat_activity is read only while xids run (about 0.1 ms, a copy of the backend table once per transaction).
// Visible rows are those of transactions older than the horizon; no later commit can sort before them.

// since: changes after cursor in (xid, seq) order, only from transactions older than every one of this database still
// running. held: some committed rows still wait behind such a transaction, which sends no second NOTIFY when it ends.
func since(ctx context.Context, tx pgx.Tx, cursor int64, limit int) (out []change, next int64, resync, held bool, err error) {
	next = cursor
	d := db.New(tx)
	var rows []db.StreamChangesFirstRow
	if cursor > 0 {
		xid, err := d.StreamChangeXid(ctx, cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			next, err = head(ctx, tx)
			return nil, next, true, false, err
		} else if err != nil {
			return nil, next, false, false, err
		}
		after, err := d.StreamChangesAfter(ctx, db.StreamChangesAfterParams{Xid: xid, Seq: cursor, Lim: int32(limit)})
		if err != nil {
			return nil, next, false, false, err
		}
		for _, r := range after {
			rows = append(rows, db.StreamChangesFirstRow(r))
		}
	} else if rows, err = d.StreamChangesFirst(ctx, int32(limit)); err != nil {
		return
	}
	out = make([]change, 0, len(rows))
	for _, r := range rows {
		c := change{Change: contract.Change{Seq: r.Seq, Kind: contract.ChangeKind(r.Kind), Id: r.ID, TaskId: r.TaskID,
			SessionId: r.SessionID, HostId: r.HostID, Op: r.Op, SettingTarget: r.SettingTarget}, ProjectID: r.ProjectID, Actor: r.Actor}
		c.Touches = contract.Touches(r.Kind, c.Id) // the operations the contract says this change can alter
		out = append(out, c)
	}
	if len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	if len(out) < limit {
		held, err = d.StreamChangesHeld(ctx)
	}
	return
}

func head(ctx context.Context, tx pgx.Tx) (int64, error) { return db.New(tx).StreamHead(ctx) }

// GET /api/stream (SL8, SL9): ready with a cursor, then every visible change after it with id: <seq>.
func (a *App) serveStream(q *Request, w http.ResponseWriter) error {
	var cursor int64 = -1
	if raw := q.r.URL.Query().Get("cursor"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > 1<<53-1 {
			return fail(400, "Invalid cursor")
		}
		cursor = n
	}
	ctx, cancel := context.WithCancel(q.ctx())
	defer cancel()
	if a.stream.ctx != nil {
		stop := context.AfterFunc(a.stream.ctx, cancel)
		defer stop()
	}
	initialVersion := ""
	if q.person != nil {
		initialVersion = grantsVersion(q.person)
	}
	wake := a.stream.join() // before the first read: a NOTIFY during it is not lost
	defer a.stream.leave(wake)
	select {
	case <-a.stoppedCh():
		return fail(503, "Coordinator shutting down")
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if cursor < 0 {
		if err := a.store.Read(ctx, q, func(q *Request) (err error) { cursor, err = head(ctx, q.tx); return }); err != nil {
			return err
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	sw := newSSE(w)
	send := sw.send
	publicCursor := cursor
	send("ready", contract.StreamCursor{Cursor: cursor}, -1)
	if err := sw.flush(); err != nil {
		return nil
	}
	// A comment line every 25 s (FLEET_STREAM_PING_MS: test seam) keeps proxies and Access from dropping an idle stream.
	ping := time.NewTicker(max(envMs("FLEET_STREAM_PING_MS", 25000), time.Millisecond))
	defer ping.Stop()
	for {
		held := false
		sw.begin()
		for {
			if q.person != nil {
				if err := a.refreshPeople(ctx); err != nil {
					return nil
				}
				p, err := a.resolveAccess(ctx, q.person.Email)
				if err != nil || grantsVersion(p) != initialVersion {
					send("resync", contract.StreamCursor{Cursor: publicCursor}, -1)
					_ = sw.flush()
					return nil
				}
				q.person = p
			}
			var cs []change
			var resync bool
			batch := a.stream.readBatch(ctx, a, q.person, cursor)
			cs, cursor, resync, held = batch.rows, batch.next, batch.resync, batch.held
			err := batch.err
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				send("error", map[string]string{"message": err.Error()}, -1)
				break
			}
			if resync {
				current := *q
				current.tx = nil
				if err := a.store.Read(ctx, &current, func(rq *Request) error {
					if err := refreshRequestAccess(rq); err != nil {
						return err
					}
					var err error
					publicCursor, err = head(ctx, rq.tx)
					return err
				}); err != nil {
					return nil
				}
				send("resync", contract.StreamCursor{Cursor: publicCursor}, -1)
				break
			}
			for _, c := range cs {
				publicCursor = c.Seq
				send("", c, c.Seq)
			}
			if !batch.more {
				break
			}
		}
		if err := sw.flush(); err != nil {
			return nil // the client is gone or stopped reading: end the handler and its subscription
		}
		var timer <-chan time.Time
		if held {
			timer = time.After(heldWait)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-a.stoppedCh():
			return nil
		case _, ok := <-wake:
			if !ok {
				return nil
			}
		case <-timer:
		case <-ping.C:
			sw.begin()
			sw.write([]byte(": ping\n\n"))
			if err := sw.flush(); err != nil {
				return nil
			}
		}
	}
}

// streamWriteWait bounds one burst of writes and its flush to a client; between bursts the stream has no deadline, so an
// idle stream stays open as long as the client keeps it (pings keep proxies from dropping it).
var streamWriteWait = 30 * time.Second

// sse: event writes of one stream. A burst starts with begin (the write deadline), ends with flush (which clears it);
// the first write or flush error sticks, and every later write is skipped.
type sse struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	err error
}

func newSSE(w http.ResponseWriter) *sse {
	s := &sse{w: w, rc: http.NewResponseController(w)}
	s.begin()
	return s
}

func (s *sse) begin() {
	if err := s.rc.SetWriteDeadline(time.Now().Add(streamWriteWait)); err != nil && !errors.Is(err, http.ErrNotSupported) && s.err == nil {
		s.err = err
	}
}

func (s *sse) write(b []byte) {
	if s.err == nil {
		_, s.err = s.w.Write(b)
	}
}

func (s *sse) send(event string, data any, id int64) {
	b, _ := json.Marshal(data)
	var out []byte
	if id >= 0 {
		out = fmt.Appendf(out, "id: %d\n", id)
	}
	if event != "" {
		out = fmt.Appendf(out, "event: %s\n", event)
	}
	s.write(fmt.Appendf(out, "data: %s\n\n", b))
}

func (s *sse) flush() error {
	if s.err == nil {
		s.err = s.rc.Flush()
	}
	if s.err == nil {
		if err := s.rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			s.err = err
		}
	}
	return s.err
}

func (b *Broadcast) signalFeed() {
	select {
	case b.feedWake <- struct{}{}:
	default:
	}
}
