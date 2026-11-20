package main

import (
	"encoding/json"
	"fmt"
	"math"

	"fleet/coordinator/db"
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
	e, _ := asT[labExp](exp)
	n := int64(0)
	for _, v := range e.Variants {
		if v.Iteration > n {
			n = v.Iteration
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
		out = append(out, doc{"label": id, "proposalLabel": label, "iteration": iteration, "overrides": ov, "config_ref": labConfigDigest(astr(exp["pipeline_id"]), ov)})
	}
	return out, nil
}

// no_gain compares tune means against the original baseline's repeat range; missing evidence is never zero.
func loopNoGain(exp doc, trials, scores []doc, k int64) (bool, bool) {
	e, _ := asT[labExp](exp)
	if len(e.Variants) == 0 {
		return false, false
	}
	baseline := e.Variants[0].Label
	byVariant := map[string][]float64{}
	byRepeat := map[string][]float64{}
	for _, s := range scoredLatest(exp, trials, scores, func(s doc) bool { return s["evaluator"] == exp["primary_evaluator"] }) {
		if s.split != "tune" {
			continue
		}
		if f, _ := asT[labScoreForm](s.score); f.unscored() {
			continue
		}
		value := scoreNumber(s.score)
		byVariant[s.variant] = append(byVariant[s.variant], value)
		if s.variant == baseline {
			r := fmt.Sprint(labRunHeadOf(s.trial).Cell.Repeat)
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
	repeats, tune := e.Repeats, e.tune()
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
		for _, v := range e.Variants {
			if v.Iteration != iteration {
				continue
			}
			values := byVariant[v.Label]
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

// labLoopState: the fields of a loop experiment's stored strategy (loopStrategy) that its advance reads; generator and
// allocator stay whole for the strategy run's snapshot.
type labLoopState struct {
	Generator     json.RawMessage `json:"generator"`
	Allocator     json.RawMessage `json:"allocator"`
	MaxIterations *int64          `json:"max_iterations"`
	NoGain        *struct {
		K int64 `json:"k"`
	} `json:"no_gain"`
	OwnerOverrides bool `json:"ownerOverrides"`
}

// labLoopGenerator: the fields of a strategy's generator the advance reads.
type labLoopGenerator struct {
	Kind   string `json:"kind"`
	Params struct {
		KeepBest bool `json:"keep_best"`
	} `json:"params"`
}

// labLoopRun: the fields of a Lab run the loop advance reads (strategy runs and the trials' steps).
type labLoopRun struct {
	ID           string `json:"id"`
	LoopStrategy *struct {
		Iteration *int64 `json:"iteration"`
	} `json:"loopStrategy"`
	Steps []struct {
		ID          string `json:"id"`
		ExecutionID string `json:"executionId"`
		State       any    `json:"state"`
	} `json:"steps"`
}

// labLoopOutput: the fields of a strategy run's output the advance reads.
type labLoopOutput struct {
	Action   string          `json:"action"`
	Reason   json.RawMessage `json:"reason"`
	Variants []any           `json:"variants"`
}

// labTranscript: one losing trial step's events, as the propose strategy reads it; keys in canonical order.
type labTranscript struct {
	Available   bool   `json:"available"`
	Error       string `json:"error,omitempty"`
	Events      any    `json:"events"`
	ExecutionID string `json:"executionId"`
	State       any    `json:"state"`
	TrialID     string `json:"trialId"`
}

// labLosing: a losing transcript's place in the strategy run's inputs; keys in canonical order.
type labLosing struct {
	Available bool   `json:"available"`
	Path      string `json:"path"`
	Step      string `json:"step"`
	TrialID   string `json:"trialId"`
}

// labStrategyInput: a file of the strategy run's example.
type labStrategyInput struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// labLoopSnapshot: what a strategy run reads of its experiment (snapshot.json); keys in canonical order.
type labLoopSnapshot struct {
	Allocator         json.RawMessage `json:"allocator"`
	Budgets           any             `json:"budgets"`
	ExperimentID      any             `json:"experimentId"`
	Generator         json.RawMessage `json:"generator"`
	Iteration         int64           `json:"iteration"`
	LosingTranscripts []labLosing     `json:"losingTranscripts"`
	PrimaryEvaluator  any             `json:"primaryEvaluator"`
	Scores            []any           `json:"scores"`
	Spend             doc             `json:"spend"`
	Variants          any             `json:"variants"`
}

func loopAdvance(q *Request, exp doc, trials, runs, scores []doc, spend doc, launched *[]string) error {
	e, _ := asT[labExp](exp)
	s, _ := asT[labLoopState](exp["strategy"])
	generator, _ := asT[labLoopGenerator](s.Generator)
	iteration := loopIteration(exp)
	stop := func(reason string) error {
		_, err := experimentTransition(q, e.ID, experimentEvent{To: "done", From: []string{"running"}, StopReason: &reason})
		return err
	}
	halt := func(reason string) error {
		_, err := experimentTransition(q, e.ID, experimentEvent{To: "stopped", From: []string{"running"}, StopReason: &reason})
		return err
	}
	if s.MaxIterations != nil && iteration+1 >= *s.MaxIterations {
		return stop("max_iterations")
	}
	if !loopPrimaryComplete(exp, trials, scores) {
		return halt("primary-score-unavailable")
	}
	if s.NoGain != nil {
		done, available := loopNoGain(exp, trials, scores, s.NoGain.K)
		if !available {
			return halt("primary-score-unavailable")
		}
		if done {
			return stop("no_gain")
		}
	}
	var strategyRun doc
	for _, r := range runs {
		if st := labLoopRunOf(r).LoopStrategy; st != nil && st.Iteration != nil && *st.Iteration == iteration {
			strategyRun = r
		}
	}
	if strategyRun == nil {
		pipeline := "lab-strategy"
		if generator.Kind == "propose" {
			pipeline = "lab-strategy-propose"
		}
		// Held-out results remain in history but never steer proposal generation.
		tuneTrials := map[any]bool{}
		for _, t := range trials {
			pin := findPin(exp, labRunHeadOf(t).Cell.Example)
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
		snapshot := labLoopSnapshot{ExperimentID: exp["id"], Iteration: iteration, Variants: exp["variants"], Scores: tuneScores, PrimaryEvaluator: exp["primary_evaluator"],
			Generator: s.Generator, Allocator: s.Allocator, Spend: spend, Budgets: exp["budgets"], LosingTranscripts: []labLosing{}}
		inputs := []labStrategyInput{}
		if generator.Kind == "propose" {
			best := loopBestVariant(exp, trials, scores)
			order, latest := expLatest(trials)
			for _, key := range order {
				t := latest[key]
				if !tuneTrials[t["id"]] || best != nil && labRunHeadOf(t).Cell.Variant == best.Label {
					continue
				}
				run := labLoopRunOf(t)
				for _, st := range run.Steps {
					if st.ExecutionID == "" {
						continue
					}
					events, err := executionEventsOf(q, st.ExecutionID)
					path := "history/" + run.ID + "/" + st.ID + ".json"
					record := labTranscript{TrialID: run.ID, ExecutionID: st.ExecutionID, State: st.State, Events: events, Available: err == nil}
					if err != nil {
						record.Error = err.Error()
					}
					inputs = append(inputs, labStrategyInput{Path: path, Text: string(jsonBytes(record))})
					snapshot.LosingTranscripts = append(snapshot.LosingTranscripts, labLosing{TrialID: run.ID, Step: st.ID, Path: path, Available: err == nil})
				}
			}
		}
		inputs = append(inputs, labStrategyInput{Path: "snapshot.json", Text: string(jsonBytes(snapshot))})
		params := mapOf(mapOf(exp["strategy"])["generator"])["params"]
		ov := mapOf(mapOf(params)["step_overrides"])
		if ov == nil {
			ov = doc{}
		}
		if !keysOnly(ov, labOverrides...) {
			return halt("strategy:invalid-overrides")
		}
		if !s.OwnerOverrides && (ov["host"] != nil || ov["account"] != nil || ov["profile"] != nil) {
			return halt("strategy:unauthorized-overrides")
		}
		next := iteration
		id, err := expInsertLabRun(q, exp, "loop:"+sha256hex(jsonBytes([]any{exp["id"], iteration})), pipeline+".json", labStrategyExample(inputs),
			&labVariant{Overrides: ov, ConfigRef: sha256hex(jsonBytes(exp["strategy"]))}, labRunExtra{LoopIteration: &next})
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
	out, _ := asT[labLoopOutput](output)
	if !keysOnly(output, "action", "variants", "reason") || output["reason"] == nil {
		return halt("strategy:invalid-output")
	}
	if out.Action == "stop" {
		var reason string
		json.Unmarshal(out.Reason, &reason)
		return stop("strategy:" + reason)
	}
	if out.Action != "next" || len(out.Variants) == 0 {
		return halt("strategy:invalid-output")
	}
	proposed := out.Variants
	if generator.Params.KeepBest {
		best := loopBestVariant(exp, trials, scores)
		if best == nil {
			return halt("strategy:best-unavailable")
		}
		for _, x := range proposed {
			v := mapOf(x)
			v["overrides"] = loopMergeOverrides(mapOf(best.Overrides), mapOf(v["overrides"]))
		}
	}
	variants, err := loopVariants(exp, proposed, iteration+1)
	if err != nil {
		return halt("strategy:" + err.Error())
	}
	return q.db().LabVariantsSetRunning(q.ctx(), db.LabVariantsSetRunningParams{ID: e.ID, Variants: jsonBytes(variants)})
}

func labLoopRunOf(run doc) labLoopRun {
	r, _ := asT[labLoopRun](run)
	return r
}

func loopBestVariant(exp doc, trials, scores []doc) *labVariant {
	if !loopPrimaryComplete(exp, trials, scores) {
		return nil
	}
	values := map[string][]float64{}
	for _, s := range scoredLatest(exp, trials, scores, func(s doc) bool { return s["evaluator"] == exp["primary_evaluator"] }) {
		if s.split == "tune" {
			values[s.variant] = append(values[s.variant], scoreNumber(s.score))
		}
	}
	e, _ := asT[labExp](exp)
	var best *labVariant
	max := math.Inf(-1)
	for i, v := range e.Variants {
		xs := values[v.Label]
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
			best = &e.Variants[i]
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

func loopPrimaryComplete(exp doc, trials, scores []doc) bool {
	e, _ := asT[labExp](exp)
	tune := e.tune()
	if tune == 0 || e.Repeats < 1 {
		return false
	}
	counts := map[string]int{}
	for _, s := range scoredLatest(exp, trials, scores, func(s doc) bool { return s["evaluator"] == exp["primary_evaluator"] }) {
		if f, _ := asT[labScoreForm](s.score); s.split != "tune" || f.unscored() {
			continue
		}
		counts[s.variant]++
	}
	for _, v := range e.Variants {
		if int64(counts[v.Label]) != int64(tune)*e.Repeats {
			return false
		}
	}
	return len(e.Variants) > 0
}
