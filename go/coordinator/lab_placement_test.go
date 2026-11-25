package main

import (
	"encoding/json"
	"testing"
	"time"
)

// DAM Lab, 2026-11-25: the first variant never ran, and each example's checks carry their own evaluator version; the
// comparison still pairs the other variants on every example, against the first variant that has scores.
func TestExpComparisonBaselineAndPerExampleVersions(t *testing.T) {
	primary := "script:example.checks:metric"
	e := &labExperiment{PrimaryEvaluator: &primary, Examples: []labPin{{ID: "a", Split: "tune"}, {ID: "b", Split: "tune"}},
		Variants: []labVariant{{Label: "claude"}, {Label: "codex"}, {Label: "openai"}}}
	trials, scores := []*labRun{}, []labScore{}
	at := time.Unix(1000, 0)
	add := func(variant, example, state string, value float64) {
		id := variant + "|" + example
		trials = append(trials, &labRun{ID: id, State: state, Cell: &labCell{Example: example, Variant: variant}})
		if state == "succeeded" {
			at = at.Add(time.Second)
			scores = append(scores, labScore{ID: "s" + id, TrialIDs: []string{id}, Evaluator: primary, EvaluatorVersion: "checks-" + example,
				Form: "metric", Value: json.RawMessage(jsonBytes(value)), CreatedAt: at})
		}
	}
	for _, x := range []string{"a", "b"} {
		add("claude", x, "cancelled", 0)
		add("codex", x, "succeeded", 1)
		add("openai", x, "succeeded", 0.5)
	}
	rows := expComparison(e, trials, scores)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (claude and openai against codex)", len(rows))
	}
	for _, r := range rows {
		if r.Baseline != "codex" || r.Variant == "codex" {
			t.Fatalf("baseline %q variant %q, want codex as baseline", r.Baseline, r.Variant)
		}
		if deref(r.Reason) != "" || r.EvaluatorVersion != "per example" {
			t.Fatalf("%s: reason %q version %q", r.Variant, deref(r.Reason), r.EvaluatorVersion)
		}
	}
	if rows[1].Variant != "openai" || rows[1].Result.N != 2 {
		t.Fatalf("openai pairs %d examples, want 2: %+v", rows[1].Result.N, rows[1].Result)
	}
}

// A candidate list where every skip needs a changed setting refuses; a passing cause (offline, an update, a limit)
// keeps the work waiting.
func TestSettingsRefuse(t *testing.T) {
	roots := &candidate{Host: "owner-mac", Skipped: "checkout outside FLEET_PROJECT_ROOTS", Cause: "host:owner-mac:roots:dam"}
	forge := &candidate{Host: "windows", Skipped: "project forge connection unavailable", Cause: "host:windows:forge"}
	offline := &candidate{Host: "pds18", Skipped: "pds18 is offline", Cause: "host:pds18:offline"}
	if !settingsRefuse([]*candidate{roots, forge}) || !settingsRefuse(nil) {
		t.Fatal("settings-only skips must refuse")
	}
	if settingsRefuse([]*candidate{roots, offline}) || settingsRefuse([]*candidate{roots, {Host: "x"}}) {
		t.Fatal("a passing cause or a usable candidate must not refuse")
	}
}
