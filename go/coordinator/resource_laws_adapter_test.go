package main

import (
	"context"
	"time"
)

// The law harness reaches the engine only through these functions, so the laws stay the same while the engine
// changes underneath them (DESIGN_RULES.md).

// lawConfigure sets the retry base and maximum the laws run with and returns a restore function.
func lawConfigure(base, maximum time.Duration) func() {
	b, m := resourceRetryBase, resourceRetryMax
	resourceRetryBase, resourceRetryMax = base, maximum
	return func() { resourceRetryBase, resourceRetryMax = b, m }
}

// lawPass runs one processor pass of every queue.
func lawPass(ctx context.Context, e *lawEnv, a *App) error { return resourceProcessorPass(ctx, a) }

// lawStep runs the replacement of one execution on its own.
func lawStep(ctx context.Context, e *lawEnv, a *App, id string) error {
	return resourceReplace(ctx, a, id)
}

// lawAdvance moves the processor's clock forward by d. Today's processor reads the database clock, so the pauses it
// wrote move back instead, and so do the holds it keeps in memory.
func lawAdvance(e *lawEnv, d time.Duration) {
	e.owner(`UPDATE resource_work SET next_attempt_at=next_attempt_at-$1*interval '1 microsecond'`, d.Microseconds())
	e.owner(`UPDATE resource_build SET next_attempt_at=next_attempt_at-$1*interval '1 microsecond'`, d.Microseconds())
	e.owner(`UPDATE resource_metadata_repair SET next_attempt_at=next_attempt_at-$1*interval '1 microsecond'`, d.Microseconds())
	resourceHeld.mu.Lock()
	for k, it := range resourceHeld.items {
		it.until = it.until.Add(-d)
		resourceHeld.items[k] = it
	}
	resourceHeld.mu.Unlock()
}

// lawRestart forgets everything the processor keeps in memory, as a new process would.
func lawRestart(e *lawEnv) {
	resourceHeld.mu.Lock()
	resourceHeld.items = map[string]resourceHold{}
	resourceHeld.mu.Unlock()
}

// lawMemoryFailing: items the processor holds in memory whose failures reached the failing count.
func lawMemoryFailing() int { return len(resourceHeld.failing(resourceFailingAttempts())) }
