// Package supervise is the one way the executor starts a child process (docs/designs/fleet-standard.md S7): its own
// process group, an optional deadline, a kill that never reaches a reused group id, and one reaper.
package supervise

import (
	"os/exec"
	"sync"
	"syscall"
	"time"

	"fleet/errtrack"
)

// Spec: one child. Cmd is configured by the caller (path, args, env, files, pipes) and started by Spawn.
type Spec struct {
	Cmd *exec.Cmd
	// SameGroup keeps the child in the caller's process group (a guardian whose own group is the unit); kills then
	// reach only the child itself.
	SameGroup bool
	// Deadline > 0 kills the group once it passes, unless the leader exited first; TimedOut reports it.
	Deadline time.Duration
	// KillLeftovers kills what the leader left in its group when it exits, before the reap, so the group id cannot
	// have been reused.
	KillLeftovers bool
	// Started runs after the start and before the reaper begins: the child exists and is not reaped, so its identity
	// (start time) can be read even when it exits at once.
	Started func(pid int)
}

// Proc: a started child. Done closes after the reap; Cmd.ProcessState is set by then.
type Proc struct {
	Cmd *exec.Cmd
	Pid int

	mu                       sync.Mutex
	exited, reaped, timedOut bool
	leaderOnly               bool
	err                      error
	done                     chan struct{}
}

// Spawn starts s.Cmd and reaps it in the background.
func Spawn(s Spec) (*Proc, error) {
	c := s.Cmd
	if !s.SameGroup {
		if c.SysProcAttr == nil {
			c.SysProcAttr = &syscall.SysProcAttr{}
		}
		c.SysProcAttr.Setpgid = true
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &Proc{Cmd: c, Pid: c.Process.Pid, leaderOnly: s.SameGroup, done: make(chan struct{})}
	if s.Started != nil {
		s.Started(p.Pid)
	}
	var timer *time.Timer
	if s.Deadline > 0 {
		timer = time.AfterFunc(s.Deadline, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.exited && !p.reaped { // a child that ended by itself just before the timer is no timeout
				p.timedOut = true
				p.signal(syscall.SIGKILL)
			}
		})
	}
	errtrack.Go("supervise.reap", func() {
		// Until the reap the exited leader is a zombie, so its pid and group id stay taken: every kill before the
		// reap reaches this child's group only. Once it exited, it counts as reaped for kills.
		exited := WaitExited(p.Pid)
		p.mu.Lock()
		if exited {
			p.exited, p.reaped = true, true
			if s.KillLeftovers && !s.SameGroup {
				syscall.Kill(-p.Pid, syscall.SIGKILL)
			}
		} else {
			p.leaderOnly = true // no waitid: a group kill after an unseen reap could hit a reused id
		}
		p.mu.Unlock()
		err := c.Wait()
		if timer != nil {
			timer.Stop()
		}
		p.mu.Lock()
		p.exited, p.reaped, p.err = true, true, err
		p.mu.Unlock()
		close(p.done)
	})
	return p, nil
}

// signal: the group, or only the leader; the caller holds p.mu and has checked the leader is not reaped.
func (p *Proc) signal(sig syscall.Signal) {
	if p.leaderOnly {
		p.Cmd.Process.Signal(sig) // os.Process refuses a reaped leader
	} else {
		syscall.Kill(-p.Pid, sig)
	}
}

// Signal sends sig to the group (or the leader) unless the leader was reaped.
func (p *Proc) Signal(sig syscall.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		p.signal(sig)
	}
}

// Kill: Signal(SIGKILL).
func (p *Proc) Kill() { p.Signal(syscall.SIGKILL) }

// Done closes once the child was reaped.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Wait waits for the reap and returns Cmd.Wait's error.
func (p *Proc) Wait() error {
	<-p.done
	return p.err
}

// TimedOut reports whether the deadline killed the child.
func (p *Proc) TimedOut() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timedOut
}
