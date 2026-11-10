package main

import (
	"io"
	"os"
	"strings"
	"testing"
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
