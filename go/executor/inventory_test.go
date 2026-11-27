package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
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
	const id = "00000000-0000-4000-8000-000000000001" // invented session ID
	kept := add(id)
	if r := teardownWith(kept, id, top, proj(M{}), teardownOptions{}); r.Removed || str0(r.Reason) != "uncommitted" {
		t.Fatal("output must keep the worktree without outputsStored", r)
	}
	stored := add("stored")
	if r := teardownWith(stored, "stored", top, proj(M{}), teardownOptions{dropOutput: true, deleteBranch: true}); !r.Removed {
		t.Fatal("stored outputs kept the worktree", str0(r.Reason))
	}
	unknown := add("unknown")
	entries, disk := takeInventory([]string{filepath.Join(root, "sessions")})
	if len(entries) != 2 || *disk.SessionsWorktreeCount != 2 || !entries[0].output {
		t.Fatal("inventory", len(entries), *disk.SessionsWorktreeCount)
	}
	db, err := openJournal(filepath.Join(root, "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worktree := filepath.Join(kept, "worktree")
	if err := os.WriteFile(filepath.Join(worktree, "dirty"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	const brokenID = "00000000-0000-4000-8000-000000000002"
	broken := filepath.Join(root, "legacy", brokenID, "workspaces", "fixture", "worktree")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, inventoryEntry{id: brokenID, root: filepath.Join(root, "legacy"), legacy: true, worktrees: []string{broken}})
	e := &executor{db: db}
	a := answer{kind: answerOK, value: json.RawMessage(`{"keep":1,"teardown":[],"unknown":["unknown","00000000-0000-4000-8000-000000000001","00000000-0000-4000-8000-000000000002"],"inventoryAt":456}`)}
	report, c := e.reconcileInventory(123, entries, a)
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if report.Response != contract.InventoryResponseReceived || report.StartedAt != 123 || report.InventoryAt == nil || *report.InventoryAt != 456 {
		t.Fatalf("uncorrelated report: %+v", report)
	}
	if c.dirs != 1 || exists(filepath.Join(unknown, "worktree")) || !exists(worktree) {
		t.Fatal("unexpected removal", c)
	}
	if len(report.Candidates) != 3 {
		t.Fatalf("candidates: %+v", report.Candidates)
	}
	protected, removed := report.Candidates[0], report.Candidates[1]
	if removed.Outcome != contract.InventoryOutcomeRemoved || removed.SessionId != nil || removed.Root != contract.InventoryRootEffective ||
		protected.Outcome != contract.InventoryOutcomeUncommitted || str0(protected.SessionId) != id || protected.Root != contract.InventoryRootEffective ||
		protected.Layout != contract.InventoryLayoutDirect {
		t.Fatalf("outcomes: %+v", report.Candidates)
	}
	if got := report.Candidates[2]; got.Outcome != contract.InventoryOutcomeGitCommonDirError ||
		got.Layout != contract.InventoryLayoutWorkspace || got.Root != contract.InventoryRootLegacy || str0(got.SessionId) != brokenID {
		t.Fatalf("broken workspace: %+v", got)
	}
}

// Failed scheduled exchanges expose a class even when no candidate can be evaluated.
func TestInventoryResponseFailure(t *testing.T) {
	e := &executor{}
	const rawDetail = "/fixture/private/path"
	for _, tc := range []struct {
		answer answer
		want   contract.InventoryResponse
	}{
		{answer{kind: answerRetry, text: rawDetail}, contract.InventoryResponseResponseFailed},
		{answer{kind: answerEdge, status: 200, text: rawDetail}, contract.InventoryResponseDecodeFailed},
		{answer{kind: answerOK, value: json.RawMessage(`{"unknown":false}`)}, contract.InventoryResponseDecodeFailed},
		{answer{kind: answerOK, value: json.RawMessage(`null`)}, contract.InventoryResponseDecodeFailed},
	} {
		report, c := e.reconcileInventory(123, nil, tc.answer)
		if report.Response != tc.want || report.InventoryAt != nil || len(report.Candidates) != 0 || c.dirs != 0 {
			t.Fatalf("failed inventory: %+v, %+v", report, c)
		}
		if b, err := json.Marshal(report); err != nil || strings.Contains(string(b), rawDetail) {
			t.Fatal("failed inventory must expose only classified fields")
		}
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
