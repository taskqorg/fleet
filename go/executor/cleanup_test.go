package main

import (
	"net"
	"os"
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

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

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

// Requirement: a session counts as busy while it has an open command (preparing) or a journaled launch. Risk: the sweep
// removes the directory a new turn is preparing into.
func TestSessionBusy(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := &executor{db: db}
	db.Exec(`INSERT INTO commands(id,body) VALUES('c1','{"sessionId":"prep"}'),('c2','{"sessionId":"done"}')`)
	db.Exec(`UPDATE commands SET terminal=1 WHERE id='c2'`)
	db.Exec(`INSERT INTO launches(execution,command,session) VALUES('x','c3','run')`)
	for s, want := range map[string]bool{"prep": true, "run": true, "done": false, "none": false} {
		if e.sessionBusy(s) != want {
			t.Fatal(s)
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
