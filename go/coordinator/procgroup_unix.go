//go:build !windows

package main

import (
	"errors"
	"os"
	osexec "os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group; cancelling its context kills the group.
func ownGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone // the group already ended: Wait reports the check's own result
	}
}

// killGroup kills what is left of cmd's process group after it exited.
// ponytail: the shell is already reaped here, so the group id could in theory be reused by a new group leader; that needs
// the pid counter to wrap in between. Kill before reaping (waitid WNOWAIT, Linux only) if that ever matters. A child
// that calls setsid leaves the group and is not covered; the executor's descendant tracking is the pattern for that.
func killGroup(cmd *osexec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
