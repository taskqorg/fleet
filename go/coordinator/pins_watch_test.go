package main

import (
	"testing"
	"time"
)

// TestPinsRegressions: a pinned version is flagged on a host only inside its first 24 hours there, with enough runs
// and failures, and a failure share clearly above the host's other versions.
func TestPinsRegressions(t *testing.T) {
	now := time.Now()
	fresh, old := now.Add(-2*time.Hour), now.Add(-30*time.Hour)
	pinned := map[string]string{"codex": "0.172.0", "claude": "2.1.332"}
	runs := []pinsWatchRun{
		{Host: "pds18", Harness: "codex", Version: "codex-cli 0.172.0", Failed: 4, Total: 6, FirstAt: fresh},
		{Host: "pds18", Harness: "codex", Version: "codex-cli 0.169.0", Failed: 1, Total: 40, FirstAt: old},
		{Host: "mac", Harness: "codex", Version: "0.172.0", Failed: 3, Total: 4, FirstAt: fresh},     // too few runs
		{Host: "win", Harness: "codex", Version: "0.172.0", Failed: 5, Total: 8, FirstAt: old},       // window over
		{Host: "pds18", Harness: "claude", Version: "2.1.332", Failed: 3, Total: 10, FirstAt: fresh}, // 30% vs 20%
		{Host: "pds18", Harness: "claude", Version: "2.1.326", Failed: 4, Total: 20, FirstAt: old},
		{Host: "mac", Harness: "claude", Version: "2.1.3320", Failed: 9, Total: 9, FirstAt: fresh}, // another version
	}
	got := pinsRegressions(runs, pinned, now)
	if len(got) != 1 || got[0].Host != "pds18" || got[0].Harness != "codex" || got[0].describe()[:21] != "revert codex 0.172.0:" {
		t.Fatalf("%+v", got)
	}
}
