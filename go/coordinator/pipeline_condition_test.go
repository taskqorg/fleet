package main

import "testing"

// Requirement: a finite condition distinguishes nonselection from bad evidence.
// Risk: truthy values, missing answers or undeclared enum strings silently skip qualified work.
func TestPipelineConditionDecision(t *testing.T) {
	boolean := doc{"kind": "boolean", "source": doc{"step": "route", "path": []any{"result", "review"}}, "equals": true}
	enum := doc{"kind": "enum", "source": doc{"step": "route", "path": []any{"action"}}, "domain": []any{"retain_candidate", "qualified_review"}, "equals": "qualified_review"}
	for _, c := range []struct {
		name, outcome, reason string
		rule                  doc
		output                any
	}{
		{"true", "selected", "condition_true", boolean, doc{"result": doc{"review": true}}},
		{"false", "not_selected", "condition_false", boolean, doc{"result": doc{"review": false}}},
		{"missing", "blocked", "condition_missing", boolean, doc{"result": doc{}}},
		{"null", "blocked", "condition_null", boolean, doc{"result": doc{"review": nil}}},
		{"string-false", "blocked", "condition_type", boolean, doc{"result": doc{"review": "false"}}},
		{"numeric-zero", "blocked", "condition_type", boolean, doc{"result": doc{"review": float64(0)}}},
		{"bad-parent", "blocked", "condition_type", boolean, doc{"result": []any{true}}},
		{"enum-true", "selected", "condition_true", enum, doc{"action": "qualified_review"}},
		{"enum-false", "not_selected", "condition_false", enum, doc{"action": "retain_candidate"}},
		{"enum-unknown", "blocked", "condition_domain", enum, doc{"action": "probably"}},
		{"unconditional", "selected", "unconditional", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			when, err := parsePipelineWhen(c.rule)
			if err != nil {
				t.Fatal(err)
			}
			d := pipelineConditionDecision(when, c.output, 123)
			if string(d.Outcome) != c.outcome || string(d.Reason) != c.reason || d.EvaluatedAtMs != 123 || !labSha.MatchString(d.RuleSha256) {
				t.Fatalf("decision: %#v", d)
			}
			if c.name == "false" && (d.ObservedBoolean == nil || *d.ObservedBoolean) {
				t.Fatal("false observation was omitted or changed")
			}
		})
	}
	negative := doc{"kind": "boolean", "source": doc{"step": "route", "path": []any{"review"}}, "equals": false}
	when, _ := parsePipelineWhen(negative)
	if d := pipelineConditionDecision(when, doc{"review": false}, 1); d.Outcome != "selected" {
		t.Fatal("exact equality to false was treated as nonselection", d)
	}
}

// Requirement: malformed/forward conditions fail definition validation before any work exists.
// Risk: an accepted expression or unbound source postpones a policy error until after paid work.
func TestPipelineConditionDefinition(t *testing.T) {
	valid := func() doc {
		return doc{"version": float64(3), "id": "guard", "resultStep": "final", "steps": []any{
			doc{"id": "route", "kind": "script", "command": "route"},
			doc{"id": "review", "kind": "script", "command": "review", "when": doc{"kind": "enum", "source": doc{"step": "route", "path": []any{"action"}}, "domain": []any{"retain", "review"}, "equals": "review"}},
			doc{"id": "final", "kind": "script", "command": "final"},
		}}
	}
	if _, err := validatePipeline(valid()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		edit func(doc, doc)
	}{
		{"v2-guard", func(d, w doc) { d["version"] = float64(2); delete(d, "resultStep") }},
		{"v2-null-result", func(d, w doc) {
			d["version"], d["resultStep"] = float64(2), nil
			delete(mapOf(anys(d["steps"])[1]), "when")
		}},
		{"self", func(d, w doc) { mapOf(w["source"])["step"] = "review" }},
		{"forward", func(d, w doc) { mapOf(w["source"])["step"] = "final" }},
		{"empty-path", func(d, w doc) { mapOf(w["source"])["path"] = []any{} }},
		{"array-path", func(d, w doc) { mapOf(w["source"])["path"] = []any{float64(0)} }},
		{"duplicate-domain", func(d, w doc) { w["domain"] = []any{"review", "review"} }},
		{"empty-domain", func(d, w doc) { w["domain"] = []any{} }},
		{"outside-domain", func(d, w doc) { w["equals"] = "other" }},
		{"expression", func(d, w doc) { w["expression"] = "x || y" }},
		{"boolean-missing-equals", func(d, w doc) { w["kind"] = "boolean"; delete(w, "domain"); delete(w, "equals") }},
		{"boolean-string", func(d, w doc) { w["kind"] = "boolean"; delete(w, "domain"); w["equals"] = "true" }},
		{"root-extra", func(d, w doc) { d["expression"] = "true" }},
		{"nonlast-result", func(d, w doc) { d["resultStep"] = "route" }},
		{"missing-result", func(d, w doc) { delete(d, "resultStep") }},
		{"conditional-result", func(d, w doc) { mapOf(anys(d["steps"])[2])["when"] = w }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := valid()
			w := mapOf(mapOf(anys(d["steps"])[1])["when"])
			c.edit(d, w)
			if _, err := validatePipeline(d); err == nil {
				t.Fatal("invalid condition accepted")
			}
		})
	}
}

// Requirement: server control/evaluation namespaces stay reserved on all hosts.
// Risk: case variants or path syntax replace trusted control or evaluator evidence.
func TestPipelineReservedInputPaths(t *testing.T) {
	for _, name := range []string{"__fleet_control/pipeline.json", "__FLEET_CONTROL/child/file", "__fleet_evaluation/trial.json", "evaluation/assessment.json"} {
		if !pipelineReservedInput(name) {
			t.Fatal("reserved namespace accepted", name)
		}
	}
	for _, name := range []string{"../escape", "/absolute", "a/../b", "a\\b", "C:/drive", "a\x00b"} {
		if pipelineInputPath(name) {
			t.Fatal("unsafe input path accepted", name)
		}
	}
	if !pipelineInputPath("example/nested/input.json") || pipelineReservedInput("example/control.json") {
		t.Fatal("valid input rejected")
	}
}
