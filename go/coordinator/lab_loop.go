package main

import (
	"fmt"
	"math"
)

func loopStrategy(q *Request, raw any) (doc, error) {
	s := mapOf(raw)
	if s == nil || !keysOnly(s, "generator", "allocator", "max_iterations", "no_gain") {
		return nil, fail(400, "Loop strategy requires generator, allocator and optional explicit stop rules")
	}
	g, a := mapOf(s["generator"]), mapOf(s["allocator"])
	if g == nil || !keysOnly(g, "kind", "params") || mapOf(g["params"]) == nil || (g["kind"] != "list" && g["kind"] != "grid" && g["kind"] != "propose") {
		return nil, fail(400, "Loop generator is list, grid or propose with params")
	}
	if a == nil || a["kind"] != "full" {
		return nil, fail(400, "Loop allocator is full")
	}
	if v, has := s["max_iterations"]; has {
		if n, ok := intOK(v); !ok || n < 1 {
			return nil, fail(400, "max_iterations must be positive")
		}
	}
	if v, has := s["no_gain"]; has {
		m := mapOf(v)
		if n, ok := intOK(m["k"]); m == nil || !keysOnly(m, "k") || !ok || n < 1 {
			return nil, fail(400, "no_gain requires explicit positive k")
		}
	}
	// Persist the caller's existing owner-override authority; background reconciliation is not a new grant.
	s["ownerOverrides"] = q.person == nil || fullAccess(q.person)
	params := mapOf(g["params"])
	if g["kind"] == "list" {
		vs, ok := params["variants"].([]any)
		if !ok {
			return nil, fail(400, "list params.variants must be an array")
		}
		if _, err := loopVariants(doc{"strategy": s}, vs, 1); err != nil {
			return nil, err
		}
	}
	if g["kind"] == "grid" {
		axes := mapOf(params["overrides"])
		if axes == nil {
			return nil, fail(400, "grid params.overrides must be an object")
		}
		if !keysOnly(axes, labVariantKeys...) {
			return nil, fail(400, "Invalid grid override key")
		}
		for _, axis := range axes {
			if _, ok := axis.([]any); !ok {
				return nil, fail(400, "Grid axes must be arrays")
			}
		}
	}
	if ov, has := params["step_overrides"]; has {
		if mapOf(ov) == nil || !keysOnly(mapOf(ov), labOverrides...) {
			return nil, fail(400, "Invalid strategy step_overrides")
		}
		if _, err := loopVariants(doc{"strategy": s}, []any{doc{"label": "strategy", "overrides": ov}}, 1); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func loopIteration(exp doc) int64 {
	n := int64(0)
	for _, v := range anys(exp["variants"]) {
		i, _ := intOK(mapOf(v)["iteration"])
		if i > n {
			n = i
		}
	}
	return n
}

func loopVariants(exp doc, proposed []any, iteration int64) ([]any, error) {
	out := append([]any{}, anys(exp["variants"])...)
	used := map[string]bool{}
	for _, v := range out {
		used[astr(mapOf(v)["label"])] = true
	}
	strategy := mapOf(exp["strategy"])
	for i, x := range proposed {
		v := mapOf(x)
		label := astr(v["label"])
		if !labLabel.MatchString(label) {
			return nil, fail(400, "Invalid strategy variant label")
		}
		ov := mapOf(v["overrides"])
		if v["overrides"] == nil {
			ov = doc{}
		}
		if ov == nil || !keysOnly(ov, labVariantKeys...) {
			return nil, fail(400, "Invalid strategy overrides")
		}
		if strategy["ownerOverrides"] != true && (ov["host"] != nil || ov["account"] != nil || ov["profile"] != nil) {
			return nil, fail(403, "Only the owner overrides host, account or profile")
		}
		if args, has := ov["arguments"]; has {
			m := mapOf(args)
			if m == nil {
				return nil, fail(400, "arguments must map step IDs to objects")
			}
			for _, arg := range m {
				if mapOf(arg) == nil {
					return nil, fail(400, "arguments must map step IDs to objects")
				}
			}
		}
		if bands, has := ov["bands"]; has {
			if _, _, err := bandsOf(bands); err != nil {
				return nil, err
			}
		}
		if w := ov["web_route"]; w != nil && w != "connections" && w != "builtin" {
			return nil, fail(400, "Invalid strategy web_route")
		}
		id := fmt.Sprintf("i%d %s", iteration, sha256hex(jsonBytes([]any{i, label, ov}))[:16])
		for used[id] {
			id += "x"
		}
		used[id] = true
		out = append(out, doc{"label": id, "proposalLabel": label, "iteration": iteration, "overrides": ov, "config_ref": sha256hex(jsonBytes(doc{"pipeline": exp["pipeline_id"], "overrides": ov}))})
	}
	return out, nil
}

// no_gain compares tune means against the original baseline's repeat range; missing evidence is never zero.
func loopNoGain(exp doc, trials, scores []doc, k int64) (bool, bool) {
	variants := anys(exp["variants"])
	if len(variants) == 0 {
		return false, false
	}
	baseline := astr(mapOf(variants[0])["label"])
	byVariant := map[string][]float64{}
	byRepeat := map[string][]float64{}
	for _, s := range scoredLatest(exp, trials, scores, func(s doc) bool { return s["evaluator"] == exp["primary_evaluator"] }) {
		if s.split != "tune" {
			continue
		}
		if s.score["form"] == "typed" && mapOf(s.score["value"])["answer"] == "unscored" {
			continue
		}
		value := scoreNumber(s.score)
		byVariant[s.variant] = append(byVariant[s.variant], value)
		if s.variant == baseline {
			r := fmt.Sprint(mapOf(s.trial["cell"])["repeat"])
			byRepeat[r] = append(byRepeat[r], value)
		}
	}
	mean := func(xs []float64) float64 {
		sum := 0.0
		for _, x := range xs {
			sum += x
		}
		return sum / float64(len(xs))
	}
	repeats, _ := intOK(exp["repeats"])
	tune := 0
	for _, p := range anys(exp["examples"]) {
		if mapOf(p)["split"] == "tune" {
			tune++
		}
	}
	if tune == 0 || int64(len(byRepeat)) != repeats {
		return false, false
	}
	low, high := math.Inf(1), math.Inf(-1)
	for _, xs := range byRepeat {
		if len(xs) != tune {
			return false, false
		}
		v := mean(xs)
		low = math.Min(low, v)
		high = math.Max(high, v)
	}
	base := mean(byVariant[baseline])
	streak := int64(0)
	for iteration := int64(0); iteration <= loopIteration(exp); iteration++ {
		best := math.Inf(-1)
		for _, x := range variants {
			v := mapOf(x)
			i, _ := intOK(v["iteration"])
			if i != iteration {
				continue
			}
			values := byVariant[astr(v["label"])]
			if int64(len(values)) != int64(tune)*repeats {
				return false, false
			}
			best = math.Max(best, mean(values))
		}
		if math.IsInf(best, -1) {
			return false, false
		}
		if best-base <= high-low {
			streak++
		} else {
			streak = 0
		}
	}
	return streak >= k, true
}

func loopAdvance(q *Request, exp doc, trials, runs, scores []doc, spend doc, launched *[]string) error {
	s := mapOf(exp["strategy"])
	iteration := loopIteration(exp)
	stop := func(reason string) error {
		return exec(q, "UPDATE experiment SET state='done',stop_reason=$2 WHERE id=$1 AND state='running'", exp["id"], reason)
	}
	halt := func(reason string) error {
		return exec(q, "UPDATE experiment SET state='stopped',stop_reason=$2 WHERE id=$1 AND state='running'", exp["id"], reason)
	}
	if n, ok := intOK(s["max_iterations"]); ok && iteration+1 >= n {
		return stop("max_iterations")
	}
	if ng := mapOf(s["no_gain"]); ng != nil {
		k, _ := intOK(ng["k"])
		done, available := loopNoGain(exp, trials, scores, k)
		if !available {
			return halt("primary-score-unavailable")
		}
		if done {
			return stop("no_gain")
		}
	}
	var strategyRun doc
	for _, r := range runs {
		m := mapOf(r["loopStrategy"])
		i, ok := intOK(m["iteration"])
		if ok && i == iteration {
			strategyRun = r
		}
	}
	if strategyRun == nil {
		generator := mapOf(s["generator"])
		pipeline := "lab-strategy"
		if generator["kind"] == "propose" {
			pipeline = "lab-strategy-propose"
		}
		// Held-out results remain in history but never steer proposal generation.
		tuneTrials := map[any]bool{}
		for _, t := range trials {
			pin := findPin(exp, mapOf(t["cell"])["example"])
			if pin["split"] == "tune" {
				tuneTrials[t["id"]] = true
			}
		}
		tuneScores := []any{}
		for _, score := range scores {
			if tuneTrials[firstTrial(score)] {
				tuneScores = append(tuneScores, score)
			}
		}
		snapshot := doc{"experimentId": exp["id"], "iteration": iteration, "variants": exp["variants"], "scores": tuneScores, "primaryEvaluator": exp["primary_evaluator"], "generator": generator, "allocator": s["allocator"], "spend": spend, "budgets": exp["budgets"]}
		inputs := []any{}
		losing := []any{}
		if generator["kind"] == "propose" {
			best := loopBestVariant(exp, trials, scores)
			order, latest := expLatest(trials)
			for _, key := range order {
				t := latest[key]
				if !tuneTrials[t["id"]] || mapOf(t["cell"])["variant"] == best["label"] {
					continue
				}
				for _, step := range anys(t["steps"]) {
					st := mapOf(step)
					id := astr(st["executionId"])
					if id == "" {
						continue
					}
					events, err := executionEventsOf(q, id)
					path := "history/" + astr(t["id"]) + "/" + astr(st["id"]) + ".json"
					record := doc{"trialId": t["id"], "executionId": id, "state": st["state"], "events": events, "available": err == nil}
					if err != nil {
						record["error"] = err.Error()
					}
					inputs = append(inputs, doc{"path": path, "text": string(jsonBytes(record))})
					losing = append(losing, doc{"trialId": t["id"], "step": st["id"], "path": path, "available": err == nil})
				}
			}
		}
		snapshot["losingTranscripts"] = losing
		inputs = append(inputs, doc{"path": "snapshot.json", "text": string(jsonBytes(snapshot))})
		ov := mapOf(mapOf(generator["params"])["step_overrides"])
		if ov == nil {
			ov = doc{}
		}
		if !keysOnly(ov, labOverrides...) {
			return halt("strategy:invalid-overrides")
		}
		if s["ownerOverrides"] != true && (ov["host"] != nil || ov["account"] != nil || ov["profile"] != nil) {
			return halt("strategy:unauthorized-overrides")
		}
		id, err := expInsertRun(q, exp, "loop:"+sha256hex(jsonBytes([]any{exp["id"], iteration})), pipeline+".json", doc{"inputs": inputs}, doc{"overrides": ov, "config_ref": sha256hex(jsonBytes(s))}, doc{"loopStrategy": doc{"iteration": iteration}})
		if err != nil {
			return err
		}
		if id != "" {
			*launched = append(*launched, id)
		}
		return nil
	}
	if !terminal(strategyRun) {
		return nil
	}
	if !comparable(strategyRun) {
		return halt("strategy:" + strOr(strategyRun["problem"]))
	}
	output := judgeOutput(strategyRun)
	if !keysOnly(output, "action", "variants", "reason") || output["reason"] == nil {
		return halt("strategy:invalid-output")
	}
	if output["action"] == "stop" {
		return stop("strategy:" + astr(output["reason"]))
	}
	if output["action"] != "next" || len(anys(output["variants"])) == 0 {
		return halt("strategy:invalid-output")
	}
	proposed := anys(output["variants"])
	if mapOf(mapOf(s["generator"])["params"])["keep_best"] == true {
		best := loopBestVariant(exp, trials, scores)
		if best == nil {
			return halt("strategy:best-unavailable")
		}
		for _, x := range proposed {
			v := mapOf(x)
			v["overrides"] = loopMergeOverrides(mapOf(best["overrides"]), mapOf(v["overrides"]))
		}
	}
	variants, err := loopVariants(exp, proposed, iteration+1)
	if err != nil {
		return halt("strategy:" + err.Error())
	}
	return exec(q, "UPDATE experiment SET variants=$2 WHERE id=$1 AND state='running'", exp["id"], jsonBytes(variants))
}

func loopBestVariant(exp doc, trials, scores []doc) doc {
	values := map[string][]float64{}
	for _, s := range scoredLatest(exp, trials, scores, func(s doc) bool { return s["evaluator"] == exp["primary_evaluator"] }) {
		if s.split == "tune" {
			values[s.variant] = append(values[s.variant], scoreNumber(s.score))
		}
	}
	var best doc
	max := math.Inf(-1)
	for _, x := range anys(exp["variants"]) {
		v := mapOf(x)
		xs := values[astr(v["label"])]
		if len(xs) == 0 {
			continue
		}
		sum := 0.0
		for _, n := range xs {
			sum += n
		}
		mean := sum / float64(len(xs))
		if mean > max {
			max = mean
			best = v
		}
	}
	return best
}

func loopMergeOverrides(base, extra doc) doc {
	out := doc{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	if extra["arguments"] != nil {
		steps := doc{}
		for k, v := range mapOf(base["arguments"]) {
			steps[k] = v
		}
		for k, v := range mapOf(extra["arguments"]) {
			args := doc{}
			for n, a := range mapOf(steps[k]) {
				args[n] = a
			}
			for n, a := range mapOf(v) {
				args[n] = a
			}
			steps[k] = args
		}
		out["arguments"] = steps
	}
	return out
}
