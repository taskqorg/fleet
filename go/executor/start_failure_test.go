package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failureReasons: the preparation.failed reasons the executor queued for an execution.
func failureReasons(t *testing.T, e *executor, execution string) []string {
	t.Helper()
	rows, err := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		b := decM(raw)
		if jStr(b, "executionId") != execution {
			continue
		}
		for _, ev := range jArr(b, "events") {
			if jStr(ev, "type") == "preparation.failed" {
				out = append(out, jStr(ev, "data", "reason"))
			}
		}
	}
	return out
}

// Risk: a registry executable that is gone fails cmd.Start, and the execution failed with no reason (windows, 2026-11-17).
func TestStartMissingExecutableSaysWhy(t *testing.T) {
	e, _ := scriptExecutor(t)
	home, accountHome := t.TempDir(), t.TempDir()
	os.Chmod(home, 0o700)
	os.Chmod(accountHome, 0o700)
	gone := filepath.Join(t.TempDir(), "codex")
	registry := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(registry, []byte(enc(M{"fixture": M{"harness": "codex", "home": home, "nativeHome": accountHome, "executable": gone}})), 0o600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	payload := M{"agentConfig": M{"id": "baseline", "model": "gpt-6.1-sol", "effort": "low", "instructions": "profile", "mcpServers": []any{}, "fleetTools": false, "collaborationTools": false}, "model": "gpt-6.1-sol", "effort": "low", "account": "fixture", "text": "hi"}
	c := M{"id": "gone-cmd", "executionId": "gone-execution", "sessionId": "gone-session", "kind": "start", "payload": payload}
	if err := e.start(typedCommand(c), "gone-cmd", "gone-execution", "gone-session", payload); err != nil {
		t.Fatal(err)
	}
	if len(e.live) != 0 || lastReceipt(e, "gone-cmd") != "failed" {
		t.Fatal("start did not fail")
	}
	reasons := failureReasons(t, e, "gone-execution")
	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], "harness did not start: ") || !strings.Contains(reasons[0], gone) {
		t.Fatalf("reasons %q", reasons)
	}
}

// Risk: a start whose run returns an error before the harness (here the session directory is not writable) failed with
// no reason; a teardown that errors stays without one, since its execution is the turn's.
func TestRunErrorBeforeHarnessSaysWhy(t *testing.T) {
	e, root := scriptExecutor(t)
	session := "locked-session"
	dir := filepath.Join(root, "sessions", session)
	os.MkdirAll(dir, 0o755)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	c := M{"id": "locked-cmd", "executionId": "locked-execution", "sessionId": session, "kind": "start", "payload": M{"harness": "codex", "text": "hi"}}
	if err := e.runOrFail(c); err != nil {
		t.Fatal(err)
	}
	if lastReceipt(e, "locked-cmd") != "failed" {
		t.Fatal("start did not fail")
	}
	reasons := failureReasons(t, e, "locked-execution")
	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], "executor error before the harness started: ") {
		t.Fatalf("reasons %q", reasons)
	}
	// The same command again is a duplicate: no second failure.
	if err := e.runOrFail(c); err != nil {
		t.Fatal(err)
	}
	if n := len(failureReasons(t, e, "locked-execution")); n != 1 {
		t.Fatalf("%d failures after a duplicate", n)
	}
}
