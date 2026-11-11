package main

import (
	"errors"
	"fleet/contract"
	"os"
	"os/exec"
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

// Requirement: after an executor crash, the next start kills every surviving process of its runs (and their linked
// descendants) before any poll, reports the run unknown with a process.reconciled event, and removes run files (K2-3,
// K2-6). Risk: an orphaned harness keeps writing to a worktree and spending after its session was given to new work.
func TestRestartKillsSurvivingProcesses(t *testing.T) {
	e, root := scriptExecutor(t)
	os.MkdirAll(filepath.Join(root, "runs"), 0o700)
	for _, f := range []string{"x7.env", "old.env"} {
		os.WriteFile(filepath.Join(root, "runs", f), []byte("FLEET_RUN_TOKEN=t\n"), 0o600)
	}
	if err := e.run(M{"id": "c7", "executionId": "x7", "sessionId": "s7", "kind": "script", "payload": M{"command": `sleep 30 & echo $! > "$FLEET_OUTPUT_DIR/../pid"; wait`}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "sessions/s7/pid")
	for i := 0; i < 100 && !exists(marker); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err := e.tick(); err != nil { // observes the grandchild into the journal
		t.Fatal(err)
	}
	b, _ := os.ReadFile(marker)
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	leader := e.live["x7"].cmd.Process.Pid
	// A second launch crashed between spawn and the identity read.
	e.launch("x8", "c7", "s8")
	// The executor dies: its live map is gone, the journal stays.
	restarted := &executor{db: e.db, ex: e.db, root: root, host: "fake", live: map[string]*live{}, sessions: e.sessions, harnessCache: map[string]M{}}
	if err := restarted.reconcile(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	table, _ := processes()
	if running(table, child) || running(table, leader) {
		t.Fatal("a process of the crashed run survived the restart")
	}
	// The launch window (x8) is not released: its process could not be named.
	if r := lastReceipt(restarted, "c7"); r != "unknown" || count(e.db, "launches") != 1 || count(e.db, "owned") != 0 {
		t.Fatalf("receipt %s, launches %d", r, count(e.db, "launches"))
	}
	if exists(filepath.Join(root, "runs", "x7.env")) || exists(filepath.Join(root, "runs", "old.env")) {
		t.Fatal("run file kept")
	}
	rs, _ := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	reconciled := 0
	for rs.Next() {
		var body string
		rs.Scan(&body)
		if strings.Contains(body, `"process.reconciled"`) {
			reconciled++
		}
	}
	rs.Close()
	if reconciled != 2 {
		t.Fatalf("%d process.reconciled events", reconciled)
	}
}

// Requirement: a launch whose process identity was never recorded survives restart and refuses its session until an
// explicit stop (W3-1). Risk: a surviving paid process keeps writing while the same session is admitted again.
func TestLaunchWindowKeepsSessionUntilStop(t *testing.T) {
	e, root := scriptExecutor(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES('c20','{}',1)")
	must(err)
	must(e.launch("x20", "c20", "s20"))
	must(os.MkdirAll(filepath.Join(root, "runs"), 0o700))
	must(os.WriteFile(filepath.Join(root, "runs", "x20.env"), []byte("FLEET_RUN_TOKEN=t\n"), 0o600))
	must(e.reconcile())
	must(e.run(M{"id": "c21", "executionId": "x21", "sessionId": "s20", "kind": "start", "payload": M{}}))
	if count(e.db, "launches") != 1 || lastReceipt(e, "c21") != "failed" || !exists(filepath.Join(root, "runs", "x20.env")) {
		t.Fatal("launch window released or its session admitted")
	}
	// A stop naming another session does not release it.
	must(e.run(M{"id": "c22", "executionId": "x20", "sessionId": "s99", "kind": "stop", "payload": M{}}))
	if count(e.db, "launches") != 1 {
		t.Fatal("a stop for another session released the launch window")
	}
	must(e.run(M{"id": "c23", "executionId": "x20", "sessionId": "s20", "kind": "stop", "payload": M{}}))
	if count(e.db, "launches") != 0 || lastReceipt(e, "c23") != "unknown" || exists(filepath.Join(root, "runs", "x20.env")) {
		t.Fatal("stop did not release the launch window as unknown")
	}
}

// Requirement: automatic output collection uploads only regular single-link files inside the output folder, the same
// policy as artifact_save (K2-7, K2-8). Risk: a hard link or a link at any level uploads bytes from outside the working area.
func TestOutputCollectionSkipsLinks(t *testing.T) {
	e, root := scriptExecutor(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, outside := filepath.Join(root, "s", "output"), filepath.Join(root, "secret")
	must(os.MkdirAll(filepath.Join(out, "sub"), 0o755))
	must(os.MkdirAll(outside, 0o755))
	must(os.WriteFile(filepath.Join(outside, "key"), []byte("k"), 0o600))
	must(os.WriteFile(filepath.Join(out, "sub", "a.txt"), []byte("a"), 0o644))
	must(os.Link(filepath.Join(outside, "key"), filepath.Join(out, "hard")))
	must(os.Symlink(outside, filepath.Join(out, "dir")))
	must(os.Symlink(filepath.Join(outside, "key"), filepath.Join(out, "file")))
	var declared []string
	must(e.artifacts(filepath.Join(root, "s"), []string{"output"}, "x1", &declared))
	if strings.Join(declared, ",") != "sub/a.txt" || count(e.db, "transfers") != 1 {
		t.Fatalf("declared %v", declared)
	}
	// The output folder itself swapped for a link: refused, nothing read through it.
	must(os.Symlink(outside, filepath.Join(root, "s", "linked")))
	if err := e.artifacts(filepath.Join(root, "s"), []string{"linked"}, "x2", &declared); err == nil {
		t.Fatal("linked output folder collected")
	}
}

// Requirement: a command payload is read through its per-kind contract type; a field of the wrong type refuses the
// command with its reason, an unknown field is ignored (C7). Risk: a drifted coordinator field silently becomes "" and a run starts with wrong input.
func TestPayloadOfWrongTypeRefused(t *testing.T) {
	// Hosts update at different times: a field this executor does not know is ignored, never a refusal.
	sp, err := payloadOf[contract.StartPayload](M{"payload": M{"instructions": "x", "fieldFromANewerCoordinator": M{"a": 1}}})
	if err != nil || str0(sp.Instructions) != "x" {
		t.Fatal("unknown payload field refused", err)
	}
	e, _ := scriptExecutor(t)
	e.run(M{"id": "c9", "executionId": "x9", "sessionId": "s9", "kind": "start", "payload": M{"instructions": 5}})
	e.run(M{"id": "c10", "executionId": "x10", "sessionId": "s10", "kind": "script", "payload": M{"command": "true", "timeout_ms": 1.5}})
	if lastReceipt(e, "c9") != "failed" || lastReceipt(e, "c10") != "failed" {
		t.Fatal(lastReceipt(e, "c9"), lastReceipt(e, "c10"))
	}
	rs, _ := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	var all strings.Builder
	for rs.Next() {
		var b string
		rs.Scan(&b)
		all.WriteString(b)
	}
	rs.Close()
	if !strings.Contains(all.String(), "invalid start payload") || !strings.Contains(all.String(), "invalid script payload") {
		t.Fatal("refusal reasons missing:", all.String())
	}
}

// Requirement: an owned pid that is still running but whose identity cannot be read is unknown, never confirmed gone;
// only a pid missing from a new snapshot is gone (W3-3). Risk: a permission or transient read failure frees a session
// while its original process keeps running.
func TestIdentityReadFailureIsNotTermination(t *testing.T) {
	defer func(f func(int) (string, error)) { identity = f }(identity)
	identity = func(int) (string, error) { return "", errors.New("permission denied") }
	l := &live{owned: map[int]string{os.Getpid(): "x"}}
	if alive, err := l.aliveOwned(); err == nil || alive {
		t.Fatal("unreadable running process reported gone")
	}
	// A recorded pid no process holds any more is gone, with or without a readable identity.
	l = &live{owned: map[int]string{1<<22 + 7: "x"}}
	if alive, err := l.aliveOwned(); err != nil || alive {
		t.Fatal("a pid nobody holds is not reported gone", err)
	}
}

// Requirement: account-inspect has an executor handler (contracts review N6): status reports the registry binding,
// limits asks the vendor CLI on this host and reports a limit_reading with source probe (fleet-people-access.md
// section 8 "Read without a run"); no token is read. Risk: account_connect and subscription_limits_refresh always fail.
func TestAccountInspectStatusAndLimits(t *testing.T) {
	e, root := scriptExecutor(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	home, native := filepath.Join(root, "h"), filepath.Join(root, "nh")
	must(os.MkdirAll(home, 0o700))
	must(os.MkdirAll(native, 0o700))
	cli := filepath.Join(root, "codex")
	must(os.WriteFile(cli, []byte("#!/bin/sh\nwhile read l; do case \"$l\" in *rateLimits/read*) echo '{\"id\":2,\"result\":{\"rateLimits\":{\"primary\":{\"usedPercent\":42,\"windowDurationMins\":300,\"resetsAt\":1794400000}}}}';; esac; done\n"), 0o700))
	reg := filepath.Join(root, "accounts.json")
	must(os.WriteFile(reg, []byte(enc(M{"work": M{"harness": "codex", "home": home, "nativeHome": native, "executable": cli, "readiness": "ready"}})), 0o600))
	t.Setenv("FLEET_NATIVE_ACCOUNTS", reg)
	must(e.run(M{"id": "c30", "executionId": "x30", "sessionId": "s30", "kind": "account-inspect", "payload": M{"accountId": "a1", "harness": "codex", "binding": "work", "mode": "status"}}))
	must(e.run(M{"id": "c31", "executionId": "x31", "sessionId": "s31", "kind": "account-inspect", "payload": M{"accountId": "a1", "harness": "codex", "binding": "work", "mode": "limits"}}))
	if lastReceipt(e, "c30") != "completed" || lastReceipt(e, "c31") != "completed" {
		t.Fatal(lastReceipt(e, "c30"), lastReceipt(e, "c31"))
	}
	rows := map[string]string{}
	rs, _ := e.db.Query("SELECT route, body FROM outbox")
	for rs.Next() {
		var r, b string
		rs.Scan(&r, &b)
		rows[r] += b
	}
	rs.Close()
	if !strings.Contains(rows["/api/hosts/accounts/report"], `"readiness":"ready"`) {
		t.Fatal("status report", rows["/api/hosts/accounts/report"])
	}
	if ev := rows["/api/hosts/events"]; !strings.Contains(ev, `"source":"probe"`) || !strings.Contains(ev, `"pct":42`) || !strings.Contains(ev, `"subscriptionId":"a1"`) {
		t.Fatal("limits event", ev)
	}
}

// Requirement: a signal that fails because the owned process exited between the check and the signal is not a cleanup
// failure; a live process with its recorded identity still is. Risk: a script that ends while being cleaned up is
// reported unknown (the CI flake of TestScriptOutputTimeoutAndGroupKill under -race).
func TestSignalRaceWithExit(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	me, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !gone(cmd.Process.Pid, "x") || !gone(os.Getpid(), me+"-other") || gone(os.Getpid(), me) {
		t.Fatal("gone misjudged an exited, a reused or a live process")
	}
}
