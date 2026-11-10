//go:build darwin

package main

import (
	"encoding/binary"
	"fmt"
	"syscall"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// libproc has no Go binding: x/sys/unix does not wrap proc_pidinfo, and proc_signal_with_audittoken is private API
// that the Rust executor also reaches through dlsym. purego calls C functions without cgo.
var (
	procPidinfo func(pid, flavor int32, arg uint64, buf *byte, size int32) int32
	procSignal  func(token *[8]uint32, sig int32) int32
)

func init() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&procPidinfo, lib, "proc_pidinfo")
	purego.RegisterLibFunc(&procSignal, lib, "proc_signal_with_audittoken")
}

// identity: start time from PROC_PIDTBSDINFO, as native.rs does (pbi_pid at 12, pbi_start_tvsec at 120, usec at 128).
// The same value is available from sysctl kern.proc.pid through x/sys/unix without purego; both are printed.
func identity(pid int) (string, error) {
	var b [136]byte
	if n := procPidinfo(int32(pid), 3, 0, &b[0], 136); n != 136 || binary.LittleEndian.Uint32(b[12:]) != uint32(pid) {
		return "", fmt.Errorf("proc_pidinfo: no process %d", pid)
	}
	sec, usec := binary.LittleEndian.Uint64(b[120:]), binary.LittleEndian.Uint64(b[128:])
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	t := k.Proc.P_starttime
	if uint64(t.Sec) != sec || uint64(t.Usec) != usec {
		return "", fmt.Errorf("sysctl and proc_pidinfo disagree")
	}
	return fmt.Sprintf("%d-%d", sec, usec), nil
}

type token [8]uint32

// capture: PROC_PIDUNIQIDENTIFIERINFO (17) gives the pid version; with the pid it forms the audit token the kernel checks.
func capture(pid int) (token, error) {
	var b [56]byte
	if n := procPidinfo(int32(pid), 17, 0, &b[0], 56); n != 56 || binary.LittleEndian.Uint64(b[16:]) == 0 {
		return token{}, fmt.Errorf("process version unavailable")
	}
	var t token
	t[5] = uint32(pid)
	t[7] = binary.LittleEndian.Uint32(b[32:])
	return t, nil
}

func signal(t token, sig syscall.Signal) error {
	if e := procSignal((*[8]uint32)(&t), int32(sig)); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}
