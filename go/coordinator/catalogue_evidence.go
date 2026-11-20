package main

import (
	"encoding/json"
	"sort"
	"strings"

	"fleet/coordinator/db"
)

func methodDigest(m doc) string {
	return labConfigDigest(strings.TrimSuffix(astr(m["pipeline"]), ".json"), mapOf(m["overrides"]))
}

// Identity links evidence; only the actual digest in this project qualifies its cohort.
func evidenceVariant(exp, method doc) doc {
	actual := methodDigest(method)
	identity := astr(method["configRef"])
	if identity == "" {
		identity = actual
	}
	for _, x := range anys(exp["variants"]) {
		v := mapOf(x)
		if astr(v["config_ref"]) != actual {
			continue
		}
		if link := astr(v["catalogueConfigRef"]); link != "" && link != identity {
			continue
		}
		return v
	}
	return nil
}

// evidenceExperiment: an experiment row as evidence reads it; lab doc is the record the Lab helpers (lab.go,
// lab_variant.go) still take as a map: the columns they read, jsonb decoded.
type evidenceExperiment struct {
	db.CatalogueExperimentsRow
	lab doc
}

func evidenceExperiments(rows []db.CatalogueExperimentsRow) ([]evidenceExperiment, error) {
	out := []evidenceExperiment{}
	for _, r := range rows {
		var examples, variants any
		if err := json.Unmarshal(r.Examples, &examples); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(r.Variants, &variants); err != nil {
			return nil, err
		}
		out = append(out, evidenceExperiment{r, doc{"id": r.ID, "pipeline_id": r.PipelineID, "examples": examples, "variants": variants}})
	}
	return out, nil
}

func methodAssessment(q *Request, project string, method doc) (doc, error) {
	out := doc{"basis": "none", "score": nil, "costUsd": nil, "costBasis": "unavailable", "wallMsMedian": nil}
	rows, err := q.db().CatalogueExperiments(q.ctx(), project)
	if err != nil {
		return nil, err
	}
	exps, err := evidenceExperiments(rows)
	if err != nil {
		return nil, err
	}
	var chosen *evidenceExperiment
	var variant doc
	for i, exp := range exps {
		v := evidenceVariant(exp.lab, method)
		if v == nil {
			continue
		}
		if exp.State == "running" || exp.State == "paused" {
			if chosen == nil && out["ongoingExperimentId"] == nil {
				out["ongoingExperimentId"], out["ongoingState"] = exp.ID, exp.State
			}
			continue
		}
		if chosen == nil && (exp.State == "done" || exp.State == "stopped") {
			chosen, variant = &exps[i], v
		}
	}
	if chosen == nil {
		totals, _, err := usageWindows(q, "pipeline", strings.TrimSuffix(astr(method["pipeline"]), ".json"), 30)
		if err != nil {
			return nil, err
		}
		if w, ok := totals[strings.TrimSuffix(astr(method["pipeline"]), ".json")]; ok && w.Runs > 0 {
			out["basis"], out["usage"], out["reason"] = "usage", w, "Visible pipeline usage over 30 days; no method quality score"
			out["wallMsMedian"] = w.WallMsMedian
		}
		if source := astr(method["sourceExperiment"]); source != "" {
			rows, err := q.db().CatalogueSourceExperiment(q.ctx(), db.CatalogueSourceExperimentParams{ID: source, ProjectID: project})
			if err != nil {
				return nil, err
			}
			sources := []db.CatalogueExperimentsRow{}
			for _, r := range rows {
				sources = append(sources, db.CatalogueExperimentsRow(r))
			}
			exps, err := evidenceExperiments(sources)
			if err != nil {
				return nil, err
			}
			if len(exps) > 0 {
				out["sourceExperimentId"] = source
				if evidenceVariant(exps[0].lab, method) == nil {
					out["reason"] = "Source experiment uses a different executable configuration (including Lab scoring bands when present)"
				} else {
					out["reason"] = "Source experiment has no terminal assessment"
				}
			}
		}
		return out, nil
	}
	var primary any // a score's evaluator compares to the primary evaluator, absent as nil
	if chosen.PrimaryEvaluator != nil {
		primary = *chosen.PrimaryEvaluator
	}
	out["basis"], out["experimentId"], out["variant"], out["state"], out["stopReason"] = "lab", chosen.ID, variant["label"], chosen.State, chosen.StopReason
	out["configRef"], out["pipelineId"], out["at"] = variant["config_ref"], variantPipelineID(chosen.lab, variant), chosen.CreatedAt
	split := "all"
	if hasHeldOut(chosen.lab) {
		split = "held_out"
	}
	out["split"] = split
	pins := map[string]bool{}
	for _, x := range anys(chosen.lab["examples"]) {
		p := mapOf(x)
		if split == "all" || p["split"] == "held_out" {
			pins[astr(p["id"])] = true
		}
	}
	expected := int64(len(pins)) * int64(chosen.Repeats)
	out["expectedCells"] = expected
	runs, err := expRuns(q, chosen.ID)
	if err != nil {
		return nil, err
	}
	trials := expTrials(runs)
	scores, err := scoresCurrent(q, chosen.ID)
	if err != nil {
		return nil, err
	}
	cohort := scoredLatest(chosen.lab, trials, scores, func(sc doc) bool {
		return sc["evaluator"] == primary && sc["form"] != "pick_best" && !(sc["form"] == "typed" && mapOf(sc["value"])["answer"] == "unscored")
	})
	version, form := "", ""
	latest := float64(-1)
	for _, st := range cohort {
		if st.variant != astr(variant["label"]) || !pins[st.example] {
			continue
		}
		seq, _ := numOK(st.score["seq"])
		if seq > latest {
			latest = seq
			version = astr(st.score["evaluatorVersion"])
			form = astr(st.score["form"])
		}
	}
	out["evaluator"], out["evaluatorVersion"], out["scoreForm"] = chosen.PrimaryEvaluator, version, form
	sum := float64(0)
	eligible := int64(0)
	examples := map[string]bool{}
	for _, st := range cohort {
		if st.variant == astr(variant["label"]) && pins[st.example] && astr(st.score["evaluatorVersion"]) == version && astr(st.score["form"]) == form {
			sum += scoreNumber(st.score)
			eligible++
			examples[st.example] = true
		}
	}
	out["eligibleCells"], out["examples"], out["excludedCells"] = eligible, len(examples), expected-eligible
	out["partial"] = eligible < expected || chosen.State != "done" || deref(chosen.StopReason) != "complete"
	if eligible > 0 {
		out["score"] = sum / float64(eligible)
	} else {
		out["reason"] = "No confirmed compatible scalar scores"
	}
	out["exclusions"] = "Failed/unconfirmed trials, missing or unscored primary values, and different evaluator versions/forms are excluded; missing cells are never zero"
	durations := []float64{}
	_, last := expLatest(trials)
	failed := 0
	for _, t := range last {
		cell := mapOf(t["cell"])
		if cell["variant"] != variant["label"] || !pins[astr(cell["example"])] {
			continue
		}
		if t["state"] == "failed" {
			failed++
		}
		if t["finishedAt"] != nil {
			if n, ok := numOK(t["durationMs"]); ok {
				durations = append(durations, n)
			}
		}
	}
	out["failedCells"], out["durationSamples"] = failed, len(durations)
	if len(durations) > 0 {
		sort.Float64s(durations)
		out["wallMsMedian"] = durations[(len(durations)-1)/2]
	}
	// Costs remain explicitly unavailable until the selected trials have attributable USD entries.
	view, err := expGet(q, chosen.ID)
	if err != nil {
		return nil, err
	}
	total := float64(0)
	known := true
	priced := 0
	basis := "measured"
	ids := map[string]bool{}
	for _, t := range last {
		if mapOf(t["cell"])["variant"] == variant["label"] && pins[astr(mapOf(t["cell"])["example"])] {
			ids[astr(t["id"])] = true
		}
	}
	for _, x := range anys(view["trials"]) {
		t := mapOf(x)
		if !ids[astr(t["id"])] {
			continue
		}
		costs := anys(t["cost"])
		if len(costs) == 0 {
			known = false
		}
		for _, x := range costs {
			c := mapOf(x)
			v, ok := numOK(c["value"])
			if !ok || c["currency"] != "USD" {
				known = false
				continue
			}
			total += v
			priced++
			if c["basis"] == "estimated" {
				basis = "estimated"
			}
		}
	}
	if known && priced > 0 {
		out["costUsd"], out["costBasis"] = total, basis
	}
	return out, nil
}

func enrichCatalogue(q *Request, c doc) error {
	project := astr(c["projectId"])
	projects, err := projectIDs(q)
	if err != nil {
		return err
	}
	for _, x := range anys(c["methods"]) {
		method := mapOf(x)
		a, err := methodAssessment(q, project, method)
		if err != nil {
			return err
		}
		method["assessment"] = a
		recommended := []any{}
		for _, id := range projects {
			if q.person != nil && !mayAccessProjectID(q.person, id) {
				continue
			}
			other, err := catalogueBase(q, id)
			if err != nil {
				return err
			}
			if other["recommendedMethod"] == method["id"] {
				recommended = append(recommended, id)
			}
		}
		method["recommendedFor"] = recommended
	}
	return nil
}
