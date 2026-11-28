package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Requirement: host publication resolves the original managed workspace from the real executor journal.
// Risk: retained executions cannot publish, or a conflicting command redirects publication to another checkout.
func TestPublishingSourceWorktree(t *testing.T) {
	e, root := scriptExecutor(t)
	insert := func(id, execution, session, kind string, payload any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": id, "executionId": execution, "sessionId": session, "kind": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", id, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	workspace := func(session, key string) string {
		t.Helper()
		dir := filepath.Join(e.commandDir(session, key), "worktree")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /invented/metadata\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	legacy := workspace("session-legacy", "")
	insert("start-legacy", "execution-legacy", "session-legacy", "start", map[string]any{})
	got, err := e.publishingSourceWorktree("execution-legacy")
	if err != nil || got != legacy {
		t.Fatalf("legacy gitfile: got %q, %v", got, err)
	}
	nested := workspace("session-transition", "project-v2")
	insert("start-transition", "execution-transition", "session-transition", "start", map[string]any{"workspaceKey": "project-v2"})
	insert("input-transition", "execution-transition", "session-transition", "input", map[string]any{"workspaceKey": "project-v2"})
	got, err = e.publishingSourceWorktree("execution-transition")
	if err != nil || got != nested {
		t.Fatalf("transition: got %q, %v", got, err)
	}
	insert("input-only", "execution-input-only", "session-transition", "input", map[string]any{"workspaceKey": "project-v2"})
	got, err = e.publishingSourceWorktree("execution-input-only")
	if err != nil || got != nested {
		t.Fatalf("input continuation: got %q, %v", got, err)
	}
	insert("unrelated-stop", "execution-legacy", "elsewhere", "stop", map[string]any{})
	got, err = e.publishingSourceWorktree("execution-legacy")
	if err != nil || got != legacy {
		t.Fatalf("stop must not redirect: %q, %v", got, err)
	}
	insert("conflict", "execution-legacy", "session-transition", "input", map[string]any{})
	if _, err = e.publishingSourceWorktree("execution-legacy"); err == nil {
		t.Fatal("ambiguous execution accepted")
	}
	insert("unsafe", "execution-unsafe", "../outside", "start", map[string]any{})
	if _, err = e.publishingSourceWorktree("execution-unsafe"); err == nil {
		t.Fatal("unsafe session accepted")
	}
	insert("bad-key", "execution-bad-key", "session-legacy", "start", map[string]any{"workspaceKey": 7})
	if _, err = e.publishingSourceWorktree("execution-bad-key"); err == nil {
		t.Fatal("non-string workspace key accepted")
	}
	insert("missing", "execution-missing", "session-missing", "start", map[string]any{})
	if _, err = e.publishingSourceWorktree("execution-missing"); err == nil {
		t.Fatal("missing worktree accepted")
	}
	if _, err = e.publishingSourceWorktree("execution-unknown"); err == nil {
		t.Fatal("unknown execution accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "sessions", "session-link")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	inside := e.commandDir("session-worktree-link", "")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inside, "worktree")); err != nil {
		t.Fatal(err)
	}
	insert("worktree-symlink", "execution-worktree-link", "session-worktree-link", "start", map[string]any{})
	if _, err = e.publishingSourceWorktree("execution-worktree-link"); err == nil {
		t.Fatal("symlink worktree accepted")
	}
	insert("symlink", "execution-link", "session-link", "start", map[string]any{})
	if _, err = e.publishingSourceWorktree("execution-link"); err == nil {
		t.Fatal("symlink session accepted")
	}
}
