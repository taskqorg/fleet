package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// labEvaluator: one experiment evaluator as stored in experiment.evaluators.
type labEvaluator struct {
	Kind      string `json:"kind"`
	Ref       any    `json:"ref"`
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

// labExampleUseCase: the use case of an example (labExampleGet).
type labExampleUseCase struct {
	Example struct {
		UseCaseID string `json:"use_case_id"`
	} `json:"example"`
}

// labCreated: the created experiment's id and stored variants, each kept whole.
type labCreated struct {
	ID       string                       `json:"id"`
	Variants []map[string]json.RawMessage `json:"variants"`
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
	var c *contract.CatalogueDefinition
	if json.Unmarshal(head.Content, &c) != nil || c == nil {
		return out, fail(500, "Invalid catalogue definition")
	}
	if r.Capability != c.Capability {
		return out, fail(400, "Unknown capability")
	}
	available := map[string]contract.CatalogueMethod{}
	for _, m := range c.Methods {
		if m.Scope == "shared" || m.Scope == project {
			available[m.Id] = m
		}
	}
	methods := []contract.CatalogueMethod{}
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
		if m.Id == baseline {
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
	for _, x := range registry {
		if u, _ := asT[contract.UseCase](x); u.Id == uc {
			entry = &u
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
		ex, err := labExampleGet(q, id)
		if err != nil {
			return out, err
		}
		if e, _ := asT[labExampleUseCase](ex); e.Example.UseCaseID == uc {
			examples = append(examples, id)
		}
	}
	if len(examples) == 0 {
		return out, fail(409, "Use case has no approved examples in this project")
	}
	variants := []labCompareVariant{}
	for _, m := range methods {
		variants = append(variants, labCompareVariant{Label: m.Id, PipelineID: strings.TrimSuffix(m.Pipeline, ".json"), Overrides: m.Overrides})
	}
	kind, pipeline := "ab", strings.TrimSuffix(methods[0].Pipeline, ".json")
	// The same create an lab_experiment_create request makes.
	created, err := expCreate(q, docOf(contract.LabExperimentCreateRequest{Kind: &kind, ProjectId: &project, PipelineId: &pipeline, Variants: jsonBytes(variants),
		Examples: jsonBytes(examples), Evaluators: jsonBytes(evaluators), PrimaryEvaluator: jsonBytes(evaluators[0].name())}))
	if err != nil {
		return out, err
	}
	exp, _ := asT[labCreated](created)
	ref := contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: head.Head, ContentSha: head.ContentSha}
	for i, snapshot := range exp.Variants {
		m := methods[i]
		identity := deref(m.ConfigRef)
		if identity == "" {
			v, _ := asT[labVariant](snapshot)
			identity = labConfigDigestOf(v.PipelineID, v.Overrides)
		}
		snapshot["catalogueConfigRef"], snapshot["methodId"], snapshot["catalogueRef"] = jsonBytes(identity), jsonBytes(m.Id), jsonBytes(ref)
	}
	if err := q.db().LabVariantsSet(q.ctx(), db.LabVariantsSetParams{ID: exp.ID, Variants: jsonBytes(exp.Variants)}); err != nil {
		return out, err
	}
	return contract.LabCompareMethodsReply{ExperimentId: exp.ID}, nil
}
