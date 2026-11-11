package main

import "testing"

// Requirement: fleet-people-access.md section 8 "Dispatch": order soonest_reset puts the ready candidate whose weekly
// limit resets first (with weekly left above 0) ahead; a full or unknown one keeps its place after; skipped candidates
// stay where they are. Risk: limit about to reset is wasted while a candidate with a later reset is used.
func TestSoonestResetOrder(t *testing.T) {
	pf := func(v float64) *float64 { return &v }
	sub := func(id string) doc { return doc{"id": id} }
	cs := []*candidate{{Subscription: sub("late")}, {Subscription: sub("skip"), Skipped: "offline"}, {Subscription: sub("full")}, {Subscription: sub("soon")}, {Subscription: sub("none")}}
	readings := map[string][]Reading{
		"late": {{Period: "weekly", Pct: pf(10), ResetsAt: pf(9e12), State: "known"}},
		"full": {{Period: "weekly", Pct: pf(100), ResetsAt: pf(1e12), State: "known"}},
		"soon": {{Period: "weekly-opus", Pct: pf(50), ResetsAt: pf(2e12), State: "known"}},
	}
	soonestReset(cs, readings)
	got := ""
	for _, c := range cs {
		got += astr(c.Subscription["id"]) + " "
	}
	if got != "soon skip late full none " {
		t.Fatal(got)
	}
}
