package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"fleet/contract"
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

// labRawObject: raw as an object with each value in rawOf form; ok false when raw is not an object.
func labRawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
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
	if m, ok := labRawObject(raw); ok {
		return m
	}
	return map[string]json.RawMessage{}
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
	s, ok := labRawObject(rawOf(raw))
	if !ok || !rawKeysOnly(s, "generator", "allocator", "max_iterations", "no_gain") {
		return nil, fail(400, "Loop strategy requires generator, allocator and optional explicit stop rules")
	}
	g, okG := labRawObject(s["generator"])
	params, okP := labRawObject(g["params"])
	kind := rawValue(g["kind"])
	if !okG || !rawKeysOnly(g, "kind", "params") || !okP || (kind != "list" && kind != "grid" && kind != "propose") {
		return nil, fail(400, "Loop generator is list, grid or propose with params")
	}
	if a, ok := labRawObject(s["allocator"]); !ok || rawValue(a["kind"]) != "full" {
		return nil, fail(400, "Loop allocator is full")
	}
	if v, has := s["max_iterations"]; has {
		if n, ok := intOK(rawValue(v)); !ok || n < 1 {
			return nil, fail(400, "max_iterations must be positive")
		}
	}
	if v, has := s["no_gain"]; has {
		m, ok := labRawObject(v)
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
		axes, ok := labRawObject(params["overrides"])
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
		if m, ok := labRawObject(ov); !ok || !rawKeysOnly(m, labOverrides...) {
			return nil, fail(400, "Invalid strategy step_overrides")
		}
		if _, err := loopVariantsOf(base, []labProposal{{Label: "strategy", Overrides: ov}}, 1); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func loopIteration(e *labExperiment) int64 {
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
func loopVariants(e *labExperiment, proposed []any, iteration int64) ([]json.RawMessage, error) {
	var s labLoopState
	json.Unmarshal(e.Strategy, &s)
	ps := []labProposal{}
	for _, x := range proposed {
		ps = append(ps, labProposalOf(x))
	}
	return loopVariantsOf(labLoopBase{PipelineID: e.PipelineID, Variants: e.storedVariants(), OwnerOverrides: s.OwnerOverrides}, ps, iteration)
}

// storedVariants: the experiment's variants as stored, each its JSON.
func (e *labExperiment) storedVariants() []json.RawMessage {
	out := []json.RawMessage{}
	for _, v := range e.Variants {
		out = append(out, jsonBytes(v))
	}
	return out
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
			m, ok := labRawObject(v.Overrides)
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
			m, ok := labRawObject(args)
			if !ok {
				return nil, fail(400, "arguments must map step IDs to objects")
			}
			for _, arg := range m {
				if _, ok := labRawObject(arg); !ok {
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
func loopNoGain(e *labExperiment, trials []*labRun, scores []labScore, k int64) (bool, bool) {
	if len(e.Variants) == 0 {
		return false, false
	}
	baseline := e.Variants[0].Label
	byVariant := map[string][]float64{}
	byRepeat := map[string][]float64{}
	for _, s := range scoredLatest(e, trials, scores, e.primaryScore) {
		if s.split != "tune" {
			continue
		}
		if s.score.unscored() {
			continue
		}
		value := scoreNumber(s.score)
		byVariant[s.variant] = append(byVariant[s.variant], value)
		if s.variant == baseline {
			r := fmt.Sprint(s.trial.cell().Repeat)
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
	for iteration := int64(0); iteration <= loopIteration(e); iteration++ {
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
	Allocator         json.RawMessage   `json:"allocator"`
	Budgets           any               `json:"budgets"`
	ExperimentID      any               `json:"experimentId"`
	Generator         json.RawMessage   `json:"generator"`
	Iteration         int64             `json:"iteration"`
	LosingTranscripts []labLosing       `json:"losingTranscripts"`
	PrimaryEvaluator  any               `json:"primaryEvaluator"`
	Scores            []labScore        `json:"scores"`
	Spend             contract.LabSpend `json:"spend"`
	Variants          any               `json:"variants"`
}

func loopAdvance(q *Request, e *labExperiment, trials, runs []*labRun, scores []labScore, spend contract.LabSpend, launched *[]string) error {
	var s labLoopState
	json.Unmarshal(e.Strategy, &s)
	var generator labLoopGenerator
	json.Unmarshal(s.Generator, &generator)
	iteration := loopIteration(e)
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
	if !loopPrimaryComplete(e, trials, scores) {
		return halt("primary-score-unavailable")
	}
	if s.NoGain != nil {
		done, available := loopNoGain(e, trials, scores, s.NoGain.K)
		if !available {
			return halt("primary-score-unavailable")
		}
		if done {
			return stop("no_gain")
		}
	}
	var strategyRun *labRun
	for _, r := range runs {
		if st := r.LoopStrategy; st != nil && st.Iteration != nil && *st.Iteration == iteration {
			strategyRun = r
		}
	}
	if strategyRun == nil {
		pipeline := "lab-strategy"
		if generator.Kind == "propose" {
			pipeline = "lab-strategy-propose"
		}
		// Held-out results remain in history but never steer proposal generation.
		tuneTrials := map[string]bool{}
		for _, t := range trials {
			if pin := e.pin(t.cell().Example); pin != nil && pin.Split == "tune" {
				tuneTrials[t.ID] = true
			}
		}
		tuneScores := []labScore{}
		for _, score := range scores {
			if tuneTrials[score.first()] {
				tuneScores = append(tuneScores, score)
			}
		}
		snapshot := labLoopSnapshot{ExperimentID: e.ID, Iteration: iteration, Variants: listOrEmpty(e.Variants), Scores: tuneScores, PrimaryEvaluator: e.PrimaryEvaluator,
			Generator: s.Generator, Allocator: s.Allocator, Spend: spend, Budgets: listOrEmpty(e.Budgets), LosingTranscripts: []labLosing{}}
		inputs := []labStrategyInput{}
		if generator.Kind == "propose" {
			best := loopBestVariant(e, trials, scores)
			order, latest := expLatest(trials)
			for _, key := range order {
				t := latest[key]
				if !tuneTrials[t.ID] || best != nil && t.cell().Variant == best.Label {
					continue
				}
				run := t
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
		id, err := labStartRun(q, e, labRunStart{RequestID: "loop:" + sha256hex(jsonBytes([]any{e.ID, iteration})), File: pipeline + ".json",
			Example: labStrategyExample{Inputs: inputs}, Variant: &labVariant{Overrides: rawValue(jsonBytes(ov)), ConfigRef: sha256hex(jsonBytes(rawValue(e.Strategy)))},
			Extra: labRunExtra{LoopStrategy: &labLoopMark{Iteration: &next}}})
		if err != nil {
			return err
		}
		if id != "" {
			*launched = append(*launched, id)
		}
		return nil
	}
	if !strategyRun.terminal() {
		return nil
	}
	if !comparable(strategyRun) {
		problem, _ := rawValue(strategyRun.Problem).(string)
		return halt("strategy:" + problem)
	}
	output, _ := labRawObject(strategyRun.outputObject())
	var out labLoopOutput
	json.Unmarshal(strategyRun.outputObject(), &out)
	if !rawKeysOnly(output, "action", "variants", "reason") || !rawSet(output, "reason") {
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
		best := loopBestVariant(e, trials, scores)
		if best == nil {
			return halt("strategy:best-unavailable")
		}
		for i, v := range proposed {
			proposed[i].Overrides = jsonBytes(loopMergeOverrides(rawObjectOr(rawOf(best.Overrides)), rawObjectOr(v.Overrides)))
		}
	}
	variants, err := loopVariantsOf(labLoopBase{PipelineID: e.PipelineID, Variants: e.storedVariants(), OwnerOverrides: s.OwnerOverrides}, proposed, iteration+1)
	if err != nil {
		return halt("strategy:" + err.Error())
	}
	return q.db().LabVariantsSetRunning(q.ctx(), db.LabVariantsSetRunningParams{ID: e.ID, Variants: jsonBytes(variants)})
}

// primaryScore: s is a score of the primary evaluator.
func (e *labExperiment) primaryScore(s labScore) bool {
	return e.PrimaryEvaluator != nil && s.Evaluator == *e.PrimaryEvaluator
}

func loopBestVariant(e *labExperiment, trials []*labRun, scores []labScore) *labVariant {
	if !loopPrimaryComplete(e, trials, scores) {
		return nil
	}
	values := map[string][]float64{}
	for _, s := range scoredLatest(e, trials, scores, e.primaryScore) {
		if s.split == "tune" {
			values[s.variant] = append(values[s.variant], scoreNumber(s.score))
		}
	}
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

func loopPrimaryComplete(e *labExperiment, trials []*labRun, scores []labScore) bool {
	tune := e.tune()
	if tune == 0 || e.Repeats < 1 {
		return false
	}
	counts := map[string]int{}
	for _, s := range scoredLatest(e, trials, scores, e.primaryScore) {
		if s.split != "tune" || s.score.unscored() {
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
