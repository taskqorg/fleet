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
	v := "1.261113.20"
	s := signalFile{Target: &v, RestartTarget: &v, AutoUpdate: "on"}
	writeJSON(signal, s, 0600)
	syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
	if release, err := guard(v); err != nil || release != nil {
		t.Fatal("active native lease ignored", err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if release, err := guard("1.261113.21"); err != nil || release != nil {
		t.Fatal("retarget accepted stale readiness", err)
	}
	s.AutoUpdate = "off"
	writeJSON(signal, s, 0600)
	if release, err := guard(v); err != nil || release != nil {
		t.Fatal("held update accepted", err)
	}
	s.AutoUpdate = "on"
	writeJSON(signal, s, 0600)
	release, err := guard(v)
	if err != nil || release == nil {
		t.Fatal("idle update refused", err)
	}
	release()
}
