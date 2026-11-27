package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
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

// Requirement: a host job command without a sessionId (fleet-conversations.md, migration step 2) runs under its
// executionId: its directory and launch row use it, events and receipts flow, FLEET_SESSION_ID stays empty and the sweep
// sees the directory busy only while the job is open. Risk: hosts refuse session-less jobs once the coordinator stops
// creating a session per job.
func TestHostJobWithoutSession(t *testing.T) {
	e, _ := scriptExecutor(t)
	if err := e.run(M{"id": "hj-ready", "executionId": "hj-ready-x", "kind": "readiness", "payload": M{"project": M{}}}); err != nil {
		t.Fatal(err)
	}
	if r := lastReceipt(e, "hj-ready"); r != "completed" && r != "failed" {
		t.Fatal("readiness receipt", r)
	}
	script := `printf %s "$FLEET_SESSION_ID" > "$FLEET_OUTPUT_DIR/sid"`
	if err := e.run(M{"id": "hj", "executionId": "hj-x", "kind": "script", "payload": M{"command": script}}); err != nil {
		t.Fatal(err)
	}
	var session string
	if err := e.db.QueryRow("SELECT session FROM launches WHERE execution='hj-x'").Scan(&session); err != nil || session != "hj-x" {
		t.Fatal("launch session", session, err)
	}
	if !e.sessionBusy("hj-x") {
		t.Fatal("open host job not busy")
	}
	settle(t, e, "hj-x")
	if r := lastReceipt(e, "hj"); r != "completed" {
		t.Fatal("script receipt", r)
	}
	if b, err := os.ReadFile(filepath.Join(e.sessions, "hj-x", "output", "sid")); err != nil || len(b) != 0 {
		t.Fatalf("FLEET_SESSION_ID %q %v", b, err)
	}
	var closed int
	e.db.QueryRow(`SELECT count(*) FROM outbox WHERE route=? AND body LIKE '%"executionId":"hj-x"%' AND body LIKE '%process.closed%'`, contract.RouteEvents.Path).Scan(&closed)
	if closed != 1 {
		t.Fatal("process.closed events", closed)
	}
	if e.sessionBusy("hj-x") {
		t.Fatal("finished host job still busy")
	}
}

// Personal script staging belongs to the run. The transfer must still have the
// exact output after staging is removed, including when the script exits nonzero.
func TestPersonalScriptRetiresStaging(t *testing.T) {
	for _, tc := range []struct {
		name, finish, status string
		timeout              int
	}{
		{"completed", "exit 0", "completed", 0},
		{"failed", "exit 2", "failed", 0},
		{"timed out", "exec sleep 60", "failed", 1500},
		{"stopped", "exec sleep 60", "stopped", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := scriptExecutor(t)
			server := scriptInputServer(t)
			e.url = server.URL
			const execution, cid = "personal-script", "personal-cmd"
			dir := e.sessionDir(execution)
			command := `cat "$FLEET_INPUT_DIR/nested/fixture" > "$FLEET_OUTPUT_DIR/result.txt"; printf 'invented diagnostic' >&2; ` + tc.finish
			payload := M{"command": command, "project": M{"data": "personal"}, "inputs": []M{{"artifactId": "fixture", "path": "nested/fixture"}}}
			if tc.timeout > 0 {
				payload["timeout_ms"] = tc.timeout
			}
			if err := e.run(M{"id": cid, "executionId": execution, "kind": "script", "payload": payload}); err != nil {
				t.Fatal(err)
			}
			worker := e.live[execution].worker
			t.Cleanup(func() { _ = workerStop(worker.sock, "") })
			if tc.name == "stopped" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					info, err := os.Stat(filepath.Join(dir, "output", "result.txt"))
					if err == nil && info.Size() == int64(len("invented fixture bytes")) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("active script output missing")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !exists(filepath.Join(dir, "input", "nested", "fixture")) {
					t.Fatal("active script input missing")
				}
				if err := e.run(M{"id": "stop-cmd", "executionId": execution, "kind": "stop"}); err != nil {
					t.Fatal(err)
				}
			}
			settle(t, e, execution)
			if got := lastReceipt(e, cid); got != tc.status {
				t.Fatalf("receipt = %q, want %q", got, tc.status)
			}
			assertScriptStagingGone(t, dir)
			var id, body string
			if err := e.db.QueryRow(`SELECT id, body FROM transfers WHERE json_extract(body,'$.name')='result.txt' AND state='queued'`).Scan(&id, &body); err != nil {
				t.Fatal(err)
			}
			job := readJob(body)
			if b, err := os.ReadFile(job.Path); err != nil || string(b) != "invented fixture bytes" {
				t.Fatalf("spool bytes = %q, err = %v", b, err)
			}
			if tc.name == "failed" {
				var n int
				if err := e.db.QueryRow(`SELECT count(*) FROM outbox WHERE route=? AND body LIKE '%invented diagnostic%'`, contract.RouteEvents.Path).Scan(&n); err != nil || n != 1 {
					t.Fatalf("failed script diagnostic count = %d, err = %v", n, err)
				}
				if err := settleJob(e.db, e.host, id, "upload", job, 0, nil, retryf("fixture transient")); err != nil {
					t.Fatal(err)
				}
				var attempts int
				if err := e.db.QueryRow("SELECT attempts FROM transfers WHERE id=?", id).Scan(&attempts); err != nil || attempts != 1 || !exists(job.Path) {
					t.Fatalf("retry attempts = %d, err = %v, spool exists = %t", attempts, err, exists(job.Path))
				}
				if err := settleJob(e.db, e.host, id, "upload", job, 1, nil, permanentf("fixture terminal")); err != nil {
					t.Fatal(err)
				}
				if err := e.db.QueryRow("SELECT count(*) FROM outbox WHERE route=? AND json_extract(body,'$.artifactId')=? AND json_extract(body,'$.error')='fixture terminal'", contract.RouteArtifactsFailed.Path, id).Scan(&n); err != nil || n != 1 {
					t.Fatalf("permanent upload failure notices = %d, err = %v", n, err)
				}
			}
		})
	}
}

func scriptInputServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case contract.RouteArtifactsGet.Path:
			var b contract.HostArtifactGetRequest
			if json.NewDecoder(r.Body).Decode(&b) != nil || b.ArtifactId != "fixture" {
				http.Error(w, "fixture unavailable", http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"artifactId":"fixture","state":"stored","url":"http://%s/fixture"}`, r.Host)
		case "/fixture":
			fmt.Fprint(w, "invented fixture bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func assertScriptStagingGone(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"input", "output"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("run staging %s was not removed: %v", name, err)
		}
	}
}

// A late preparation refusal must remove inputs fetched earlier in that attempt.
// A linked output directory is a collection failure and its target stays intact.
func TestPersonalScriptCleansPreparationAndCollectionFailures(t *testing.T) {
	e, root := scriptExecutor(t)
	e.url = scriptInputServer(t).URL
	if err := e.run(M{"id": "bad-input", "executionId": "prep", "kind": "script", "payload": M{
		"command": "exit 0", "project": M{"data": "personal"}, "inputs": []M{
			{"artifactId": "fixture", "path": "nested/fixture"}, {"artifactId": "missing", "path": "missing"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := lastReceipt(e, "bad-input"); got != "failed" {
		t.Fatalf("preparation receipt = %q", got)
	}
	assertScriptStagingGone(t, e.sessionDir("prep"))
	var reason string
	if err := e.db.QueryRow(`SELECT json_extract(body,'$.events[0].data.reason') FROM outbox WHERE route=? AND json_extract(body,'$.events[0].type')='preparation.failed'`, contract.RouteEvents.Path).Scan(&reason); err != nil || !strings.Contains(reason, "input missing") {
		t.Fatalf("preparation reason = %q, err = %v", reason, err)
	}
	outside := filepath.Join(root, "separate")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("invented outside bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`mv "$FLEET_OUTPUT_DIR" "$FLEET_OUTPUT_DIR.saved"; ln -s %q "$FLEET_OUTPUT_DIR"`, outside)
	if err := e.run(M{"id": "bad-output", "executionId": "collect", "kind": "script", "payload": M{"command": command, "project": M{"data": "personal"}}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, "collect")
	assertScriptStagingGone(t, e.sessionDir("collect"))
	if got := lastReceipt(e, "bad-output"); got != "failed" {
		t.Fatalf("collection receipt = %q", got)
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM outbox WHERE route=? AND json_extract(body,'$.events[0].type')='script.personal-cleanup' AND json_extract(body,'$.events[0].data.collectionFailed')=1 AND json_extract(body,'$.events[0].data.removed')=1`, contract.RouteEvents.Path).Scan(&n); err != nil || n != 1 {
		t.Fatalf("collection failure notices = %d, err = %v", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(outside, "marker")); err != nil || string(b) != "invented outside bytes" {
		t.Fatalf("outside marker changed: %q, %v", b, err)
	}
}

// A journal failure before queueing keeps the source available. After cleanup,
// a restarted poller still declares the queued artifact on the terminal receipt.
func TestPersonalScriptCompletionResumesAcrossJournalFailures(t *testing.T) {
	e, _ := scriptExecutor(t)
	const execution, cid = "replayed-script", "replayed-cmd"
	dir := e.sessionDir(execution)
	command := `printf fixture > "$FLEET_OUTPUT_DIR/result.txt"`
	if err := e.run(M{"id": cid, "executionId": execution, "kind": "script", "payload": M{"command": command, "project": M{"data": "personal"}}}); err != nil {
		t.Fatal(err)
	}
	mustExec := func(statement string) {
		t.Helper()
		if _, err := e.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`CREATE TRIGGER block_upload BEFORE INSERT ON transfers
		WHEN json_extract(NEW.body,'$.name')='result.txt'
		BEGIN SELECT RAISE(FAIL,'fixture queue failure'); END`)
	awaitJournalFailure := func(reason string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := e.tick(); err != nil {
				if !strings.Contains(err.Error(), reason) {
					t.Fatal(err)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("did not hit %s", reason)
	}
	awaitJournalFailure("fixture queue failure")
	if !exists(filepath.Join(dir, "output", "result.txt")) || !exists(filepath.Join(dir, "input")) {
		t.Fatal("staging removed before the queue accepted the output")
	}
	mustExec("DROP TRIGGER block_upload")
	mustExec(`CREATE TRIGGER block_receipt BEFORE INSERT ON outbox
		WHEN json_extract(NEW.body,'$.status')='completed'
		BEGIN SELECT RAISE(FAIL,'fixture receipt failure'); END`)
	awaitJournalFailure("fixture receipt failure")
	assertScriptStagingGone(t, dir)
	mustExec("DROP TRIGGER block_receipt")

	e.live = map[string]*live{}
	if err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	settle(t, e, execution)
	var uploads, receipts, cleanups int
	if err := e.db.QueryRow(`SELECT count(*) FROM transfers WHERE json_extract(body,'$.name')='result.txt'`).Scan(&uploads); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT count(*) FROM outbox WHERE json_extract(body,'$.status')='completed' AND
		EXISTS (SELECT 1 FROM json_each(body,'$.artifacts') WHERE value='result.txt')`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT count(*) FROM outbox WHERE json_extract(body,'$.events[0].type')='script.personal-cleanup'`).Scan(&cleanups); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || receipts != 1 || cleanups != 1 {
		t.Fatalf("replayed completion: uploads=%d receipts=%d cleanups=%d", uploads, receipts, cleanups)
	}
}

// An unresolved process may still use its staging. Keep the files and ownership
// fence even when the worker can report that its result is unknown.
func TestPersonalScriptPreservesUnconfirmedStaging(t *testing.T) {
	e, root := scriptExecutor(t)
	const execution, cid = "unconfirmed-script", "unconfirmed-cmd"
	body := enc(M{"id": cid, "executionId": execution, "kind": "script", "payload": M{"command": "true", "project": M{"data": "personal"}}})
	if _, err := e.db.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cid, body); err != nil {
		t.Fatal(err)
	}
	if err := e.launch(execution, cid, execution); err != nil {
		t.Fatal(err)
	}
	dir := e.sessionDir(execution)
	for _, name := range []string{"input", "output"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "fixture"), []byte("invented"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wdir := workerDirFor(root, execution)
	if err := os.MkdirAll(wdir, 0o700); err != nil {
		t.Fatal(err)
	}
	exit := workerExit{Status: "unknown", Seq: 5, CleanupUnknown: true, ProcessesUnresolved: true}
	if err := os.WriteFile(filepath.Join(wdir, "exit.json"), []byte(enc(exit)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.live[execution] = &live{command: commandOf(body), worker: &liveWorker{dir: wdir}}
	settle(t, e, execution)
	if got := lastReceipt(e, cid); got != "unknown" || count(e.db, "launches") != 1 || count(e.db, "transfers") != 0 {
		t.Fatalf("uncertain exit: receipt=%q launches=%d uploads=%d", got, count(e.db, "launches"), count(e.db, "transfers"))
	}
	for _, name := range []string{"input", "output"} {
		if !exists(filepath.Join(dir, name, "fixture")) {
			t.Errorf("unconfirmed %s staging removed", name)
		}
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
	sp, err := payloadOf[contract.StartPayload](typedCommand(M{"payload": M{"instructions": "x", "fieldFromANewerCoordinator": M{"a": 1}}}))
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

// Requirement: teardown and project-script data are the host-protocol Teardown and ProjectScriptResult, with the exit
// code and the refusal reason (W4). Risk: a renamed field reaches the coordinator as missing and a failed teardown
// script looks clean.
func TestTeardownAndProjectScriptTyped(t *testing.T) {
	dir := t.TempDir()
	if r := teardown(dir, "s1", dir, proj(M{}), false, false); r.Removed || str0(r.Reason) != "no-worktree" {
		t.Fatal(enc(r))
	}
	r := projectScript("setup", "echo out; exit 3", dir, dir, "s1")
	if r.Ok || r.ExitCode == nil || *r.ExitCode != 3 || str0(r.Stdout) != "out\n" {
		t.Fatal(enc(r))
	}
	if b := enc(refusedTeardown("uncommitted")); b != `{"reason":"uncommitted","removed":false}` {
		t.Fatal(b)
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

// Requirement: a failed pipeline script reports the end of its stderr, as a failed harness does.
// Risk: 2026-11-25 avatar-make's request step exited 1 on a missing input file; the cause carried no reason at all.
func TestStderrTailOfScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output", "script.stderr"), []byte("Traceback\nFileNotFoundError: binding.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	if got := stderrTail(dir, "exec-1", &one); got == nil || !strings.Contains(*got, "FileNotFoundError: binding.json") {
		t.Fatalf("script stderr tail = %v", got)
	}
}

// Requirement: a start whose profile fails validation fails with the validation message as its preparation problem
// (owner, 2026-11-25: a refused profile ended as unknown:unreported).
func TestInvalidProfileReported(t *testing.T) {
	e, _ := scriptExecutor(t)
	e.run(M{"id": "c11", "executionId": "x11", "sessionId": "s11", "kind": "start", "payload": M{"instructions": "x", "agentConfig": M{"id": "p", "model": "m", "instructions": "i", "mcpServers": []any{}, "unknownField": true}}})
	if lastReceipt(e, "c11") != "failed" {
		t.Fatal(lastReceipt(e, "c11"))
	}
	rs, _ := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	var all strings.Builder
	for rs.Next() {
		var b string
		rs.Scan(&b)
		all.WriteString(b)
	}
	rs.Close()
	if !strings.Contains(all.String(), "invalid profile") || !strings.Contains(all.String(), "unknownField") {
		t.Fatal("profile refusal reason missing:", all.String())
	}
}
