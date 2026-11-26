package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Requirement: docs/designs/fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind: a run's stored outputs
// are not work, so a teardown with outputs stored removes the worktree that only output/ made dirty, and an unknown
// session's worktree goes through the same path. Risk: untracked output/ keeps every pipeline worktree forever
// (2026-11-26: 502 left on three hosts).
func TestTeardownDropsStoredOutput(t *testing.T) {
	top, root := t.TempDir(), t.TempDir()
	top, _ = filepath.EvalSymlinks(top)
	root, _ = filepath.EvalSymlinks(root)
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
	add := func(session string) string {
		dir := filepath.Join(root, "sessions", session)
		w := filepath.Join(dir, "worktree")
		git(top, "worktree", "add", "-q", "--no-track", "-b", "fleet/"+session, w, "main")
		os.MkdirAll(filepath.Join(w, "output"), 0o755)
		os.WriteFile(filepath.Join(w, "output", "result.json"), []byte("{}"), 0o600)
		return dir
	}
	kept := add("kept")
	if r := teardownWith(kept, "kept", top, proj(M{}), teardownOptions{roots: []string{root}}); r.Removed || str0(r.Reason) != "uncommitted" {
		t.Fatal("output must keep the worktree without outputsStored", r)
	}
	stored := add("stored")
	if r := teardownWith(stored, "stored", top, proj(M{}), teardownOptions{dropOutput: true, deleteBranch: true, roots: []string{root}}); !r.Removed {
		t.Fatal("stored outputs kept the worktree", str0(r.Reason))
	}
	unknown := add("unknown")
	entries, disk := takeInventory([]string{filepath.Join(root, "sessions")})
	if len(entries) != 2 || *disk.SessionsWorktreeCount != 2 || !entries[0].output {
		t.Fatal("inventory", len(entries), *disk.SessionsWorktreeCount)
	}
	if c := teardownUnknown(filepath.Join(unknown, "worktree"), "unknown", []string{root}); c.dirs != 1 || exists(filepath.Join(unknown, "worktree")) {
		t.Fatal("unknown session's worktree stayed", c)
	}
}

// Requirement: docs/designs/fleet-host-cleanup.md#5-readings: a top consumer names its whole program even when its
// path has spaces. Risk: "Google Chrome Helper" shows as "Google".
func TestPsConsumer(t *testing.T) {
	pid, user, rss, _, command, ok := psConsumer(" 812 fleet-ci-vm  16721196   13-11:57:19 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome Helper")
	if !ok || pid != 812 || user != "fleet-ci-vm" || rss != 16721196 || command != "Google Chrome Helper" {
		t.Fatal(pid, user, rss, command, ok)
	}
	if _, _, _, _, _, ok := psConsumer("812 kirs 10"); ok {
		t.Fatal("short row parsed")
	}
}

// Requirement: docs/designs/fleet-host-cleanup.md#11-inventory-reconcile: teardown removes only worktrees Fleet made
// under its own roots; a Claude Code checkout under .claude/worktrees, a path outside the roots, or a link into one is
// refused. Risk: Fleet cleanup removes a person's live worktree.
func TestTeardownOnlyInsideFleetRoots(t *testing.T) {
	top, root, other := t.TempDir(), t.TempDir(), t.TempDir()
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
	sessions := filepath.Join(root, "sessions")
	// A Claude Code worktree placed as if it were a session's: under .claude/worktrees inside the roots.
	claude := filepath.Join(sessions, ".claude", "worktrees", "pull-1")
	git(top, "worktree", "add", "-q", "--no-track", "-b", "claude/pull-1", filepath.Join(claude, "worktree"), "main")
	if r := teardownWith(claude, "pull-1", top, proj(M{}), teardownOptions{roots: []string{root}}); r.Removed || str0(r.Reason) != "outside-fleet-roots" {
		t.Fatal("removed a .claude/worktrees checkout", r)
	}
	// A worktree outside the roots, and a session directory that links to it.
	outside := filepath.Join(other, "s1")
	git(top, "worktree", "add", "-q", "--no-track", "-b", "fleet/s1", filepath.Join(outside, "worktree"), "main")
	if r := teardownWith(outside, "s1", top, proj(M{}), teardownOptions{roots: []string{root}}); r.Removed {
		t.Fatal("removed a worktree outside the roots")
	}
	linked := filepath.Join(sessions, "s1")
	os.MkdirAll(linked, 0o755)
	os.Symlink(filepath.Join(outside, "worktree"), filepath.Join(linked, "worktree"))
	if c := teardownUnknown(filepath.Join(linked, "worktree"), "s1", []string{root}); c.dirs != 0 || !exists(filepath.Join(outside, "worktree", ".git")) {
		t.Fatal("followed a link out of the roots")
	}
	if entries, _ := takeInventory([]string{sessions}); len(entries) != 0 {
		t.Fatal("inventory listed .claude or a linked worktree", len(entries))
	}
}
