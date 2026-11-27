package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerReattachesAcrossPollerRestart(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	jpath := filepath.Join(root, "journal.sqlite")
	db, err := openJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "spool"), 0o755)
	e1 := &executor{db: db, ex: db, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}

	gate := filepath.Join(root, "gate")
	script := `while [ ! -f "` + gate + `" ]; do sleep 0.05; done; printf ok > "$FLEET_OUTPUT_DIR/res.txt"`
	if err := e1.run(M{"id": "w1-cmd", "executionId": "w1-exec", "sessionId": "w1-sess", "kind": "script", "payload": M{"command": script}}); err != nil {
		t.Fatal(err)
	}
	if len(e1.live) != 1 {
		t.Fatalf("expected 1 live worker, got %d", len(e1.live))
	}

	// Simulate poller restart: close e1's DB handle without touching the detached run-worker, then open e2 and reconcile.
	db.Close()
	db2, err := openJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	e2 := &executor{db: db2, ex: db2, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}
	if err := e2.reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e2.live["w1-exec"]; !ok {
		t.Fatal("e2 did not reattach live detached worker")
	}
	if lastReceipt(e2, "w1-cmd") == "unknown" {
		t.Fatal("reconcile marked live detached worker unknown")
	}

	// Unblock worker and verify e2 drains exit.json, collects res.txt, and finishes completed.
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	settle(t, e2, "w1-exec")
	if got := lastReceipt(e2, "w1-cmd"); got != "completed" {
		t.Fatalf("receipt = %q, want completed", got)
	}
	if count(db2, "launches") != 0 || count(db2, "transfers") != 3 {
		t.Fatalf("launches=%d transfers=%d, want 0 and 3", count(db2, "launches"), count(db2, "transfers"))
	}
	if _, err := os.Stat(workerDirFor(root, "w1-exec")); !os.IsNotExist(err) {
		t.Fatalf("worker dir not cleaned up: %v", err)
	}
}

func TestWorkerReattachesExitedRunDuringRestart(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	jpath := filepath.Join(root, "journal.sqlite")
	db, err := openJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "spool"), 0o755)
	e1 := &executor{db: db, ex: db, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}

	if err := e1.run(M{"id": "w2-cmd", "executionId": "w2-exec", "sessionId": "w2-sess", "kind": "script", "payload": M{"command": `printf done > "$FLEET_OUTPUT_DIR/out.txt"`}}); err != nil {
		t.Fatal(err)
	}
	// Wait for the detached worker to write exit.json and release worker.lock WITHOUT calling e1.tick().
	wdir := workerDirFor(root, "w2-exec")
	for i := 0; i < 200; i++ {
		if _, hasExit, _ := readWorkerExit(wdir); hasExit {
			if alive, _ := workerLockAlive(wdir); !alive {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	db.Close()

	db2, err := openJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	e2 := &executor{db: db2, ex: db2, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}
	if err := e2.reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e2.live["w2-exec"]; !ok {
		t.Fatal("e2 did not attach exited worker with exit.json")
	}
	settle(t, e2, "w2-exec")
	if got := lastReceipt(e2, "w2-cmd"); got != "completed" {
		t.Fatalf("receipt = %q, want completed", got)
	}
}

func TestWorkerStopOverSocket(t *testing.T) {
	e, _ := scriptExecutor(t)
	if err := e.run(M{"id": "w3-cmd", "executionId": "w3-exec", "sessionId": "w3-sess", "kind": "script", "payload": M{"command": `sleep 30`}}); err != nil {
		t.Fatal(err)
	}
	if err := e.run(M{"id": "w3-stop", "executionId": "w3-exec", "sessionId": "w3-sess", "kind": "stop", "payload": M{}}); err != nil {
		t.Fatal(err)
	}
	if got := lastReceipt(e, "w3-stop"); got != "accepted" {
		t.Fatalf("stop receipt = %q, want accepted", got)
	}
	settle(t, e, "w3-exec")
	if got := lastReceipt(e, "w3-cmd"); got != "stopped" {
		t.Fatalf("run receipt = %q, want stopped", got)
	}
	if got := lastReceipt(e, "w3-stop"); got != "stopped" {
		t.Fatalf("final stop receipt = %q, want stopped", got)
	}
}

func TestSelfExecTargetFromAndBaseEnv(t *testing.T) {
	relRoot, _ := filepath.EvalSymlinks(t.TempDir())
	v1Dir := filepath.Join(relRoot, "1.261127.10")
	v2Dir := filepath.Join(relRoot, "1.261127.11")
	for _, d := range []string{v1Dir, v2Dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "fleet-executor"), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin1 := filepath.Join(v1Dir, "fleet-executor")
	bin2 := filepath.Join(v2Dir, "fleet-executor")
	cur := filepath.Join(relRoot, "current")
	if err := os.Symlink("1.261127.10", cur); err != nil {
		t.Fatal(err)
	}
	if got := selfExecTargetFrom(bin1); got != "" {
		t.Fatalf("same release selfExecTargetFrom = %q, want empty", got)
	}
	os.Remove(cur)
	if err := os.Symlink("1.261127.11", cur); err != nil {
		t.Fatal(err)
	}
	if got := selfExecTargetFrom(bin1); got != bin2 {
		t.Fatalf("updated release selfExecTargetFrom = %q, want %q", got, bin2)
	}

	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", "")
	os.Unsetenv("FLEET_PLAYWRIGHT_MODULE")
	base := captureBaseEnv(map[string]string{"playwright": "FLEET_PLAYWRIGHT_MODULE"})
	os.Setenv("PATH", "/pinned/node/bin:/usr/bin:/bin")
	os.Setenv("FLEET_PLAYWRIGHT_MODULE", "/pinned/pw/index.js")
	base.restore()
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("restored PATH = %q", got)
	}
	if _, ok := os.LookupEnv("FLEET_PLAYWRIGHT_MODULE"); ok {
		t.Fatal("FLEET_PLAYWRIGHT_MODULE remained set after restore")
	}
}

func TestWorkerSteerResultDoesNotDeadlock(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	db, err := openJournal(filepath.Join(root, "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	os.MkdirAll(filepath.Join(root, "spool"), 0o755)
	e := &executor{db: db, ex: db, root: root, host: "fake", live: map[string]*live{}, sessions: filepath.Join(root, "sessions"), harnessCache: map[string]M{}}

	runBody := enc(M{"id": "run-1", "executionId": "exec-1", "sessionId": "sess-1", "kind": "script", "payload": M{"command": "true"}})
	steerBody := enc(M{"id": "steer-1", "executionId": "exec-1", "sessionId": "sess-1", "kind": "steer", "payload": M{"message": "ping"}})
	for id, b := range map[string]string{"run-1": runBody, "steer-1": steerBody} {
		if _, err := db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,0)", id, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO launches(execution,command,session) VALUES(?,?,?)", "exec-1", runBody, "sess-1"); err != nil {
		t.Fatal(err)
	}
	l := &live{command: commandOf(runBody)}
	e.live["exec-1"] = l

	done := make(chan error, 1)
	go func() {
		done <- e.applyWorkerRecord("exec-1", l, workerRecord{Seq: 1, Kind: "steer_result", CommandId: "steer-1", Ok: true})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("applyWorkerRecord(steer_result) failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("applyWorkerRecord(steer_result) deadlocked on single-connection SQLite journal")
	}
	if got := lastReceipt(e, "steer-1"); got != "completed" {
		t.Fatalf("steer receipt = %q, want completed", got)
	}
	var term int
	if err := db.QueryRow("SELECT terminal FROM commands WHERE id='steer-1'").Scan(&term); err != nil || term != 1 {
		t.Fatalf("commands.terminal = %d (err=%v), want 1", term, err)
	}
	var seq int
	if err := db.QueryRow("SELECT worker_seq FROM launches WHERE execution='exec-1'").Scan(&seq); err != nil || seq != 1 {
		t.Fatalf("launches.worker_seq = %d (err=%v), want 1", seq, err)
	}
}
