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
