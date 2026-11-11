//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/ebitengine/purego"
)

// libproc through purego (no cgo): proc_pidinfo, and the private proc_signal_with_audittoken looked up at run time.
var (
	procPidinfo func(pid, flavor int32, arg uint64, buf *byte, size int32) int32
	procPidpath func(pid int32, buffer *byte, size uint32) int32
	procSignal  func(token *[8]uint32, sig int32) int32
)

func init() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return // identity and signal refuse below
	}
	if sym, err := purego.Dlsym(lib, "proc_pidpath"); err == nil {
		purego.RegisterFunc(&procPidpath, sym)
	}
	if sym, err := purego.Dlsym(lib, "proc_pidinfo"); err == nil {
		purego.RegisterFunc(&procPidinfo, sym)
	}
	if sym, err := purego.Dlsym(lib, "proc_signal_with_audittoken"); err == nil {
		purego.RegisterFunc(&procSignal, sym)
	}
}

// processIdentity is the start time from PROC_PIDTBSDINFO (pbi_pid at 12, pbi_start_tvsec at 120, usec at 128).
func processIdentity(pid int) (string, error) {
	if procPidinfo == nil {
		return "", errors.New("proc_pidinfo unavailable")
	}
	var b [136]byte
	if n := procPidinfo(int32(pid), 3, 0, &b[0], 136); n != 136 {
		return "", fmt.Errorf("proc_pidinfo: no process %d", pid)
	}
	sec, usec := binary.LittleEndian.Uint64(b[120:]), binary.LittleEndian.Uint64(b[128:])
	if binary.LittleEndian.Uint32(b[12:]) != uint32(pid) || sec == 0 {
		return "", errors.New("invalid creation identity")
	}
	return fmt.Sprintf("%d-%d", sec, usec), nil
}

// signalOwned delivers sig only to the incarnation named by expected: the audit token carries the pid version, which the
// kernel checks. A missing private symbol refuses the signal.
func signalOwned(pid int, expected string, sig syscall.Signal) error {
	if procPidinfo == nil {
		return errors.New("process version unavailable")
	}
	var b [56]byte
	if n := procPidinfo(int32(pid), 17, 0, &b[0], 56); n != 56 || binary.LittleEndian.Uint64(b[16:]) == 0 {
		return errors.New("process version unavailable")
	}
	if id, err := identity(pid); err != nil || id != expected {
		return errors.New("process incarnation changed")
	}
	if procSignal == nil {
		return errors.New("identity-bound signal unsupported")
	}
	var t [8]uint32
	t[5] = uint32(pid)
	t[7] = binary.LittleEndian.Uint32(b[32:])
	if e := procSignal(&t, int32(sig)); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}

func processExecutable(pid int) (string, error) {
	if procPidpath == nil {
		return "", errors.New("process executable unavailable")
	}
	var b [4096]byte
	n := procPidpath(int32(pid), &b[0], uint32(len(b)))
	if n <= 0 {
		return "", errors.New("process executable unavailable")
	}
	return filepath.EvalSymlinks(string(bytes.TrimRight(b[:n], "\x00")))
}
