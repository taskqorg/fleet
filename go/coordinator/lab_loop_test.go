package main

import (
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"
)

func TestLoopNoGainUsesTuneRepeatNoise(t *testing.T) {
	// Requirement: UC9 stops only on measured tune no_gain; risk: held-out leakage or missing scores becoming zero.
	exp := doc{"variants": []any{doc{"label": "base"}, doc{"label": "next", "iteration": 1}}, "examples": []any{doc{"id": "tune", "split": "tune"}, doc{"id": "held", "split": "held_out"}}, "repeats": 2, "primary_evaluator": "judge:scale:scale"}
	trials, scores := []doc{}, []doc{}
	for _, v := range []string{"base", "next"} {
		for r := 0; r < 2; r++ {
			id := v + string(rune('0'+r))
			trials = append(trials, doc{"id": id, "state": "succeeded", "cell": doc{"iteration": map[string]int{"base": 0, "next": 1}[v], "example": "tune", "variant": v, "repeat": r, "attempt": 0}})
			scores = append(scores, doc{"evaluator": "judge:scale:scale", "form": "scale", "trialIds": []any{id}, "value": 3})
		}
	}
	trials = append(trials, doc{"id": "held", "state": "succeeded", "cell": doc{"example": "held", "variant": "base", "repeat": 0}})
	scores = append(scores, doc{"evaluator": "judge:scale:scale", "form": "scale", "trialIds": []any{"held"}, "value": 5})
	if done, available := loopNoGain(exp, trials, scores, 2); !available || !done {
		t.Fatalf("no_gain: %v %v", done, available)
	}
	scores[0]["value"], scores[1]["value"] = 1, 3
	scores[2]["value"], scores[3]["value"] = 4, 4
	if done, available := loopNoGain(exp, trials, scores, 2); !available || !done {
		t.Fatal("baseline repeat range ignored")
	}
	scores[2]["value"], scores[3]["value"] = 5, 5
	if done, available := loopNoGain(exp, trials, scores, 2); !available || done {
		t.Fatalf("gain ignored: %v %v", done, available)
	}
	if done, available := loopNoGain(exp, trials, scores[1:], 2); available || done {
		t.Fatal("missing baseline score treated as measured")
	}
}

func TestLoopExplicitStopsAndKeepBest(t *testing.T) {
	// Requirement: explicit no_gain k, unchanged budgets and keep_best settings; risk: invented limits or dropped argument fields.
	raw := doc{"generator": doc{"kind": "list", "params": doc{"variants": []any{}}}, "allocator": doc{"kind": "full"}}
	s, err := loopStrategy(&Request{}, raw)
	if err != nil || s["no_gain"] != nil || s["max_iterations"] != nil {
		t.Fatalf("%v %v", s, err)
	}
	raw["no_gain"] = doc{"k": 0}
	delete(raw, "ownerOverrides")
	if _, err := loopStrategy(&Request{}, raw); err == nil {
		t.Fatal("zero k accepted")
	}
	base := doc{"model": "original", "arguments": doc{"gen": doc{"ai_model": "mesh", "target_polycount": 3000}}}
	got := loopMergeOverrides(base, doc{"arguments": doc{"gen": doc{"target_polycount": 2400}}})
	args := mapOf(mapOf(got["arguments"])["gen"])
	if got["model"] != "original" || args["ai_model"] != "mesh" || args["target_polycount"] != 2400 || mapOf(mapOf(base["arguments"])["gen"])["target_polycount"] != 3000 {
		t.Fatal(got)
	}
}

func TestLoopScriptGenerators(t *testing.T) {
	// Requirement: retained list/grid strategy output; risk: incorrect cartesian cells or divergent strategy/output receipts.
	for _, g := range []doc{{"kind": "list", "params": doc{"variants": []any{doc{"label": "one", "overrides": doc{}}}}}, {"kind": "grid", "params": doc{"overrides": doc{"model": []any{"a", "b"}, "prompt": []any{"x", "y"}}}}} {
		root := t.TempDir()
		in, out := filepath.Join(root, "in"), filepath.Join(root, "out")
		os.MkdirAll(filepath.Join(in, "example"), 0700)
		os.MkdirAll(out, 0700)
		os.WriteFile(filepath.Join(in, "example", "snapshot.json"), jsonBytes(doc{"generator": g}), 0600)
		cmd := osexec.Command("python3", filepath.Join("..", "..", "scripts", "lab", "loop-strategy.py"))
		cmd.Env = append(os.Environ(), "FLEET_INPUT_DIR="+in, "FLEET_OUTPUT_DIR="+out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", b, err)
		}
		a, _ := os.ReadFile(filepath.Join(out, "strategy.json"))
		b, _ := os.ReadFile(filepath.Join(out, "output.json"))
		if string(a) != string(b) {
			t.Fatal("different strategy receipts")
		}
		var result doc
		json.Unmarshal(a, &result)
		n := 1
		if g["kind"] == "grid" {
			n = 4
		}
		if result["action"] != "next" || len(anys(result["variants"])) != n {
			t.Fatal(result)
		}
	}
}
