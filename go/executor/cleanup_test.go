package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"fleet/contract"
)

// TestHelperSleep is the marked process of the cleanup tests when FLEET_HELPER_SLEEP is set: the test binary itself,
// because macOS hides the environment of Apple's own programs (sleep, tail) from kern.procargs2.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("FLEET_HELPER_SLEEP") == "1" {
		time.Sleep(time.Minute)
	}
}

// startMarked starts a detached sleeper (its own session) carrying the run marker of execution under root.
func startMarked(t *testing.T, root, execution string) int {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	c.Env = append(os.Environ(), "FLEET_HELPER_SLEEP=1", runMarkerKey+"="+execution, runOwnerKey+"="+root)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }() // reaped as soon as it is killed, so kill(pid, 0) sees it gone
	t.Cleanup(func() { c.Process.Kill(); <-done })
	for i := 0; i < 100; i++ {
		if v, ok := processEnv(c.Process.Pid, runMarkerKey); ok && v == execution {
			return c.Process.Pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("marker not readable")
	return 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Requirement: a process a run started outlives the run in no way it forks (setsid, double fork); only this executor's
// marker of that run is killed. Risk: forgotten databases and servers fill a host's memory and swap for days.
func TestCleanupKillsMarkedEscapes(t *testing.T) {
	root := t.TempDir()
	mine := startMarked(t, root, "x1")
	other := startMarked(t, root, "x2")
	foreign := startMarked(t, root+"-other", "x1")
	os.MkdirAll(runTempDir(root, "x1"), 0o700)
	os.WriteFile(filepath.Join(runTempDir(root, "x1"), "f"), []byte("data"), 0o600)
	if n := cleanupRun(root, "x1"); n != 1 {
		t.Fatalf("orphans %d", n)
	}
	time.Sleep(50 * time.Millisecond)
	if alive(mine) || !alive(other) || !alive(foreign) {
		t.Fatalf("mine %v other %v foreign %v", alive(mine), alive(other), alive(foreign))
	}
	if _, err := os.Stat(runTempDir(root, "x1")); !os.IsNotExist(err) {
		t.Fatal("run TMPDIR kept")
	}
	// At start every marked process and run TMPDIR of a run that is not unresolved is a leftover.
	held := startMarked(t, root, "x3")
	os.MkdirAll(runTempDir(root, "x2"), 0o700)
	os.MkdirAll(runTempDir(root, "x3"), 0o700)
	cleanupAtStart(root, map[string]bool{"x3": true})
	time.Sleep(50 * time.Millisecond)
	if alive(other) || !alive(held) || !alive(foreign) {
		t.Fatalf("other %v held %v foreign %v", alive(other), alive(held), alive(foreign))
	}
	if _, err := os.Stat(runTempDir(root, "x2")); !os.IsNotExist(err) {
		t.Fatal("leftover TMPDIR kept")
	}
	if _, err := os.Stat(runTempDir(root, "x3")); err != nil {
		t.Fatal("unresolved run's TMPDIR removed")
	}
}

// Requirement: a script run gets its own TMPDIR and the run marker, and both its TMPDIR and an escaped process are gone
// when it ends. Risk: per-task caches in /tmp (tmpfs on PDS18) fill memory and swap.
func TestScriptRunTempDirAndMarker(t *testing.T) {
	e, root := scriptExecutor(t)
	cmd := `case "$TMPDIR" in "$FLEET_EXECUTION_OWNER"/run-tmp/x1) ;; *) exit 3;; esac; test "$FLEET_EXECUTION_ID" = x1 || exit 4; echo hi > "$TMPDIR/f"`
	if err := e.run(M{"id": "c1", "executionId": "x1", "sessionId": "s1", "kind": "script", "payload": M{"command": cmd}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, "x1")
	if r := lastReceipt(e, "c1"); r != "completed" {
		b, _ := os.ReadFile(filepath.Join(root, "sessions/s1/output/script.stderr"))
		t.Fatal(r, string(b))
	}
	if _, err := os.Stat(runTempDir(root, "x1")); !os.IsNotExist(err) {
		t.Fatal("run TMPDIR kept")
	}
}

// Requirement: the /tmp sweep removes only old entries of the executor user that hold no socket. Risk: a live
// program's endpoint or a fresh cache is removed under it.
func TestSweepTmp(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sweep") // short: a socket path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	old := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{"stale/a", "fresh/a", "sock"} {
		os.MkdirAll(filepath.Join(dir, d), 0o700)
	}
	os.WriteFile(filepath.Join(dir, "stale/a/f"), []byte("12345"), 0o600)
	os.Chmod(filepath.Join(dir, "stale/a"), 0o500) // read-only, as a Go module cache is
	os.WriteFile(filepath.Join(dir, "fresh/a/f"), nil, 0o600)
	l, err := net.Listen("unix", filepath.Join(dir, "sock", "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		if !strings.HasPrefix(p, filepath.Join(dir, "fresh/a/f")) {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	c := sweepTmp(dir, 24*time.Hour, time.Now())
	if c.dirs != 1 || c.bytes != 5 {
		t.Fatalf("%+v", c)
	}
	for d, want := range map[string]bool{"stale": false, "fresh": true} {
		if _, err := os.Stat(filepath.Join(dir, d)); (err == nil) != want {
			t.Fatal(d, err)
		}
	}
}

// Requirement: the sweep removes a session directory only when it holds no worktree (or set-aside copy), its session
// has no journaled launch and nothing changed in it for the age. Risk: a session's work or a running turn's files go.
func TestSweepSessions(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{"done/input", "fresh", "busy", "wt/worktree", "aside/worktree.stale-1", "ws/workspaces/k/worktree"} {
		os.MkdirAll(filepath.Join(root, d), 0o700)
	}
	os.WriteFile(filepath.Join(root, "done/input/f"), []byte("12345"), 0o600)
	os.MkdirAll(filepath.Join(root, "done/go-cache/x"), 0o700)
	os.Chmod(filepath.Join(root, "done/go-cache/x"), 0o500) // read-only, as a Go module cache is
	filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error {
		if !strings.HasPrefix(p, filepath.Join(root, "fresh")) {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	c := sweepSessions([]string{root, filepath.Join(root, "missing")}, func(s string) bool { return s == "busy" }, 24*time.Hour, time.Now())
	if c.dirs != 1 || c.bytes != 5 {
		t.Fatalf("%+v", c)
	}
	for d, want := range map[string]bool{"done": false, "fresh": true, "busy": true, "wt": true, "aside": true, "ws": true} {
		if _, err := os.Stat(filepath.Join(root, d)); (err == nil) != want {
			t.Fatal(d, err)
		}
	}
}

// Requirement: host readings name swap and the top memory consumers, the test process among them as not Fleet's
// unless marked. Risk: a saturated host is reported without what saturates it.
func TestHostPressureReadings(t *testing.T) {
	var l contract.HostLoad
	hostPressure(&l)
	if l.SwapTotal == nil || l.SwapUsed == nil || len(l.Top) == 0 || len(l.Top) > 5 {
		t.Fatalf("%+v", l)
	}
	for i, c := range l.Top {
		if c.Command == "" || strings.Contains(c.Command, "/") || c.Fleet == nil || c.User == nil || (i > 0 && c.MemBytes > l.Top[i-1].MemBytes) {
			t.Fatalf("%d %+v", i, c)
		}
	}
	if a := topAttr(l.Top); strings.Count(a, ",") != len(l.Top)-1 {
		t.Fatal(a)
	}
}
