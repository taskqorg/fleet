package main

import (
	"context"
	"errors"
	"fmt"
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

// A heartbeat poll cancelled after its headers arrived (its exchange ended) leaves a cut JSON body: a transport retry,
// never an edge answer that is not JSON (17 Nov 2026: owner-mac logged one with nearly every exchange).
func TestCutBodyIsRetryNotEdge(t *testing.T) {
	for _, status := range []int{200, 400} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"pad":"xx`)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // the rest never comes
		}))
		ctx, cancel := context.WithCancel(context.Background())
		r, err := postJSONCtx(ctx, outboxClient, srv.URL, "t", M{})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		a := classify(r, err)
		srv.Close()
		if a.kind != answerRetry || a.status != 0 || a.timeout || !strings.Contains(a.text, fmt.Sprintf("%d answer cut after", status)) {
			t.Fatalf("cut %d body: %+v", status, a)
		}
		if ok, perr := pollSignal(a); ok || perr != "network" {
			t.Fatalf("poll signal = %v %q", ok, perr)
		}
	}
}

// The exchange's own cancelled heartbeat poll leaves update.json as the last real poll wrote it.
func TestCancelledPollKeepsUpdateSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"pad":"xx`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	root := t.TempDir()
	e := &executor{url: srv.URL, token: "t", host: "h", root: root}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
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
