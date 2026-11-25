package main

import (
	"errors"
	"testing"

	"fleet/contract"
)

func planFixture(t *testing.T, id string) doc {
	t.Helper()
	definition := doc{"version": float64(3), "id": "guard", "resultStep": "final", "steps": []any{
		doc{"id": "route", "kind": "script", "command": "fixture-route"},
		doc{"id": "review", "kind": "script", "command": "fixture-review", "when": doc{"kind": "enum", "source": doc{"step": "route", "path": []any{"action"}}, "domain": []any{"retain_candidate", "qualified_review"}, "equals": "qualified_review"}},
		doc{"id": "final", "kind": "script", "command": "fixture-final"},
	}}
	steps := []any{}
	for _, v := range anys(definition["steps"]) {
		d := mapOf(v)
		steps = append(steps, doc{"id": d["id"], "kind": d["kind"]})
	}
	run := doc{"id": id, "pipelineId": "guard", "projectId": "hello-world", "requestedBy": doc{"email": testOwnerEmail}, "definition": definition, "definitionSha256": sha256hex([]byte(canonical(definition))), "state": "running", "startedAt": nowMs(), "steps": steps}
	if err := initPipelinePlan(run); err != nil {
		t.Fatal(err)
	}
	return run
}

// Requirement: ordinary rerun cannot turn an ambiguous API outcome into a new
// provider invocation. Risk: a retry label bypasses the durable handoff fence.
func TestPipelineRecoveryRejectsUnconfirmedExternalAction(t *testing.T) {
	run := planFixture(t, "unknown-action")
	definition := mapOf(run["definition"])
	mapOf(anys(definition["steps"])[0])["kind"] = "api"
	entry := pipelineEntry(run, 0)
	entry.Activation, entry.ExecutionRef, entry.Dispatch = "selected", &contract.PipelineExecutionRef{Mode: "fresh"}, &contract.PipelineDispatch{Phase: "handed_off"}
	mapOf(anys(run["steps"])[0])["observation"] = "unconfirmed"
	var conflict *HTTPError
	if err := pipelineRecoveryFence(nil, run); !errors.As(err, &conflict) || conflict.Code != 409 {
		t.Fatal("ambiguous action accepted for replay", err)
	}
}
