package main

import (
	"net"
	"os"
	"path/filepath"
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

// Publish an already verified directory in one operation, without replacing
// any entry the designer created. The capture workflow is supported on Linux.
func publishCaptureDirectory(source, final string) error {
	d, err := unix.Open(filepath.Dir(final), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(d)
	return unix.Renameat2(unix.AT_FDCWD, source, d, filepath.Base(final), unix.RENAME_NOREPLACE)
}
