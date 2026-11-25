package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Requirement: execution-continuity.md session project selection. Risk: cleaning an old workspace removes the new
// project's checkout, or returning to a dirty project reuses its branch and native cwd.
func TestTransitionWorkspacesPreserveDirtyOldCheckout(t *testing.T) {
	top, root := t.TempDir(), t.TempDir()
	top, _ = filepath.EvalSymlinks(top)
	root, _ = filepath.EvalSymlinks(root)
	git := func(d string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	git(top, "init", "-q", "-b", "main")
	git(top, "commit", "-q", "--allow-empty", "-m", "base")
	e := &executor{root: root, sessions: filepath.Join(root, "sessions")}
	old, newer := "old", "new"
	oldDir, newDir := e.commandDir("session", old), e.commandDir("session", newer)
	os.MkdirAll(oldDir, 0o755)
	os.MkdirAll(newDir, 0o755)
	w, _, _, ok, err := worktree(oldDir, workspaceSession("session", old), top, true, []string{top}, nil, proj(M{}), false)
	if err != nil || !ok {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w, "dirty"), []byte("keep"), 0o600)
	fresh, _, _, ok, err := worktree(newDir, workspaceSession("session", newer), top, true, []string{top}, nil, proj(M{}), false)
	if err != nil || !ok || fresh == w {
		t.Fatal(err, fresh, w)
	}
	if result := teardown(oldDir, workspaceSession("session", old), top, proj(M{}), false, false); result.Removed || str0(result.Reason) != "uncommitted" {
		t.Fatal(result)
	}
	if !exists(filepath.Join(fresh, ".git")) || !exists(filepath.Join(w, "dirty")) {
		t.Fatal("cleanup crossed workspace identity")
	}
	if e.commandDir("session", "") != e.sessionDir("session") {
		t.Fatal("legacy directory changed")
	}
}

// Requirement: docs/execution-continuity.md Working areas cleanup removes the session worktree, including read-only
// directory trees (such as Go module caches) created inside it, without traversing symlinks outside the worktree.
// Risk: permission-denied errors on 0555/0444 tool caches leave orphaned session worktrees behind after teardown.
func TestTeardownReadOnlyTree(t *testing.T) {
	top, dir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	outsideFile := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(outside, 0o755)
		_ = os.Chmod(outsideFile, 0o644)
	})

	git := func(d string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(args, err, string(out))
		}
	}
	git(top, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(top, ".gitignore"), []byte("go/.cache/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(top, "add", ".gitignore")
	git(top, "commit", "-q", "-m", "base")
	git(top, "remote", "add", "origin", "https://example.invalid/r.git")
	git(top, "update-ref", "refs/remotes/origin/main", "main")

	w := filepath.Join(dir, "worktree")
	git(top, "worktree", "add", "-q", "--no-track", "-b", "fleet/s1", w, "main")

	modDir := filepath.Join(w, "go", ".cache", "modules", "golang.org", "x", "crypto@v0.37.0")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module golang.org/x/crypto\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w, "go", ".cache", "outside-link")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		modDir,
		filepath.Join(w, "go", ".cache", "modules", "golang.org", "x"),
		filepath.Join(w, "go", ".cache", "modules", "golang.org"),
		filepath.Join(w, "go", ".cache", "modules"),
	} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}

	r := teardown(dir, "s1", top, proj(M{}), false, false)
	if !r.Removed || r.Reason != nil {
		t.Fatalf("teardown failed on read-only tree: removed=%v reason=%q", r.Removed, str0(r.Reason))
	}
	if _, err := os.Stat(w); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after teardown: err=%v", err)
	}
	st, err := os.Lstat(outside)
	if err != nil {
		t.Fatalf("external symlink target missing: %v", err)
	}
	if st.Mode().Perm() != 0o555 {
		t.Fatalf("external symlink target permissions modified: got %o want 0555", st.Mode().Perm())
	}
	if !exists(outsideFile) {
		t.Fatal("external symlink target file missing")
	}
}

// Requirement: a session's next start recovers from an interrupted teardown. Risk: 2026-11-24 a worktree directory
// whose .git link was gone (files left behind) failed every later start with "git worktree add failed".
func TestWorktreeSetsAsideStaleDirectory(t *testing.T) {
	top, dir := t.TempDir(), t.TempDir()
	top, _ = filepath.EvalSymlinks(top)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", top}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(args, err, string(out))
		}
	}
	stale := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(filepath.Join(stale, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stale, "app", "work.txt"), []byte("only copy"), 0o600)
	w, _, created, ok, err := worktree(dir, "s1", top, true, []string{top}, nil, proj(M{"worktree": M{"startFromOrigin": false}}), false)
	if err != nil || !ok || !created || !exists(filepath.Join(w, ".git")) {
		t.Fatal(err, ok, created)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, "worktree.stale-*", "app", "work.txt"))
	if len(kept) != 1 {
		t.Fatalf("the stale files were not kept aside: %v", kept)
	}
	// An accepted task's teardown (deleteBranch) removes the set-aside copy with the worktree.
	teardown(dir, "s1", top, proj(M{}), true, false)
	if left, _ := filepath.Glob(filepath.Join(dir, "worktree.stale-*")); len(left) != 0 {
		t.Fatalf("stale copy left after acceptance: %v", left)
	}
	// A detached conversation keeps no work: its release removes a set-aside copy too.
	os.MkdirAll(filepath.Join(dir, "worktree.stale-1"), 0o700)
	teardown(dir, "s2", top, proj(M{}), false, true)
	if left, _ := filepath.Glob(filepath.Join(dir, "worktree.stale-*")); len(left) != 0 {
		t.Fatalf("stale copy left after a detached teardown: %v", left)
	}
}
