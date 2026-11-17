package main

import (
	"strings"
	"testing"
)

// Requirement: a conversation found by words its title does not show carries a short snippet of the matching text (spotlight.md).
// Risk: the row shows a long boilerplate first message with no sign of why it matched, or a snippet repeats the visible title.
func TestMatchSnippet(t *testing.T) {
	long := "You are a worker agent. Follow AGENTS.md fully and read every contract on the path before you start.\n\nTask: fix the deploy script so it waits for the health check. Then report the result with links to the run and the pull request."
	got := matchSnippet(long, "Deploy")
	if !strings.Contains(strings.ToLower(got), "deploy") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || len([]rune(got)) > snippetLen+2 {
		t.Fatalf("snippet %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("whitespace kept: %q", got)
	}
	if got := matchSnippet(long, "worker"); got != "" {
		t.Fatalf("a match in the visible start has no snippet: %q", got)
	}
	if got := matchSnippet(long, "nothing-here"); got != "" {
		t.Fatalf("no match, no snippet: %q", got)
	}
	if got := matchSnippet(long, ""); got != "" {
		t.Fatalf("no words, no snippet: %q", got)
	}
	// The earliest of the words leads; near the end the snippet starts at a word and has no closing ellipsis.
	if got := matchSnippet(long, "health contract"); !strings.Contains(got, "contract") {
		t.Fatalf("earliest word: %q", got)
	}
	if got := matchSnippet(long, "request."); strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "…check.") || !strings.HasSuffix(got, "pull request.") {
		t.Fatalf("end: %q", got)
	}
}
