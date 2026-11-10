package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Live change feed (packages/coordinator/stream.ts). One LISTEN fleet_change connection per process wakes every open
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

type change struct {
	Seq       int64   `json:"seq"`
	Kind      string  `json:"kind"`
	ID        string  `json:"id"`
	TaskID    *string `json:"taskId"`
	SessionID *string `json:"sessionId"`
	HostID    *string `json:"hostId"`
	Op        string  `json:"op"`
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
	q := "SELECT seq,kind,id,task_id,session_id,host_id,op FROM change WHERE " + visible
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
		return c, r.Scan(&c.Seq, &c.Kind, &c.ID, &c.TaskID, &c.SessionID, &c.HostID, &c.Op)
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

// jsCursor: the cursor as app.ts reads it, Number(raw) then a safe non-negative integer (so " 5 ", "5.0", "5e0" and
// "0x5" are 5). A repeated key is an array in Fastify's query, which Number makes NaN.
var jsDecimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

func jsCursor(r *http.Request) (cursor int64, given, ok bool) {
	vs, present := r.URL.Query()["cursor"]
	if !present {
		return 0, false, true
	}
	if len(vs) != 1 {
		return 0, true, false
	}
	if vs[0] == "" {
		return 0, false, true
	}
	t := strings.Trim(vs[0], " \t\n\v\f\r\u00a0\ufeff\u2028\u2029")
	var f float64
	switch {
	case t == "":
		return 0, true, true
	case len(t) > 2 && t[0] == '0' && strings.ContainsRune("xXoObB", rune(t[1])):
		base := map[byte]int{'x': 16, 'o': 8, 'b': 2}[t[1]|0x20]
		if strings.ContainsAny(t[2:], "_+-") {
			return 0, true, false
		}
		n, err := strconv.ParseUint(t[2:], base, 64)
		if err != nil {
			return 0, true, false
		}
		f = float64(n)
	case jsDecimal.MatchString(t):
		f, _ = strconv.ParseFloat(t, 64)
	default:
		return 0, true, false
	}
	if f < 0 || f != math.Trunc(f) || f > 1<<53-1 {
		return 0, true, false
	}
	return int64(f), true, true
}

const pingEvery, debounce = 25 * time.Second, 50 * time.Millisecond

// GET /api/stream (stream.ts): ready with a cursor, then every visible change after it with id: <seq>; a comment ping every
// 25 s; a close frame when the server stops.
func (a *App) serveStream(q *Request, w http.ResponseWriter) error {
	cursor, given, ok := jsCursor(q.r)
	if !ok {
		w.Header().Del("Referrer-Policy")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"Invalid cursor"}`)
		return nil
	}
	ctx, sc := q.ctx(), scopeOf(q.person)
	wake := a.stream.join() // before the first read: a NOTIFY during it is not lost
	defer a.stream.leave(wake)
	if !given {
		if err := a.store.Tx(ctx, sc, false, "", func(tx pgx.Tx) (err error) { cursor, err = head(ctx, tx); return }); err != nil {
			return nil // as stream.ts: the response ends without a status line of its own
		}
	}
	fl, _ := w.(http.Flusher)
	h := w.Header()
	h.Del("Referrer-Policy") // a hijacked Fastify reply sends only the headers stream.ts writes
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(event string, data any, id int64) {
		b, _ := marshal(data)
		if id >= 0 {
			fmt.Fprintf(w, "id: %d\n", id)
		}
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	send("ready", map[string]int64{"cursor": cursor}, -1)
	fl.Flush()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for drain := given; ; drain = true {
		held := false
		for drain {
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
				send("resync", map[string]int64{"cursor": cursor}, -1)
				break
			}
			for _, c := range cs {
				send("", c, c.Seq)
			}
			if len(cs) < 1000 {
				break
			}
		}
		fl.Flush()
		var timer <-chan time.Time
		if held {
			timer = time.After(heldWait)
		}
	wait:
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-wake:
			if !ok {
				io.WriteString(w, "event: close\ndata: {}\n\n")
				fl.Flush()
				return nil
			}
			time.Sleep(debounce)
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
			goto wait
		case <-timer:
		}
	}
}
