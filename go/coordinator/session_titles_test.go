package main

import (
	"strings"
	"testing"
)

// Requirement: fleet-conversations.md, Titles: a model reply becomes one short line without quotes, markdown marks, a
// final period or a UUID (OR-38). Risk: a title carries a whole paragraph, a UUID or the model's quoting.
func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"\"Fix the login loop.\"\n\nBecause...":                       "Fix the login loop",
		"## **Review run 5d370c31-c765-479f-b72c-87b55d36ed46**":      "Review run",
		"  «Добавить ключ Anthropic»  ":                               "Добавить ключ Anthropic",
		"1. Fix the login loop":                                       "Fix the login loop",
		"Support for \"dark mode\"":                                   "Support for \"dark mode\"",
		"Fix session_123e4567-e89b-12d3-a456-426614174000_transcript": "Fix session__transcript",
		"Title: Fix login redirect":                                   "Fix login redirect",
		"Fix session (3f2a0c1d-1111-4222-8333-444455556666) crash":    "Fix session crash",
		"```text\nFix login\n```":                                     "Fix login",
		"```\nFix login\n```":                                         "Fix login",
		"Title:\nFix login":                                           "Fix login",
		"Название: Починить вход":                                     "Починить вход",
		"Fix bug (see 123e4567-e89b-12d3-a456-426614174000)":          "Fix bug",
		"Fix useEffect() cleanup":                                     "Fix useEffect() cleanup",
		"'a' vs 'b'":                                                  "'a' vs 'b'",
		"#635 Fix dispatch":                                           "#635 Fix dispatch",
		"-v flag in CLI":                                              "-v flag in CLI",
		"- Fix the flag":                                              "Fix the flag",
		"Update the users'":                                           "Update the users'",
		"'Quoted title'":                                              "Quoted title",
		"\"Fix login loop\".":                                         "Fix login loop",
		"\n\n":                                                        "",
		strings.Repeat("слово ", 30):                                  strings.TrimSpace(string([]rune(strings.Repeat("слово ", 30))[:79])) + "…",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
