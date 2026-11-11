package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/obs"
)

func TestPsTime(t *testing.T) {
	for in, want := range map[string]float64{"0:01.50": 1.5, "12:03.00": 723, "01:02:03": 3723, "2-00:00:01": 172801} {
		if got := psTime(in); got != want {
			t.Fatal(in, got)
		}
	}
}

// A heartbeat writes host readings; shipping posts them as one batch and removes the file once accepted.
func TestHeartbeatShips(t *testing.T) {
	root := t.TempDir()
	spanDir = filepath.Join(root, "spans")
	os.MkdirAll(spanDir, 0o700)
	obs.SetSink(appendSpan)
	defer obs.SetSink(nil)
	prev := map[int]float64{}
	heartbeat(root, prev)
	b, _ := os.ReadFile(filepath.Join(spanDir, "current.jsonl"))
	var hb obs.Span
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var s obs.Span
		json.Unmarshal([]byte(l), &s)
		if s.Name == "host.heartbeat" {
			hb = s
		}
	}
	if hb.MemFree == nil || hb.DiskFree == nil || hb.Load1 == nil || hb.Attrs["fleet.cpus"] == "" || hb.Kind != "check" {
		t.Fatalf("heartbeat %s", b)
	}
	got, fail := 0, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "down", 503)
			return
		}
		var req struct {
			BatchId string
			Spans   []json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		got += len(req.Spans)
		w.Write([]byte(`{"accepted":1,"skipped":0}`))
	}))
	defer srv.Close()
	shipSpans(srv.URL, "t")
	if left, _ := filepath.Glob(filepath.Join(spanDir, "pending-*")); len(left) != 1 {
		t.Fatal("a refused batch keeps its file", left)
	}
	fail = false
	shipSpans(srv.URL, "t")
	if left, _ := filepath.Glob(filepath.Join(spanDir, "*")); len(left) != 0 || got == 0 {
		t.Fatal(left, got)
	}
}
