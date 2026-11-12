package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Requirement: a slow GitHub read of one path never delays a read of another path, and concurrent misses of one path
// reach GitHub once.
// Risk: Delivery's first pass after a start waited ~20 min behind the Runners screen's run-by-run job reads.
func TestGithubOnce(t *testing.T) {
	release := make(chan struct{})
	go githubOnce("test slow", time.Minute, func() ([]byte, error) { <-release; return nil, nil }, func(error) bool { return true })
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		githubOnce("test other", time.Minute, func() ([]byte, error) { return []byte("x"), nil }, func(error) bool { return true })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a read of another path waited for the slow one")
	}
	close(release)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			githubOnce("test same", time.Minute, func() ([]byte, error) { calls.Add(1); time.Sleep(20 * time.Millisecond); return nil, nil }, func(error) bool { return true })
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
