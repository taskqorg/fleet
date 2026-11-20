package main

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	"fleet/executor/supervise"
)

// boundedOutput runs an external readiness/forge call in its own group for at most wait; what it leaves in its group
// is killed when it exits.
func boundedOutput(c *exec.Cmd, wait time.Duration) ([]byte, error) {
	var out bytes.Buffer
	c.Stdout = &out
	p, err := supervise.Spawn(supervise.Spec{Cmd: c, Deadline: wait, KillLeftovers: true})
	if err != nil {
		return nil, err
	}
	err = p.Wait()
	if p.TimedOut() {
		return nil, context.DeadlineExceeded
	}
	return out.Bytes(), err
}
