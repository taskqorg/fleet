package main

import (
	"testing"
	"time"

	"fleet/coordinator/db"
)

func TestBotAutomationScheduleClockChanges(t *testing.T) {
	// FLT-26: repeated local times must not repeat a wake and nonexistent local
	// times must not silently move to a different clock time.
	a := db.Trigger{WhenKind: "daily", Enabled: true, DailyTime: "02:30", DailyZone: "Europe/Paris"}
	spring := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)
	if _, due := dueDaily(a, spring); due {
		t.Fatal("woke on a nonexistent local slot")
	}
	next := nextDaily(a, spring)
	if next == nil || !next.Equal(time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected next valid local date, got %v", next)
	}
	fall := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	date, due := dueDaily(a, fall)
	if !due {
		t.Fatal("no wake after repeated local hour")
	}
	a.LastSlot = date
	if _, due := dueDaily(a, fall.Add(time.Minute)); due {
		t.Fatal("repeated local date woke twice")
	}
}
