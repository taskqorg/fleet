package main

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// boundedOutput follows rpcExchange's process-group cleanup for an external readiness/forge call.
func boundedOutput(c *exec.Cmd, wait time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	defer syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-ctx.Done():
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, ctx.Err()
	}
}
