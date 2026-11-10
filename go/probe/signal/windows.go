//go:build windows

package main

import (
	"fmt"
	"syscall"
)

// Windows has no signals. A process handle names one process object and stays bound to it after the pid is reused, so
// TerminateProcess on a handle opened at start gives the same guarantee for kill; stop/continue (SIGSTOP) has no
// equivalent. Standard library syscall covers it.
type token struct{ h syscall.Handle }

func identity(pid int) (string, error) {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)
	var c, e, k, u syscall.Filetime
	if err := syscall.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return "", err
	}
	return fmt.Sprint(c.Nanoseconds()), nil
}

func capture(pid int) (token, error) {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE|syscall.SYNCHRONIZE, false, uint32(pid))
	return token{h}, err
}

func signal(t token, sig syscall.Signal) error {
	if sig != syscall.SIGKILL {
		return fmt.Errorf("signal %v unsupported on windows", sig)
	}
	return syscall.TerminateProcess(t.h, 1)
}
