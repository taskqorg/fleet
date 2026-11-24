package main

import (
	"errors"
	"fleet/contract"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (e *executor) openActivityLock() (*os.File, error) {
	dir := filepath.Join(e.root, "update")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "activity.lock"), os.O_CREATE|os.O_RDWR, 0600)
}

// A shared activity lease survives preparation until retained terminal delivery drains.
// The updater's exclusive lease closes the readiness/admission snapshot race. Queued span batches do not hold an update:
// telemetry never blocks a release.
func (e *executor) activityLease() (bool, error) {
	if e.activity != nil {
		return true, nil
	}
	f, err := e.openActivityLock()
	if err != nil {
		return false, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	e.activity = f
	return true, nil
}

func updatePending(s *updateSignal) bool {
	if s == nil || s.Target == nil || !updateNewer(*s.Target, s.Running) || s.AutoUpdate == "off" {
		return false
	}
	if t, err := time.Parse(time.RFC3339, s.AutoUpdate); err == nil && t.After(time.Now()) {
		return false
	}
	return true
}

func (e *executor) drainUpdate() error {
	// One store per tick: the held poll's goroutine reads updateReady at any moment and must not see a transient nil.
	ready, err := e.idleUpdate()
	e.updateReady.Store(ready)
	return err
}

// idleUpdate releases the activity lease once nothing holds it and returns the pending update target, or nil.
func (e *executor) idleUpdate() (*string, error) {
	var busy bool
	err := e.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM launches)
		OR EXISTS(SELECT 1 FROM owned)
		OR EXISTS(SELECT 1 FROM mcp_groups)
		OR EXISTS(SELECT 1 FROM commands WHERE terminal=0)
		OR EXISTS(SELECT 1 FROM outbox WHERE route<>?)
		OR EXISTS(SELECT 1 FROM outbox_failed WHERE status=401)
		OR EXISTS(SELECT 1 FROM transfers WHERE state IN ('queued','running'))`, contract.RouteSpans.Path).Scan(&busy)
	if err != nil {
		return nil, err
	}
	if busy || len(e.live) > 0 {
		_, err := e.activityLease()
		return nil, err
	}
	hadLease := e.activity != nil
	if hadLease {
		e.activity.Close()
		e.activity = nil
	}
	signalMu.Lock()
	defer signalMu.Unlock()
	if updatePending(lastSignal) {
		// An idle executor with no prior work still needs a lock file for the updater.
		// Leave it unlocked so readiness stays valid while the updater holds LOCK_EX.
		if !hadLease && e.updateReady.Load() == nil {
			f, err := e.openActivityLock()
			if err != nil {
				return nil, err
			}
			if err := f.Close(); err != nil {
				return nil, err
			}
		}
		v := *lastSignal.Target
		return &v, nil
	}
	return nil, nil
}

var updateVersion = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]{6}\.(0|[1-9][0-9]*)$`)

func updateNewer(a, b string) bool {
	if !updateVersion.MatchString(a) {
		return false
	}
	if !updateVersion.MatchString(b) {
		return true
	}
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range x {
		n, _ := strconv.Atoi(x[i])
		m, _ := strconv.Atoi(y[i])
		if n != m {
			return n > m
		}
	}
	return false
}

// Withdraw a prior idle claim durably before spawning work; a crash releases flock.
func (e *executor) withdrawUpdateReady() error {
	e.updateReady.Store(nil)
	signalMu.Lock()
	defer signalMu.Unlock()
	if lastSignal == nil || lastSignal.RestartTarget == nil {
		return nil
	}
	s := *lastSignal
	s.RestartTarget = nil
	return writeUpdateSignal(e.root, s)
}
