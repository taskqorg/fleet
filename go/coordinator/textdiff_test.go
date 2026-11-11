package main

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: definition_export's patch applies to the release (fleet-definition-editors.md, definition_export).
// Risk: an export the owner cannot apply, or one that changes lines nobody edited.
func TestUnifiedDiffApplies(t *testing.T) {
	git, err := osexec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	var a, b strings.Builder
	for i := 0; i < 40; i++ {
		a.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	lines := strings.Split(strings.TrimSuffix(a.String(), "\n"), "\n")
	lines[2], lines[30] = "changed near the top", "changed near the end"
	lines = append(lines[:10], append([]string{"inserted"}, lines[10:]...)...)
	b.WriteString(strings.Join(lines, "\n") + "\n")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	os.WriteFile(filepath.Join(dir, "config", "x.json"), []byte(a.String()), 0o644)
	patch := unifiedDiff("config/x.json", a.String(), b.String(), true, true) + unifiedDiff("config/new.json", "", "{}\n", false, true)
	os.WriteFile(filepath.Join(dir, "p.diff"), []byte(patch), 0o644)
	run := func(args ...string) {
		c := osexec.Command(git, args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s\n%s", args, err, out, patch)
		}
	}
	run("init", "-q")
	run("apply", "p.diff")
	got, _ := os.ReadFile(filepath.Join(dir, "config", "x.json"))
	if string(got) != b.String() {
		t.Fatalf("applied text differs:\n%s", got)
	}
	if unifiedDiff("x", "same\n", "same\n", true, true) != "" {
		t.Fatal("equal texts gave a diff")
	}
}

// Requirement: the field list of definition_diff (arrays of objects match by id). Risk: a reordered step reads as every
// step changed, or a changed prompt is not marked.
func TestFieldChanges(t *testing.T) {
	a := `{"steps":[{"id":"a","prompt":"x"},{"id":"b","prompt":"y"}],"version":2}`
	b := `{"steps":[{"id":"b","prompt":"y"},{"id":"a","prompt":"z"}],"version":2,"x":1}`
	got := fieldChanges(a, b)
	if len(got) != 2 || got[0].Path != "/steps/a/prompt" || got[0].Op != "replace" || got[1].Path != "/x" || got[1].Op != "add" {
		t.Fatalf("%+v", got)
	}
}
