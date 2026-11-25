package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// pipeline_run inputs: each path is a distinct relative file path outside Fleet's reserved folders, checked before any
// artifact read.
func TestPipelineRunInputPaths(t *testing.T) {
	for _, bad := range [][]contract.PipelineRunInput{
		{{Path: "../passport.jpg", ArtifactId: "a"}},
		{{Path: "/etc/passwd", ArtifactId: "a"}},
		{{Path: "__fleet_control/pipeline.json", ArtifactId: "a"}},
		{{Path: "", ArtifactId: "a"}},
		{{Path: "a.jpg", ArtifactId: "a"}, {Path: "a.jpg", ArtifactId: "b"}},
	} {
		if _, err := pipelineRunInputs(&Request{}, "dam", bad); err == nil {
			t.Fatalf("inputs %v must be refused", bad)
		}
	}
	if out, err := pipelineRunInputs(&Request{}, "dam", nil); err != nil || len(out) != 0 {
		t.Fatalf("no inputs: %v %v", out, err)
	}
}

// A promoted method carries its overrides (the Lab variant's profile): pipeline_run reads them from the catalogue.
func TestPipelineCatalogueMethodOverrides(t *testing.T) {
	var c pipelineCatalogue
	raw := `{"recommendedMethod":"m","methods":[{"id":"m","pipeline":"doc-recognize-agent.json","overrides":{"profile":"doc-reader-codex"}}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	var o pipelineOverrides
	if err := json.Unmarshal(c.Methods[0].Overrides, &o); err != nil || o.Profile != "doc-reader-codex" {
		t.Fatalf("method overrides lost: %s %v", c.Methods[0].Overrides, err)
	}
}
