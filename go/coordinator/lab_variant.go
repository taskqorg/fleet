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

// labCell: a trial's place in its experiment (expLaunchTrial); an older cell has no iteration.
type labCell struct {
	Iteration *int64 `json:"iteration"`
	Example   string `json:"example"`
	Variant   string `json:"variant"`
	Repeat    int64  `json:"repeat"`
	Attempt   int64  `json:"attempt"`
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

// labVariantOf: a stored variant map as its struct; nil stays nil.
func labVariantOf(d doc) *labVariant {
	if d == nil {
		return nil
	}
	v, _ := asT[labVariant](d)
	return &v
}

// labExp: the fields of an experiment row (expRow) the evaluation reads.
type labExp struct {
	ID               string       `json:"id"`
	ProjectID        string       `json:"project_id"`
	PipelineID       string       `json:"pipeline_id"`
	State            string       `json:"state"`
	PrimaryEvaluator string       `json:"primary_evaluator"`
	Repeats          int64        `json:"repeats"`
	Variants         []labVariant `json:"variants"`
	Examples         []labPin     `json:"examples"`
}

// labPin: an example pinned by an experiment (expCreate).
type labPin struct {
	ID    string `json:"id"`
	Split string `json:"split"`
}

// tune: how many of the experiment's examples are tune examples.
func (e labExp) tune() int {
	n := 0
	for _, p := range e.Examples {
		if p.Split == "tune" {
			n++
		}
	}
	return n
}

// labScoreForm: a score's form and, for a typed score, its answer.
type labScoreForm struct {
	Form  string `json:"form"`
	Value struct {
		Answer string `json:"answer"`
	} `json:"value"`
}

// unscored: a typed score that could not score its trial (it counts in no statistic).
func (s labScoreForm) unscored() bool { return s.Form == "typed" && s.Value.Answer == "unscored" }

// labJudge: what a judge run judges (its run's judge field).
type labJudge struct {
	Evaluator string   `json:"evaluator"`
	GroupID   string   `json:"groupId,omitempty"`
	TrialID   string   `json:"trialId,omitempty"`
	TrialIDs  []string `json:"trialIds,omitempty"`
	Order     *int64   `json:"order,omitempty"`
}

// labRunHead: the fields of a Lab run (getRun) the evaluation reads.
type labRunHead struct {
	ID    string    `json:"id"`
	State string    `json:"state"`
	Cell  labCell   `json:"cell"`
	Judge *labJudge `json:"judge"`
}

func labRunHeadOf(run doc) labRunHead {
	h, _ := asT[labRunHead](run)
	return h
}

// labScoreHead: the fields of a score view (scoreView) the evaluation reads.
type labScoreHead struct {
	Evaluator        string   `json:"evaluator"`
	EvaluatorVersion string   `json:"evaluatorVersion"`
	TrialIDs         []string `json:"trialIds"`
}

// labExampleInput: an artifact a Lab run reads as an example input file.
type labExampleInput struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
}

// labRunExtra: what a Lab run records besides its pipeline input (expInsertRun); pipelineStart copies each set field
// into the run.
type labRunExtra struct {
	Cell          *labCell
	Judge         *labJudge
	ExampleInputs []labExampleInput
	LoopIteration *int64
}
