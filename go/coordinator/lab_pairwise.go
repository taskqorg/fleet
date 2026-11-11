package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Pairwise judges retain both orders; a position-dependent answer never becomes a score.
func pairwiseEvaluate(q *Request, exp, evaluator doc, trials, runs, current []doc, launched *[]string) (bool, error) {
	groups := map[string][]doc{}
	for _, t := range trials {
		if !comparable(t) {
			continue
		}
		c := mapOf(t["cell"])
		key := canonical([]any{c["iteration"], c["example"], c["repeat"]})
		groups[key] = append(groups[key], t)
	}
	complete := true
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			return sha256hex([]byte(astr(exp["id"])+astr(group[i]["id"]))) < sha256hex([]byte(astr(exp["id"])+astr(group[j]["id"])))
		})
		ids := []any{}
		for _, t := range group {
			ids = append(ids, t["id"])
		}
		name := evaluatorName(evaluator)
		gid := sha256hex([]byte(key))
		found := false
		for _, s := range current {
			if s["evaluator"] == name && canonical(s["trialIds"]) == canonical(ids) {
				found = true
			}
		}
		if found {
			continue
		}
		pair := []doc{nil, nil}
		for _, r := range runs {
			j := mapOf(r["judge"])
			if j["evaluator"] == name && j["groupId"] == gid {
				if n, ok := intOK(j["order"]); ok && n >= 0 && n < 2 {
					pair[n] = r
				}
			}
		}
		for order := 0; order < 2; order++ {
			if pair[order] != nil {
				continue
			}
			complete = false
			if exp["state"] != "running" {
				continue
			}
			ordered := append([]doc{}, group...)
			if order == 1 {
				for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
			inputs := []any{}
			candidates := []any{}
			for _, t := range ordered {
				full, _, err := exampleOf(q, findPin(exp, mapOf(t["cell"])["example"]))
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
					aid, err := saveArtifact(q, t, astr(t["id"]), "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, doc{"artifactId": aid, "path": "example/trial/" + astr(t["id"]) + "/" + f.path})
				}
				candidates = append(candidates, t["id"])
			}
			full, _, err := exampleOf(q, findPin(exp, mapOf(group[0]["cell"])["example"]))
			if err != nil {
				return false, err
			}
			ov := mapOf(evaluator["overrides"])
			variant := doc{"overrides": ov, "config_ref": sha256hex(jsonBytes(ov))}
			rid, err := expInsertRun(q, exp, "lab:"+sha256hex(jsonBytes([]any{exp["id"], name, gid, order})), astr(evaluator["ref"])+".json", doc{"prompt": full["prompt"], "candidates": candidates}, variant, doc{"exampleInputs": inputs, "judge": doc{"evaluator": name, "groupId": gid, "trialIds": ids, "order": order}})
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
		if pair[0]["state"] != "succeeded" || pair[1]["state"] != "succeeded" {
			continue
		}
		a, b := judgeOutput(pair[0]), judgeOutput(pair[1])
		if checkValue("pick_best", a["value"], ids) != nil || checkValue("pick_best", b["value"], ids) != nil {
			continue
		}
		if a["value"] != b["value"] {
			continue
		} // Both run outputs retain the evidence; the view names order dependence.
		version := sha256hex(jsonBytes([]any{pair[0]["definitionSha256"], evaluator["overrides"]}))[:16]
		_, err := scoreAdd(q, doc{"experimentId": exp["id"], "projectId": exp["project_id"], "trialIds": ids, "evaluator": name, "evaluatorVersion": version, "form": "pick_best", "value": a["value"], "sourceExecutionId": pair[0]["id"], "rationale": fmt.Sprintf("Both orders agree. %v; %v", a["rationale"], b["rationale"])})
		if err != nil {
			return false, err
		}
	}
	return complete, nil
}

func judgeOutput(run doc) doc {
	steps := anys(run["steps"])
	if len(steps) == 0 {
		return nil
	}
	return mapOf(mapOf(steps[len(steps)-1])["output"])
}

func sameTrialSet(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := []string{}, []string{}
	for _, v := range a {
		x = append(x, astr(v))
	}
	for _, v := range b {
		y = append(y, astr(v))
	}
	sort.Strings(x)
	sort.Strings(y)
	return canonical(x) == canonical(y)
}

func agreement(scores []doc) doc {
	out := agreementBase(scores)
	versions := map[string][]doc{}
	humans := []doc{}
	for _, s := range scores {
		if strings.HasPrefix(astr(s["evaluator"]), "human:") {
			humans = append(humans, s)
		}
		if strings.HasPrefix(astr(s["evaluator"]), "judge:") {
			k := astr(s["evaluator"]) + "@" + astr(s["evaluatorVersion"])
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

func pairwiseView(runs []doc) []any {
	groups := map[string][]doc{}
	for _, r := range runs {
		j := mapOf(r["judge"])
		if j["groupId"] != nil {
			key := astr(j["evaluator"]) + ":" + astr(j["groupId"])
			groups[key] = append(groups[key], r)
		}
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []any{}
	for _, k := range keys {
		rs := groups[k]
		reason := "pending"
		if len(rs) == 2 && terminal(rs[0]) && terminal(rs[1]) {
			reason = "invalid-output"
			a, b := judgeOutput(rs[0]), judgeOutput(rs[1])
			ids := anys(mapOf(rs[0]["judge"])["trialIds"])
			if checkValue("pick_best", a["value"], ids) == nil && checkValue("pick_best", b["value"], ids) == nil {
				reason = "agree"
				if a["value"] != b["value"] {
					reason = "order-dependent"
				}
			}
		}
		ids := []any{}
		for _, r := range rs {
			ids = append(ids, r["id"])
		}
		out = append(out, doc{"group": k, "reason": reason, "runIds": ids})
	}
	return out
}
