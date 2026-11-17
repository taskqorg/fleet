//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// processIdentity is /proc/<pid>/stat field 22 (start time in clock ticks).
func processIdentity(pid int) (string, error) {
	s, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	i := strings.LastIndexByte(string(s), ')')
	if i < 0 {
		return "", errors.New("invalid stat")
	}
	f := strings.Fields(string(s[i+1:]))
	if len(f) < 20 {
		return "", errors.New("start missing")
	}
	return f[19], nil
}

// signalOwned: os.FindProcess holds a pidfd on Linux (Go 1.23+) and Signal uses pidfd_send_signal; the identity is checked
// after the pidfd is open, so the signal never reaches a process that took the pid later.
func signalOwned(pid int, expected string, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	if id, err := identity(pid); err != nil || id != expected {
		return errors.New("process incarnation changed")
	}
	return p.Signal(sig)
}

// waitExited blocks until pid exits without reaping it (waitid with WNOWAIT): its PID stays taken until the caller
// reaps it.
func waitExited(pid int) {
	var info [128]byte // siginfo_t
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), 4|0x01000000 /* WEXITED|WNOWAIT */, 0, 0)
		if e != syscall.EINTR {
			return
		}
	}
}
