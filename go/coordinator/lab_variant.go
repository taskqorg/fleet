package main

import (
	"encoding/json"
	"time"

	"fleet/coordinator/db"
)

// labVariant: one experiment variant as stored in experiment.variants (expCreate, loopVariants, lab_compare_methods);
// every stored key has its field, so a variant written back keeps them.
type labVariant struct {
	Label              string          `json:"label"`
	PipelineID         string          `json:"pipelineId,omitempty"`
	Iteration          int64           `json:"iteration,omitempty"`
	ProposalLabel      string          `json:"proposalLabel,omitempty"`
	Overrides          any             `json:"overrides"`
	ConfigRef          string          `json:"config_ref,omitempty"`
	CatalogueConfigRef string          `json:"catalogueConfigRef,omitempty"`
	MethodID           string          `json:"methodId,omitempty"`
	CatalogueRef       json.RawMessage `json:"catalogueRef,omitempty"`
}

// bands: the variant's bands override (bandsOf reads it); nil when it has none.
func (v labVariant) bands() any {
	o, _ := asT[struct {
		Bands any `json:"bands"`
	}](v.Overrides)
	return o.Bands
}

// labCell: a trial's place in its experiment (expLaunchTrial); an older cell has no iteration.
type labCell struct {
	Iteration *int64 `json:"iteration"`
	Example   string `json:"example"`
	Variant   string `json:"variant"`
	Repeat    int64  `json:"repeat"`
	Attempt   int64  `json:"attempt"`
}

// key: the cell's key (cellKey) without its attempt.
func (c labCell) key() string { return cellKey(c.Example, c.Variant, c.Repeat) }

// Legacy experiments keep their outer pipeline; new variants retain their own selection.
func variantPipelineID(exp *labExperiment, variant *labVariant) string {
	if variant != nil && variant.PipelineID != "" {
		return variant.PipelineID
	}
	return exp.PipelineID
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

// labExperiment: an experiment row as the Lab reads it: its columns, the JSON ones decoded once (expOf). The JSON names
// are the row's columns.
type labExperiment struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"project_id"`
	Kind               string          `json:"kind"`
	PipelineID         string          `json:"pipeline_id"`
	Examples           []labPin        `json:"examples"`
	Variants           []labVariant    `json:"variants"`
	Repeats            int64           `json:"repeats"`
	Evaluators         []labEvaluator  `json:"evaluators"`
	PrimaryEvaluator   *string         `json:"primary_evaluator"`
	Strategy           json.RawMessage `json:"strategy"`
	Budgets            []labBudget     `json:"budgets"`
	State              string          `json:"state"`
	StopReason         *string         `json:"stop_reason"`
	CreatedAt          time.Time       `json:"created_at"`
	Goal               *string         `json:"goal"`
	Hypothesis         *string         `json:"hypothesis"`
	DecisionRule       *string         `json:"decision_rule"`
	LabGroup           *string         `json:"lab_group"`
	Decision           *string         `json:"decision"`
	DecisionReason     *string         `json:"decision_reason"`
	DecidedBy          *string         `json:"decided_by"`
	DecidedAt          *time.Time      `json:"decided_at"`
	LaunchedAt         *time.Time      `json:"launched_at"`
	RequestedBy        *string         `json:"requested_by"`
	RequesterRemovedAt *time.Time      `json:"requester_removed_at"`
}

// expOf: an experiment row with its JSON columns decoded.
func expOf(e db.Experiment) (*labExperiment, error) {
	x := &labExperiment{ID: e.ID, ProjectID: e.ProjectID, Kind: e.Kind, PipelineID: e.PipelineID, Repeats: int64(e.Repeats), PrimaryEvaluator: e.PrimaryEvaluator,
		Strategy: e.Strategy, State: e.State, StopReason: e.StopReason, CreatedAt: e.CreatedAt, Goal: e.Goal, Hypothesis: e.Hypothesis, DecisionRule: e.DecisionRule,
		LabGroup: e.LabGroup, Decision: e.Decision, DecisionReason: e.DecisionReason, DecidedBy: e.DecidedBy, DecidedAt: e.DecidedAt, LaunchedAt: e.LaunchedAt,
		RequestedBy: e.RequestedBy, RequesterRemovedAt: e.RequesterRemovedAt}
	for _, c := range []struct {
		b []byte
		v any
	}{{e.Examples, &x.Examples}, {e.Variants, &x.Variants}, {e.Evaluators, &x.Evaluators}, {e.Budgets, &x.Budgets}} {
		if len(c.b) > 0 {
			if err := json.Unmarshal(c.b, c.v); err != nil {
				return nil, err
			}
		}
	}
	return x, nil
}

// primary: the primary evaluator's name, "" when the experiment has none.
func (e *labExperiment) primary() string { return deref(e.PrimaryEvaluator) }

// pin: the pinned example id, nil when the experiment pins none such.
func (e *labExperiment) pin(id string) *labPin {
	for i := range e.Examples {
		if e.Examples[i].ID == id {
			return &e.Examples[i]
		}
	}
	return nil
}

// variant: the variant labelled label, nil when there is none.
func (e *labExperiment) variant(label string) *labVariant {
	for i := range e.Variants {
		if e.Variants[i].Label == label {
			return &e.Variants[i]
		}
	}
	return nil
}

// labPin: an example pinned by an experiment (expCreate).
type labPin struct {
	ID         string `json:"id"`
	ArtifactID string `json:"artifact_id"`
	Hash       string `json:"hash"`
	Split      string `json:"split"`
}

// labBudget: one experiment budget as stored in experiment.budgets; firedAt is set once it fires.
type labBudget struct {
	Unit    string  `json:"unit"`
	Limit   float64 `json:"limit"`
	Action  string  `json:"action"`
	FiredAt *int64  `json:"firedAt,omitempty"`
}

// tune: how many of the experiment's examples are tune examples.
func (e *labExperiment) tune() int {
	n := 0
	for _, p := range e.Examples {
		if p.Split == "tune" {
			n++
		}
	}
	return n
}

// labJudge: what a judge run judges (its run's judge field).
type labJudge struct {
	Evaluator string   `json:"evaluator"`
	GroupID   string   `json:"groupId,omitempty"`
	TrialID   string   `json:"trialId,omitempty"`
	TrialIDs  []string `json:"trialIds,omitempty"`
	Order     *int64   `json:"order,omitempty"`
}

// labExampleInput: an artifact a Lab run reads as an example input file.
type labExampleInput struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
}

// labRunExtra: what a Lab run records besides its pipeline input (expInsertRun); pipelineStart copies each set field
// into the run.
type labRunExtra struct {
	Cell          *labCell        `json:"cell,omitempty"`
	Judge         *labJudge       `json:"judge,omitempty"`
	ExampleInputs json.RawMessage `json:"exampleInputs,omitempty"`
	LoopStrategy  *labLoopMark    `json:"loopStrategy,omitempty"`
	Recovery      *labRecovery    `json:"recovery,omitempty"`
	Priority      string          `json:"priority"`
}

// labLoopMark: a loop strategy run's iteration (its run's loopStrategy field).
type labLoopMark struct {
	Iteration *int64 `json:"iteration"`
}

// labScore: a score as the Lab reads and shows it (scoreView): the value stays as stored.
type labScore struct {
	ID                string          `json:"id"`
	ExperimentID      string          `json:"experimentId"`
	TrialIDs          []string        `json:"trialIds"`
	Evaluator         string          `json:"evaluator"`
	EvaluatorVersion  string          `json:"evaluatorVersion"`
	Form              string          `json:"form"`
	Value             json.RawMessage `json:"value"`
	SourceExecutionID *string         `json:"sourceExecutionId"`
	QuestionID        *string         `json:"questionId"`
	Rationale         *string         `json:"rationale"`
	ReplacesScoreID   *string         `json:"replacesScoreId"`
	CreatedAt         time.Time       `json:"createdAt"`
}

// typed: a typed score's value (an object; anything else, or a field that is not text, reads empty).
func (s labScore) typed() (v struct {
	Tag    string `json:"tag"`
	Answer string `json:"answer"`
	Label  string `json:"label"`
}) {
	m, _ := labRawObject(s.Value) // exact keys
	json.Unmarshal(m["tag"], &v.Tag)
	json.Unmarshal(m["answer"], &v.Answer)
	json.Unmarshal(m["label"], &v.Label)
	return v
}

// unscored: a typed score that could not score its trial (it counts in no statistic).
func (s labScore) unscored() bool { return s.Form == "typed" && s.typed().Answer == "unscored" }

// text: the value when it is text, else "".
func (s labScore) text() string {
	var t string
	json.Unmarshal(s.Value, &t)
	return t
}

// value: the value decoded.
func (s labScore) value() any { return rawValue(s.Value) }

// first: the score's first trial, "" when it has none.
func (s labScore) first() string {
	if len(s.TrialIDs) > 0 {
		return s.TrialIDs[0]
	}
	return ""
}
