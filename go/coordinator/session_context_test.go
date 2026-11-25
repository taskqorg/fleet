package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Requirement: pages/home.md "Session card": the now line is the first sentence of the latest agent message as plain
// text, else the task title, cut to one line. Risk: a card shows markdown syntax, a code block or a whole paragraph.
func TestNowLine(t *testing.T) {
	for _, c := range []struct{ md, task, want string }{
		{"I'll **rebase** on `main` first. Then push.", "", "I'll rebase on main first."},
		{"```go\nx := 1\n```\n\n## Plan\n- Read [the spec](https://x/y) and fix it", "", "Read the spec and fix it"},
		{"PR #812 passed v1.2.3 checks! Merged.", "", "PR #812 passed v1.2.3 checks!"},
		{"#812 merged. Next.", "", "#812 merged."},
		{"---\n| a | b |\n|---|---|", "Fix the build", "| a | b |"},
		{"", "  Fix   the build ", "Fix the build"},
		{"", "", ""},
		{strings.Repeat("word ", 60), "", strings.TrimSpace(strings.Repeat("word ", 31)) + "…"},
	} {
		got := ""
		if l := nowLine(c.md, c.task); l != nil {
			got = *l
		}
		if got != c.want {
			t.Errorf("nowLine(%q, %q) = %q, want %q", c.md, c.task, got, c.want)
		}
	}
}

// Requirement: the now-line read probes event_agent_text, so its predicate must be the index's. Risk: the two drift
// and every sessions_list page scans all events of each listed session again.
func TestAgentTextIndexPredicate(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, "CREATE INDEX event_agent_text")
	if i < 0 || !strings.Contains(s[i:], "jsonb_path_exists(payload, '"+agentTextPath+"')") {
		t.Fatal("event_agent_text predicate differs from agentTextPath")
	}
}

// Requirement: docs/designs/fleet-conversations.md#entities: pipeline and Lab agent-step conversations are hidden; the
// Conversations list, its counts and the nav counter leave them out. Risk: Lab trials fill the list (61% of its rows on
// 2026-11-26) and hide the conversations people work in.
func TestSessionListHidesStepConversations(t *testing.T) {
	c, err := sessionWhere(rowFilter{}, &[]any{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c, "x.pipeline_run_id IS NULL") {
		t.Fatalf("conditions %v", c)
	}
	if !strings.Contains(sqlSessionBase, "d.pipeline_run_id") || !strings.Contains(sqlSessionCountBase, "d.pipeline_run_id") {
		t.Fatal("both session bases carry pipeline_run_id")
	}
}
