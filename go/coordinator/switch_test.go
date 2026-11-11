package main

import (
	"strings"
	"testing"
)

// A subscription at 100 % with no switchPct is skipped (people-access §6): it cannot run anyway, so dispatch must not
// pick it and fail on the host. Below 100 % with no switchPct it stays a candidate.
func TestFullSubscriptionWithoutSwitchPointIsSkipped(t *testing.T) {
	at := float64(nowMs() + 3600_000)
	reading := func(pct float64) []Reading { return []Reading{{Period: "5h", Pct: &pct, ResetsAt: &at, At: nowMs(), State: "known"}} }
	if got := readingOverSwitch(map[string]any{"switchPct": nil}, reading(100)); !strings.HasPrefix(got, "subscription full") {
		t.Fatalf("100 %% without switchPct: %q", got)
	}
	if got := readingOverSwitch(map[string]any{}, reading(99)); got != "" {
		t.Fatalf("99 %% without switchPct: %q", got)
	}
	if got := readingOverSwitch(map[string]any{"switchPct": 80.0}, reading(85)); !strings.HasPrefix(got, "switch point 80") {
		t.Fatalf("switchPct 80: %q", got)
	}
}
