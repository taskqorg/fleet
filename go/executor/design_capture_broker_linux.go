package main

import (
	"errors"
	"fmt"
	"io"
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

// A capture stays unnamed until the data and writer close have succeeded.
// Keep a second descriptor only to link the complete inode into the input dir.
func publishCaptureArchive(dirfd int, tag, name string, source io.Reader, size int64) (string, error) {
	fd, err := unix.Openat(dirfd, ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", err
	}
	out := os.NewFile(uintptr(fd), "private capture download")
	linkFD, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = out.Close()
		return "", err
	}
	defer unix.Close(linkFD)
	n, copyErr := io.Copy(out, io.LimitReader(source, size+1))
	if copyErr == nil && n != size {
		copyErr = io.ErrUnexpectedEOF
	}
	var syncErr error
	if copyErr == nil {
		syncErr = out.Sync()
	}
	if err := errors.Join(copyErr, syncErr, out.Close()); err != nil {
		return "", err
	}
	sourceFD := fmt.Sprintf("/proc/self/fd/%d", linkFD)
	for n := 0; n < 1000; n++ {
		target := tag + "-" + name
		if n > 0 {
			target = fmt.Sprintf("%s-%d-%s", tag, n, name)
		}
		err := unix.Linkat(unix.AT_FDCWD, sourceFD, dirfd, target, unix.AT_SYMLINK_FOLLOW)
		if err == nil {
			return target, nil
		}
		if err != unix.EEXIST {
			return "", err
		}
	}
	return "", unix.EEXIST
}

func captureStageReady(root string) bool {
	if st, err := os.Stat("/usr/bin/node"); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return false
	}
	if st, err := os.Stat(signedCaptureReplayEntry()); err != nil || !st.Mode().IsRegular() {
		return false
	}
	memory, err := anonymousCaptureFile()
	if err != nil {
		return false
	}
	_ = memory.Close()
	fd, err := unix.Open(root, unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		return false
	}
	reopen, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	closeErr := unix.Close(fd)
	return err == nil && unix.Close(reopen) == nil && closeErr == nil
}
