package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: frozen profile applicability; risk: excluded PM receives forge/preflight requirements or empty changes legacy behavior.
func TestProjectRequirements(t *testing.T) {
	for _, ids := range [][]any{nil, {}, {"coding-worker"}} {
		p := M{"execution": M{"profiles": ids, "tools": []any{"pnpm"}, "forge": true}}
		for _, profile := range []string{"pm", "coding-worker"} {
			got := projectRequirements(proj(p), profile)
			want := len(ids) == 0 || profile == "coding-worker"
			if (got != nil) != want {
				t.Fatalf("profiles=%v profile=%s applicability=%v", ids, profile, got)
			}
		}
	}
}

// Requirement: readiness follows effective profiles; risk: PM-only readiness on PDS falsely requires coding tools/forge.
func TestReadinessProfileRequirements(t *testing.T) {
	for _, ids := range [][]any{nil, {}, {"coding-worker"}, {"pm"}} {
		p := M{"roles": M{"pm": "codex"}, "profileIds": M{"pm": "pm"}, "execution": M{"profiles": ids, "tools": []any{"pnpm"}, "forge": true}}
		r := readiness(M{"projectId": "fixture", "project": p}, "pds", "", false)
		coding := false
		for _, c := range r.Checks {
			if c.Check == "forge" || c.Check == "coding-tools" {
				coding = true
			}
		}
		want := len(ids) == 0 || jStr(ids[0]) == "pm"
		if coding != want {
			t.Fatalf("profiles=%v checks=%s", ids, enc(r))
		}
	}
}

// Requirement: a harness a host leaves off on purpose is not offered, not a failure (owner, 2026-11-26: no Claude on
// PDS18). Risk: every readiness check of a project with a Claude role fails on PDS18 with claude-placement-unset, and a
// bot opens a "Host pds18-native · readiness" conversation for it.
func TestReadinessHarnessNotOffered(t *testing.T) {
	t.Setenv("FLEET_CLAUDE_PLACEMENT", "")
	reg := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(reg, []byte(`{"codex-1":{"harness":"codex","readiness":"ready"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := M{"roles": M{"worker": "codex", "reviewer": "claude", "designer": "antigravity"}}
	r := readiness(M{"projectId": "fixture", "project": p}, "pds", reg, false)
	got := map[string]string{}
	for _, c := range r.Checks {
		got[c.Check] = string(c.Status) + " " + str0(c.Reason)
	}
	if !r.Ok || got["harness:claude"] != "ok not-offered" || got["harness:antigravity"] != "ok not-offered" || got["account"] != "ok " {
		t.Fatalf("not offered harnesses: %s", enc(r))
	}
	if _, ok := got["claude-placement"]; ok {
		t.Fatalf("a harness not offered gets no checks of its own: %s", enc(r))
	}
	// A placement set but wrong for the platform stays a failure: the host meant to offer Claude.
	t.Setenv("FLEET_CLAUDE_PLACEMENT", "windows-wsl")
	reg2 := filepath.Join(t.TempDir(), "accounts.json")
	os.WriteFile(reg2, []byte(`{"codex-1":{"harness":"codex","readiness":"ready"},"claude-1":{"harness":"claude","readiness":"ready"}}`), 0o600)
	if r := readiness(M{"projectId": "fixture", "project": p}, "pds", reg2, false); r.Ok {
		t.Fatalf("misplaced Claude passes: %s", enc(r))
	}
}
