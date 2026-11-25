package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// Requirement: a definition nobody changed in Fleet follows the repository on a release; one changed in Fleet stays
// and shows repository-changed. Risk: a merged repository change never reaches the store (prod ran pipelines without the
// descriptions #1041 shipped, 2026-11-26), or a release overwrites an edit made in Fleet.
func TestDefinitionsFollowTheRepository(t *testing.T) {
	e := openTestCoordinator(t, false)
	files, broken := shippedDefinitions()
	release := func(id, description string) {
		t.Helper()
		next := make([]definitionFile, 0, len(files))
		for _, f := range files {
			if f.kind == "pipeline" && f.id == id {
				var d map[string]any
				json.Unmarshal(f.raw, &d)
				d["description"] = description
				f.raw, _ = json.Marshal(d)
			}
			next = append(next, f)
		}
		if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
			return reconcileDefinitions(q, next, broken, "test-"+description)
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) contract.DefinitionGetReply {
		t.Helper()
		var r contract.DefinitionGetReply
		e.opInto(t, "definition_get", map[string]any{"kind": "pipeline", "id": id}, e.ownerHeaders(), &r)
		return r
	}

	before := get("search-basic")
	release("search-basic", "Searches the web.")
	after := get("search-basic")
	if after.Revision != before.Revision+1 || after.State != contract.DefinitionStateSynced || after.Content.Pipeline == nil {
		t.Fatalf("a synced head follows the repository: %d -> %d %s", before.Revision, after.Revision, after.State)
	}
	var d map[string]any
	json.Unmarshal([]byte(after.Text), &d)
	if d["description"] != "Searches the web." {
		t.Fatalf("head content: %s", after.Text)
	}

	// An edit in Fleet: the next release records the repository and leaves the head.
	var edited map[string]any
	json.Unmarshal([]byte(after.Text), &edited)
	edited["description"] = "Edited in Fleet."
	var saved map[string]any
	e.opInto(t, "definition_save", map[string]any{"kind": "pipeline", "id": "search-basic", "baseRevision": after.Revision, "content": map[string]any{"pipeline": edited}, "message": "edit"}, e.ownerHeaders(), &saved)
	mid := get("search-basic")
	release("search-basic", "Searches the web, again.")
	last := get("search-basic")
	if last.Revision != mid.Revision || last.State != contract.DefinitionStateRepositoryChanged {
		t.Fatalf("an edited head stays: %d -> %d %s", mid.Revision, last.Revision, last.State)
	}
}
