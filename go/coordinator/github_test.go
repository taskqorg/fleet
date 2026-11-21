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
	run := time.Now().String() // keys of this run only: the cache is global
	release := make(chan struct{})
	go githubOnce("test slow"+run, time.Minute, func() ([]byte, error) { <-release; return nil, nil }, func(error) bool { return true })
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		githubOnce("test other"+run, time.Minute, func() ([]byte, error) { return []byte("x"), nil }, func(error) bool { return true })
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
			githubOnce("test same"+run, time.Minute, func() ([]byte, error) { calls.Add(1); time.Sleep(20 * time.Millisecond); return nil, nil }, func(error) bool { return true })
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

// Requirement: keyedLock serializes concurrent callers per key, lets distinct keys proceed concurrently, and evicts
// idle entries when the last holder or waiter unlocks.
// Risk: an unbounded mutex map retains memory for every past key, or premature deletion breaks mutual exclusion under -race.
func TestKeyedLockEviction(t *testing.T) {
	var kl keyedLock

	// Two distinct keys may be held at the same time without blocking each other.
	aHeld, bHeld := make(chan struct{}), make(chan struct{})
	var pairWG sync.WaitGroup
	pairWG.Add(2)
	go func() {
		defer pairWG.Done()
		unlock := kl.lock("a")
		defer unlock()
		close(aHeld)
		<-bHeld
	}()
	go func() {
		defer pairWG.Done()
		unlock := kl.lock("b")
		defer unlock()
		close(bHeld)
		<-aHeld
	}()
	pairWG.Wait()

	// Concurrent contention across shared keys must serialize writes and leave zero entries once released.
	const workers = 16
	const iters = 50
	keys := []string{"k0", "k1", "k2", "k3"}
	counts := make([]int, len(keys))
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iters {
				idx := (id + i) % len(keys)
				unlock := kl.lock(keys[idx])
				counts[idx]++
				unlock()
			}
		}(w)
	}
	wg.Wait()

	want := (workers * iters) / len(keys)
	for i, k := range keys {
		if counts[i] != want {
			t.Fatalf("counts[%s] = %d, want %d", k, counts[i], want)
		}
	}
	kl.mu.Lock()
	remaining := len(kl.entries)
	kl.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("entries retained after unlock = %d, want 0", remaining)
	}
}
