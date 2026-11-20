package main

// labVariant: one experiment variant as stored in experiment.variants (expCreate, loopVariants, lab_compare_methods).
type labVariant struct {
	Label         string `json:"label"`
	PipelineID    string `json:"pipelineId,omitempty"`
	Iteration     int64  `json:"iteration,omitempty"`
	ProposalLabel string `json:"proposalLabel,omitempty"`
	Overrides     any    `json:"overrides"`
	ConfigRef     any    `json:"config_ref,omitempty"`
}

// labExpPipeline: the experiment row's outer pipeline.
type labExpPipeline struct {
	PipelineID string `json:"pipeline_id"`
}

// Legacy experiments keep their outer pipeline; new variants retain their own selection.
func variantPipelineID(exp, variant doc) string {
	if v, _ := asT[labVariant](variant); v.PipelineID != "" {
		return v.PipelineID
	}
	e, _ := asT[labExpPipeline](exp)
	return e.PipelineID
}

// labConfigIdentity: what a variant's config digest covers; its fields are in canonical (sorted) key order.
type labConfigIdentity struct {
	Overrides any    `json:"overrides"`
	Pipeline  string `json:"pipeline"`
}

// Catalogue linkage never substitutes for this actual pipeline/override identity.
func labConfigDigest(pipeline string, overrides doc) string {
	if overrides == nil {
		return labConfigDigestOf(pipeline, nil)
	}
	return labConfigDigestOf(pipeline, overrides)
}

// labConfigDigestOf: labConfigDigest of a decoded overrides object; nil digests as {}.
func labConfigDigestOf(pipeline string, overrides any) string {
	if overrides == nil {
		overrides = struct{}{}
	}
	return sha256hex([]byte(canonical(labConfigIdentity{Overrides: overrides, Pipeline: pipeline})))
}
