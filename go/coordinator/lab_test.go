package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: a Lab script check observes cancellation (docs/designs/fleet-lab.md, script evaluators). Risk: a hung check
// keeps the experiment busy forever and graceful shutdown cannot stop it; a cancelled check must not be recorded as failed.
func TestRunChecksStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := runChecks(ctx, []any{doc{"id": "hang", "run": "sleep 30"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatalf("runChecks = %v, %v; want no result and the context error", r, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the check ran %v after cancellation", d)
	}
}

func TestRegistryLoadRefusesBadEntries(t *testing.T) {
	t.Setenv("FLEET_LAB_DIRECTORY", "../../config/lab")
	if _, err := useCases(); err != nil {
		t.Fatalf("repo registry: %v", err)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "use-cases"), 0o755)
	t.Setenv("FLEET_LAB_DIRECTORY", dir)
	base := `{"id":"x:1","source":{"repo":"r","path":"p","anchor":"a","commit":"c","sha256":"s"},"source_status":"current","actor":"a","goal":"g","expected_outcome":"e","checks":[],"lab_fit":"none","tags":[],"maturity":"m"`
	for name, entry := range map[string]string{"maps_to": base + `,"maps_to":["fleet:UC99"]}`, "field": base + `,"extra":1}`, "status": base + `,"status":"gone"}`} {
		os.WriteFile(filepath.Join(dir, "use-cases", "x.json"), []byte("["+entry+"]"), 0o644)
		if _, err := useCases(); err == nil || !strings.Contains(err.Error(), "x:1") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestVariantArgumentsMergeOverStep(t *testing.T) {
	// Requirement: S4 variant arguments apply without base arguments; risk: silent omission.
	if got := mapOf(withArguments(doc{"id": "gen"}, doc{"arguments": doc{"gen": doc{"topology": "quad"}}})); got["topology"] != "quad" {
		t.Fatalf("override without base arguments: %v", got)
	}
	def := doc{"id": "gen", "arguments": doc{"image_url": "${steps.encode.output.data_uri}", "target_polycount": 3000}}
	got := mapOf(withArguments(def, doc{"arguments": doc{"gen": doc{"target_polycount": 8000, "topology": "quad"}, "other": doc{"x": 1}}}))
	if got["target_polycount"] != 8000 || got["topology"] != "quad" || got["image_url"] != "${steps.encode.output.data_uri}" || mapOf(def["arguments"])["target_polycount"] != 3000 {
		t.Fatalf("%v", got)
	}
}

func TestJudgeArtifactInputsReachSteps(t *testing.T) {
	// Requirement: S5 stages binary trial artifacts; risk: dropping inputs without text example files.
	run := doc{"example": doc{}, "exampleInputs": []any{doc{"artifactId": "render-artifact", "path": "example/trial/renders/front.png"}}, "steps": []any{}}
	got, err := inputsFor(nil, run, 0)
	if err != nil || len(got) != 1 || mapOf(got[0])["artifactId"] != "render-artifact" || mapOf(got[0])["path"] != "example/trial/renders/front.png" {
		t.Fatalf("binary artifact inputs: %v, %v", got, err)
	}
}

func TestJudgeRerunRetainsArtifactInputs(t *testing.T) {
	// Requirement: judge reruns receive original renders; risk: a rerun silently judges no files.
	original := doc{"exampleInputs": []any{doc{"artifactId": "immutable-png", "path": "example/trial/front.png"}}}
	rerun := rerunInputs(original, "all")
	rerun["example"], rerun["steps"] = doc{}, []any{}
	got, err := inputsFor(nil, rerun, 0)
	if err != nil || len(got) != 1 || mapOf(got[0])["artifactId"] != "immutable-png" {
		t.Fatalf("rerun inputs: %v, %v", got, err)
	}
}

func TestPairwiseOrderDependenceAndVersions(t *testing.T) {
	// Requirement: S5 never scores position bias and separates rubrics; risk: false agreement across versions.
	run := func(id, value string, order int) doc {
		return doc{"id": id, "state": "succeeded", "judge": doc{"evaluator": "judge:pair:pick_best", "groupId": "g", "trialIds": []any{"a", "b"}, "order": order}, "steps": []any{doc{"output": doc{"value": value}}}}
	}
	view := pairwiseView([]doc{run("one", "a", 0), run("two", "b", 1)})
	if len(view) != 1 || mapOf(view[0])["reason"] != "order-dependent" {
		t.Fatalf("%v", view)
	}
	scores := []doc{
		{"evaluator": "human:owner:pick_best", "form": "pick_best", "trialIds": []any{"b", "a"}, "value": "a"},
		{"evaluator": "judge:pair:pick_best", "evaluatorVersion": "v1", "form": "pick_best", "trialIds": []any{"a", "b"}, "value": "a"},
		{"evaluator": "judge:pair:pick_best", "evaluatorVersion": "v2", "form": "pick_best", "trialIds": []any{"a", "b"}, "value": "b"},
	}
	a := agreement(scores)
	vs := anys(a["byVersion"])
	if len(vs) != 2 || mapOf(vs[0])["matched"] != 1 || mapOf(vs[1])["matched"] != 0 {
		t.Fatalf("%v", a)
	}
}

func TestPairwiseWaitsForAllLatestCandidates(t *testing.T) {
	// Requirement: compare complete groups of latest attempts; risk: scoring a candidate never shown to the judges.
	exp := doc{"variants": []any{doc{}, doc{}, doc{}}}
	trial := func(id, variant, state string, attempt int) doc {
		return doc{"id": id, "state": state, "cell": doc{"iteration": 0, "example": "e", "repeat": 0, "variant": variant, "attempt": attempt}}
	}
	trials := []doc{trial("a", "a", "succeeded", 0), trial("b", "b", "succeeded", 0), trial("c", "c", "running", 0)}
	if groups, complete := pairwiseGroups(exp, trials); complete || len(groups) != 0 {
		t.Fatalf("premature group: %v", groups)
	}
	trials[2]["state"] = "succeeded"
	trials = append(trials, trial("a-retry", "a", "running", 1))
	if groups, complete := pairwiseGroups(exp, trials); complete || len(groups) != 0 {
		t.Fatalf("stale retry group: %v", groups)
	}
	trials[3]["state"] = "succeeded"
	groups, complete := pairwiseGroups(exp, trials)
	if !complete || len(groups) != 1 {
		t.Fatalf("missing complete group: %v", groups)
	}
	for _, g := range groups {
		if len(g) != 3 {
			t.Fatal(g)
		}
		for _, tr := range g {
			if tr["id"] == "a" {
				t.Fatal("old attempt included")
			}
		}
	}
}
