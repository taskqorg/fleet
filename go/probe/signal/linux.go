//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Standard library only: since Go 1.23 os.FindProcess holds a pidfd on Linux and Signal uses pidfd_send_signal, so a
// signal never reaches a process that took the pid after the handle was opened. identity is /proc/<pid>/stat field 22.
func identity(pid int) (string, error) {
	s, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(s[strings.LastIndexByte(string(s), ')')+1:]))
	return f[19], nil
}

type token struct {
	p  *os.Process
	id string
}

func capture(pid int) (token, error) {
	p, err := os.FindProcess(pid) // pidfd_open
	if err != nil {
		return token{}, err
	}
	id, err := identity(pid) // checked after the pidfd is open, as native.rs does
	return token{p, id}, err
}

// Linux exec keeps the pidfd valid, so the darwin exec case is delivered here; reuse after exit is refused.
func signal(t token, sig syscall.Signal) error { return t.p.Signal(sig) }
