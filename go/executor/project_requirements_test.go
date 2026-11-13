package main

import "testing"

// Requirement: frozen profile applicability; risk: excluded PM receives forge/preflight requirements or empty changes legacy behavior.
func TestProjectRequirements(t *testing.T) {
	for _, ids := range [][]any{nil, {}, {"coding-worker"}} {
		p := M{"execution": M{"profiles": ids, "tools": []any{"pnpm"}, "forge": true}}
		for _, profile := range []string{"pm", "coding-worker"} {
			got := projectRequirements(p, profile)
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
		r := readiness(p, "pds", "")
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
