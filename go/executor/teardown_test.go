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
	if r := teardown(dir, "s1", top, M{}, false); !r.Removed {
		t.Fatalf("no commits of its own: removed: %+v", r)
	}
	add()
	git(w, "commit", "-q", "--allow-empty", "-m", "work")
	if r := teardown(dir, "s1", top, M{}, false); r.Removed || str0(r.Reason) != "unpushed" {
		t.Fatalf("local commit: kept: %+v", r)
	}
	os.WriteFile(filepath.Join(w, "x"), []byte("x"), 0o600)
	if r := teardown(dir, "s1", top, M{}, true); r.Removed || str0(r.Reason) != "uncommitted" {
		t.Fatalf("dirty: kept: %+v", r)
	}
	os.Remove(filepath.Join(w, "x"))
	git(top, "update-ref", "refs/remotes/origin/fleet/s1", "fleet/s1") // pushed (git push origin HEAD), squash-merged elsewhere
	if r := teardown(dir, "s1", top, M{}, false); !r.Removed || r.BranchDeleted == nil || *r.BranchDeleted {
		t.Fatalf("not delivered: removed, -d keeps the unmerged branch: %+v", r)
	}
	git(top, "worktree", "add", "-q", w, "fleet/s1")
	git(top, "update-ref", "-d", "refs/remotes/origin/fleet/s1") // the remote branch deleted on merge and pruned
	if r := teardown(dir, "s1", top, M{}, true); !r.Removed || r.BranchDeleted == nil || !*r.BranchDeleted {
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
	old, newer := M{"workspaceKey": "old"}, M{"workspaceKey": "new"}
	oldDir, newDir := e.commandDir("session", old), e.commandDir("session", newer)
	os.MkdirAll(oldDir, 0o755)
	os.MkdirAll(newDir, 0o755)
	w, _, _, ok, err := worktree(oldDir, workspaceSession("session", old), top, true, []string{top}, nil, M{})
	if err != nil || !ok {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w, "dirty"), []byte("keep"), 0o600)
	fresh, _, _, ok, err := worktree(newDir, workspaceSession("session", newer), top, true, []string{top}, nil, M{})
	if err != nil || !ok || fresh == w {
		t.Fatal(err, fresh, w)
	}
	if result := teardown(oldDir, workspaceSession("session", old), top, M{}, false); result.Removed || str0(result.Reason) != "uncommitted" {
		t.Fatal(result)
	}
	if !exists(filepath.Join(fresh, ".git")) || !exists(filepath.Join(w, "dirty")) {
		t.Fatal("cleanup crossed workspace identity")
	}
	if e.commandDir("session", M{}) != e.sessionDir("session") {
		t.Fatal("legacy directory changed")
	}
}

// Requirement: execution-continuity.md preserves failure evidence across a project transition.
// Risk: process.closed reads the legacy session directory and loses the transitioned harness's actual stderr.
func TestTransitionFailureRetainsProcessClosedStderr(t *testing.T) {
	e, _ := scriptExecutor(t)
	payload := M{"workspaceKey": "transitioned"}
	dir := e.commandDir("s1", payload)
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
	l := &live{cmd: cmd, state: cmd.ProcessState, exited: done, eof: true, lines: lines, command: M{"id": "c1", "sessionId": "s1", "executionId": "x1", "kind": "start", "payload": payload}, owned: map[int]string{}, journaled: map[int]bool{}, cpu: map[int]float64{}}
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
