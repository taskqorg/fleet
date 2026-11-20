package main

import (
	"encoding/json"
	"sort"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

func methodDigest(m labMethod) string {
	return labConfigDigestOf(strings.TrimSuffix(m.Pipeline, ".json"), m.overridesValue())
}

// Identity links evidence; only the actual digest in this project qualifies its cohort.
func evidenceVariant(exp *labExperiment, method labMethod) *labVariant {
	actual := methodDigest(method)
	identity := method.ConfigRef
	if identity == "" {
		identity = actual
	}
	for i, v := range exp.Variants {
		if v.ConfigRef != actual {
			continue
		}
		if link := v.CatalogueConfigRef; link != "" && link != identity {
			continue
		}
		return &exp.Variants[i]
	}
	return nil
}

// evidenceExperiment: an experiment row as evidence reads it, with the record the Lab helpers read (its JSON columns
// decoded).
type evidenceExperiment struct {
	db.CatalogueExperimentsRow
	lab *labExperiment
}

func evidenceExperiments(rows []db.CatalogueExperimentsRow) ([]evidenceExperiment, error) {
	out := []evidenceExperiment{}
	for _, r := range rows {
		e, err := expOf(db.Experiment{ID: r.ID, State: r.State, StopReason: r.StopReason, CreatedAt: r.CreatedAt, PipelineID: r.PipelineID, PrimaryEvaluator: r.PrimaryEvaluator,
			Examples: r.Examples, Variants: r.Variants, Repeats: r.Repeats})
		if err != nil {
			return nil, err
		}
		out = append(out, evidenceExperiment{r, e})
	}
	return out, nil
}

func methodAssessment(q *Request, project string, method labMethod) (contract.MethodAssessment, error) {
	out := contract.MethodAssessment{Basis: contract.AssessmentBasisNone, CostBasis: "unavailable"}
	rows, err := q.db().CatalogueExperiments(q.ctx(), project)
	if err != nil {
		return out, err
	}
	exps, err := evidenceExperiments(rows)
	if err != nil {
		return out, err
	}
	var chosen *evidenceExperiment
	var variant *labVariant
	for i, exp := range exps {
		v := evidenceVariant(exp.lab, method)
		if v == nil {
			continue
		}
		if exp.State == "running" || exp.State == "paused" {
			if chosen == nil && out.OngoingExperimentId == nil {
				id, state := exp.ID, exp.State
				out.OngoingExperimentId, out.OngoingState = &id, &state
			}
			continue
		}
		if chosen == nil && (exp.State == "done" || exp.State == "stopped") {
			chosen, variant = &exps[i], v
		}
	}
	pipeline := strings.TrimSuffix(method.Pipeline, ".json")
	if chosen == nil {
		totals, _, err := usageWindows(q, "pipeline", pipeline, 30)
		if err != nil {
			return out, err
		}
		if w, ok := totals[pipeline]; ok && w.Runs > 0 {
			reason := "Visible pipeline usage over 30 days; no method quality score"
			out.Basis, out.Reason = contract.AssessmentBasisUsage, &reason
			json.Unmarshal(jsonBytes(w), &out.Usage)
			out.WallMsMedian = w.WallMsMedian
		}
		if source := method.SourceExperiment; source != "" {
			rows, err := q.db().CatalogueSourceExperiment(q.ctx(), db.CatalogueSourceExperimentParams{ID: source, ProjectID: project})
			if err != nil {
				return out, err
			}
			sources := []db.CatalogueExperimentsRow{}
			for _, r := range rows {
				sources = append(sources, db.CatalogueExperimentsRow(r))
			}
			exps, err := evidenceExperiments(sources)
			if err != nil {
				return out, err
			}
			if len(exps) > 0 {
				reason := "Source experiment has no terminal assessment"
				if evidenceVariant(exps[0].lab, method) == nil {
					reason = "Source experiment uses a different executable configuration (including Lab scoring bands when present)"
				}
				out.SourceExperimentId, out.Reason = &source, &reason
			}
		}
		return out, nil
	}
	exp := chosen.lab
	label, configRef, pipelineID, at := variant.Label, variant.ConfigRef, variantPipelineID(exp, variant), *labTimeText(&chosen.CreatedAt)
	out.Basis, out.ExperimentId, out.Variant, out.State, out.StopReason = contract.AssessmentBasisLab, &exp.ID, &label, &exp.State, chosen.StopReason
	out.ConfigRef, out.PipelineId, out.At = &configRef, &pipelineID, &at
	split := "all"
	if hasHeldOut(exp) {
		split = "held_out"
	}
	out.Split = &split
	pins := map[string]bool{}
	for _, p := range exp.Examples {
		if split == "all" || p.Split == "held_out" {
			pins[p.ID] = true
		}
	}
	expected := int64(len(pins)) * int64(chosen.Repeats)
	out.ExpectedCells = &expected
	runs, err := expRuns(q, chosen.ID)
	if err != nil {
		return out, err
	}
	trials := expTrials(runs)
	scores, err := scoresCurrent(q, chosen.ID)
	if err != nil {
		return out, err
	}
	cohort := scoredLatest(exp, trials, scores, func(sc labScore) bool {
		return exp.primaryScore(sc) && sc.Form != "pick_best" && !sc.unscored()
	})
	// The cohort's evaluator version and form are those of its first score of this variant's pinned cells.
	version, form, found := "", "", false
	for _, st := range cohort {
		if st.variant == label && pins[st.example] && !found {
			version, form, found = st.score.EvaluatorVersion, st.score.Form, true
		}
	}
	out.Evaluator, out.EvaluatorVersion, out.ScoreForm = chosen.PrimaryEvaluator, &version, &form
	sum := float64(0)
	eligible := int64(0)
	examples := map[string]bool{}
	for _, st := range cohort {
		if st.variant == label && pins[st.example] && st.score.EvaluatorVersion == version && st.score.Form == form {
			sum += scoreNumber(st.score)
			eligible++
			examples[st.example] = true
		}
	}
	excluded, exampleCount := expected-eligible, int64(len(examples))
	out.EligibleCells, out.Examples, out.ExcludedCells = &eligible, &exampleCount, &excluded
	partial := eligible < expected || chosen.State != "done" || deref(chosen.StopReason) != "complete"
	out.Partial = &partial
	if eligible > 0 {
		score := sum / float64(eligible)
		out.Score = &score
	} else {
		reason := "No confirmed compatible scalar scores"
		out.Reason = &reason
	}
	exclusions := "Failed/unconfirmed trials, missing or unscored primary values, and different evaluator versions/forms are excluded; missing cells are never zero"
	out.Exclusions = &exclusions
	durations := []float64{}
	_, last := expLatest(trials)
	failed := int64(0)
	ids := map[string]bool{}
	for _, t := range last {
		if c := t.cell(); c.Variant != label || !pins[c.Example] {
			continue
		}
		ids[t.ID] = true
		if t.State == "failed" {
			failed++
		}
		if rawValue(t.FinishedAt) != nil && t.DurationMs != nil {
			durations = append(durations, *t.DurationMs)
		}
	}
	samples := int64(len(durations))
	out.FailedCells, out.DurationSamples = &failed, &samples
	if len(durations) > 0 {
		sort.Float64s(durations)
		median := int64(durations[(len(durations)-1)/2])
		out.WallMsMedian = &median
	}
	// Costs remain explicitly unavailable until the selected trials have attributable USD entries.
	view, err := expGet(q, chosen.ID)
	if err != nil {
		return out, err
	}
	total := float64(0)
	known := true
	priced := 0
	basis := "measured"
	for _, t := range view.Trials {
		if !ids[t.Id] {
			continue
		}
		if len(t.Cost) == 0 {
			known = false
		}
		for _, c := range t.Cost {
			if c.Value == nil || deref(c.Currency) != "USD" {
				known = false
				continue
			}
			total += *c.Value
			priced++
			if c.Basis == "estimated" {
				basis = "estimated"
			}
		}
	}
	if known && priced > 0 {
		out.CostUsd, out.CostBasis = &total, basis
	}
	return out, nil
}

// labCatalogueView: the fields of a project's catalogue (catalogueBase) evidence reads.
type labCatalogueView struct {
	ProjectID         string            `json:"projectId"`
	RecommendedMethod json.RawMessage   `json:"recommendedMethod"`
	Methods           []json.RawMessage `json:"methods"`
}

// enrichCatalogue adds each method's assessment and the projects recommending it to c, a project's catalogue as
// catalogueBase gives it (projects.go reads it as that map); numbers keep their text.
func enrichCatalogue(q *Request, c doc) error {
	view, _ := asT[labCatalogueView](c)
	projects, err := projectIDs(q)
	if err != nil {
		return err
	}
	methods := []map[string]json.RawMessage{}
	for _, raw := range view.Methods {
		var method labMethod
		json.Unmarshal(raw, &method)
		a, err := methodAssessment(q, view.ProjectID, method)
		if err != nil {
			return err
		}
		recommended := []string{}
		for _, id := range projects {
			if q.person != nil && !mayAccessProjectID(q.person, id) {
				continue
			}
			other, err := catalogueBase(q, id)
			if err != nil {
				return err
			}
			if o, _ := asT[labCatalogueView](other); rawValue(o.RecommendedMethod) == method.ID {
				recommended = append(recommended, id)
			}
		}
		var m map[string]json.RawMessage
		json.Unmarshal(raw, &m)
		m["assessment"], m["recommendedFor"] = jsonBytes(a), jsonBytes(recommended)
		methods = append(methods, m)
	}
	c["methods"] = decodeNumbers(string(jsonBytes(methods)))
	return nil
}
