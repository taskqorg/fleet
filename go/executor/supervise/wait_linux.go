//go:build linux

package supervise

import "golang.org/x/sys/unix"

// WaitExited blocks until pid exits without reaping it (waitid with WNOWAIT): its PID stays taken until the caller
// reaps it. It reports false when waitid fails: the process may still run.
func WaitExited(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}
