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

// drainWindow bounds how long a drain refuses new starts; drainPause is how long the executor then admits them
// before it fences again. A run can last hours, and releases land every few minutes: an unbounded fence left a busy
// host taking no work (2026-11-26, pds18-native: one long run held 13 starts for the whole drain). While the fence is
// open the update still installs at the first idle moment (idleUpdate), and the coordinator places work there again.
const (
	drainWindow = 10 * time.Minute
	drainPause  = 30 * time.Minute
)

// drainState: the current drain episode, guarded by signalMu. since is zero while no update is pending.
type drainState struct {
	since, reopenAt time.Time
	waitsFor        []string
}

var drain drainState

// fencedLocked reports whether command admission refuses new starts now; the caller holds signalMu. One drain episode
// lasts while an update is pending, across target changes: a newer release does not restart the window.
func fencedLocked(now time.Time) bool {
	if !updatePending(lastSignal) {
		drain = drainState{}
		return false
	}
	if drain.since.IsZero() {
		drain.since = now
	}
	if !drain.reopenAt.IsZero() {
		if now.Before(drain.reopenAt) {
			return false
		}
		drain.since, drain.reopenAt = now, time.Time{} // the pause is over: a new window
	}
	if now.Sub(drain.since) >= drainWindow && len(drain.waitsFor) > 0 {
		drain.reopenAt = now.Add(drainPause)
		return false
	}
	return true
}

// drainReport is the poll's drain field; nil when no update is pending.
func drainReport(now time.Time) *contract.DrainReport {
	signalMu.Lock()
	defer signalMu.Unlock()
	fenced := fencedLocked(now)
	if drain.since.IsZero() {
		return nil
	}
	r := &contract.DrainReport{Target: *lastSignal.Target, Since: drain.since.UTC().Format(time.RFC3339), Fenced: fenced,
		WaitsFor: append([]string{}, drain.waitsFor...)}
	if !fenced {
		t := drain.reopenAt.UTC().Format(time.RFC3339)
		r.ReopenAt = &t
	}
	return r
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
	var procs, cmds, receipts, unauth, transfers bool
	err := e.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM launches) OR EXISTS(SELECT 1 FROM owned) OR EXISTS(SELECT 1 FROM mcp_groups),
		EXISTS(SELECT 1 FROM commands WHERE terminal=0),
		EXISTS(SELECT 1 FROM outbox WHERE route<>?),
		EXISTS(SELECT 1 FROM outbox_failed WHERE status=401),
		EXISTS(SELECT 1 FROM transfers WHERE state IN ('queued','running'))`, contract.RouteSpans.Path).Scan(&procs, &cmds, &receipts, &unauth, &transfers)
	if err != nil {
		return nil, err
	}
	var waits []string
	if n := len(e.live); n == 1 {
		waits = append(waits, "1 run")
	} else if n > 1 {
		waits = append(waits, strconv.Itoa(n)+" runs")
	}
	for _, w := range []struct {
		on   bool
		what string
	}{{procs && len(e.live) == 0, "processes of a stopped run"}, {cmds, "open commands"}, {receipts, "receipts to deliver"},
		{unauth, "receipts refused as unauthenticated"}, {transfers, "file transfers"}} {
		if w.on {
			waits = append(waits, w.what)
		}
	}
	signalMu.Lock()
	drain.waitsFor = waits
	signalMu.Unlock()
	if len(waits) > 0 {
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
