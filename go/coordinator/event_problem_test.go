package main

import "testing"

// Requirement: an agent failure records its cause on the execution: the preparation reason, a protocol failure, or a
// non-zero exit with the last stderr line. Risk: Claude runs on owner-mac failed with no reason anywhere (2026-11-11,
// Lab experiment 5e9021a4: "connection-missing" was dropped).
func TestEventProblem(t *testing.T) {
	for _, c := range []struct {
		kind string
		data doc
		want string
	}{
		{"preparation.failed", doc{"reason": "connection-missing"}, "preparation failed: connection-missing"},
		{"protocol.failed", doc{"error": "ACP EOF before terminal outcome; no replay"}, "harness protocol failed: ACP EOF before terminal outcome; no replay"},
		{"process.closed", doc{"exitCode": 1.0, "stderrTail": "warming up\nError: model not available\n"}, "harness exited 1: Error: model not available"},
		{"process.closed", doc{"exitCode": 0.0}, ""},
		{"setup", doc{"ok": true}, ""},
	} {
		if got := eventProblem(c.kind, c.data); got != c.want {
			t.Errorf("%s: got %q want %q", c.kind, got, c.want)
		}
	}
}
