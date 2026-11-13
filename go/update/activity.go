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
		var s signalFile
		if !readJSON(signal, &s) || (s.Running != target && (s.Target != target || s.RestartTarget != target)) || u.held(s.AutoUpdate) {
			f.Close()
			return nil, nil
		}
		return func() { f.Close() }, nil
	}
}
