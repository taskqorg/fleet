package contract

import (
	"strings"
	"testing"
)

// Requirement (FLT-40): every cause code a run can store has a plain sentence, specific where the code is known, and
// the code itself stays on the answer for Details. Risk: a raw code such as unknown:abandoned reaches a person.
func TestPlainCauseCoversStoredCodes(t *testing.T) {
	if PlainCause("", "x") != nil {
		t.Fatal("no code, no failure")
	}
	// The cause codes production executions held on 2026-11-25, and their expected entries.
	for code, want := range map[string]string{
		"unknown:abandoned": "abandoned", "unknown:caller-cancelled": "stopped", "cancelled": "stopped", "step:cancelled": "stopped",
		"step:provider-432": "provider-limit", "step:provider-429": "provider-limit", "step:provider-502": "provider-down",
		"unknown:provider-body": "provider-down", "unknown:provider-timeout": "provider-down", "step:provider-404": "provider-rejected",
		"step:bad-request": "provider-rejected", "step:budget-reached": "budget", "step:draining": "draining", "ref:example.prompt": "reference",
		"preparation failed: coding-tool-unready: browser": "host-not-ready",
		"preparation failed: readiness-check-failed: checkout checkout-missing, remote connection-missing": "host-checkout",
		"preparation failed: readiness-check-failed: remote ls-remote-unclassified-exit-128": "host-remote", "preparation failed: readiness-check-failed": "host-not-ready", "preparation failed: git worktree add failed": "host-files",
		"preparation failed: native account/session binding mismatch": "host-login", "turn.failed: other: timed out": "agent-turn",
		"step:secret-found": "secret", "step:not-granted": "key", "step:download-cut": "download-cut", "step:failed": "step",
		"harness exited 1: boom": "agent-crashed", "script exited 1: boom": "script-failed", "unknown:interrupted": "interrupted", "something new": "other",
	} {
		f := PlainCause(code, "the message")
		if f == nil || f.Cause != want || f.Code != code || f.Text == "" || strings.Contains(f.Text, code) || deref(f.Detail) != "the message" {
			t.Errorf("PlainCause(%q) = %+v, want cause %s with the code kept", code, f, want)
		}
	}
	for _, c := range Vocabulary.Causes {
		if !strings.HasSuffix(c.Text, ".") || len(c.Text) > 200 {
			t.Errorf("cause %s: a sentence of at most 200 characters, got %q", c.ID, c.Text)
		}
	}
	if last := Vocabulary.Causes[len(Vocabulary.Causes)-1]; len(last.Match) != 1 || last.Match[0] != "" {
		t.Error("the last cause must match every code")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
