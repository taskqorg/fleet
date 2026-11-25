package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Requirement: an operation answers its marked phases in Server-Timing (names and milliseconds only), so a slow read
// on the deployed site is broken down from curl or the browser. Risk: the header goes missing, or a phase is unmarked.
func TestServerTimingPhases(t *testing.T) {
	e := openTestCoordinator(t, false)
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+"/api/ops/access_status", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.ownerHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("access_status %d", resp.StatusCode)
	}
	st := resp.Header.Get("Server-Timing")
	for _, want := range []string{"profiles;dur=", "readings;dur=", "subscriptions;dur=", "lastcalls;dur=", "connections;dur=", "total;dur="} {
		if !strings.Contains(st, want) {
			t.Fatalf("Server-Timing %q lacks %s", st, want)
		}
	}
}
