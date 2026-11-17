package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: fleet-conversations.md "Start a conversation": a project's presets are validated with its settings, absent
// fields take their defaults, and a project without presets gets the system presets, each with a shipped profile to fall
// back on. Risk: a project definition with a broken preset is saved, or a project without roles (cs) cannot start.
func TestPresets(t *testing.T) {
	ps, source, err := presetsOf(nil)
	if err != nil || source != "system" || len(ps) != 5 {
		t.Fatal(ps, source, err)
	}
	for _, p := range ps {
		if _, err := os.Stat(filepath.Join("..", "..", "config", "agents", p.Profile+".json")); err != nil {
			t.Errorf("system preset %s falls back on %s, which is not shipped: %v", p.ID, p.Profile, err)
		}
		if p.Branch != (p.Access == "write") {
			t.Errorf("system preset %s: branch %v with access %s", p.ID, p.Branch, p.Access)
		}
	}
	got, source, err := presetsOf(doc{"presets": []any{map[string]any{"id": "triage", "name": "Triage", "role": "pm", "access": "read"}, map[string]any{"id": "ship", "name": "Ship", "profile": "coding-worker", "branch": false}}})
	if err != nil || source != "project" || len(got) != 2 {
		t.Fatal(got, source, err)
	}
	if got[0].Branch || got[0].Access != "read" || got[1].Access != "write" || got[1].Branch {
		t.Fatalf("defaults: %+v", got)
	}
	if _, _, err := presetsOf(doc{"presets": []any{}}); err != nil {
		t.Fatal("an empty list is the system presets", err)
	}
	for _, bad := range []any{
		"discuss",
		[]any{map[string]any{"id": "x", "name": "X"}},                                                                     // neither role nor profile
		[]any{map[string]any{"id": "X!", "name": "X", "role": "pm"}},                                                      // id
		[]any{map[string]any{"id": "x", "name": " ", "role": "pm"}},                                                       // name
		[]any{map[string]any{"id": "x", "name": "X", "role": "pm", "access": "admin"}},                                    // access
		[]any{map[string]any{"id": "x", "name": "X", "role": "pm", "branch": "yes"}},                                      // branch
		[]any{map[string]any{"id": "x", "name": "X", "role": "pm", "model": "opus"}},                                      // unknown key
		[]any{map[string]any{"id": "x", "name": "X", "role": "pm"}, map[string]any{"id": "x", "name": "Y", "role": "pm"}}, // duplicate
	} {
		if _, err := parsePresets(bad); err == nil {
			t.Errorf("accepted %v", bad)
		} else if he, ok := err.(*HTTPError); !ok || he.Code != 400 {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, _, err := projectSettings(doc{"id": "p", "name": "P", "presets": []any{map[string]any{"id": "x", "name": "X"}}}); err == nil {
		t.Fatal("project settings accepted a broken preset")
	}
}
