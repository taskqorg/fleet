package main

import (
	"encoding/json"
	"os"
	osexec "os/exec"
	"testing"
)

func TestAssetJobIdentity(t *testing.T) {
	// Requirement: S7 adopts only exact original inputs; risk: a charged unrelated task is adopted after a lost POST.
	tool := mapOf(providerTools("meshy")["image_to_image"])
	args := doc{"reference_image_urls": []any{"data:image/png;base64,a", "data:image/png;base64,b"}, "prompt": "own character", "ai_model": "nano-banana-2", "generate_multi_view": true}
	clone := func() doc { var x doc; json.Unmarshal(jsonBytes(args), &x); return x }
	if !jobMatches(clone(), args, tool) {
		t.Fatal("exact identity refused")
	}
	for _, field := range []string{"reference_image_urls", "prompt", "ai_model", "generate_multi_view"} {
		x := clone()
		delete(x, field)
		if jobMatches(x, args, tool) {
			t.Fatalf("missing %s adopted", field)
		}
	}
	for field, value := range (doc{"reference_image_urls": []any{"data:image/png;base64,b", "data:image/png;base64,a"}, "prompt": "other", "ai_model": "other", "generate_multi_view": false}) {
		x := clone()
		x[field] = value
		if jobMatches(x, args, tool) {
			t.Fatalf("changed %s adopted", field)
		}
	}
	if jobMatches(doc{"image_urls": args["reference_image_urls"]}, args, tool) {
		t.Fatal("output views substituted for request inputs")
	}
	if jobMatches(clone(), args, doc{}) {
		t.Fatal("undeclared identity adopted")
	}
	started := float64(1794400000000)
	if !jobCreatedDuring(started/1000, started, started+1000) || !jobCreatedDuring(started, started, started+1000) {
		t.Fatal("timestamp units")
	}
	if jobCreatedDuring(0, started, started+1000) || jobCreatedDuring(started-6000, started, started+1000) || jobCreatedDuring(started+7000, started, started+1000) {
		t.Fatal("task outside attempt adopted")
	}
}

func TestSourceRigIdentity(t *testing.T) {
	// Requirement: source rig is bound to the exact generated task and height; risk: a different rig is adopted after lost POST.
	tool := mapOf(providerTools("meshy")["rigging"])
	args := doc{"input_task_id": "invented-task", "height_meters": 2.05}
	for _, candidate := range []doc{{"input_task_id": "invented-task"}, {"height_meters": 2.05}, {"input_task_id": "other", "height_meters": 2.05}, {"input_task_id": "invented-task", "height_meters": 1.8}} {
		if jobMatches(candidate, args, tool) {
			t.Fatal("incomplete/different rig identity adopted", candidate)
		}
	}
	if !jobMatches(args, args, tool) {
		t.Fatal("exact rig identity rejected")
	}
}

func TestAssetJobTools(t *testing.T) {
	// Requirement: explicit S7 status/list tools preserve defaults; risk: preparation polls the wrong provider job API.
	for _, tc := range []struct {
		step           doc
		status, recent string
	}{
		{doc{"tool": "image_to_3d"}, "job_status", "recent_tasks"},
		{doc{"tool": "multi_image_to_3d"}, "multi_job_status", "multi_recent_tasks"},
		{doc{"tool": "image_to_image", "status_tool": "image_job_status", "recent_tool": "image_recent_tasks"}, "image_job_status", "image_recent_tasks"},
	} {
		if jobTool(tc.step, "job_status") != tc.status || jobTool(tc.step, "recent_tasks") != tc.recent {
			t.Fatal(tc)
		}
	}
	step := doc{"id": "prep", "kind": "api", "connection": "meshy", "tool": "image_to_image", "mode": "job", "arguments": doc{}, "status_tool": "image_job_status", "recent_tool": "image_recent_tasks"}
	definition := doc{"version": 2, "id": "character-pilot", "steps": []any{step}}
	if _, err := validatePipeline(definition); err != nil {
		t.Fatal(err)
	}
	step["mode"] = "sync"
	if _, err := validatePipeline(definition); err == nil {
		t.Fatal("custom polling on sync call accepted")
	}
	step["mode"] = "job"
	step["status_tool"] = ""
	if _, err := validatePipeline(definition); err == nil {
		t.Fatal("empty status tool accepted")
	}
}

func TestAssetViewEncoderBoundary(t *testing.T) {
	// Requirement: S7 verifies view bytes and mapping before a paid POST; risk: the portable encoder checks are never run in CI.
	command := osexec.Command("python3", "../../tests/assets/character-views.test.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("view encoder: %v\n%s", err, output)
	}
}

func TestCharacterPipelineDefinitions(t *testing.T) {
	// Requirement: all explicit S7 methods are executable registered definitions; risk: a missing dependency or wrong output reference breaks the shared downstream path.
	for _, name := range []string{"character-multiview", "character-multiview-hf", "character-multiview-meshy-prep"} {
		raw, err := os.ReadFile("../../config/pipelines/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var definition doc
		if err = json.Unmarshal(raw, &definition); err != nil {
			t.Fatal(err)
		}
		if _, err = validatePipeline(definition); err != nil {
			t.Fatal(name, err)
		}
	}
}
