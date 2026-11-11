package main

import (
	"strings"

	"fleet/contract"
)

// Compare uses one locked stored catalogue head; selection does not launch work.
func labCompareMethods(q *Request, b doc) (doc, error) {
	project, err := expID(b["projectId"], "projectId")
	if err != nil {
		return nil, err
	}
	if !projectKnown(q, project) {
		return nil, fail(404, "Unknown project")
	}
	head, err := headOf(q, "catalogue", "catalogue", true)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, fail(404, "No catalogue")
	}
	c := mapOf(decodeNumbers(string(head.Content)))
	if c == nil {
		return nil, fail(500, "Invalid catalogue definition")
	}
	if b["capability"] != c["capability"] {
		return nil, fail(400, "Unknown capability")
	}
	available := map[string]doc{}
	for _, x := range anys(c["methods"]) {
		m := mapOf(x)
		if m["scope"] == "shared" || m["scope"] == project {
			available[astr(m["id"])] = m
		}
	}
	methods := []doc{}
	selected := map[string]bool{}
	for _, x := range anys(b["methods"]) {
		id := astr(x)
		if selected[id] {
			return nil, fail(400, "Duplicate method")
		}
		m := available[id]
		if m == nil {
			return nil, fail(404, "Unknown method")
		}
		selected[id] = true
		methods = append(methods, m)
	}
	if len(methods) < 2 {
		return nil, fail(400, "Select at least two methods")
	}
	baseline := astr(b["baseline"])
	if baseline == "" {
		resolved, err := catalogueBase(q, project)
		if err != nil {
			return nil, err
		}
		baseline = astr(resolved["recommendedMethod"])
	}
	if !selected[baseline] {
		return nil, fail(400, "Select a baseline from the selected methods")
	}
	for i, m := range methods {
		if m["id"] == baseline {
			methods[0], methods[i] = methods[i], methods[0]
			break
		}
	}
	uc := astr(b["useCase"])
	if uc == "" {
		return nil, fail(400, "Select a use case; no capability mapping is configured")
	}
	registry, err := useCases()
	if err != nil {
		return nil, err
	}
	var entry doc
	for _, x := range registry {
		if x["id"] == uc {
			entry = x
			break
		}
	}
	if entry == nil {
		return nil, fail(404, "Unknown use case")
	}
	evaluators := []any{}
	seen := map[string]bool{}
	for _, x := range anys(entry["checks"]) {
		check := mapOf(x)
		k, ref, form := astr(check["kind"]), astr(check["ref"]), astr(check["metric"])
		valid := false
		switch k {
		case "script":
			valid = ref == "example.checks" && (form == "pass_fail" || form == "metric")
		case "judge":
			valid = ref != "" && (form == "pass_fail" || form == "scale")
		case "typed":
			valid = ref == "labels" && form == "typed"
		case "human":
			valid = ref == "owner" && (form == "pass_fail" || form == "pick_best" || form == "scale")
		}
		if !valid {
			return nil, fail(409, "Use case checks need a supported evaluator kind, ref and metric/form")
		}
		ev := doc{"kind": k, "ref": ref, "form": form}
		name := evaluatorName(ev)
		if !seen[name] {
			evaluators = append(evaluators, ev)
			seen[name] = true
		}
	}
	if len(evaluators) == 0 {
		return nil, fail(409, "Use case has no evaluators")
	}
	rows, err := queryRows(q, "SELECT id FROM artifact WHERE role='example' AND state='stored' AND approved_at IS NOT NULL AND project_id=$1 ORDER BY created_at,id", project)
	if err != nil {
		return nil, err
	}
	examples := []any{}
	for _, row := range rows {
		ex, err := labExampleGet(q, astr(row["id"]))
		if err != nil {
			return nil, err
		}
		if mapOf(ex["example"])["use_case_id"] == uc {
			examples = append(examples, row["id"])
		}
	}
	if len(examples) == 0 {
		return nil, fail(409, "Use case has no approved examples in this project")
	}
	variants := []any{}
	for _, m := range methods {
		variants = append(variants, doc{"label": m["id"], "pipelineId": strings.TrimSuffix(astr(m["pipeline"]), ".json"), "overrides": m["overrides"]})
	}
	created, err := expCreate(q, doc{"kind": "ab", "projectId": project, "pipelineId": strings.TrimSuffix(astr(methods[0]["pipeline"]), ".json"), "variants": variants, "examples": examples, "evaluators": evaluators, "primaryEvaluator": evaluatorName(mapOf(evaluators[0]))})
	if err != nil {
		return nil, err
	}
	ref := contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: head.Head, ContentSha: head.ContentSha}
	snapshots := anys(created["variants"])
	for i, x := range snapshots {
		v, m := mapOf(x), methods[i]
		identity := astr(m["configRef"])
		if identity == "" {
			identity = labConfigDigest(variantPipelineID(doc{}, v), mapOf(v["overrides"]))
		}
		v["catalogueConfigRef"], v["methodId"], v["catalogueRef"] = identity, m["id"], ref
	}
	if err := exec(q, "UPDATE experiment SET variants=$2 WHERE id=$1", created["id"], jsonBytes(snapshots)); err != nil {
		return nil, err
	}
	return doc{"experimentId": created["id"]}, nil
}
