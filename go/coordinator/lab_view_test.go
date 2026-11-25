package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: lab_view (pages/laboratory.md) answers the Lab screen as its four reads would: the experiments, the
// registry, the open ratings and the last 30 days' leaderboard. Risk: a part drifts from its own operation.
func TestLabView(t *testing.T) {
	e := openTestCoordinator(t, false)
	var got contract.LabViewReply
	e.opInto(t, "lab_view", map[string]any{}, e.ownerHeaders(), &got)
	var reg contract.LabRegistryReply
	e.opInto(t, "lab_registry", map[string]any{}, e.ownerHeaders(), &reg)
	var lb contract.LabLeaderboardReply
	e.opInto(t, "lab_leaderboard", map[string]any{"days": 30}, e.ownerHeaders(), &lb)
	if len(got.UseCases) == 0 || len(got.UseCases) != len(reg.UseCases) {
		t.Fatalf("use cases: %d in the view, %d in lab_registry", len(got.UseCases), len(reg.UseCases))
	}
	if got.Experiments == nil || got.Ratings == nil || len(got.Leaderboard.Entries) != len(lb.Entries) || len(got.Leaderboard.Collections) != len(lb.Collections) {
		t.Fatalf("parts: %+v", got)
	}
}
