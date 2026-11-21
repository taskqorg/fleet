package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// labEvaluator: one experiment evaluator as stored in experiment.evaluators.
type labEvaluator struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Form      string `json:"form"`
	Overrides any    `json:"overrides,omitempty"`
}

// name: the evaluator's name, as evaluatorName.
func (e labEvaluator) name() string { return fmt.Sprintf("%v:%v:%v", e.Kind, e.Ref, e.Form) }

// labCompareVariant: a variant of a compare-methods experiment as expCreate reads it.
type labCompareVariant struct {
	Label      string `json:"label"`
	PipelineID string `json:"pipelineId"`
	Overrides  any    `json:"overrides"`
}

// labCatalogue: the catalogue definition's fields method comparison reads; overrides stay as stored.
type labCatalogue struct {
	Capability string               `json:"capability"`
	Methods    []labCatalogueMethod `json:"methods"`
}

type labCatalogueMethod struct {
	ID        string          `json:"id"`
	Scope     string          `json:"scope"`
	Pipeline  string          `json:"pipeline"`
	ConfigRef string          `json:"configRef"`
	Overrides json.RawMessage `json:"overrides"`
}

// Compare uses one locked stored catalogue head; selection does not launch work.
func labCompareMethods(q *Request, r *contract.LabCompareMethodsRequest) (contract.LabCompareMethodsReply, error) {
	var out contract.LabCompareMethodsReply
	project, err := expID(r.ProjectId, "projectId")
	if err != nil {
		return out, err
	}
	if !projectKnown(q, project) {
		return out, fail(404, "Unknown project")
	}
	head, err := headOf(q, "catalogue", "catalogue", true)
	if err != nil {
		return out, err
	}
	if head == nil {
		return out, fail(404, "No catalogue")
	}
	var c labCatalogue
	var typeErr *json.UnmarshalTypeError
	if t := bytes.TrimSpace(head.Content); len(t) == 0 || t[0] != '{' {
		return out, fail(500, "Invalid catalogue definition")
	} else if err := json.Unmarshal(t, &c); err != nil && !errors.As(err, &typeErr) {
		return out, fail(500, "Invalid catalogue definition")
	}
	if r.Capability != c.Capability {
		return out, fail(400, "Unknown capability")
	}
	available := map[string]labCatalogueMethod{}
	for _, m := range c.Methods {
		if m.Scope == "shared" || m.Scope == project {
			available[m.ID] = m
		}
	}
	methods := []labCatalogueMethod{}
	selected := map[string]bool{}
	for _, id := range r.Methods {
		if selected[id] {
			return out, fail(400, "Duplicate method")
		}
		m, ok := available[id]
		if !ok {
			return out, fail(404, "Unknown method")
		}
		selected[id] = true
		methods = append(methods, m)
	}
	if len(methods) < 2 {
		return out, fail(400, "Choose at least two methods")
	}
	baseline := deref(r.Baseline)
	if baseline == "" {
		resolved, err := catalogueBase(q, project)
		if err != nil {
			return out, err
		}
		base, _ := asT[struct {
			RecommendedMethod string `json:"recommendedMethod"`
		}](resolved)
		baseline = base.RecommendedMethod
	}
	if !selected[baseline] {
		return out, fail(400, "Choose a baseline from the chosen methods")
	}
	for i, m := range methods {
		if m.ID == baseline {
			methods[0], methods[i] = methods[i], methods[0]
			break
		}
	}
	uc := deref(r.UseCase)
	if uc == "" {
		return out, fail(400, "Choose a use case; no capability mapping is configured")
	}
	registry, err := useCases()
	if err != nil {
		return out, err
	}
	var entry *contract.UseCase
	for i, u := range registry {
		if u.Id == uc {
			entry = &registry[i]
			break
		}
	}
	if entry == nil {
		return out, fail(404, "Unknown use case")
	}
	evaluators := []labEvaluator{}
	seen := map[string]bool{}
	for _, check := range entry.Checks {
		k, ref, form := check.Kind, check.Ref, deref(check.Metric)
		valid := false
		switch k {
		case "script":
			valid = ref == "example.checks" && (form == "pass_fail" || form == "metric")
		case "judge":
			valid = ref != "" && (form == "pass_fail" || form == "scale" || form == "pick_best")
		case "typed":
			valid = ref == "labels" && form == "typed"
		case "human":
			valid = ref == "owner" && (form == "pass_fail" || form == "pick_best" || form == "scale")
		}
		if !valid {
			return out, fail(409, "Use case checks need a supported evaluator kind, ref and metric/form")
		}
		ev := labEvaluator{Kind: k, Ref: ref, Form: form}
		if !seen[ev.name()] {
			evaluators = append(evaluators, ev)
			seen[ev.name()] = true
		}
	}
	if len(evaluators) == 0 {
		return out, fail(409, "Use case has no evaluators")
	}
	ids, err := q.db().LabApprovedExamples(q.ctx(), strPtr(project))
	if err != nil {
		return out, err
	}
	examples := []string{}
	for _, id := range ids {
		ex, err := labExampleRead(q, id)
		if err != nil {
			return out, err
		}
		if ex.example.UseCaseID == uc {
			examples = append(examples, id)
		}
	}
	if len(examples) == 0 {
		return out, fail(409, "Use case has no approved examples in this project")
	}
	variants := []labCompareVariant{}
	for _, m := range methods {
		variants = append(variants, labCompareVariant{Label: m.ID, PipelineID: strings.TrimSuffix(m.Pipeline, ".json"), Overrides: m.Overrides})
	}
	kind, pipeline := "ab", strings.TrimSuffix(methods[0].Pipeline, ".json")
	// The create a lab_experiment_create request makes, with the catalogue's numbers as stored.
	created, err := expCreate(q, &contract.LabExperimentCreateRequest{Kind: &kind, ProjectId: &project, PipelineId: &pipeline, Variants: jsonBytes(variants),
		Examples: jsonBytes(examples), Evaluators: jsonBytes(evaluators), PrimaryEvaluator: jsonBytes(evaluators[0].name())}, true)
	if err != nil {
		return out, err
	}
	exp, err := expRow(q, created.Id)
	if err != nil {
		return out, err
	}
	ref := jsonBytes(contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: head.Head, ContentSha: head.ContentSha})
	for i := range exp.Variants {
		v, m := &exp.Variants[i], methods[i]
		identity := m.ConfigRef
		if identity == "" {
			identity = labConfigDigestOf(v.PipelineID, v.Overrides)
		}
		v.CatalogueConfigRef, v.MethodID, v.CatalogueRef = identity, m.ID, ref
	}
	if err := q.db().LabVariantsSet(q.ctx(), db.LabVariantsSetParams{ID: exp.ID, Variants: jsonBytes(exp.Variants)}); err != nil {
		return out, err
	}
	return contract.LabCompareMethodsReply{ExperimentId: exp.ID}, nil
}
