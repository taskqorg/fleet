package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func scriptExecutor(t *testing.T) (*executor, string) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	db := journal(t)
	uploadSchema(db)
	os.MkdirAll(filepath.Join(root, "spool"), 0o755)
	return &executor{db: db, ex: db, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}, root
}

func lastReceipt(e *executor, cid string) string {
	rs, _ := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/receipts'")
	defer rs.Close()
	last := ""
	for rs.Next() {
		var b string
		rs.Scan(&b)
		if v := decM(b); jStr(v, "commandId") == cid {
			last = jStr(v, "status")
		}
	}
	return last
}

func settle(t *testing.T, e *executor, execution string) {
	for i := 0; i < 400; i++ {
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.live[execution]; !ok {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("script did not settle")
}

func groupAlive(t *testing.T, marker string) bool {
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return syscall.Kill(pid, 0) == nil
}

// Requirement: a lab script's output becomes artifacts; timeout and stop kill its whole process group, including a
// reparented grandchild; bad payloads are refused. Risk: a timed-out trial keeps running and spending after it failed.
func TestScriptOutputTimeoutAndGroupKill(t *testing.T) {
	e, root := scriptExecutor(t)
	if err := e.run(M{"id": "c1", "executionId": "x1", "sessionId": "s1", "kind": "script", "payload": M{"command": `echo hello; echo result > "$FLEET_OUTPUT_DIR/r.txt"; test -d "$FLEET_INPUT_DIR"`}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, "x1")
	if r := lastReceipt(e, "c1"); r != "completed" {
		t.Fatal(r)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sessions/s1/output/script.stdout")); string(b) != "hello\n" {
		t.Fatalf("%q", b)
	}
	names := map[string]bool{}
	rs, _ := e.db.Query("SELECT body FROM transfers")
	for rs.Next() {
		var b string
		rs.Scan(&b)
		names[jStr(decM(b), "name")] = true
	}
	rs.Close()
	for _, n := range []string{"r.txt", "script.stdout", "script.stderr"} {
		if !names[n] {
			t.Fatal(n, names)
		}
	}
	e.run(M{"id": "c2", "executionId": "x2", "sessionId": "s2", "kind": "script", "payload": M{"timeout_ms": 300, "command": `(sleep 30 & echo $! > "$FLEET_OUTPUT_DIR/../pid"); sleep 30`}})
	settle(t, e, "x2")
	if r := lastReceipt(e, "c2"); r != "failed" {
		t.Fatal(r)
	}
	time.Sleep(100 * time.Millisecond)
	if groupAlive(t, filepath.Join(root, "sessions/s2/pid")) {
		t.Fatal("timed-out grandchild alive")
	}
	e.run(M{"id": "c3", "executionId": "x3", "sessionId": "s3", "kind": "script", "payload": M{"command": `sleep 30 & echo $! > "$FLEET_OUTPUT_DIR/../pid"; wait`}})
	for i := 0; i < 100 && !exists(filepath.Join(root, "sessions/s3/pid")); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	e.run(M{"id": "c4", "executionId": "x3", "sessionId": "s3", "kind": "stop", "payload": M{}})
	settle(t, e, "x3")
	if r := lastReceipt(e, "c3"); r != "stopped" {
		t.Fatal(r)
	}
	time.Sleep(100 * time.Millisecond)
	if groupAlive(t, filepath.Join(root, "sessions/s3/pid")) {
		t.Fatal("stopped child alive")
	}
	e.run(M{"id": "c5", "executionId": "x5", "sessionId": "s5", "kind": "script", "payload": M{"command": "true", "timeout_ms": 0}})
	e.run(M{"id": "c6", "executionId": "x6", "sessionId": "s6", "kind": "script", "payload": M{"command": "true", "commit": "0000000000000000000000000000000000000000"}})
	if lastReceipt(e, "c5") != "failed" || lastReceipt(e, "c6") != "failed" {
		t.Fatal("refusals")
	}
}

// Requirement: a repeated command never spawns again, and a changed body under the same id is a conflict. Risk: a poll
// retry runs an agent twice.
func TestDuplicateAndChangedIdentity(t *testing.T) {
	e, _ := scriptExecutor(t)
	c := M{"id": "c1", "executionId": "x1", "sessionId": "s1", "kind": "script", "payload": M{"command": "true"}}
	e.run(c)
	settle(t, e, "x1")
	if err := e.run(c); err != nil || len(e.live) != 0 {
		t.Fatal("duplicate spawned", err)
	}
	c2 := M{"id": "c1", "executionId": "x1", "sessionId": "s1", "kind": "script", "payload": M{"command": "false"}}
	if err := e.run(c2); err == nil || len(e.live) != 0 {
		t.Fatal("changed identity accepted")
	}
	for _, s := range []string{"../x", "/tmp/x", "", "a/b"} {
		if _, err := cmdID(M{"id": s}, "id"); err == nil {
			t.Fatal(s)
		}
	}
}
