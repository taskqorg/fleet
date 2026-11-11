package main

import (
	"sort"
	"strings"
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

func methodAssessment(q *Request, project string, method doc) (doc, error) {
	out := doc{"basis": "none", "score": nil, "costUsd": nil, "costBasis": "unavailable", "wallMsMedian": nil}
	rows, err := queryRows(q, "SELECT * FROM experiment WHERE project_id=$1 AND state <> 'draft' ORDER BY created_at DESC,id DESC", project)
	if err != nil {
		return nil, err
	}
	var chosen, variant doc
	for _, exp := range rows {
		v := evidenceVariant(exp, method)
		if v == nil {
			continue
		}
		if exp["state"] == "running" || exp["state"] == "paused" {
			if chosen == nil && out["ongoingExperimentId"] == nil {
				out["ongoingExperimentId"], out["ongoingState"] = exp["id"], exp["state"]
			}
			continue
		}
		if chosen == nil && (exp["state"] == "done" || exp["state"] == "stopped") {
			chosen, variant = exp, v
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
			sources, err := queryRows(q, "SELECT * FROM experiment WHERE id=$1 AND project_id=$2", source, project)
			if err != nil {
				return nil, err
			}
			if len(sources) > 0 {
				out["sourceExperimentId"] = source
				if evidenceVariant(sources[0], method) == nil {
					out["reason"] = "Source experiment uses a different executable configuration (including Lab scoring bands when present)"
				} else {
					out["reason"] = "Source experiment has no terminal assessment"
				}
			}
		}
		return out, nil
	}
	out["basis"], out["experimentId"], out["variant"], out["state"], out["stopReason"] = "lab", chosen["id"], variant["label"], chosen["state"], chosen["stop_reason"]
	out["configRef"], out["pipelineId"], out["at"] = variant["config_ref"], variantPipelineID(chosen, variant), chosen["created_at"]
	split := "all"
	if hasHeldOut(chosen) {
		split = "held_out"
	}
	out["split"] = split
	pins := map[string]bool{}
	for _, x := range anys(chosen["examples"]) {
		p := mapOf(x)
		if split == "all" || p["split"] == "held_out" {
			pins[astr(p["id"])] = true
		}
	}
	repeats, _ := intOK(chosen["repeats"])
	expected := int64(len(pins)) * repeats
	out["expectedCells"] = expected
	runs, err := expRuns(q, astr(chosen["id"]))
	if err != nil {
		return nil, err
	}
	trials := expTrials(runs)
	scores, err := scoresCurrent(q, astr(chosen["id"]))
	if err != nil {
		return nil, err
	}
	cohort := scoredLatest(chosen, trials, scores, func(sc doc) bool {
		return sc["evaluator"] == chosen["primary_evaluator"] && sc["form"] != "pick_best" && !(sc["form"] == "typed" && mapOf(sc["value"])["answer"] == "unscored")
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
	out["evaluator"], out["evaluatorVersion"], out["scoreForm"] = chosen["primary_evaluator"], version, form
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
	out["partial"] = eligible < expected || chosen["state"] != "done" || chosen["stop_reason"] != "complete"
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
	view, err := expGet(q, astr(chosen["id"]))
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
	projects := projectIDs()
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
