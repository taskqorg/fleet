package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Requirement: fleet-system-management.md, Worktree on delivery. A worktree whose commits are not on its upstream (or
// that has none) is kept with reason unpushed; a pushed one is removed, and on delivery its local branch goes with -D
// (git -d refuses a branch merged neither into HEAD nor its upstream, as after a squash merge). Risk: delivered worktrees pile up, or unpushed work is deleted.
func TestTeardownUnpushedAndDelivered(t *testing.T) {
	top, dir := t.TempDir(), t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(args, err, string(out))
		}
	}
	git(top, "init", "-q", "-b", "main")
	git(top, "commit", "-q", "--allow-empty", "-m", "base")
	w := filepath.Join(dir, "worktree")
	git(top, "worktree", "add", "-q", "-b", "fleet/s1", w)
	git(w, "commit", "-q", "--allow-empty", "-m", "work") // not merged into main: -d would refuse
	if r := teardown(dir, "s1", top, M{}, true); r.Removed || str0(r.Reason) != "unpushed" {
		t.Fatalf("no upstream: %+v", r)
	}
	git(top, "remote", "add", "origin", "https://example.invalid/r.git")
	git(top, "update-ref", "refs/remotes/origin/fleet/s1", "fleet/s1")
	git(top, "config", "branch.fleet/s1.remote", "origin")
	git(top, "config", "branch.fleet/s1.merge", "refs/heads/fleet/s1")
	if r := teardown(dir, "s1", top, M{"cleanup": M{"keepWhen": []any{"uncommitted"}}}, false); !r.Removed {
		t.Fatalf("pushed: removed: %+v", r)
	}
	git(top, "worktree", "add", "-q", w, "fleet/s1")
	git(w, "commit", "-q", "--allow-empty", "-m", "more")
	git(top, "update-ref", "refs/remotes/origin/fleet/s1", "fleet/s1") // pushed, not merged into main
	os.WriteFile(filepath.Join(w, "x"), []byte("x"), 0o600)
	if r := teardown(dir, "s1", top, M{}, true); r.Removed || str0(r.Reason) != "uncommitted" {
		t.Fatalf("dirty: %+v", r)
	}
	os.Remove(filepath.Join(w, "x"))
	if r := teardown(dir, "s1", top, M{}, true); !r.Removed || r.BranchDeleted == nil || !*r.BranchDeleted {
		t.Fatalf("delivered: removed with its branch: %+v %s", r, str0(r.Reason))
	}
}
