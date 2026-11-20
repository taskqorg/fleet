// Background scheduling (docs/designs/fleet-standard.md S6): the coordinator's periodic passes run only through every()
// and its coalesced after-commit effects only through coalesced(); retried item work is the work kit's (work_kit.go).
// This file owns the goroutines and timers those need (designRulesOwned).
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"fleet/obs"
)

// every runs fn on a ticker until ctx ends, each run as the pass span pass.<name> in fn's ctx; single flight (a slow
// pass never overlaps the next); d <= 0 turns it off.
func every(ctx context.Context, name string, d time.Duration, fn func(context.Context) error) {
	if d <= 0 {
		return
	}
	passIntervals.Store(name, d)
	go func() {
		// The first run is at most a minute after start: a deploy more often than d restarts the timer, so a pass that
		// waited a full interval first would never run (absence-pass, 2026-11-12: reconcile and delivery starved by
		// deploys); the minute lets hosts poll before the rules judge them silent.
		t := time.NewTimer(min(d, time.Minute))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			obs.Pass(ctx, name, func(ctx context.Context) (int, error) { return 0, fn(ctx) })
			t.Reset(d)
		}
	}()
}

// coalesced returns a trigger that runs fn soon in the background, once for every burst of triggers, with the
// argument of the burst's last trigger: a trigger while a run is queued joins it; one during a run queues the next.
// Callers trigger after their commit (afterCommit), so the run reads what they wrote; what the run needs from the
// caller's state is its argument, read by the caller, never a global read in the background. A failure is logged as
// name.
func coalesced[T any](name string, fn func(context.Context, T) error) func(T) {
	var mu sync.Mutex
	queued := false
	var arg T
	return func(v T) {
		mu.Lock()
		defer mu.Unlock()
		arg = v
		if queued {
			return
		}
		queued = true
		go func() {
			mu.Lock()
			queued = false
			v := arg
			mu.Unlock()
			if err := fn(context.Background(), v); err != nil {
				log.Printf("%s: %v", name, err)
			}
		}()
	}
}

// untilDone runs fn now and then every d until it succeeds or ctx ends (a start-up step that waits for a dependency,
// such as the span store's schema). A failure is logged as name.
func untilDone(ctx context.Context, name string, d time.Duration, fn func(context.Context) error) {
	go func() {
		for ctx.Err() == nil {
			err := fn(ctx)
			if err == nil {
				return
			}
			log.Printf("%s: %v", name, err)
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}()
}
