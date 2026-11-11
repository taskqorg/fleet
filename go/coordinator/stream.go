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
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// Live change feed (docs/designs/fleet-operator-app.md §3, SL8). One LISTEN fleet_change connection per process wakes every open
// stream; each stream reads in its own short snapshot in its viewer's scope, so it never holds a transaction between wakes.
type Broadcast struct {
	mu     sync.Mutex
	subs   map[chan struct{}]bool
	cancel context.CancelFunc
	done   chan struct{}
}

// heldWait: the next look when committed changes wait behind an older running transaction anywhere on the server.
const heldWait = 250 * time.Millisecond

func listen(parent context.Context, url string) *Broadcast {
	ctx, cancel := context.WithCancel(parent)
	b := &Broadcast{subs: map[chan struct{}]bool{}, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(b.done)
		for ctx.Err() == nil {
			conn, err := pgx.Connect(ctx, url)
			if err == nil {
				if _, err = conn.Exec(ctx, "LISTEN fleet_change"); err == nil {
					b.wake() // anything committed while not listening
					for err == nil {
						_, err = conn.WaitForNotification(ctx)
						if err == nil {
							b.wake()
						}
					}
				}
				conn.Close(context.Background())
			}
			if ctx.Err() == nil {
				log.Printf("listen: %v; again in 1 s", err)
				time.Sleep(time.Second)
			}
		}
	}()
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
	b.subs[c] = true
	b.mu.Unlock()
	return c
}

func (b *Broadcast) leave(c chan struct{}) { b.mu.Lock(); delete(b.subs, c); b.mu.Unlock() }

func (b *Broadcast) close() {
	b.mu.Lock()
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

const visible = "xid < pg_snapshot_xmin(pg_current_snapshot())"

// since: changes after cursor in (xid, seq) order, only from transactions older than every one still running. held: some
// committed rows still wait behind such a transaction, which sends no second NOTIFY when it ends.
func since(ctx context.Context, tx pgx.Tx, cursor int64, limit int) (out []change, next int64, resync, held bool, err error) {
	next = cursor
	var xid string
	if cursor > 0 {
		if err = tx.QueryRow(ctx, "SELECT xid::text FROM change WHERE seq=$1", cursor).Scan(&xid); err == pgx.ErrNoRows {
			next, err = head(ctx, tx)
			return nil, next, true, false, err
		} else if err != nil {
			return
		}
	}
	q := "SELECT seq,kind,id,task_id,session_id,host_id,op,project_id,actor FROM change WHERE " + visible
	args := []any{}
	if xid != "" {
		q += " AND (xid, seq) > ($1::xid8, $2::bigint)"
		args = append(args, xid, cursor)
	}
	rs, err := tx.Query(ctx, q+" ORDER BY xid, seq LIMIT "+strconv.Itoa(limit), args...)
	if err != nil {
		return
	}
	out, err = pgx.CollectRows(rs, func(r pgx.CollectableRow) (change, error) {
		var c change
		return c, r.Scan(&c.Seq, &c.Kind, &c.Id, &c.TaskId, &c.SessionId, &c.HostId, &c.Op, &c.ProjectID, &c.Actor)
	})
	if err != nil {
		return
	}
	if len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	if len(out) < limit {
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM change WHERE NOT ("+visible+"))").Scan(&held)
	}
	return
}

func head(ctx context.Context, tx pgx.Tx) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, "SELECT coalesce((SELECT seq FROM change WHERE "+visible+" ORDER BY xid DESC, seq DESC LIMIT 1),0)").Scan(&seq)
	return seq, err
}

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
	ctx, sc := q.ctx(), scopeOf(q.person)
	initialVersion := ""
	if q.person != nil {
		initialVersion = grantsVersion(q.person)
	}
	wake := a.stream.join() // before the first read: a NOTIFY during it is not lost
	defer a.stream.leave(wake)
	if cursor < 0 {
		if err := a.store.Tx(ctx, sc, false, "", func(tx pgx.Tx) (err error) { cursor, err = head(ctx, tx); return }); err != nil {
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
					send("resync", contract.StreamCursor{Cursor: cursor}, -1)
					_ = sw.flush()
					return nil
				}
				q.person = p
				sc = scopeOf(p)
			}
			var cs []change
			var resync bool
			err := a.store.Tx(ctx, sc, false, "", func(tx pgx.Tx) (err error) {
				cs, cursor, resync, held, err = since(ctx, tx, cursor, 1000)
				return
			})
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				send("error", map[string]string{"message": err.Error()}, -1)
				break
			}
			if resync {
				send("resync", contract.StreamCursor{Cursor: cursor}, -1)
				break
			}
			for _, c := range cs {
				send("", c, c.Seq)
			}
			if len(cs) < 1000 {
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
