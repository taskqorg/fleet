package main

import (
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func captureBrokerReady() bool {
	st, err := os.Lstat(captureBrokerSocket)
	return err == nil && st.Mode()&os.ModeSocket != 0 && st.Mode().Perm()&0007 == 0 && owner(st) == 0
}

func dialCaptureBroker() (net.Conn, error) {
	if !captureBrokerReady() {
		return nil, errCaptureBroker
	}
	conn, err := net.DialTimeout("unix", captureBrokerSocket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	var peer *unix.Ucred
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			peer, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		})
	}
	if err != nil || peer == nil || peer.Uid != 0 {
		conn.Close()
		return nil, errCaptureBroker
	}
	return conn, nil
}

// The mediator never creates local archive files. Also keep the bytes
// out of same-UID procfs/ptrace reads while the executor is validating them.
func protectCaptureMemory() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

func anonymousCaptureFile() (*os.File, error) {
	fd, err := unix.MemfdCreate("fleet-capture", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "fleet-capture"), nil
}
