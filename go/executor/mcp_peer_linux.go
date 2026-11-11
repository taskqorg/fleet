//go:build linux

package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
)

func mcpPeer(c *net.UnixConn) (pid, uid int, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, e
	}
	e = raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			pid = int(cred.Pid)
			uid = int(cred.Uid)
		}
	})
	if e != nil {
		err = e
	}
	return
}
func processExecutable(pid int) (string, error) {
	p, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(p)
}
