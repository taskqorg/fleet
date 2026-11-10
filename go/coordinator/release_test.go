package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidAutoUpdate(t *testing.T) {
	for v, want := range map[any]bool{"on": true, "off": true, "2026-11-13T09:00:00Z": true, "2020-01-01T00:00:00Z": true,
		"2026-11-13T09:00:00+02:00": false, "later": false, "": false, 1.0: false} {
		if validAutoUpdate(v) != want {
			t.Errorf("validAutoUpdate(%v) != %v", v, want)
		}
	}
}

func TestVersionBehindEdge(t *testing.T) {
	// The coordinator runs N = 1.261113.0 with previous N-1 = 1.261112.0: N and N-1 are served without a flag.
	prev := "1.261112.0"
	for v, want := range map[string]bool{"1.261113.0": false, "1.261112.0": false, "1.261111.0": true, "dev": true} {
		if olderVersion(v, prev) != want {
			t.Errorf("olderVersion(%q) != %v", v, want)
		}
	}
	if olderVersion("1.261111.0", "") {
		t.Error("flagged without a previous release")
	}
}

func TestHealthyTarget(t *testing.T) {
	root := t.TempDir()
	cur, prev := filepath.Join(root, "1.261112.0"), filepath.Join(root, "1.261111.0")
	os.MkdirAll(cur, 0o755)
	os.MkdirAll(prev, 0o755)
	os.WriteFile(filepath.Join(prev, "release.json"), []byte(`{"version":"1.261111.0"}`), 0o644)
	os.Symlink(prev, filepath.Join(root, "previous"))
	oldDir, oldVer := releaseDir, version
	t.Cleanup(func() { releaseDir, version = oldDir, oldVer })
	releaseDir, version = func() string { return cur }, "1.261112.0"
	if v := healthyTarget(); v != nil {
		t.Fatalf("no markers: %v", *v)
	}
	os.WriteFile(filepath.Join(prev, "healthy"), nil, 0o644)
	if v := healthyTarget(); v == nil || *v != "1.261111.0" {
		t.Fatalf("previous marker: %v", v)
	}
	os.WriteFile(filepath.Join(cur, "healthy"), nil, 0o644)
	if v := healthyTarget(); v == nil || *v != "1.261112.0" {
		t.Fatalf("current marker: %v", v)
	}
}

func TestMergeUpdateWorseWins(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pds18-state.json")
	old := pds18StateFile
	t.Cleanup(func() { pds18StateFile = old })
	pds18StateFile = f
	os.WriteFile(f, []byte(`{"coordinator":{"last":{"version":"1.261112.0","state":"ok","at":"x"}},
		"executor":{"last":{"version":"1.261112.0","state":"refused","error":"selftest","at":"y"}}}`), 0o644)
	m := mergeUpdate(map[string]any{"version": "1.261112.0", "state": "pending", "at": "z"}).(doc)
	if m["state"] != UpdateStateRefused || m["error"] != "selftest" || len(m["parts"].(doc)) != 2 {
		t.Fatalf("%v", m)
	}
	m = mergeUpdate(map[string]any{"version": "1.261112.0", "state": "failed", "at": "z"}).(doc)
	if m["state"] != UpdateStateFailed {
		t.Fatalf("%v", m)
	}
}
