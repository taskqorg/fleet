package main

import "testing"

// Requirement: Compare preserves historical single-pipeline experiments; risk: new variant resolution rewrites legacy identity.
func TestVariantPipelineLegacyFallback(t *testing.T) {
	legacy := doc{"pipeline_id": "outer"}
	if got := variantPipelineID(legacy, doc{}); got != "outer" {
		t.Fatalf("legacy pipeline = %s", got)
	}
	if got := variantPipelineID(legacy, doc{"pipelineId": "selected"}); got != "selected" {
		t.Fatalf("variant pipeline = %s", got)
	}
	if labConfigDigest("outer", nil) != labConfigDigest("outer", doc{}) {
		t.Fatal("absent overrides changed legacy digest")
	}
}

// Requirement: Catalogue evidence follows an actual loop-generated method; risk: HTML escaping creates a different identity after Promote.
func TestLoopVariantEvidenceWithHTMLPrompt(t *testing.T) {
	exp := doc{"pipeline_id": "web", "variants": []any{}}
	overrides := doc{"prompt": "Render <h1>A & B</h1>"}
	variants, err := loopVariants(exp, []any{doc{"label": "html", "overrides": overrides}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	exp["variants"] = variants
	variant := mapOf(variants[0])
	method := doc{"pipeline": "web.json", "overrides": overrides, "configRef": variant["config_ref"]}
	if evidenceVariant(exp, method) == nil {
		t.Fatal("loop-generated HTML configuration lost its Catalogue evidence after promotion")
	}
}
