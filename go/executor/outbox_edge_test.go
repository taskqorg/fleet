package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A store route that logged the edge redirect logs one line when it succeeds again, so the log does not read as stuck.
func TestStoreEdgeLogsReachableAgain(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	edgeRedirect("store fleet-store-pds18.ufobe.com", 302, "https://example.invalid/login")
	edgeOK("store fleet-store-pds18.ufobe.com")
	edgeOK("store fleet-store-pds18.ufobe.com") // already clear: no second line
	os.Stderr = old
	w.Close()
	b, _ := io.ReadAll(r)
	if got := strings.Count(string(b), "executor: store fleet-store-pds18.ufobe.com reachable again\n"); got != 1 {
		t.Fatalf("reachable-again lines = %d in %q", got, b)
	}
}

// cutBodyServer answers status with the start of a JSON body and holds the rest until the request ends; flushed is
// closed once that start is sent.
func cutBodyServer(status int) (*httptest.Server, chan struct{}) {
	flushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"pad":"xx`)
		w.(http.Flusher).Flush()
		close(flushed)
		<-r.Context().Done() // the rest never comes
	}))
	return srv, flushed
}

// A heartbeat poll cancelled after its headers arrived (its exchange ended) leaves a cut JSON body: a transport retry,
// never an edge answer that is not JSON (17 Nov 2026: owner-mac logged one with nearly every exchange). A cut 503 keeps
// its status, so the flush still slows down.
func TestCutBodyIsRetryNotEdge(t *testing.T) {
	for _, status := range []int{200, 503} {
		srv, _ := cutBodyServer(status)
		ctx, cancel := context.WithCancel(context.Background())
		r, err := postJSONCtx(ctx, outboxClient, srv.URL, "t", M{})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		a := classify(r, err)
		srv.Close()
		if status == 503 {
			if a.kind != answerRetry || a.status != 503 {
				t.Fatalf("cut 503 body: %+v", a)
			}
			continue
		}
		if a.kind != answerRetry || a.status != 0 || !strings.Contains(a.text, "200 answer cut after") {
			t.Fatalf("cut 200 body: %+v", a)
		}
		if ok, perr := pollSignal(a); ok || perr != "network" {
			t.Fatalf("poll signal = %v %q", ok, perr)
		}
	}
}

// The exchange's own cancelled heartbeat poll leaves update.json as the last real poll wrote it.
func TestCancelledPollKeepsUpdateSignal(t *testing.T) {
	srv, flushed := cutBodyServer(200)
	defer srv.Close()
	root := t.TempDir()
	e := &executor{url: srv.URL, token: "t", host: "h", root: root}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-flushed; time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := e.pollWait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "update", "update.json")); !os.IsNotExist(err) {
		t.Fatalf("update.json written for a cancelled poll: %v", err)
	}
}

// A whole 2xx answer that is not JSON names its content type, length and first bytes.
func TestNotJSONNamesTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!doctype html>\n<title>Sign in</title>")
	}))
	defer srv.Close()
	a := classify(postJSON(outboxClient, srv.URL, "t", M{}))
	want := `an answer that is not JSON: text/html, 38 bytes: "<!doctype html>\n<title>Sign in</title>"`
	if a.kind != answerEdge || a.status != 200 || a.text != want {
		t.Fatalf("got %+v, want text %s", a, want)
	}
}
