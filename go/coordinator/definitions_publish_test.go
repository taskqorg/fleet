package main

import (
	"encoding/json"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement (fleet-definition-editors.md, One home per kind): a save of a product definition in Fleet sends it to
// the repository by itself, as one Fleet task in the project of Fleet's repository; a revision is sent once; a store-only
// definition goes when its author publishes it, unless a live experiment names it (a Lab variant). Risk: Fleet edits
// stay only in the store and a person copies them to git by hand (prod, 2026-11-26).
func TestSaveSendsTheEditToTheRepository(t *testing.T) {
	e := openTestCoordinator(t, false)
	var created map[string]any
	e.opInto(t, "project_create", map[string]any{"requestId": "pub-1", "id": "fleet-repo", "content": map[string]any{
		"settings": map[string]any{"id": "fleet-repo", "name": "fleet-repo", "contextFiles": []any{"rules.md"}, "roles": map[string]any{"worker": "claude-worker"},
			"repos": []any{map[string]any{"id": "main", "remote": "https://github.com/taskqorg/fleet.git", "forge": "github", "defaultBranch": "main"}}},
		"contextFiles": []any{map[string]any{"path": "rules.md", "text": "Rules."}}}}, e.ownerHeaders(), &created)

	var head contract.DefinitionGetReply
	e.opInto(t, "definition_get", map[string]any{"kind": "pipeline", "id": "search-basic"}, e.ownerHeaders(), &head)
	var edited map[string]any
	json.Unmarshal([]byte(head.Text), &edited)
	edited["description"] = "Hotfix in Fleet."
	var saved map[string]any
	e.opInto(t, "definition_save", map[string]any{"kind": "pipeline", "id": "search-basic", "baseRevision": head.Revision, "content": map[string]any{"pipeline": edited}, "message": "hotfix"}, e.ownerHeaders(), &saved)

	var changes contract.SystemChangesReply
	e.opInto(t, "system_changes", map[string]any{}, e.ownerHeaders(), &changes)
	var row *contract.SystemChangeDefinition
	for i := range changes.Ahead {
		if changes.Ahead[i].Id == "search-basic" {
			row = &changes.Ahead[i]
		}
	}
	if row == nil || row.Publish == nil || row.Publish.Code == "" {
		t.Fatalf("a saved hotfix names its publish task: %+v", changes.Ahead)
	}
	var task contract.TaskDetail
	e.opInto(t, "task_get", map[string]any{"taskId": row.Publish.TaskId}, e.ownerHeaders(), &task)
	if b, _ := json.Marshal(task); !strings.Contains(string(b), "Hotfix in Fleet.") || !strings.Contains(string(b), "fleet-repo") {
		t.Fatalf("the task carries the file in the Fleet repository's project: %s", b)
	}

	// The same revision is not sent twice.
	var again contract.DefinitionPublishReply
	e.opInto(t, "definition_publish", map[string]any{"keys": []any{map[string]any{"kind": "pipeline", "id": "search-basic"}}, "preview": false}, e.ownerHeaders(), &again)
	if again.TaskId != nil || again.Items[0].Status != contract.BulkOpStatusSkipped {
		t.Fatalf("a sent revision is skipped: %+v", again)
	}

	// The repository changes the file under the hotfix: a conflict, with no publish shown, until the person keeps theirs.
	files, broken := shippedDefinitions()
	for i, f := range files {
		if f.kind == "pipeline" && f.id == "search-basic" {
			var d map[string]any
			json.Unmarshal(f.raw, &d)
			d["description"] = "Changed in the repository."
			files[i].raw, _ = json.Marshal(d)
		}
	}
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return reconcileDefinitions(q, files, broken, "test-conflict") }); err != nil {
		t.Fatal(err)
	}
	e.opInto(t, "system_changes", map[string]any{}, e.ownerHeaders(), &changes)
	if len(changes.RepositoryChanged) != 1 || changes.RepositoryChanged[0].Publish != nil {
		t.Fatalf("a conflict needs a person: %+v", changes.RepositoryChanged)
	}
	var kept contract.DefinitionPublishReply
	e.opInto(t, "definition_publish", map[string]any{"keys": []any{map[string]any{"kind": "pipeline", "id": "search-basic"}}, "preview": false, "keep": true}, e.ownerHeaders(), &kept)
	e.opInto(t, "system_changes", map[string]any{}, e.ownerHeaders(), &changes)
	if kept.TaskId == nil || len(changes.RepositoryChanged) != 0 || len(changes.Ahead) != 1 || changes.Ahead[0].Publish == nil || changes.Ahead[0].Publish.TaskId != *kept.TaskId {
		t.Fatalf("keep mine sends the Fleet version over the repository's: %+v, %+v", kept, changes)
	}

	// A store-only definition a live experiment names is a Lab variant: not sent.
	edited["id"] = "pub-variant"
	edited["description"] = "Variant."
	e.opInto(t, "definition_create", map[string]any{"kind": "pipeline", "id": "pub-variant", "content": map[string]any{"pipeline": edited}, "message": "variant"}, e.ownerHeaders(), &created)
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO experiment (id, project_id, kind, pipeline_id, state, variants) VALUES ('pub-x', 'fleet-repo', 'ab', 'search-basic', 'draft', '[{"label":"v","pipelineId":"pub-variant"}]')`); err != nil {
		t.Fatal(err)
	}
	var lab contract.DefinitionPublishReply
	e.opInto(t, "definition_publish", map[string]any{"keys": []any{map[string]any{"kind": "pipeline", "id": "pub-variant"}}, "preview": false}, e.ownerHeaders(), &lab)
	if lab.TaskId != nil || lab.Items[0].Reason == nil || !strings.Contains(*lab.Items[0].Reason, "Lab variant") {
		t.Fatalf("a live Lab variant is not sent: %+v", lab)
	}
	e.opInto(t, "system_changes", map[string]any{}, e.ownerHeaders(), &changes)
	if len(changes.Experiments) != 1 || changes.Experiments[0].Id != "pub-x" || len(changes.StoreOnly) != 1 || deref(changes.StoreOnly[0].Experiment) != "pub-x" {
		t.Fatalf("Changes shows the variant under its experiment: %+v, %+v", changes.Experiments, changes.StoreOnly)
	}
}
