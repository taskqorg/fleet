package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Requirement: a pipeline's structure reads as numbered plain lines, the same for people and agents.
// Risk: the outline names interpreters and environment paths, loses which step reads which, or hides a condition.
func TestPipelineOutline(t *testing.T) {
	when := doc{"kind": "enum", "source": doc{"step": "route", "path": []any{"action"}}, "domain": []any{"retain", "review"}, "equals": "review"}
	b, _ := json.Marshal(doc{"version": 3, "id": "p", "description": "Routes and reviews.", "resultStep": "result", "steps": []any{
		doc{"id": "route", "kind": "api", "connection": "openai-2", "tool": "create_response", "arguments": doc{"model": "gpt-6.2-sol", "input": "x"}},
		doc{"id": "review", "kind": "agent", "profile": "reviewer", "prompt": "Read ${steps.route.output.text}", "when": when},
		doc{"id": "fix", "kind": "agent", "profile": "fixer", "description": "fixes what the review found", "prompt": "${steps.review.output}", "when": when},
		doc{"id": "result", "kind": "script", "command": `python3 "$FLEET_LAB_SCRIPTS"/doc_recognize.py finish --from route`},
	}})
	d, err := validatePipelineDef(b)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range pipelineOutline(d) {
		got = append(got, strings.Repeat("  ", int(l.Depth))+l.Text)
	}
	want := []string{
		"gpt-6.2-sol (create_response, openai-2)",
		"if route.action is review",
		"  agent reviewer · reads route",
		"  fixes what the review found · agent fixer · reads review",
		"doc_recognize.py finish",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outline:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Requirement: every pipeline in the repository validates and has an outline line per step.
// Risk: a description field breaks loading, or a step disappears from the outline.
func TestPipelineOutlineRepository(t *testing.T) {
	files, _ := os.ReadDir("../../config/pipelines")
	for _, f := range files {
		b, err := os.ReadFile("../../config/pipelines/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		d, err := validatePipelineDef(b)
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		steps := 0
		for _, l := range pipelineOutline(d) {
			if l.N != nil {
				steps++
			}
		}
		if steps != len(d.Steps) {
			t.Fatalf("%s: %d outline steps for %d", f.Name(), steps, len(d.Steps))
		}
	}
}

func TestPipelineDescriptionMustBeText(t *testing.T) {
	for _, bad := range []string{
		`{"version":2,"id":"p","description":1,"steps":[{"id":"a","kind":"script","command":"x"}]}`,
		`{"version":2,"id":"p","steps":[{"id":"a","kind":"script","command":"x","description":["x"]}]}`,
	} {
		if _, err := validatePipelineDef([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
