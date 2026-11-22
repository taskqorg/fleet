package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: fleet-system-management.md, Worktree on delivery. A worktree with commits on no remote branch is kept
// as unpushed; a pushed one is removed. A session branch has no upstream (--no-track) and a squash merge leaves it
// unmerged into main, so git branch -d refuses it; on delivery it goes with -D. Risk: delivered worktrees and branches
// pile up, or unpushed work is deleted.
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
	git(top, "remote", "add", "origin", "https://example.invalid/r.git")
	git(top, "update-ref", "refs/remotes/origin/main", "main")
	w := filepath.Join(dir, "worktree")
	add := func() { git(top, "worktree", "add", "-q", "--no-track", "-b", "fleet/s1", w, "main") }
	add()
	if r := teardown(dir, "s1", top, proj(M{}), false, false); !r.Removed {
		t.Fatalf("no commits of its own: removed: %+v", r)
	}
	add()
	git(w, "commit", "-q", "--allow-empty", "-m", "work")
	if r := teardown(dir, "s1", top, proj(M{}), false, false); r.Removed || str0(r.Reason) != "unpushed" {
		t.Fatalf("local commit: kept: %+v", r)
	}
	os.WriteFile(filepath.Join(w, "x"), []byte("x"), 0o600)
	if r := teardown(dir, "s1", top, proj(M{}), true, false); r.Removed || str0(r.Reason) != "uncommitted" {
		t.Fatalf("dirty: kept: %+v", r)
	}
	os.Remove(filepath.Join(w, "x"))
	git(top, "update-ref", "refs/remotes/origin/fleet/s1", "fleet/s1") // pushed (git push origin HEAD), squash-merged elsewhere
	if r := teardown(dir, "s1", top, proj(M{}), false, false); !r.Removed || r.BranchDeleted == nil || *r.BranchDeleted {
		t.Fatalf("not delivered: removed, -d keeps the unmerged branch: %+v", r)
	}
	git(top, "worktree", "add", "-q", w, "fleet/s1")
	git(top, "update-ref", "-d", "refs/remotes/origin/fleet/s1") // the remote branch deleted on merge and pruned
	if r := teardown(dir, "s1", top, proj(M{}), true, false); !r.Removed || r.BranchDeleted == nil || !*r.BranchDeleted {
		t.Fatalf("delivered: removed with its branch: %+v", r)
	}
}

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

// Requirement: execution-continuity.md preserves failure evidence across a project transition.
// Risk: process.closed reads the legacy session directory and loses the transitioned harness's actual stderr.
func TestTransitionFailureRetainsProcessClosedStderr(t *testing.T) {
	e, _ := scriptExecutor(t)
	payload := M{"workspaceKey": "transitioned"}
	dir := e.commandDir("s1", "transitioned")
	os.MkdirAll(dir, 0o755)
	stderr, err := os.Create(filepath.Join(dir, "x1.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "echo transitioned-failure >&2; exit 7")
	cmd.Stderr = stderr
	_ = cmd.Run()
	stderr.Close()
	done := make(chan struct{})
	close(done)
	lines := make(chan string)
	close(lines)
	l := &live{cmd: cmd, exited: done, eof: true, lines: lines, command: typedCommand(M{"id": "c1", "sessionId": "s1", "executionId": "x1", "kind": "start", "payload": payload}), owned: map[int]string{}, journaled: map[int]bool{}, cpu: map[int]float64{}}
	if _, err := e.tickOne("x1", l); err != nil {
		t.Fatal(err)
	}
	rows, err := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var body string
		rows.Scan(&body)
		if strings.Contains(body, "process.closed") && strings.Contains(body, "transitioned-failure") {
			found = true
		}
	}
	if !found {
		t.Fatal("process.closed omitted transitioned stderr")
	}
}

// Requirement: fleet-conversations.md "Start a conversation": a preset without a branch gets a worktree detached at the
// base, so no session branch is created; the teardown removes it. Risk: every read-only conversation leaves a branch.
func TestDetachedWorktreeHasNoBranch(t *testing.T) {
	top, dir := t.TempDir(), t.TempDir()
	top, _ = filepath.EvalSymlinks(top)
	run := func(d string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", d, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	run(top, "init", "-q", "-b", "main")
	run(top, "commit", "-q", "--allow-empty", "-m", "base")
	run(top, "remote", "add", "origin", "https://example.invalid/r.git")
	run(top, "update-ref", "refs/remotes/origin/main", "main")
	w, _, created, ok, err := worktree(dir, "s1", top, true, []string{top}, nil, proj(M{"worktree": M{"startFromOrigin": false}}), true)
	if err != nil || !ok || !created {
		t.Fatal(err, ok, created)
	}
	if b := run(w, "branch", "--show-current"); b != "" {
		t.Fatalf("detached worktree is on branch %q", b)
	}
	if run(top, "branch", "--list", "fleet/s1") != "" {
		t.Fatal("a session branch was created")
	}
	if r := teardown(dir, "s1", top, proj(M{}), false, false); !r.Removed {
		t.Fatalf("removed: %+v", r)
	}
	// Based on a local commit the remote lacks: still not the session's own work, so it goes.
	run(top, "commit", "-q", "--allow-empty", "-m", "local only")
	if _, _, _, _, err := worktree(dir, "s2", top, true, []string{top}, nil, proj(M{"worktree": M{"startFromOrigin": false}}), true); err != nil {
		t.Fatal(err)
	}
	if r := teardown(dir, "s2", top, proj(M{}), false, true); !r.Removed {
		t.Fatalf("a checkout's local commit kept the detached worktree: %+v", r)
	}
	// Its own commit keeps it.
	if _, _, _, _, err := worktree(dir, "s3", top, true, []string{top}, nil, proj(M{"worktree": M{"startFromOrigin": false}}), true); err != nil {
		t.Fatal(err)
	}
	run(filepath.Join(dir, "worktree"), "commit", "-q", "--allow-empty", "-m", "work")
	if r := teardown(dir, "s3", top, proj(M{}), false, true); r.Removed || str0(r.Reason) != "unpushed" {
		t.Fatalf("its own commit keeps it: %+v", r)
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
