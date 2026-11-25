package main

import (
	"testing"
)

// Requirement: host storage health uses its own fresh observations.
// Risk: global reachability or stale/missing checks falsely claim usable storage.
func TestHostStoreStatus(t *testing.T) {
	report := func(v bool) any { return map[string]any{"id": "fixture", "reachable": v} }
	for _, c := range []struct {
		name    string
		reports any
		seen    float64
		want    string
	}{
		{"missing", nil, 100000, "unknown"},
		{"empty", []any{}, 100000, "unknown"},
		{"reachable", []any{report(true)}, 100000, "reachable"},
		{"unreachable", []any{report(false)}, 100000, "unreachable"},
		{"mixed", []any{report(true), report(false)}, 100000, "mixed"},
		{"stale", []any{report(true)}, 70000, "unknown"},
		{"unobserved", []any{report(true)}, 0, "unknown"},
		{"unsupported", []any{map[string]any{"id": "old"}}, 100000, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, reason := hostStoreStatus(c.reports, c.seen, 100000)
			if got != c.want || reason == "" {
				t.Fatalf("got %s (%s), want %s", got, reason, c.want)
			}
		})
	}
}
