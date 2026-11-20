package main

import (
	"encoding/json"
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

func judgeAnswer(run *labRun) labJudgeAnswer {
	var a labJudgeAnswer
	json.Unmarshal(run.output(), &a)
	return a
}

// Pairwise judges retain both orders; a position-dependent answer never becomes a score.
func pairwiseEvaluate(q *Request, e *labExperiment, ev labEvaluator, trials, runs []*labRun, current []labScore, launched *[]string) (bool, error) {
	groups, complete := pairwiseGroups(e, trials)
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(groups[key]) < 2 {
			continue
		}
		group := append([]*labRun{}, groups[key]...)
		sort.Slice(group, func(i, j int) bool {
			return sha256hex([]byte(e.ID+group[i].ID)) < sha256hex([]byte(e.ID+group[j].ID))
		})
		ids := []string{}
		for _, t := range group {
			ids = append(ids, t.ID)
		}
		name := ev.name()
		gid := sha256hex(jsonBytes([]any{key, ids, ev.Overrides}))
		found := false
		for _, sc := range current {
			if sc.Evaluator == name && canonical(sc.TrialIDs) == canonical(ids) {
				found = true
			}
		}
		if found {
			continue
		}
		pair := []*labRun{nil, nil}
		for _, r := range runs {
			j := r.Judge
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
			ordered := append([]*labRun{}, group...)
			if order == 1 {
				for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
			inputs := []labExampleInput{}
			candidates := []string{}
			for _, t := range ordered {
				full, err := exampleOf(q, e.pin(t.cell().Example))
				if err != nil {
					return false, err
				}
				files, err := trialFiles(q, t, full)
				if err != nil {
					return false, err
				}
				for _, f := range files {
					if strings.HasSuffix(strings.ToLower(f.path), ".glb") {
						continue
					}
					aid, err := saveArtifact(q, t.rec, t.ID, "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, labExampleInput{ArtifactID: aid, Path: "example/trial/" + t.ID + "/" + f.path})
				}
				candidates = append(candidates, t.ID)
			}
			full, err := exampleOf(q, e.pin(group[0].cell().Example))
			if err != nil {
				return false, err
			}
			n := order
			rid, err := labStartRun(q, e, labRunStart{RequestID: "lab:" + sha256hex(jsonBytes([]any{e.ID, name, gid, order})), File: ev.Ref + ".json",
				Example: labJudgeExample{Prompt: full.Prompt, Candidates: candidates}, Variant: &labVariant{Overrides: ev.Overrides, ConfigRef: sha256hex(jsonBytes(ev.Overrides))},
				Extra: labRunExtra{ExampleInputs: jsonBytes(inputs), Judge: &labJudge{Evaluator: name, GroupID: gid, TrialIDs: ids, Order: &n}}})
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
		if !pair[0].terminal() || !pair[1].terminal() {
			complete = false
			continue
		}
		if pair[0].State != "succeeded" || pair[1].State != "succeeded" {
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
		version := sha256hex(jsonBytes([]any{pair[0].DefinitionSha256, ev.Overrides}))[:16]
		rationale := fmt.Sprintf("Both orders agree. %v; %v", a.Rationale, b.Rationale)
		_, err := scoreInsert(q, labNewScore{ExperimentID: e.ID, ProjectID: e.ProjectID, TrialIDs: ids, Evaluator: name, EvaluatorVersion: version, Form: "pick_best", Value: a.Value,
			SourceExecutionID: &pair[0].ID, Rationale: &rationale})
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

func agreement(scores []labScore) labAgreement {
	out := agreementBase(scores)
	versions := map[string][]labScore{}
	humans := []labScore{}
	for _, sc := range scores {
		if strings.HasPrefix(sc.Evaluator, "human:") {
			humans = append(humans, sc)
		}
		if strings.HasPrefix(sc.Evaluator, "judge:") {
			k := sc.Evaluator + "@" + sc.EvaluatorVersion
			versions[k] = append(versions[k], sc)
		}
	}
	keys := []string{}
	for k := range versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out.ByVersion = []labAgreementRow{}
	for _, k := range keys {
		row := agreementBase(append(append([]labScore{}, humans...), versions[k]...))
		out.ByVersion = append(out.ByVersion, labAgreementRow{Compared: row.Compared, Matched: row.Matched, Percent: row.Percent, Version: k})
	}
	return out
}

func pairwiseView(runs []*labRun) []contract.LabPairwise {
	groups := map[string][]*labRun{}
	for _, r := range runs {
		if j := r.Judge; j != nil && j.GroupID != "" {
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
		if len(rs) == 2 && rs[0].terminal() && rs[1].terminal() {
			reason = "invalid-output"
			a, b := judgeAnswer(rs[0]), judgeAnswer(rs[1])
			ids := []any{}
			for _, id := range rs[0].Judge.TrialIDs {
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
			ids = append(ids, r.ID)
		}
		group := k
		out = append(out, contract.LabPairwise{Group: &group, Reason: &reason, RunIds: ids})
	}
	return out
}

// A comparison is bound to the latest attempt of every variant in the completed group.
func pairwiseGroups(e *labExperiment, trials []*labRun) (map[string][]*labRun, bool) {
	latest := map[string]*labRun{}
	attempts := map[string]int64{}
	for _, t := range trials {
		c := t.cell()
		k := canonical([]any{c.Iteration, c.Example, c.Variant, c.Repeat})
		if a, seen := attempts[k]; !seen || c.Attempt > a {
			latest[k], attempts[k] = t, c.Attempt
		}
	}
	groups := map[string][]*labRun{}
	iterations := map[string]int64{}
	for _, t := range latest {
		c := t.cell()
		k := canonical([]any{c.Iteration, c.Example, c.Repeat})
		groups[k] = append(groups[k], t)
		if c.Iteration != nil {
			iterations[k] = *c.Iteration
		}
	}
	ready := map[string][]*labRun{}
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
			done = done && t.terminal()
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
