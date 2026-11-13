package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A shared activity lease survives preparation until retained terminal delivery drains.
// The updater's exclusive lease closes the readiness/admission snapshot race.
func (e *executor) activityLease() (bool, error) {
	if e.activity != nil {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Join(e.root, "update"), 0700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(e.root, "update", "activity.lock"), os.O_CREATE|os.O_RDWR, 0600)
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
	e.updateReady.Store(nil)
	if ok, err := e.activityLease(); err != nil {
		return err
	} else if !ok {
		return nil
	}
	var n int
	if err := e.db.QueryRow(`SELECT (SELECT count(*) FROM launches)+(SELECT count(*) FROM owned)+(SELECT count(*) FROM mcp_groups)+(SELECT count(*) FROM commands WHERE terminal=0)+(SELECT count(*) FROM outbox)+(SELECT count(*) FROM outbox_failed)+(SELECT count(*) FROM transfers WHERE state IN ('queued','running'))`).Scan(&n); err != nil {
		return err
	}
	if len(e.live) > 0 || n > 0 {
		return nil
	}
	if e.activity != nil {
		e.activity.Close()
		e.activity = nil
	}
	signalMu.Lock()
	defer signalMu.Unlock()
	if updatePending(lastSignal) {
		v := *lastSignal.Target
		e.updateReady.Store(&v)
	}
	return nil
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
