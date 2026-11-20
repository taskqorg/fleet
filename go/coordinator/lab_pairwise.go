package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"fleet/contract"
)

// labJudgeAnswer: the fields of a judge run's output a pairwise comparison reads.
type labJudgeAnswer struct {
	Value     any `json:"value"`
	Rationale any `json:"rationale"`
}

func judgeAnswer(run doc) labJudgeAnswer {
	a, _ := asT[labJudgeAnswer](pipelineResultValue(run))
	return a
}

// labGroupTrial: a trial of a pairwise group with its decoded head.
type labGroupTrial struct {
	run  doc
	head labRunHead
}

// Pairwise judges retain both orders; a position-dependent answer never becomes a score.
func pairwiseEvaluate(q *Request, exp, evaluator doc, trials, runs, current []doc, launched *[]string) (bool, error) {
	e, _ := asT[labExp](exp)
	ev, _ := asT[labEvaluator](evaluator)
	groups, complete := pairwiseGroups(exp, trials)
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(groups[key]) < 2 {
			continue
		}
		group := []labGroupTrial{}
		for _, t := range groups[key] {
			group = append(group, labGroupTrial{t, labRunHeadOf(t)})
		}
		sort.Slice(group, func(i, j int) bool {
			return sha256hex([]byte(e.ID+group[i].head.ID)) < sha256hex([]byte(e.ID+group[j].head.ID))
		})
		ids := []string{}
		for _, t := range group {
			ids = append(ids, t.head.ID)
		}
		name := ev.name()
		gid := sha256hex(jsonBytes([]any{key, ids, evaluator["overrides"]}))
		found := false
		for _, s := range current {
			if sc, _ := asT[labScoreHead](s); sc.Evaluator == name && canonical(sc.TrialIDs) == canonical(ids) {
				found = true
			}
		}
		if found {
			continue
		}
		pair := []doc{nil, nil}
		for _, r := range runs {
			j := labRunHeadOf(r).Judge
			if j != nil && j.Evaluator == name && j.GroupID == gid && j.Order != nil && *j.Order >= 0 && *j.Order < 2 {
				pair[*j.Order] = r
			}
		}
		for order := int64(0); order < 2; order++ {
			if pair[order] != nil {
				continue
			}
			complete = false
			if e.State != "running" {
				continue
			}
			ordered := append([]labGroupTrial{}, group...)
			if order == 1 {
				for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
			inputs := []labExampleInput{}
			candidates := []string{}
			for _, t := range ordered {
				full, _, err := exampleOf(q, findPin(exp, t.head.Cell.Example))
				if err != nil {
					return false, err
				}
				files, err := trialFiles(q, t.run, full)
				if err != nil {
					return false, err
				}
				for _, f := range files {
					if strings.HasSuffix(strings.ToLower(f.path), ".glb") {
						continue
					}
					aid, err := saveArtifact(q, t.run, t.head.ID, "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, labExampleInput{ArtifactID: aid, Path: "example/trial/" + t.head.ID + "/" + f.path})
				}
				candidates = append(candidates, t.head.ID)
			}
			full, _, err := exampleOf(q, findPin(exp, group[0].head.Cell.Example))
			if err != nil {
				return false, err
			}
			variant := &labVariant{Overrides: evaluator["overrides"], ConfigRef: sha256hex(jsonBytes(evaluator["overrides"]))}
			n := order
			rid, err := expInsertLabRun(q, exp, "lab:"+sha256hex(jsonBytes([]any{exp["id"], name, gid, order})), ev.Ref+".json", labJudgeExample(full["prompt"], candidates), variant,
				labRunExtra{ExampleInputs: inputs, Judge: &labJudge{Evaluator: name, GroupID: gid, TrialIDs: ids, Order: &n}})
			if err != nil {
				return false, err
			}
			if rid != "" {
				*launched = append(*launched, rid)
			}
		}
		if pair[0] == nil || pair[1] == nil {
			continue
		}
		if !terminal(pair[0]) || !terminal(pair[1]) {
			complete = false
			continue
		}
		heads := []labRunHead{labRunHeadOf(pair[0]), labRunHeadOf(pair[1])}
		if heads[0].State != "succeeded" || heads[1].State != "succeeded" {
			continue
		}
		candidates := []any{}
		for _, id := range ids {
			candidates = append(candidates, id)
		}
		a, b := judgeAnswer(pair[0]), judgeAnswer(pair[1])
		if checkValue("pick_best", a.Value, candidates) != nil || checkValue("pick_best", b.Value, candidates) != nil {
			continue
		}
		if a.Value != b.Value {
			continue
		} // Both run outputs retain the evidence; the view names order dependence.
		version := sha256hex(jsonBytes([]any{pair[0]["definitionSha256"], evaluator["overrides"]}))[:16]
		rationale := fmt.Sprintf("Both orders agree. %v; %v", a.Rationale, b.Rationale)
		_, err := scoreInsert(q, labNewScore{ExperimentID: e.ID, ProjectID: e.ProjectID, TrialIDs: ids, Evaluator: name, EvaluatorVersion: version, Form: "pick_best", Value: a.Value,
			SourceExecutionID: &heads[0].ID, Rationale: &rationale})
		if err != nil {
			return false, err
		}
	}
	return complete, nil
}

func sameTrialSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return canonical(x) == canonical(y)
}

func agreement(scores []doc) doc {
	out := agreementBase(scores)
	versions := map[string][]doc{}
	humans := []doc{}
	for _, s := range scores {
		sc, _ := asT[labScoreHead](s)
		if strings.HasPrefix(sc.Evaluator, "human:") {
			humans = append(humans, s)
		}
		if strings.HasPrefix(sc.Evaluator, "judge:") {
			k := sc.Evaluator + "@" + sc.EvaluatorVersion
			versions[k] = append(versions[k], s)
		}
	}
	keys := []string{}
	for k := range versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := []any{}
	for _, k := range keys {
		row := agreementBase(append(append([]doc{}, humans...), versions[k]...))
		row["version"] = k
		rows = append(rows, row)
	}
	out["byVersion"] = rows
	return out
}

func pairwiseView(runs []doc) []contract.LabPairwise {
	groups := map[string][]doc{}
	for _, r := range runs {
		if j := labRunHeadOf(r).Judge; j != nil && j.GroupID != "" {
			key := j.Evaluator + ":" + j.GroupID
			groups[key] = append(groups[key], r)
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []contract.LabPairwise{}
	for _, k := range keys {
		rs := groups[k]
		reason := "pending"
		if len(rs) == 2 && terminal(rs[0]) && terminal(rs[1]) {
			reason = "invalid-output"
			a, b := judgeAnswer(rs[0]), judgeAnswer(rs[1])
			ids := []any{}
			for _, id := range labRunHeadOf(rs[0]).Judge.TrialIDs {
				ids = append(ids, id)
			}
			if checkValue("pick_best", a.Value, ids) == nil && checkValue("pick_best", b.Value, ids) == nil {
				reason = "agree"
				if a.Value != b.Value {
					reason = "order-dependent"
				}
			}
		}
		ids := []string{}
		for _, r := range rs {
			ids = append(ids, labRunHeadOf(r).ID)
		}
		group := k
		out = append(out, contract.LabPairwise{Group: &group, Reason: &reason, RunIds: ids})
	}
	return out
}

// A comparison is bound to the latest attempt of every variant in the completed group.
func pairwiseGroups(exp doc, trials []doc) (map[string][]doc, bool) {
	e, _ := asT[labExp](exp)
	latest := map[string]doc{}
	attempts := map[string]int64{}
	for _, t := range trials {
		c := labRunHeadOf(t).Cell
		k := canonical([]any{c.Iteration, c.Example, c.Variant, c.Repeat})
		if a, seen := attempts[k]; !seen || c.Attempt > a {
			latest[k], attempts[k] = t, c.Attempt
		}
	}
	groups := map[string][]doc{}
	iterations := map[string]int64{}
	for _, t := range latest {
		c := labRunHeadOf(t).Cell
		k := canonical([]any{c.Iteration, c.Example, c.Repeat})
		groups[k] = append(groups[k], t)
		if c.Iteration != nil {
			iterations[k] = *c.Iteration
		}
	}
	ready := map[string][]doc{}
	complete := true
	for k, g := range groups {
		expected := 0
		for _, v := range e.Variants {
			if v.Iteration == iterations[k] {
				expected++
			}
		}
		done := len(g) == expected
		for _, t := range g {
			done = done && terminal(t)
		}
		if !done {
			complete = false
			continue
		}
		for _, t := range g {
			if comparable(t) {
				ready[k] = append(ready[k], t)
			}
		}
	}
	return ready, complete
}
