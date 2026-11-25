package main

import "testing"

// Requirement: a host tool's version is read from its --version output whatever its form. Risk: node's "v26.13.1" or
// gh's "gh version 2.105.0 (date)" reads as no version, and every host shows its tools as unknown.
func TestToolSemver(t *testing.T) {
	for out, want := range map[string]string{
		"v26.13.1\n":                                 "26.13.1",
		"12.23.0\n":                                  "12.23.0",
		"gh version 2.105.0 (2026-11-01)\nhttps://x": "2.105.0",
		"glab 1.133.0 (abc123)\n":                    "1.133.0",
	} {
		if got := toolSemver.FindString(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}
