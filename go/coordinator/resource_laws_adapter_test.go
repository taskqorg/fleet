package main

import (
	"context"
	"time"
)

// The law harness reaches the engine only through these functions, so the laws stay the same while the engine
// changes underneath them (DESIGN_RULES.md).

// lawConfigure sets the retry base and maximum the laws run with and returns a restore function.
func lawConfigure(base, maximum time.Duration) func() {
	b, m := kitRetryBase, kitRetryMax
	kitRetryBase, kitRetryMax = base, maximum
	return func() { kitRetryBase, kitRetryMax = b, m }
}

// lawNow: the processor's clock: real time moved forward by every advance, so new work is due at once.
func lawNow(e *lawEnv) time.Time { return time.Now().Add(e.now) }

// lawAttach gives a (a new process's App) its kit: the fake clock, a seeded jitter and the seed's cohort size.
func lawAttach(e *lawEnv, a *App) {
	k := newWorkKit(a, func() time.Time { return lawNow(e) }, uint64(e.seed))
	k.cohort = 2 + int(e.seed%7)
	kitDefault.Store(a, k)
	e.t.Cleanup(func() { kitDefault.Delete(a) })
}

// lawPass runs one processor pass of every queue.
func lawPass(ctx context.Context, e *lawEnv, a *App) error { return kitOf(a).pass(ctx) }

// lawStep runs the replacement of one execution on its own.
func lawStep(ctx context.Context, e *lawEnv, a *App, id string) error { return resourceReplace(ctx, a, id) }

// lawAdvance moves the processor's clock forward by d (the harness adds d to e.now).
func lawAdvance(e *lawEnv, d time.Duration) {}

// lawRestart: a new process starts with a new kit (lawAttach on open).
func lawRestart(e *lawEnv) {}

// lawMemoryFailing: items the processor keeps in memory as failing; the kit keeps none.
func lawMemoryFailing() int { return 0 }
