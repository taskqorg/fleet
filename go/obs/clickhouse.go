package obs

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// ClickHouse is the HTTP interface of the span store (default port 8123; PDS18 publishes it on 127.0.0.1:9480).
type ClickHouse struct {
	URL, Database, User, Password string
	Client                        *http.Client
}

//go:embed schema.sql
var schemaSQL string

// Migrate applies the span schema: idempotent statements separated by ";" at a line end; "--" lines are comments.
func (c *ClickHouse) Migrate(ctx context.Context) error {
	var b strings.Builder
	for _, l := range strings.Split(schemaSQL, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			b.WriteString(l + "\n")
		}
	}
	for _, st := range strings.Split(b.String(), ";\n") {
		if st = strings.TrimSpace(st); st != "" {
			if _, err := c.do(ctx, st, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Insert writes JSONEachRow rows into table; token makes a retried batch idempotent (insert_deduplication_token).
func (c *ClickHouse) Insert(ctx context.Context, table string, rows []byte, token string) error {
	p := url.Values{}
	if token != "" {
		p.Set("insert_deduplication_token", token)
	}
	_, err := c.do(ctx, "INSERT INTO "+table+" FORMAT JSONEachRow", p, rows)
	return err
}

// Query runs a SELECT with {name:Type} parameters and decodes its JSONEachRow rows into out (a pointer to a slice).
func (c *ClickHouse) Query(ctx context.Context, sql string, params map[string]string, out any) error {
	p := url.Values{}
	for k, v := range params {
		p.Set("param_"+k, v)
	}
	body, err := c.do(ctx, sql+" FORMAT JSONEachRow", p, nil)
	if err != nil {
		return err
	}
	var lines []json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append(json.RawMessage(nil), sc.Bytes()...))
		}
	}
	js, _ := json.Marshal(lines)
	return json.Unmarshal(js, out)
}

func (c *ClickHouse) do(ctx context.Context, query string, p url.Values, body []byte) ([]byte, error) {
	if c == nil || c.URL == "" {
		return nil, errors.New("clickhouse: no url")
	}
	if p == nil {
		p = url.Values{}
	}
	p.Set("output_format_json_quote_64bit_integers", "0")
	if c.Database != "" {
		p.Set("database", c.Database)
	}
	var rd io.Reader
	if body != nil {
		p.Set("query", query)
		rd = bytes.NewReader(body)
	} else {
		rd = strings.NewReader(query)
	}
	r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/?"+p.Encode(), rd)
	if err != nil {
		return nil, err
	}
	if c.User != "" {
		r.Header.Set("X-ClickHouse-User", c.User)
		r.Header.Set("X-ClickHouse-Key", c.Password)
	}
	cl := c.Client
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cl.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("clickhouse: %d %s", resp.StatusCode, strings.TrimSpace(string(b[:min(len(b), 300)])))
	}
	return b, nil
}

// Writer feeds finished spans to ClickHouse from one goroutine: a full channel or a failed insert drops and counts,
// a request never waits. After Fails failed flushes in a row OnDown runs once; the next good flush runs OnUp with the
// count dropped meanwhile and writes one telemetry.recovered check span.
type Writer struct {
	CH          *ClickHouse
	Every       time.Duration
	Rows, Fails int
	OnDown      func()
	OnUp        func(dropped int64)
	Enabled     func() bool
	ch          chan *Span
	dropped     atomic.Int64
	downDrop    int64
	failed      int
	down        bool
}

// Dropped is the count of spans dropped since start.
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// Add queues a span; never blocks.
func (w *Writer) Add(s *Span) {
	if w.Enabled != nil && !w.Enabled() {
		w.dropped.Add(1)
		return
	}
	select {
	case w.ch <- s:
	default:
		w.dropped.Add(1)
	}
}

// Run starts the writer; it stops when ctx ends.
func (w *Writer) Run(ctx context.Context) {
	if w.Every == 0 {
		w.Every = time.Second
	}
	if w.Rows == 0 {
		w.Rows = 1000
	}
	if w.Fails == 0 {
		w.Fails = 3
	}
	w.ch = make(chan *Span, 10*w.Rows)
	go func() {
		t := time.NewTicker(w.Every)
		defer t.Stop()
		var batch []*Span
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-w.ch:
				if batch = append(batch, s); len(batch) >= w.Rows {
					w.flush(ctx, batch)
					batch = nil
				}
			case <-t.C:
				if len(batch) > 0 {
					w.flush(ctx, batch)
					batch = nil
				}
			}
		}
	}()
}

func (w *Writer) flush(ctx context.Context, batch []*Span) {
	var b bytes.Buffer
	for _, s := range batch {
		js, _ := json.Marshal(s)
		b.Write(js)
		b.WriteByte('\n')
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.CH.Insert(c, "span", b.Bytes(), ""); err != nil {
		if w.failed == 0 {
			w.downDrop = w.dropped.Load()
		}
		w.dropped.Add(int64(len(batch)))
		if w.failed++; w.failed >= w.Fails && !w.down {
			w.down = true
			if w.OnDown != nil {
				w.OnDown()
			}
		}
		return
	}
	w.failed = 0
	if w.down {
		w.down = false
		lost := w.dropped.Load() - w.downDrop
		_, s := Start(ctx, "telemetry.recovered", KindCheck)
		u := uint64(max(lost, 0))
		s.Items = &u
		s.End(nil)
		if w.OnUp != nil {
			w.OnUp(lost)
		}
	}
}
