package main

import (
	"encoding/json"
	"os"
	"testing"

	"fleet/contract"
)

// Requirement (fleet-definition-editors.md, One home per kind): a head someone took from the repository follows the
// next release like a head nobody touched. Risk: the take is a Fleet edit to reconcile, so the definition falls behind
// at the next release again and a person copies it by hand (prod, 2026-11-26: 9 definitions, about 25 hand copies).
func TestTakenRepositoryVersionFollowsTheNextRelease(t *testing.T) {
	e := openTestCoordinator(t, false)
	files, broken := shippedDefinitions()
	release := func(description string) {
		t.Helper()
		next := make([]definitionFile, 0, len(files))
		for _, f := range files {
			if f.kind == "pipeline" && f.id == "search-basic" {
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
	get := func() contract.DefinitionGetReply {
		t.Helper()
		var r contract.DefinitionGetReply
		e.opInto(t, "definition_get", map[string]any{"kind": "pipeline", "id": "search-basic"}, e.ownerHeaders(), &r)
		return r
	}
	head := get()
	var edited map[string]any
	json.Unmarshal([]byte(head.Text), &edited)
	edited["description"] = "Edited in Fleet."
	var saved map[string]any
	e.opInto(t, "definition_save", map[string]any{"kind": "pipeline", "id": "search-basic", "baseRevision": head.Revision, "content": map[string]any{"pipeline": edited}, "message": "hotfix"}, e.ownerHeaders(), &saved)
	release("Shipped one.")
	if s := get().State; s != contract.DefinitionStateRepositoryChanged {
		t.Fatalf("an edited head stays at a release: %s", s)
	}
	var took contract.DefinitionTakeRepositoryReply
	e.opInto(t, "definition_take_repository", map[string]any{"preview": false, "keys": []any{map[string]any{"kind": "pipeline", "id": "search-basic"}}}, e.ownerHeaders(), &took)
	if took.Applied != 1 || get().Meta.Source != contract.RevisionSourceRepo {
		t.Fatalf("take repository: %+v, source %s", took, get().Meta.Source)
	}
	taken := get().Revision
	release("Shipped two.")
	after := get()
	var d map[string]any
	json.Unmarshal([]byte(after.Text), &d)
	if after.Revision != taken+1 || after.State != contract.DefinitionStateSynced || d["description"] != "Shipped two." {
		t.Fatalf("a taken head follows the next release: r%d -> r%d %s %v", taken, after.Revision, after.State, d["description"])
	}
}

// Requirement: a decided experiment's Lab variants leave the store, except the adopted one and any that something
// else still uses. Risk: variants pile up as "made in Fleet" after their experiment (prod: 24 of 31 store-only).
func TestDecidedExperimentRetiresItsVariants(t *testing.T) {
	e := openTestCoordinator(t, false)
	raw, err := os.ReadFile(profilesDir() + "/claude-worker.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"lv-dropped", "lv-adopted", "lv-shared"} {
		var p map[string]any
		json.Unmarshal(raw, &p)
		p["id"] = id
		var out map[string]any
		e.opInto(t, "definition_create", map[string]any{"kind": "agent", "id": id, "content": map[string]any{"agent": p}, "message": "variant"}, e.ownerHeaders(), &out)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`SELECT set_config('fleet.project_ids', '*', false)`)
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id, state, variants) VALUES ('lv-x', 'fleet', 'ab', 'search-basic', 'done',
	 '[{"label":"a","overrides":{"profile":"lv-dropped"}},{"label":"b","overrides":{"profile":"lv-adopted"}},{"label":"c","overrides":{"profile":"lv-shared"}}]')`)
	exec(`INSERT INTO experiment (id, project_id, kind, pipeline_id, state, variants) VALUES ('lv-y', 'fleet', 'single', 'search-basic', 'draft',
	 '[{"label":"c","overrides":{"profile":"lv-shared"}}]')`)
	var decided map[string]any
	e.opInto(t, "lab_experiment_decide", map[string]any{"experimentId": "lv-x", "decision": "adopt b"}, e.ownerHeaders(), &decided)
	for id, want := range map[string]int{"lv-dropped": 404, "lv-adopted": 200, "lv-shared": 200} {
		if code, _ := e.callJSON(t, "POST", "/api/ops/definition_get", map[string]any{"kind": "agent", "id": id}, e.ownerHeaders()); code != want {
			t.Fatalf("%s after the decision: %d, want %d", id, code, want)
		}
	}
	// The shipped pipeline the experiment ran is the repository's: never deleted.
	if code, _ := e.callJSON(t, "POST", "/api/ops/definition_get", map[string]any{"kind": "pipeline", "id": "search-basic"}, e.ownerHeaders()); code != 200 {
		t.Fatalf("shipped pipeline: %d", code)
	}
}
