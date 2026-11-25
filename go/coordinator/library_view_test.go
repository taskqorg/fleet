package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: library_view (pages/library.md) answers the tree's lists in one read, as definition_list, skills_list,
// bot_list, system_changes and recommendation_changes would, without the 30-day resource rollup. Risk: a list is
// missing from the tree, or the rollup creeps back and the read costs seconds again.
func TestLibraryView(t *testing.T) {
	e := openTestCoordinator(t, false)
	var got contract.LibraryViewReply
	e.opInto(t, "library_view", map[string]any{}, e.ownerHeaders(), &got)
	var agents contract.DefinitionListReply
	e.opInto(t, "definition_list", map[string]any{"kind": "agent"}, e.ownerHeaders(), &agents)
	if len(got.Agents) == 0 || len(got.Agents) != len(agents.Items) {
		t.Fatalf("agents: %d in the view, %d in definition_list", len(got.Agents), len(agents.Items))
	}
	for _, a := range got.Agents {
		if r := a.Last30.Resources; r == nil || r.Tokens.Total != nil || len(r.Cost) != 0 {
			t.Fatalf("%s: last30 carries a resource rollup: %+v", a.Id, r)
		}
	}
	if got.Pipelines == nil || got.Methods == nil || got.Skills == nil || got.Bots == nil || got.Recommendations == nil || got.Changes.Versions.Hosts == nil {
		t.Fatalf("every list is present (empty, not null): %+v", got)
	}
}
