package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"fleet/contract"
	"fleet/executor/supervise"
)

// A completed turn must release the worker and deliver a terminal receipt even
// if a descendant keeps the harness stdout pipe open after the leader exits.
func TestWorkerFinalizesCompletedTurnWithLiveDescendant(t *testing.T) {
	t.Run("descendant keeps stdout open", func(t *testing.T) {
		testCompletedTurnShutdown(t, "exit 0", "completed")
	})
	t.Run("server ignores stdin EOF", func(t *testing.T) {
		testCompletedTurnShutdown(t, "wait", "unknown")
	})
}

// T-FLT-141: a rejected native resume before turn/start must produce a
// specific protocol event and a failed receipt once the worker has cleaned up.
func TestWorkerReportsRejectedCodexResume(t *testing.T) {
	e, root := scriptExecutor(t)
	defer e.db.Close()
	const execution, cid, sid = "rejected-resume", "resume-cmd", "session"
	dir := e.commandDir(sid, "")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(root, "fake-codex")
	const fixture = `#!/bin/sh
read -r line
printf '%s\n' '{"id":1,"result":{}}'
read -r line
read -r line
case "$line" in
  *'"method":"thread/resume"'*) ;;
  *) exit 1 ;;
esac
printf '%s\n' '{"id":2,"error":{"code":-32600,"message":"no rollout found"}}'
if read -r line; then exit 1; fi
`
	if err := os.WriteFile(harness, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	command := typedCommand(M{"id": cid, "executionId": execution, "sessionId": sid, "kind": "start", "payload": M{"harness": "codex"}})
	if err := e.launch(execution, cid, sid); err != nil {
		t.Fatal(err)
	}
	l, err := e.spawnWorker(command, workerSpec{Kind: "harness", Execution: execution, CommandId: cid, Session: sid,
		DataDir: root, Dir: dir, Work: dir, Root: dir, Harness: "codex", Native: "recorded-thread",
		CmdPath: harness, CmdArgs: []string{harness}, CmdDir: dir, Profile: M{"readOnly": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.live[execution] = l
	t.Cleanup(func() { _ = workerStop(l.worker.sock, "") })
	settle(t, e, execution)
	if got := lastReceipt(e, cid); got != "failed" {
		t.Fatalf("receipt = %q, want failed", got)
	}
	var reason string
	err = e.db.QueryRow(`SELECT json_extract(event.value,'$.data.reason') FROM outbox, json_each(body,'$.events') AS event
	 WHERE route='/api/hosts/events' AND json_extract(event.value,'$.type')='protocol.failed'`).Scan(&reason)
	if err != nil || reason != "thread/resume rejected by Codex (RPC -32600)" {
		t.Fatalf("protocol failure = %q, err = %v", reason, err)
	}
}

func testCompletedTurnShutdown(t *testing.T, afterEOF, wantReceipt string) {
	t.Helper()
	e, root := scriptExecutor(t)
	defer e.db.Close()
	const execution, cid, sid = "completed-turn", "start-command", "session"
	dir := e.commandDir(sid, "")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(root, "fake-codex")
	const fixture = `#!/bin/sh
case "$1" in
  --version) printf 'codex 0.0.0\n'; exit 0 ;;
  app-server) ;;
  *) exit 1 ;;
esac
read -r line
printf '%s\n' '{"id":1,"result":{}}'
read -r line
read -r line
printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread"}}}'
read -r line
sleep 60 &
for arg do
  case "$arg" in
    mcp_servers.fixture.command=*) mcp_command=${arg#*=} ;;
    mcp_servers.fixture.args=*) mcp_args=${arg#*=} ;;
  esac
done
node -e 'const [command, args] = process.argv.slice(1).map(JSON.parse);
  require("node:child_process").spawn(command, args, {stdio: ["pipe", "ignore", "ignore"]});' "$mcp_command" "$mcp_args" &
printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn"}}}'
while [ ! -f complete ]; do sleep 0.05; done
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}'
read -r line
`
	if err := os.WriteFile(harness, []byte(fixture+afterEOF+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "accounts.json")
	home, nativeHome := filepath.Join(root, "home"), filepath.Join(root, "codex-home")
	for _, path := range []string{home, nativeHome} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	account := M{"harness": "codex", "home": home, "nativeHome": nativeHome, "executable": harness,
		"mcpServers": M{"fixture": M{"transport": "stdio", "command": exe(), "args": []string{"mcp-fixture", "inspect"}}}}
	if err := os.WriteFile(registry, []byte(enc(M{"fixture": account})), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	grants := []any{M{"id": "fixture", "data": M{"tools": []string{"inspect"}}}}
	payload := M{"agentConfig": M{"id": "baseline", "model": "gpt-6.1-sol", "effort": "low", "instructions": "profile", "mcpServers": grants, "fleetTools": false, "collaborationTools": false},
		"model": "gpt-6.1-sol", "effort": "low", "account": "fixture", "text": "fixture", "readOnly": true}
	if err := e.run(M{"id": cid, "executionId": execution, "sessionId": sid, "kind": "start", "payload": payload}); err != nil {
		t.Fatal(err)
	}
	l := e.live[execution]
	if l == nil {
		t.Fatalf("harness did not start: %v", failureReasons(t, e, execution))
	}
	t.Cleanup(func() { _ = workerStop(l.worker.sock, "") })
	var receivedMu sync.Mutex
	var received []string
	var harnessSeq, firstProtocolSeq int64
	url, _ := serve(t, func(route string, body M) int {
		receivedMu.Lock()
		defer receivedMu.Unlock()
		switch route {
		case contract.RouteEvents.Path:
			batch := typed[contract.HostEvents](body)
			for _, event := range batch.Events {
				if event.Type == string(contract.FleetEventTypeHarness) {
					harnessSeq = event.Seq
					return http.StatusConflict
				}
			}
			for _, event := range batch.Events {
				received = append(received, event.Type)
				if event.Type == "protocol.write" && firstProtocolSeq == 0 {
					firstProtocolSeq = event.Seq
				}
			}
		case contract.RouteReceipts.Path:
			received = append(received, jStr(body, "status"))
		}
		return http.StatusOK
	})
	for until := time.Now().Add(8 * time.Second); count(e.db, "owned") < 2 || count(e.db, "mcp_groups") == 0; {
		if time.Now().After(until) {
			t.Fatal("worker did not observe the harness descendant and register its MCP group")
		}
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(tickEvery)
	}
	if err := flush(e.db, url, "invented-token", e.host); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "complete"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	settle(t, e, execution)
	if got := lastReceipt(e, cid); got != wantReceipt {
		t.Fatalf("receipt = %q, want %q", got, wantReceipt)
	}
	for range 2 {
		if err := flush(e.db, url, "invented-token", e.host); err != nil {
			t.Fatal(err)
		}
	}
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if harnessSeq == 0 || firstProtocolSeq <= harnessSeq {
		t.Fatalf("event sequence overlap: harness=%d, first protocol write=%d", harnessSeq, firstProtocolSeq)
	}
	for _, marker := range []string{"outbox.failed", "turn.completed", "process.closed", wantReceipt} {
		if !slices.Contains(received, marker) {
			t.Fatalf("missing %q in delivered events and receipts %v", marker, received)
		}
	}
	if active := e.activeExecutions(); len(active) != 0 {
		t.Fatalf("completed execution still owns a host slot: %v", active)
	}
}

func TestWorkerUnresolvedExitReleasesCommandAndKeepsOwnership(t *testing.T) {
	e, root := scriptExecutor(t)
	defer e.db.Close()
	const execution, cid, sid = "unresolved-exit", "start-command", "session"
	body := enc(M{"id": cid, "executionId": execution, "sessionId": sid, "kind": "start", "payload": M{"harness": "codex"}})
	if _, err := e.db.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cid, body); err != nil {
		t.Fatal(err)
	}
	if err := e.launch(execution, cid, sid); err != nil {
		t.Fatal(err)
	}
	// Model a worker that exhausted its cleanup grace while an owned process survived.
	cmd := exec.Command("sleep", "60")
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Kill(); _ = proc.Wait() })
	id, err := identity(proc.Pid)
	if err != nil {
		t.Fatal(err)
	}
	dir := workerDirFor(root, execution)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exit.json"), []byte(enc(workerExit{
		Status: "unknown", Seq: 10, StdoutClosed: true, CleanupUnknown: true, ProcessesUnresolved: true,
	})), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &live{command: commandOf(body), worker: &liveWorker{dir: dir}, owned: map[int]string{proc.Pid: id}}
	if err := e.own(execution, l); err != nil {
		t.Fatal(err)
	}
	e.live[execution] = l
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if got := lastReceipt(e, cid); got != "unknown" || len(e.live) != 0 || count(e.db, "launches") != 1 || count(e.db, "owned") != 1 {
		t.Fatalf("got receipt=%q active=%d launches=%d owned=%d", got, len(e.live), count(e.db, "launches"), count(e.db, "owned"))
	}
	if err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(e.live) != 0 || count(e.db, "launches") != 0 || count(e.db, "owned") != 0 {
		t.Fatal("restart reattached the terminal worker or retained its resolved ownership")
	}
	select {
	case <-proc.Done():
	case <-time.After(time.Second):
		t.Fatal("recorded owned process survived reconciliation")
	}
}

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
