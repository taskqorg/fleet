package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"fleet/coordinator/db"
)

// A JSON object of a loop strategy or variant overrides, by key. Values are kept as rawOf writes them (keys sorted, no
// HTML escaping), so a digest through canonical and a hash through jsonBytes see what they saw of the decoded object.
//
// rawOf: v as JSON, keys sorted and without HTML escaping (the form canonical gives a decoded value).
func rawOf(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return json.RawMessage(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// rawObject: raw as an object with each value in rawOf form; ok false when raw is not an object.
func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(t, &m) != nil {
		return nil, false
	}
	for k, x := range m {
		m[k] = rawOf(rawValue(x))
	}
	return m, true
}

// rawObjectOr: raw as an object, or an empty one when it is absent, null or not an object.
func rawObjectOr(raw json.RawMessage) map[string]json.RawMessage {
	if m, ok := rawObject(raw); ok {
		return m
	}
	return map[string]json.RawMessage{}
}

// rawValue: a JSON value decoded (nil for absent).
func rawValue(raw json.RawMessage) any {
	var v any
	if len(raw) > 0 {
		json.Unmarshal(raw, &v)
	}
	return v
}

// rawSet: m has key k with a value other than null.
func rawSet(m map[string]json.RawMessage, k string) bool {
	return rawValue(m[k]) != nil
}

// rawArray: raw is a JSON array.
func rawArray(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '['
}

func rawKeysOnly(m map[string]json.RawMessage, allowed ...string) bool {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}

// loopStrategy validates a loop's strategy and returns it as stored, with the caller's owner-override authority.
func loopStrategy(q *Request, raw any) (map[string]json.RawMessage, error) {
	s, ok := rawObject(rawOf(raw))
	if !ok || !rawKeysOnly(s, "generator", "allocator", "max_iterations", "no_gain") {
		return nil, fail(400, "Loop strategy requires generator, allocator and optional explicit stop rules")
	}
	g, okG := rawObject(s["generator"])
	params, okP := rawObject(g["params"])
	kind := rawValue(g["kind"])
	if !okG || !rawKeysOnly(g, "kind", "params") || !okP || (kind != "list" && kind != "grid" && kind != "propose") {
		return nil, fail(400, "Loop generator is list, grid or propose with params")
	}
	if a, ok := rawObject(s["allocator"]); !ok || rawValue(a["kind"]) != "full" {
		return nil, fail(400, "Loop allocator is full")
	}
	if v, has := s["max_iterations"]; has {
		if n, ok := intOK(rawValue(v)); !ok || n < 1 {
			return nil, fail(400, "max_iterations must be positive")
		}
	}
	if v, has := s["no_gain"]; has {
		m, ok := rawObject(v)
		if n, okK := intOK(rawValue(m["k"])); !ok || !rawKeysOnly(m, "k") || !okK || n < 1 {
			return nil, fail(400, "no_gain requires explicit positive k")
		}
	}
	// Persist the caller's existing owner-override authority; background reconciliation is not a new grant.
	owner := q.person == nil || fullAccess(q.person)
	s["ownerOverrides"] = rawOf(owner)
	base := labLoopBase{OwnerOverrides: owner}
	if kind == "list" {
		if !rawArray(params["variants"]) {
			return nil, fail(400, "list params.variants must be an array")
		}
		var vs []labProposal
		for _, x := range rawValue(params["variants"]).([]any) {
			vs = append(vs, labProposalOf(x))
		}
		if _, err := loopVariantsOf(base, vs, 1); err != nil {
			return nil, err
		}
	}
	if kind == "grid" {
		axes, ok := rawObject(params["overrides"])
		if !ok {
			return nil, fail(400, "grid params.overrides must be an object")
		}
		if !rawKeysOnly(axes, labVariantKeys...) {
			return nil, fail(400, "Invalid grid override key")
		}
		for _, axis := range axes {
			if !rawArray(axis) {
				return nil, fail(400, "Grid axes must be arrays")
			}
		}
	}
	if ov, has := params["step_overrides"]; has {
		if m, ok := rawObject(ov); !ok || !rawKeysOnly(m, labOverrides...) {
			return nil, fail(400, "Invalid strategy step_overrides")
		}
		if _, err := loopVariantsOf(base, []labProposal{{Label: "strategy", Overrides: ov}}, 1); err != nil {
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

// labProposal: a proposed loop variant (a strategy's list, a strategy run's output).
type labProposal struct {
	Label     string          `json:"label"`
	Overrides json.RawMessage `json:"overrides"`
}

func labProposalOf(x any) labProposal {
	p, _ := asT[labProposal](x)
	return p
}

// labLoopBase: what new loop variants are added to: the experiment's pipeline, its variants and the owner authority.
type labLoopBase struct {
	PipelineID     string
	Variants       []json.RawMessage
	OwnerOverrides bool
}

// labLoopVariant: a loop-generated variant as stored in experiment.variants; keys in canonical order.
type labLoopVariant struct {
	ConfigRef     string                     `json:"config_ref"`
	Iteration     int64                      `json:"iteration"`
	Label         string                     `json:"label"`
	Overrides     map[string]json.RawMessage `json:"overrides"`
	ProposalLabel string                     `json:"proposalLabel"`
}

// loopVariants: the experiment's variants and the proposed ones, validated and labelled for iteration.
func loopVariants(exp doc, proposed []any, iteration int64) ([]json.RawMessage, error) {
	e, _ := asT[struct {
		PipelineID string            `json:"pipeline_id"`
		Variants   []json.RawMessage `json:"variants"`
		Strategy   struct {
			OwnerOverrides bool `json:"ownerOverrides"`
		} `json:"strategy"`
	}](exp)
	ps := []labProposal{}
	for _, x := range proposed {
		ps = append(ps, labProposalOf(x))
	}
	return loopVariantsOf(labLoopBase{PipelineID: e.PipelineID, Variants: e.Variants, OwnerOverrides: e.Strategy.OwnerOverrides}, ps, iteration)
}

func loopVariantsOf(base labLoopBase, proposed []labProposal, iteration int64) ([]json.RawMessage, error) {
	out := append([]json.RawMessage{}, base.Variants...)
	used := map[string]bool{}
	for _, v := range out {
		existing, _ := asT[labVariant](v)
		used[existing.Label] = true
	}
	for i, v := range proposed {
		if !labLabel.MatchString(v.Label) {
			return nil, fail(400, "Invalid strategy variant label")
		}
		ov := map[string]json.RawMessage{}
		if rawValue(v.Overrides) != nil {
			m, ok := rawObject(v.Overrides)
			if !ok {
				return nil, fail(400, "Invalid strategy overrides")
			}
			ov = m
		}
		if !rawKeysOnly(ov, labVariantKeys...) {
			return nil, fail(400, "Invalid strategy overrides")
		}
		if !base.OwnerOverrides && (rawSet(ov, "host") || rawSet(ov, "account") || rawSet(ov, "profile")) {
			return nil, fail(403, "Only the owner overrides host, account or profile")
		}
		if args, has := ov["arguments"]; has {
			m, ok := rawObject(args)
			if !ok {
				return nil, fail(400, "arguments must map step IDs to objects")
			}
			for _, arg := range m {
				if _, ok := rawObject(arg); !ok {
					return nil, fail(400, "arguments must map step IDs to objects")
				}
			}
		}
		if bands, has := ov["bands"]; has {
			if _, _, err := bandsOf(rawValue(bands)); err != nil {
				return nil, err
			}
		}
		if w := rawValue(ov["web_route"]); w != nil && w != "connections" && w != "builtin" {
			return nil, fail(400, "Invalid strategy web_route")
		}
		id := fmt.Sprintf("i%d %s", iteration, sha256hex(jsonBytes([]any{i, v.Label, ov}))[:16])
		for used[id] {
			id += "x"
		}
		used[id] = true
		out = append(out, jsonBytes(labLoopVariant{Label: id, ProposalLabel: v.Label, Iteration: iteration, Overrides: ov, ConfigRef: labConfigDigestOf(base.PipelineID, ov)}))
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
		KeepBest      bool            `json:"keep_best"`
		StepOverrides json.RawMessage `json:"step_overrides"`
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
		ov := rawObjectOr(generator.Params.StepOverrides)
		if !rawKeysOnly(ov, labOverrides...) {
			return halt("strategy:invalid-overrides")
		}
		if !s.OwnerOverrides && (rawSet(ov, "host") || rawSet(ov, "account") || rawSet(ov, "profile")) {
			return halt("strategy:unauthorized-overrides")
		}
		next := iteration
		id, err := expInsertLabRun(q, exp, "loop:"+sha256hex(jsonBytes([]any{exp["id"], iteration})), pipeline+".json", labStrategyExample(inputs),
			&labVariant{Overrides: rawValue(jsonBytes(ov)), ConfigRef: sha256hex(jsonBytes(exp["strategy"]))}, labRunExtra{LoopIteration: &next})
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
	proposed := []labProposal{}
	for _, x := range out.Variants {
		proposed = append(proposed, labProposalOf(x))
	}
	if generator.Params.KeepBest {
		best := loopBestVariant(exp, trials, scores)
		if best == nil {
			return halt("strategy:best-unavailable")
		}
		for i, v := range proposed {
			proposed[i].Overrides = jsonBytes(loopMergeOverrides(rawObjectOr(rawOf(best.Overrides)), rawObjectOr(v.Overrides)))
		}
	}
	e2, _ := asT[struct {
		Variants []json.RawMessage `json:"variants"`
	}](exp)
	variants, err := loopVariantsOf(labLoopBase{PipelineID: e.PipelineID, Variants: e2.Variants, OwnerOverrides: s.OwnerOverrides}, proposed, iteration+1)
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

// loopMergeOverrides: extra over base; arguments merge per step and argument.
func loopMergeOverrides(base, extra map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	if rawSet(extra, "arguments") {
		steps := rawObjectOr(base["arguments"])
		for k, v := range rawObjectOr(extra["arguments"]) {
			args := rawObjectOr(steps[k])
			for n, a := range rawObjectOr(v) {
				args[n] = a
			}
			steps[k] = rawOf(args)
		}
		out["arguments"] = rawOf(steps)
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
