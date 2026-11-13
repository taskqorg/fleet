package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Requirement: native Git writes reach only this project's linked metadata; risk: protected admin paths deny commits or another checkout is granted.
func TestNativeGitRoots(t *testing.T) {
	top, work, other := t.TempDir(), filepath.Join(t.TempDir(), "work"), t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	git(top, "init", "-q")
	git(top, "commit", "--allow-empty", "-qm", "fixture")
	git(top, "worktree", "add", "-qb", "fixture", work)
	roots, err := nativeGitRoots(top, work)
	if err != nil {
		t.Fatal(err)
	}
	common, _ := canon(filepath.Join(top, ".git"))
	admin, _ := canon(filepath.Join(top, ".git", "worktrees", "work"))
	if enc(roots) != enc([]any{common, admin}) {
		t.Fatalf("unexpected authority %v", roots)
	}
	git(other, "init", "-q")
	if _, err := nativeGitRoots(other, work); err == nil {
		t.Fatal("foreign checkout accepted")
	}
	// The policy must reach turn/start on fresh and resumed turns, not merely the legacy thread config.
	s := codexSession{thread: "fixture", profile: M{"gitNetwork": true, "writableRoots": roots, "text": "fixture"}}
	policy := jAt(s.turnRequest(), "params", "sandboxPolicy")
	if jStr(policy, "type") != "workspaceWrite" || enc(jAt(policy, "writableRoots")) != enc(roots) || jAt(policy, "networkAccess") != true {
		t.Fatal("native turn lost bounded Git grant")
	}
}

// Requirement: native commit keeps project identity and disables repository hooks; risk: granting Git writes inherits host config or executes a hook.
func TestNativeGitCommitIsolation(t *testing.T) {
	top := t.TempDir()
	if b, err := hardenedGit("-C", top, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, b)
	}
	if err := os.WriteFile(filepath.Join(top, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 73\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "-C", top, "commit", "--allow-empty", "-qm", "fixture")
	gitApply(c, []string{"GIT_AUTHOR_NAME=Fixture", "GIT_COMMITTER_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_EMAIL=fixture@example.invalid"})
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("isolated commit: %v %s", err, b)
	}
}
