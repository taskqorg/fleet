package main

// Legacy experiments keep their outer pipeline; new variants retain their own selection.
func variantPipelineID(exp, variant doc) string {
	if id := astr(variant["pipelineId"]); id != "" {
		return id
	}
	return astr(exp["pipeline_id"])
}

// Catalogue linkage never substitutes for this actual pipeline/override identity.
func labConfigDigest(pipeline string, overrides doc) string {
	if overrides == nil {
		overrides = doc{}
	}
	return sha256hex([]byte(canonical(doc{"pipeline": pipeline, "overrides": overrides})))
}
