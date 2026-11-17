package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// A heartbeat poll cancelled after its headers arrived (its exchange ended) leaves a cut JSON body: a transport retry,
// never an edge answer that is not JSON (17 Nov 2026: owner-mac logged one with nearly every exchange).
func TestCutBodyIsRetryNotEdge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"pad":"`+strings.Repeat("x", 200_000)+`"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	r, err := postJSONCtx(ctx, outboxClient, srv.URL, "t", M{})
	cancel()
	time.Sleep(20 * time.Millisecond)
	a := classify(r, err)
	if a.kind != answerRetry || a.status != 0 || !strings.Contains(a.text, "200 answer cut after") {
		t.Fatalf("cut body: %+v", a)
	}
	if ok, perr := pollSignal(a); ok || perr != "network" {
		t.Fatalf("poll signal = %v %q", ok, perr)
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
	want := `an answer that is not JSON: text/html, 38 bytes: "<!doctype html> <title>Sign in</title>"`
	if a.kind != answerEdge || a.status != 200 || a.text != want {
		t.Fatalf("got %+v, want text %s", a, want)
	}
}
