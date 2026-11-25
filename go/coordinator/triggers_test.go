package main

import (
	"testing"
	"time"

	"fleet/coordinator/db"
)

// Requirement (docs/designs/fleet-triggers.md, Pass): a trigger every pass checks on each pass. Risk: its next time
// falls a moment after the next pass, so it waits two passes (30 s instead of 15 s on prod).
func TestTriggerDueWithinHalfPass(t *testing.T) {
	now := time.Now()
	tick := time.Duration(triggerTickMs()) * time.Millisecond
	every := triggerTickMs()
	for _, c := range []struct {
		next time.Duration
		due  bool
	}{{0, true}, {tick/2 - time.Millisecond, true}, {tick/2 + time.Millisecond, false}, {tick, false}} {
		next := now.Add(c.next)
		if _, due := triggerDue(db.Trigger{Enabled: true, WhenKind: "every", EveryMs: &every, NextAt: &next}, now); due != c.due {
			t.Fatalf("next in %v: due %v, want %v", c.next, due, c.due)
		}
	}
}
