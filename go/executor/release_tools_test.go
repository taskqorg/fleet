package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fleet/harnessrelease"
)

// Requirement: a host tool's version is read from its --version output whatever its form. Risk: node's "v26.13.1" or
// gh's "gh version 2.105.0 (date)" reads as no version, and every host shows its tools as unknown.
func TestToolSemver(t *testing.T) {
	for out, want := range map[string]string{
		"v26.13.1\n": "26.13.1",
		"12.23.0\n":  "12.23.0",
		"gh version 2.105.0 (2026-11-01)\nhttps://x": "2.105.0",
		"glab 1.133.0 (abc123)\n":                    "1.133.0",
		"jq-1.8.2\n":                                 "1.8.2",
		"Python 3.15.2\n":                            "3.15.2",
		"Google Chrome for Testing 158.0.8127.88\n":  "158.0.8127.88",
	} {
		if got := toolSemver.FindString(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

// Requirement: pinned PATH tools put their directories on PATH, while the project browser's Playwright module and
// Chromium go to FLEET_PLAYWRIGHT_MODULE and FLEET_BROWSER_EXECUTABLE. Risk: the browser keeps the host's hand-installed
// copy, or Chromium's directory lands on every run's PATH.
func TestManagedTools(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	assets := map[string]any{}
	for _, name := range harnessrelease.Tools.Names {
		entry := "bin/" + name
		if name == "playwright" {
			entry = "package/cli.js"
		}
		assets[name] = map[string]string{"version": "1.2.3", "url": harnessrelease.Tools.Origins[name] + name, "sha256": strings.Repeat("a", 64), "entry": entry}
	}
	raw, _ := json.Marshal(map[string]any{"enabled": true, "targets": map[string]any{"linux-amd64": assets}})
	os.WriteFile(filepath.Join(dir, "config", "tool-pins.json"), raw, 0o644)
	read, err := harnessrelease.Tools.Read(dir, "linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range read {
		path := filepath.Join(root, a.Executable(name))
		if name == "playwright" {
			path = filepath.Join(filepath.Dir(path), "index.js")
		}
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, nil, 0o755)
	}
	dirs, env, err := managedTools(root, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != len(harnessrelease.Tools.Names)-2 || slices.ContainsFunc(dirs, func(d string) bool { return strings.Contains(d, "chromium") || strings.Contains(d, "playwright") }) {
		t.Errorf("PATH dirs %v", dirs)
	}
	if !strings.HasSuffix(env["FLEET_PLAYWRIGHT_MODULE"], "package/index.js") || !strings.HasSuffix(env["FLEET_BROWSER_EXECUTABLE"], "bin/chromium") {
		t.Errorf("env %v", env)
	}
	os.Remove(filepath.Join(root, read["chromium"].Executable("chromium")))
	if _, _, err := managedTools(root, read); err == nil {
		t.Error("a missing Chromium starts the executor")
	}
}
