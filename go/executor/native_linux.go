//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func socketFilter(scene bool) ([]unix.SockFilter, error) {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return nil, fmt.Errorf("unsupported shell sandbox architecture: %s", runtime.GOARCH)
	}
	const (
		load = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jge  = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
		ret  = unix.BPF_RET | unix.BPF_K
	)
	family := unix.SockFilter{Code: jeq, Jf: 1, K: unix.AF_UNIX}
	if scene {
		// Chromium uses inherited CDP streams and Unix IPC. Deny every other socket
		// family in all descendants, including DNS and WebRTC's direct sockets.
		family.Jt, family.Jf = 1, 0
	}
	return []unix.SockFilter{
		{Code: load, K: 4}, // seccomp_data.arch
		{Code: jeq, Jt: 1, K: arch},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: load, K: 0},                // seccomp_data.nr
		{Code: jge, Jf: 1, K: 0x40000000}, // reject the x32 ABI
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: jeq, Jt: 3, K: unix.SYS_IO_URING_SETUP},
		{Code: jeq, Jf: 3, K: unix.SYS_SOCKET},
		{Code: load, K: 16}, // seccomp_data.args[0], the socket family
		family,
		{Code: ret, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{Code: ret, K: unix.SECCOMP_RET_ALLOW},
	}, nil
}

// bubblewrap's read-only mounts still permit connecting to sockets. Deny Unix socket creation under
// either shell network policy, including via io_uring.
func shellSocketFilter() (*os.File, error) {
	filter, err := socketFilter(false)
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	err = binary.Write(w, binary.NativeEndian, filter)
	closeErr := w.Close()
	if err = errors.Join(err, closeErr); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func execSceneChromium(args []string) error {
	filter, err := socketFilter(true)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return err
	}
	runtime.KeepAlive(filter)
	return syscall.Exec(args[0], args, os.Environ())
}

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
