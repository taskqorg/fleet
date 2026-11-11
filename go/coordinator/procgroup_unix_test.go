//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Requirement: cancelling a Lab evaluator stops everything its check started (review W3-4). Risk: a child the check
// shell left behind (`sleep 1000 &`) keeps running, and acting, after Fleet stopped observing the evaluator.
func TestRunChecksCancelKillsChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runChecks(ctx, []any{doc{"id": "orphan", "run": "sleep 1000 & echo $! > " + pidFile + "; wait"}}, nil)
		done <- err
	}()
	var pid int
	for deadline := time.Now().Add(10 * time.Second); pid == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the check did not start its child")
		}
		runtime.Gosched()
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled check returned a result")
	}
	// The child is no longer ours to wait for (init reaps it), so wait for the kernel to report it gone.
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; {
		runtime.Gosched()
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d outlived the cancelled check", pid)
		}
	}
}
