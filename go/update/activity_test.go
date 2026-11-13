package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Requirement: executor replacement waits for owned work and retained delivery; risk: a race interrupts active work or admits a start during replacement.
func TestExecutorActivityRequiresExactDrainAndLease(t *testing.T) {
	d := t.TempDir()
	signal := filepath.Join(d, "update.json")
	u := newUpdater(nil)
	guard := u.executorActivity(signal)
	if release, err := guard("1.261113.20"); err != nil || release != nil {
		t.Fatal("legacy missing readiness must defer", err)
	}
	f, err := os.OpenFile(filepath.Join(d, "activity.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := signalFile{Target: "1.261113.20", RestartTarget: "1.261113.20", AutoUpdate: "on"}
	writeJSON(signal, s, 0600)
	syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
	if release, err := guard(s.Target); err != nil || release != nil {
		t.Fatal("active native lease ignored", err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if release, err := guard("1.261113.21"); err != nil || release != nil {
		t.Fatal("retarget accepted stale readiness", err)
	}
	s.AutoUpdate = "off"
	writeJSON(signal, s, 0600)
	if release, err := guard(s.Target); err != nil || release != nil {
		t.Fatal("held update accepted", err)
	}
	s.AutoUpdate = "on"
	writeJSON(signal, s, 0600)
	release, err := guard(s.Target)
	if err != nil || release == nil {
		t.Fatal("idle update refused", err)
	}
	release()
}

// Requirement: executor replacement waits for owned work and retained delivery; risk: a race interrupts active work or admits a start during replacement.
func TestExecutorBusyDefersSwitchAndRestart(t *testing.T) {
	f := newFixture(t)
	f.release("1.261113.19", 0)
	f.run("1.261113.19")
	f.release("1.261113.20", 0)
	f.restarts = 0
	busy := true
	released := false
	f.p.activity = func(string) (func(), error) {
		if busy {
			return nil, nil
		}
		return func() { released = true }, nil
	}
	if r := f.run("1.261113.20"); r.State != "pending" || f.current() != "1.261113.19" || f.restarts != 0 {
		t.Fatalf("busy replacement %+v current%s restarts%d", r, f.current(), f.restarts)
	}
	busy = false
	if r := f.run("1.261113.20"); r.State != "ok" || f.current() != "1.261113.20" || f.restarts != 1 || !released {
		t.Fatalf("idle replacement %+v", r)
	}
	// A previously pending unhealthy install must not bypass the same activity guard.
	os.Remove(filepath.Join(f.root, "current", "healthy"))
	f.restarts = 0
	busy = true
	f.health["1.261113.20"] = down
	if r := f.run("1.261113.20"); r.State != "pending" || f.restarts != 0 {
		t.Fatalf("resume restarted active executor %+v", r)
	}
}
