//go:build darwin

package main

import (
	"golang.org/x/sys/unix"
	"net"
)

func mcpPeer(c *net.UnixConn) (pid, uid int, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, e
	}
	e = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, err = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil {
			uid = int(cred.Uid)
			pid, err = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		}
	})
	if e != nil {
		err = e
	}
	return
}
