//go:build !windows

package main

import (
	osexec "os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group; cancelling its context kills the group.
func ownGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// killGroup kills what is left of cmd's process group after it exited.
func killGroup(cmd *osexec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
