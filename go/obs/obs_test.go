package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestTraceparentRoundTrip(t *testing.T) {
	ctx, s := Start(context.Background(), "x", KindInternal)
	tr, sp, ok := ParseTraceparent(Traceparent(ctx))
	if !ok || tr != s.TraceID || sp != s.SpanID {
		t.Fatal(Traceparent(ctx))
	}
	for _, bad := range []string{"", "00-" + fmt.Sprintf("%032d", 0) + "-0000000000000001-01", "01-abc", "00-ABCDEF00000000000000000000000000-0000000000000001-01"} {
		if _, _, ok := ParseTraceparent(bad); ok {
			t.Fatal(bad)
		}
	}
	child, c := Start(WithRemote(context.Background(), Traceparent(ctx)), "y", KindServer)
	if c.TraceID != s.TraceID || c.ParentSpanID != s.SpanID || From(child) != c {
		t.Fatal("remote parent")
	}
}

func TestHTTPOutcome(t *testing.T) {
	for _, c := range []struct {
		status  int
		refused bool
		outcome string
		class   string
	}{{200, false, OK, ""}, {302, false, OK, ""}, {401, false, Refused, ""}, {403, true, Refused, ""}, {403, false, Error, "http_4xx"},
		{409, true, Refused, ""}, {404, false, Error, "http_4xx"}, {429, false, Refused, ""}, {500, false, Error, "http_5xx"}} {
		if o, k := HTTPOutcome(c.status, c.refused); o != c.outcome || k != c.class {
			t.Fatal(c.status, o, k)
		}
	}
}

func TestSetAllowList(t *testing.T) {
	_, s := Start(context.Background(), "x", KindInternal)
	s.Set("http.route", "GET /a")
	s.Set("prompt", "secret text")
	s.Set("task.text", "x")
	s.Set("fleet.long", string(make([]byte, 1000)))
	if s.Attrs["prompt"] != "" || s.Attrs["missing.attrs"] != "2" || len(s.Attrs["fleet.long"]) != AttrMaxBytes || s.Attrs["http.route"] != "GET /a" {
		t.Fatal(s.Attrs)
	}
}

func TestWriterDropsWhenFull(t *testing.T) {
	w := &Writer{CH: &ClickHouse{URL: "http://127.0.0.1:1"}, Rows: 1, Every: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the goroutine stops at once: nothing drains the channel
	w.Run(ctx)
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 100; i++ {
		w.Add(&Span{})
	}
	if w.Dropped() < 90 {
		t.Fatal(w.Dropped())
	}
}

// ClickHouse stopped: requests through Handler still succeed, the drop counter rises, OnDown runs, and recovery writes
// one telemetry.recovered span and runs OnUp.
func TestWriterDownAndUp(t *testing.T) {
	var mu sync.Mutex
	up := false
	var got []Span
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			http.Error(w, "down", 503)
			return
		}
		dec := json.NewDecoder(r.Body)
		for dec.More() {
			var s Span
			dec.Decode(&s)
			got = append(got, s)
		}
	}))
	defer ch.Close()
	down, recovered := make(chan bool, 1), make(chan int64, 1)
	w := &Writer{CH: &ClickHouse{URL: ch.URL}, Every: 5 * time.Millisecond, Rows: 1000, OnDown: func() { down <- true }, OnUp: func(n int64) { recovered <- n }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Run(ctx)
	SetSink(w.Add)
	defer SetSink(nil)
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }), func(*http.Request) string { return "GET /x" })
	srv := httptest.NewServer(h)
	defer srv.Close()
	for i := 0; i < 100; i++ {
		r, err := http.Get(srv.URL)
		if err != nil || r.StatusCode != 200 {
			t.Fatal(err)
		}
		r.Body.Close()
		if i%30 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	select {
	case <-down:
	case <-time.After(2 * time.Second):
		t.Fatal("no OnDown")
	}
	time.Sleep(20 * time.Millisecond)
	if w.Dropped() < 100 {
		t.Fatal("dropped", w.Dropped())
	}
	mu.Lock()
	up = true
	mu.Unlock()
	_, s := Start(context.Background(), "after", KindInternal)
	s.End(nil)
	select {
	case n := <-recovered:
		if n < 100 {
			t.Fatal("recovered count", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no OnUp")
	}
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	names := map[string]int{}
	for _, s := range got {
		names[s.Name]++
	}
	if names["telemetry.recovered"] != 1 || names["after"] != 1 {
		t.Fatal(names)
	}
}

// With FLEET_TEST_CLICKHOUSE_URL (user t, password t, database fleet): the schema applies twice, a batch posted twice
// with one token yields one set of rows.
func TestClickHouseLive(t *testing.T) {
	u := os.Getenv("FLEET_TEST_CLICKHOUSE_URL")
	if u == "" {
		t.Skip("FLEET_TEST_CLICKHOUSE_URL not set")
	}
	c := &ClickHouse{URL: u, Database: "fleet", User: "t", Password: "t"}
	ctx := context.Background()
	if err := c.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, s := Start(context.Background(), "live.test", KindInternal)
	s.Service = "test"
	s.End(nil)
	row, _ := json.Marshal(s)
	tok := s.SpanID
	for i := 0; i < 2; i++ {
		if err := c.Insert(ctx, "span", append(row, '\n'), tok); err != nil {
			t.Fatal(err)
		}
	}
	var n []struct{ N uint64 }
	if err := c.Query(ctx, "SELECT count() AS N FROM span WHERE trace_id = {t:String}", map[string]string{"t": s.TraceID}, &n); err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || n[0].N != 1 {
		t.Fatal(n)
	}
}
