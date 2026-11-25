package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Guard both readiness and command admission; an idle snapshot alone races new work.
func (u *updater) executorActivity(signal string) func(string) (func(), error) {
	return func(target string) (func(), error) {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(signal), "activity.lock"), os.O_RDWR, 0600)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, nil
			}
			return nil, err
		}
		// The executor is drained when it marked its current target ready. The release staged here may be older than that
		// target: a newer one landed while this run downloaded. It still installs (cycle checked it is newer than the
		// running one); requiring the two to match left a host chasing releases that land faster than it downloads
		// (2026-11-26, windows: "executor work is draining" with nothing running, for every release).
		var s signalFile
		ok := readJSON(signal, &s)
		drained := str(s.RestartTarget) != "" && str(s.RestartTarget) == str(s.Target)
		if !ok || (s.Running != target && !drained) || u.held(s.AutoUpdate) {
			f.Close()
			return nil, nil
		}
		return func() { f.Close() }, nil
	}
}
