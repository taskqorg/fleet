package main

import (
	"strings"
	"testing"
)

// Requirement: a pipeline's output.json names what it holds through its top-level summary, the result's title in lists.
// Risk: another file, a non-text summary or a multi-line one becomes a title; a huge one floods a row.
func TestOutputSummary(t *testing.T) {
	long := strings.Repeat("a", 300)
	for _, c := range []struct {
		name, data, want string
	}{
		{"output.json", `{"summary":"KATARZYNA NOWAK · 5 files · 1 unreadable","files":5}`, "KATARZYNA NOWAK · 5 files · 1 unreadable"},
		{"output.json", `{"summary":"  two\n lines "}`, "two lines"},
		{"output.json", `{"summary":7}`, ""},
		{"output.json", `not json`, ""},
		{"result.json", `{"summary":"x"}`, ""},
		{"output.json", `{"summary":"` + long + `"}`, strings.Repeat("a", 199) + "…"},
	} {
		got := deref(outputSummary(c.name, []byte(c.data)))
		if got != c.want {
			t.Fatalf("%s %q: %q, want %q", c.name, c.data, got, c.want)
		}
	}
}
