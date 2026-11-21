// Lab (docs/designs/fleet-lab.md): examples, experiments, scores and ratings.
// An example is a lab-scope artifact with role 'example'. An experiment is pinned approved examples x variants x repeats of
// one v2 pipeline; a trial is a pipeline_run with experiment_id, variant and request_id; a judge run has variant null.
// labWorkStep (the work kit's lab queue, pipeline_work.go) is the one function that moves an experiment, under the
// experiment's scope lock.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var (
	labIdent     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
	labIdent200  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	labOrigin    = regexp.MustCompile(`^(task|export):[A-Za-z0-9._-]{1,80}$`) // export: a project export row (contracts/lab-export.schema.json)
	labCommit    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	labLabel     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,59}$`)
	draftExample = regexp.MustCompile(`/examples/[^/]+\.json$`)
	labOverrides = []string{"profile", "model", "skills", "prompt", "web_route", "account", "host", "arguments"}
	// labVariantKeys: a Lab variant's keys; bands steer its typed evaluator, never a run, so runs take labOverrides only.
	labVariantKeys = append(append([]string{}, labOverrides...), "bands")
)

func labStr(v any, what string, max int) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > max {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func labID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func expID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent200.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

// labOpt: an optional column as a map value: nil when absent.
func labOpt[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// labJSON: a JSON column's value decoded; SQL NULL is nil.
func labJSON(b []byte) any {
	if b == nil {
		return nil
	}
	var v any
	json.Unmarshal(b, &v)
	return v
}

// labIDStrings: trial IDs as strings (a non-string is "").
func labIDStrings(ids []any) []string {
	out := []string{}
	for _, id := range ids {
		s, _ := id.(string)
		out = append(out, s)
	}
	return out
}

// ---- experiments ----

func cellKey(ex, variant string, repeat int64) string {
	return fmt.Sprintf("%v|%v|%d", ex, variant, repeat)
}

func expRow(q *Request, id string) (*labExperiment, error) {
	e, err := q.db().LabExperiment(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	return expOf(e)
}

// labTimeText: a timestamp as its JSON text (RFC 3339), as a reply shows it.
func labTimeText(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

// expView: the experiment's record as the Lab shows it.
func expView(e *labExperiment) (contract.LabExperimentRecord, error) {
	r := contract.LabExperimentRecord{Id: e.ID, ProjectId: e.ProjectID, Kind: e.Kind, PipelineId: e.PipelineID, Repeats: float64(e.Repeats), PrimaryEvaluator: e.PrimaryEvaluator,
		Strategy: e.Strategy, State: e.State, StopReason: e.StopReason, CreatedAt: *labTimeText(&e.CreatedAt), Goal: e.Goal, Hypothesis: e.Hypothesis, DecisionRule: e.DecisionRule,
		LabGroup: e.LabGroup, Decision: e.Decision, DecisionReason: e.DecisionReason, DecidedBy: e.DecidedBy, DecidedAt: labTimeText(e.DecidedAt), LaunchedAt: labTimeText(e.LaunchedAt),
		RequestedBy: e.RequestedBy, RequesterRemovedAt: labTimeText(e.RequesterRemovedAt), Evaluators: []json.RawMessage{}}
	if r.Strategy == nil {
		r.Strategy = json.RawMessage("null")
	}
	for _, ev := range e.Evaluators {
		r.Evaluators = append(r.Evaluators, jsonBytes(ev))
	}
	var err error
	if r.Examples, err = asT[[]contract.LabExample](listOrEmpty(e.Examples)); err != nil {
		return r, err
	}
	if r.Variants, err = asT[[]contract.LabVariant](listOrEmpty(e.Variants)); err != nil {
		return r, err
	}
	r.Budgets, err = asT[[]contract.LabBudget](listOrEmpty(e.Budgets))
	return r, err
}

// listOrEmpty: a list, empty when it is nil (a reply shows [] for none).
func listOrEmpty[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// recordFields: the experiment record fields of a create request, frozen at launch (fleet-lab.md, Experiment record).
var recordFields = []string{"goal", "hypothesis", "decisionRule", "labGroup", "primaryEvaluator"}

var recordColumns = map[string]string{"goal": "goal", "hypothesis": "hypothesis", "decisionRule": "decision_rule", "labGroup": "lab_group", "primaryEvaluator": "primary_evaluator"}

// expLocked: the experiment row, locked for this transaction (a record change reads and writes it once).
func expLocked(q *Request, id string) (*labExperiment, error) {
	e, err := q.db().LabExperimentForUpdate(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	return expOf(e)
}

// expLockedRow: the experiment row, locked for this transaction.
func expLockedRow(q *Request, id string) (db.Experiment, error) {
	e, err := q.db().LabExperimentForUpdate(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, fail(404, "Unknown experiment")
	}
	return e, err
}

// labExperimentView: an experiment as lab_experiment_get shows it (expGet).
type labExperimentView = contract.LabExperimentGetReply

// labDecode: a request value decoded; exact keeps numbers as their text (json.Number), as a stored definition keeps them.
func labDecode(raw json.RawMessage, exact bool) any {
	if exact {
		return decodeNumbers(string(raw))
	}
	return rawValue(raw)
}

// labOwnerOverride: overrides naming a host, account or profile, which only the owner sets.
func labOwnerOverride(q *Request, o map[string]json.RawMessage) bool {
	return q.person != nil && !fullAccess(q.person) && (rawSet(o, "host") || rawSet(o, "account") || rawSet(o, "profile"))
}

// labVariantIn: a variant as a create request gives it.
type labVariantIn struct {
	Label      json.RawMessage `json:"label"`
	PipelineID json.RawMessage `json:"pipelineId"`
	Overrides  json.RawMessage `json:"overrides"`
}

// labEvaluatorIn: an evaluator as a create request gives it.
type labEvaluatorIn struct {
	Kind      json.RawMessage `json:"kind"`
	Ref       json.RawMessage `json:"ref"`
	Form      json.RawMessage `json:"form"`
	Overrides json.RawMessage `json:"overrides"`
}

// labBudgetIn: a budget as a create request gives it.
type labBudgetIn struct {
	Unit   json.RawMessage `json:"unit"`
	Limit  json.RawMessage `json:"limit"`
	Action json.RawMessage `json:"action"`
}

// labList: a request list, empty when it is absent or not a list.
func labList(raw json.RawMessage) []json.RawMessage {
	var l []json.RawMessage
	json.Unmarshal(raw, &l)
	return l
}

// labText: a request value when it is text, else "".
func labText(raw json.RawMessage) string {
	s, _ := rawValue(raw).(string)
	return s
}

// expCreate: a draft experiment from a create request; exact keeps the request's numbers as their text (the variants'
// overrides of a catalogue method, lab_compare_methods).
func expCreate(q *Request, r *contract.LabExperimentCreateRequest, exact bool) (labExperimentView, error) {
	var none labExperimentView
	// Serialize creator validation with the existing people projection/removal transaction.
	if err := acquireTxLock(q, GLOBAL); err != nil {
		return none, err
	}
	if q.person == nil {
		return none, fail(403, "Lab creation needs an authenticated requester")
	}
	if err := refreshRequestAccess(q); err != nil {
		return none, err
	}
	// The declared auth again, under the lock: a grant removed meanwhile refuses the creation.
	if err := declaredAuth(q, "lab_experiment_create"); err != nil {
		return none, err
	}

	record := map[string]json.RawMessage{"goal": jsonBytes(r.Goal), "hypothesis": jsonBytes(r.Hypothesis), "decisionRule": r.DecisionRule, "labGroup": r.LabGroup,
		"primaryEvaluator": r.PrimaryEvaluator}
	for _, k := range recordFields {
		if v := rawValue(record[k]); v != nil {
			if _, ok := v.(string); !ok {
				return none, fail(400, k+" must be text")
			}
		}
	}
	kind := deref(r.Kind)
	if kind != "single" && kind != "ab" && kind != "loop" {
		return none, fail(400, "Invalid kind")
	}
	if r.Strategy != nil && kind != "loop" {
		return none, fail(400, "strategy is for loop experiments")
	}
	projectID, err := expID(optText(r.ProjectId), "projectId")
	if err != nil {
		return none, err
	}
	pipelineID, err := expID(optText(r.PipelineId), "pipelineId")
	if err != nil {
		return none, err
	}
	if _, err := loadPipelineDef(q, pipelineID+".json"); err != nil {
		return none, err
	}
	if !projectKnown(q, projectID) {
		return none, fail(400, "Unknown project "+projectID)
	}
	exl := labList(r.Examples)
	if len(exl) == 0 {
		return none, fail(400, "Expected examples (approved example artifact IDs)")
	}
	examples, seen := []labPin{}, map[string]bool{}
	for _, x := range exl {
		id, err := expID(rawValue(x), "example artifact")
		if err != nil {
			return none, err
		}
		ex, err := labExampleRead(q, id)
		if he, ok := err.(*HTTPError); ok && he.Code == 404 {
			return none, fail(400, "Unknown example "+id)
		} else if err != nil {
			return none, err
		}
		if ex.state != "approved" {
			return none, fail(409, "Example "+id+" is not approved")
		}
		hash, err := q.db().LabArtifactHash(q.ctx(), id)
		if err != nil {
			return none, err
		}
		pin := exampleRef(ex.example.ID, ex.example.Version)
		if seen[pin] {
			return none, fail(400, "Duplicate example")
		}
		seen[pin] = true
		examples = append(examples, labPin{ID: pin, ArtifactID: id, Hash: hash, Split: ex.example.Split})
	}
	vl := labList(r.Variants)
	if len(vl) == 0 {
		return none, fail(400, "Expected variants")
	}
	variants, labels := []labVariant{}, map[string]bool{}
	for _, x := range vl {
		var v labVariantIn
		json.Unmarshal(x, &v)
		label := labText(v.Label)
		if !labLabel.MatchString(label) {
			return none, fail(400, "Invalid variant label")
		}
		raw := v.Overrides
		if rawValue(raw) == nil {
			raw = json.RawMessage("{}")
		}
		o, ok := labRawObject(raw)
		if !ok || !rawKeysOnly(o, labVariantKeys...) {
			return none, fail(400, "Variant overrides are "+strings.Join(labVariantKeys, ", "))
		}
		if labOwnerOverride(q, o) {
			return none, fail(403, "Only the owner overrides host, account or profile")
		}
		if w, has := o["web_route"]; has && rawValue(w) != "connections" && rawValue(w) != "builtin" {
			return none, fail(400, "web_route must be connections or builtin")
		}
		if args, has := o["arguments"]; has {
			m, ok := labRawObject(args)
			for _, step := range m {
				if _, isObject := labRawObject(step); !isObject {
					ok = false
				}
			}
			if !ok {
				return none, fail(400, "arguments must be {<step id>: {<argument>: value}}")
			}
		}
		if bands, has := o["bands"]; has {
			if _, _, err := bandsOf(rawValue(bands)); err != nil {
				return none, err
			}
		}
		if labels[label] {
			return none, fail(400, "Duplicate variant label")
		}
		labels[label] = true
		variantPipeline := pipelineID
		if rawValue(v.PipelineID) != nil {
			variantPipeline, err = expID(rawValue(v.PipelineID), "variant pipelineId")
			if err != nil {
				return none, err
			}
			if _, err := loadPipelineDef(q, variantPipeline+".json"); err != nil {
				return none, err
			}
		}
		overrides := labDecode(raw, exact)
		variants = append(variants, labVariant{Label: label, PipelineID: variantPipeline, Overrides: overrides, ConfigRef: labConfigDigestOf(variantPipeline, overrides)})
	}
	if kind == "single" && len(variants) != 1 {
		return none, fail(400, "A single experiment has one variant")
	}
	if kind == "ab" && len(variants) < 2 {
		return none, fail(400, "An ab experiment has two or more variants")
	}
	repeats := int64(1)
	if rawValue(r.Repeats) != nil {
		n, ok := intOK(rawValue(r.Repeats))
		if !ok || n < 1 {
			return none, fail(400, "Invalid repeats")
		}
		repeats = n
	}
	evaluators := []labEvaluator{}
	names := map[string]bool{}
	for _, x := range labList(r.Evaluators) {
		var e labEvaluatorIn
		json.Unmarshal(x, &e)
		k, f, ref := labText(e.Kind), labText(e.Form), rawValue(e.Ref)
		switch k {
		case "script":
			if ref != "example.checks" || f != "pass_fail" && f != "metric" {
				return none, fail(400, "A script evaluator is {kind: script, ref: example.checks, form: pass_fail|metric}")
			}
		case "judge":
			r, err := expID(ref, "judge pipeline")
			if err != nil {
				return none, err
			}
			if _, err := loadPipelineDef(q, r+".json"); err != nil {
				return none, err
			}
			if f != "pass_fail" && f != "scale" && f != "pick_best" {
				return none, fail(400, "A judge form is pass_fail, scale or pick_best")
			}
		case "typed":
			if ref != "labels" || f != "typed" {
				return none, fail(400, "A typed evaluator is {kind: typed, ref: labels, form: typed}: the trial's p through the variant's bands against the example's label")
			}
		case "human":
			if ref != "owner" || f != "pass_fail" && f != "pick_best" && f != "scale" {
				return none, fail(400, "A human evaluator is {kind: human, ref: owner, form: pass_fail|pick_best|scale}")
			}
		default:
			return none, fail(400, "Evaluator kind is script, typed, judge or human")
		}
		ev := labEvaluator{Kind: k, Ref: labText(e.Ref), Form: f}
		if len(e.Overrides) > 0 {
			ov, ok := labRawObject(e.Overrides)
			if k != "judge" || !ok || !rawKeysOnly(ov, labOverrides...) {
				return none, fail(400, "Judge overrides use pipeline override keys")
			}
			if labOwnerOverride(q, ov) {
				return none, fail(403, "Only the owner overrides host, account or profile")
			}
			ev.Overrides = labDecode(e.Overrides, exact)
		}
		names[ev.name()] = true
		evaluators = append(evaluators, ev)
	}
	primary := labText(r.PrimaryEvaluator)
	if rawValue(r.PrimaryEvaluator) != nil && !names[primary] {
		return none, fail(400, "primaryEvaluator names none of the evaluators")
	}
	var strategy any
	if kind == "loop" {
		if primary == "" || strings.HasSuffix(primary, ":pick_best") {
			return none, fail(400, "Loop primaryEvaluator must name a scalar evaluator")
		}
		st, err := loopStrategy(q, r.Strategy)
		if err != nil {
			return none, err
		}
		strategy = st
	}
	budgets := []labBudget{}
	for _, x := range labList(r.Budgets) {
		var bu labBudgetIn
		json.Unmarshal(x, &bu)
		u := labText(bu.Unit)
		if u != "trials" && u != "wall_ms" && u != "calls" && u != "credits" {
			return none, fail(400, "Budget unit is trials, wall_ms, calls, credits")
		}
		l, ok := numOK(rawValue(bu.Limit))
		if !ok || !(l > 0) || math.IsInf(l, 0) {
			return none, fail(400, "Invalid budget limit")
		}
		act := labText(bu.Action)
		if rawValue(bu.Action) == nil {
			act = "pause"
		}
		if act != "warn" && act != "pause" && act != "stop" {
			return none, fail(400, "Budget action is warn, pause or stop")
		}
		budgets = append(budgets, labBudget{Unit: u, Limit: l, Action: act})
	}
	if repeats > math.MaxInt32 {
		return none, fail(400, "Invalid repeats")
	}
	text := func(k string) *string {
		if s, ok := rawValue(record[k]).(string); ok {
			return &s
		}
		return nil
	}
	id := newID()
	if err := q.db().LabExperimentInsert(q.ctx(), db.LabExperimentInsertParams{ID: id, ProjectID: projectID, Kind: kind, PipelineID: pipelineID, Examples: jsonBytes(examples),
		Variants: jsonBytes(variants), Repeats: int32(repeats), Evaluators: jsonBytes(evaluators), PrimaryEvaluator: text("primaryEvaluator"), Budgets: jsonBytes(budgets),
		Goal: text("goal"), Hypothesis: text("hypothesis"), DecisionRule: text("decisionRule"), LabGroup: text("labGroup"), Strategy: jsonBytes(strategy),
		RequestedBy: &q.person.Email}); err != nil {
		return none, err
	}
	return expGet(q, id)
}

func expSpend(q *Request, id string) (contract.LabSpend, error) {
	run, err := q.db().LabTrialSpend(q.ctx(), &id)
	if err != nil {
		return contract.LabSpend{}, err
	}
	tool, err := q.db().LabToolSpend(q.ctx(), &id)
	if err != nil {
		return contract.LabSpend{}, err
	}
	basis := "measured"
	if tool.Unmeasured > 0 {
		basis = "partial"
	}
	unmeasured := float64(tool.Unmeasured)
	return contract.LabSpend{Trials: contract.LabMeasure{Value: float64(run.Trials), Basis: "measured"}, WallMs: contract.LabMeasure{Value: math.Round(run.WallMs), Basis: "measured"},
		Calls: contract.LabMeasure{Value: float64(tool.Calls), Basis: "measured"}, Credits: contract.LabMeasure{Value: tool.Credits, Basis: basis, Unmeasured: &unmeasured}}, nil
}

// spendOf: the spend in a budget's unit.
func spendOf(s contract.LabSpend, unit string) float64 {
	switch unit {
	case "trials":
		return s.Trials.Value
	case "wall_ms":
		return s.WallMs.Value
	case "calls":
		return s.Calls.Value
	case "credits":
		return s.Credits.Value
	}
	return 0
}

// labConflict: a 409 whose body names its reason.
func labConflict(msg, reason string) error {
	return &HTTPError{Code: 409, Msg: msg, Extra: reasonExtra{reason}}
}

// labJudgeView: a judge run as the experiment shows it.
type labJudgeView struct {
	ID        string          `json:"id"`
	State     string          `json:"state"`
	Evaluator string          `json:"evaluator"`
	TrialID   *string         `json:"trialId"`
	TrialIDs  []string        `json:"trialIds"`
	GroupID   *string         `json:"groupId"`
	Order     *int64          `json:"order"`
	Output    json.RawMessage `json:"output"`
	Problem   json.RawMessage `json:"problem"`
}

// labNoteView: an experiment note as the experiment shows it.
type labNoteView struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Field  *string         `json:"field"`
	Old    json.RawMessage `json:"old"`
	New    json.RawMessage `json:"new"`
	Text   string          `json:"text"`
	Author string          `json:"author"`
	At     time.Time       `json:"at"`
}

// labOptText: text, nil when empty.
func labOptText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// labNull: raw, null when absent.
func labNull(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return json.RawMessage("null")
	}
	return raw
}

func expGet(q *Request, id string) (labExperimentView, error) {
	var out labExperimentView
	exp, err := expRow(q, id)
	if err != nil {
		return out, err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return out, err
	}
	trials := expTrials(runs)
	cur, err := scoresCurrent(q, id)
	if err != nil {
		return out, err
	}
	s, err := expSpend(q, id)
	if err != nil {
		return out, err
	}
	stepRows, err := q.db().LabStepAgents(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	credits, err := q.db().LabRunCredits(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	// Each trial's provider usage, priced per call (fleet-analytics.md: usage x the latest entry in force at the call).
	usageRows, err := q.db().LabRunUsage(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	costs := map[string][]usage{}
	for _, u := range usageRows {
		var amount *float64
		if u.HasAmount {
			a := u.Amount
			amount = &a
		}
		costs[u.Run] = append(costs[u.Run], usage{u.Subject, u.Unit, amount, u.At.Time})
	}
	prices, err := priceEntries(q)
	if err != nil {
		return out, err
	}
	trialCost := map[string][]contract.CostFigure{}
	for run, us := range costs {
		trialCost[run] = estimateCost(prices, us)
	}
	uniq := func(l []string) []string {
		seen, out := map[string]bool{}, []string{}
		for _, s := range l {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	trialView := func(t *labRun) (contract.LabTrialRecord, error) {
		hosts, harnesses, models := []string{}, []string{}, []string{}
		for _, r := range stepRows {
			if r.Run == "" || r.Run != t.ID {
				continue
			}
			hosts, models = append(hosts, deref(r.HostID)), append(models, deref(r.Model))
			harnesses = append(harnesses, strings.TrimSpace(strings.Join([]string{deref(r.Harness), deref(r.HarnessVersion)}, " ")))
		}
		calls, credit, basis := float64(0), float64(0), "measured"
		for _, c := range credits {
			if c.Run != "" && c.Run == t.ID {
				calls, credit = float64(c.Calls), c.Credits
				if c.Unmeasured > 0 {
					basis = "partial"
				}
			}
		}
		var o struct {
			WebRoute json.RawMessage `json:"web_route"`
		}
		json.Unmarshal(t.Overrides, &o)
		web := o.WebRoute
		if rawValue(web) == nil {
			web = jsonBytes("connections")
		}
		mine := []contract.LabScore{}
		for _, sc := range cur {
			if slices.Contains(sc.TrialIDs, t.ID) {
				v, err := asT[contract.LabScore](sc)
				if err != nil {
					return contract.LabTrialRecord{}, err
				}
				mine = append(mine, v)
			}
		}
		cell, err := asT[contract.LabTrialCell](t.Cell)
		if err != nil {
			return contract.LabTrialRecord{}, err
		}
		v := contract.LabTrialRecord{Id: t.ID, Cell: cell, State: contract.ExecutionState(t.State), Observation: contract.PipelineObservation(t.Observation), Problem: labNull(t.Problem),
			Comparable: comparable(t), StartedAt: labNull(t.StartedAt), FinishedAt: labNull(t.FinishedAt), DurationMs: t.DurationMs,
			Spend: contract.LabTrialSpend{Calls: &calls, Credits: &credit, Basis: &basis}, Cost: append([]contract.CostFigure{}, trialCost[t.ID]...),
			Confounders: contract.LabConfounders{Hosts: uniq(hosts), Harnesses: uniq(harnesses), Models: uniq(models), WebRoute: web}, Scores: mine, PlanSummary: planSummary(t.run.entries())}
		if rawValue(t.CanonicalResult) != nil {
			r, err := asT[contract.PipelineCanonicalResult](t.CanonicalResult)
			if err != nil {
				return v, err
			}
			v.CanonicalResult = &r
		}
		if rawValue(t.ProjectAssessments) != nil {
			if v.ProjectAssessments, err = asT[[]contract.ProjectAssessmentRef](t.ProjectAssessments); err != nil {
				return v, err
			}
		}
		if r := t.Recovery; r != nil {
			source := r.Identity.SourceRunID
			v.Recovery = &contract.LabRecovery{SourceRunId: &source, ReusedStepIds: r.ReusedStepIDs}
		}
		return v, nil
	}
	order, last := expLatest(trials)
	idsOf := func(variant string) []string {
		out := []string{}
		for _, k := range order {
			if t := last[k]; t.cell().Variant == variant && comparable(t) {
				out = append(out, t.ID)
			}
		}
		return out
	}
	summaryOf := func(trials []string) (map[string]contract.LabScoreSummary, error) {
		return asT[map[string]contract.LabScoreSummary](summarize(cur, trials))
	}
	rec, err := expView(exp)
	if err != nil {
		return out, err
	}
	if out, err = asT[labExperimentView](rec); err != nil {
		return out, err
	}
	out.Cells = []contract.LabCell{}
	for _, p := range exp.Examples {
		for _, v := range exp.Variants {
			ts := []*labRun{}
			for _, k := range order {
				if c := last[k].cell(); c.Example == p.ID && c.Variant == v.Label {
					ts = append(ts, last[k])
				}
			}
			sort.SliceStable(ts, func(i, j int) bool { return ts[i].cell().Repeat < ts[j].cell().Repeat })
			keys, comp := []string{}, []string{}
			for _, t := range ts {
				keys = append(keys, cellKey(p.ID, v.Label, t.cell().Repeat))
				if comparable(t) {
					comp = append(comp, t.ID)
				}
			}
			summary, err := summaryOf(comp)
			if err != nil {
				return out, err
			}
			example, variant := p.ID, v.Label
			out.Cells = append(out.Cells, contract.LabCell{Example: &example, Variant: &variant, Trials: keys, Summary: summary})
		}
	}
	out.Variants = []contract.LabVariant{}
	for _, v := range exp.Variants {
		view, err := asT[contract.LabVariant](v)
		if err != nil {
			return out, err
		}
		if view.Summary, err = summaryOf(idsOf(v.Label)); err != nil {
			return out, err
		}
		out.Variants = append(out.Variants, view)
	}
	out.Trials = []contract.LabTrialRecord{}
	for _, t := range trials {
		view, err := trialView(t)
		if err != nil {
			return out, err
		}
		out.Trials = append(out.Trials, view)
	}
	out.Judges = []json.RawMessage{}
	for _, r := range runs {
		if j := r.Judge; j != nil {
			out.Judges = append(out.Judges, jsonBytes(labJudgeView{ID: r.ID, State: r.State, Evaluator: j.Evaluator, TrialID: labOptText(j.TrialID), TrialIDs: j.TrialIDs,
				GroupID: labOptText(j.GroupID), Order: j.Order, Output: labNull(r.outputObject()), Problem: labNull(r.Problem)}))
		}
	}
	if out.Scores, err = asT[[]contract.LabScore](cur); err != nil {
		return out, err
	}
	noteRows, err := q.db().LabNotes(q.ctx(), id)
	if err != nil {
		return out, err
	}
	out.Notes = []json.RawMessage{}
	for _, n := range noteRows {
		out.Notes = append(out.Notes, jsonBytes(labNoteView{ID: n.ID, Kind: n.Kind, Field: n.Field, Old: labNull(n.Old), New: labNull(n.New), Text: n.Text, Author: n.Author, At: n.At}))
	}
	out.Spend = s
	if out.Agreement, err = asT[contract.LabAgreement](agreement(cur)); err != nil {
		return out, err
	}
	out.Pairwise = pairwiseView(runs)
	out.Strategies = []contract.LabStrategyRun{}
	for _, r := range runs {
		if st := r.LoopStrategy; st != nil {
			view := contract.LabStrategyRun{Id: &r.ID, Iteration: jsonBytes(st.Iteration), State: &r.State, Problem: labNull(r.Problem)}
			if o := r.outputObject(); o != nil {
				output, err := asT[contract.LabJudgeOutput](o)
				if err != nil {
					return out, err
				}
				view.Output = &output
			}
			out.Strategies = append(out.Strategies, view)
		}
	}
	if out.Comparison, err = asT[[]contract.LabComparison](expComparison(exp, trials, cur)); err != nil {
		return out, err
	}
	if out.Typed, err = asT[[]contract.LabTypedBlock](expTyped(exp, trials, cur)); err != nil {
		return out, err
	}
	value, note := "background", "No effect until the scheduler exists"
	out.Priority = contract.LabPriority{Value: &value, Note: &note}
	return out, nil
}

// optText: an optional request string as a query argument (nil when absent).
func optText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func labIDArg(s, what string) error {
	if s == "" || len(s) > 300 {
		return fail(400, "Expected "+what)
	}
	return nil
}

// labMapReply: a typed reply as an operation's open (map) reply.
func labMapReply(v any, err error) (map[string]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return rawObjectOr(jsonBytes(v)), nil
}

func init() {
	// Lab operations (contracts/ops-lab.schema.json): lab_experiment_action and lab_cell_action are long and open their own
	// transactions; the rest run in runOp's.
	op("lab_compare_methods", func(q *Request, r *contract.LabCompareMethodsRequest) (contract.LabCompareMethodsReply, error) {
		return labCompareMethods(q, r)
	})
	op("lab_examples_add", func(q *Request, r *contract.LabExamplesAddRequest) (contract.LabExamplesAddReply, error) {
		return labExamplesAdd(q, r)
	})
	op("lab_examples_list", func(q *Request, r *contract.LabExamplesListRequest) (contract.LabExamplesListReply, error) {
		return labExamplesList(q, r.State)
	})
	op("lab_example_get", func(q *Request, r *contract.LabExampleGetRequest) (contract.LabExampleGetReply, error) {
		if err := labIDArg(r.ArtifactId, "artifactId"); err != nil {
			return contract.LabExampleGetReply{}, err
		}
		ex, err := labExampleRead(q, r.ArtifactId)
		return ex.reply, err
	})
	op("lab_experiment_create", func(q *Request, r *contract.LabExperimentCreateRequest) (contract.LabExperimentCreateReply, error) {
		return labMapReply(expCreate(q, r, false))
	})
	op("lab_experiment_note", func(q *Request, r *contract.LabExperimentNoteRequest) (contract.LabExperimentNoteReply, error) {
		n, err := expNote(q, r)
		if err != nil {
			return contract.LabExperimentNoteReply{}, err
		}
		return asT[contract.LabExperimentNoteReply](n)
	})
	op("lab_experiment_decide", func(q *Request, r *contract.LabExperimentDecideRequest) (contract.LabExperimentDecideReply, error) {
		return labMapReply(expDecide(q, r))
	})
	op("lab_experiments_list", func(q *Request, _ *contract.LabExperimentsListRequest) (contract.LabExperimentsListReply, error) {
		out := contract.LabExperimentsListReply{Experiments: []contract.LabExperimentRecord{}}
		rs, err := q.db().LabExperiments(q.ctx())
		if err != nil {
			return out, err
		}
		for _, r := range rs {
			e, err := expOf(r)
			if err != nil {
				return out, err
			}
			v, err := expView(e)
			if err != nil {
				return out, err
			}
			out.Experiments = append(out.Experiments, v)
		}
		return out, nil
	})
	op("lab_experiment_get", func(q *Request, r *contract.LabExperimentGetRequest) (contract.LabExperimentGetReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabExperimentGetReply{}, err
		}
		return expGet(q, r.ExperimentId)
	})
	op("lab_experiment_action", func(q *Request, r *contract.LabExperimentActionRequest) (contract.LabExperimentActionReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabExperimentActionReply{}, err
		}
		return labMapReply(expAction(q, r.ExperimentId, string(r.Action)))
	})
	op("lab_cell_action", func(q *Request, r *contract.LabCellActionRequest) (contract.LabCellActionReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return nil, err
		}
		if err := labIDArg(r.Cell, "cell"); err != nil {
			return nil, err
		}
		if r.Action == contract.LabCellActionRetrySuffix {
			return labMapReply(expRetrySuffix(q, r.ExperimentId, r.Cell, deref(r.SourceRunId), deref(r.RequestId)))
		}
		return labMapReply(expCellAction(q, r.ExperimentId, r.Cell, string(r.Action)))
	})
	op("lab_promote", func(q *Request, r *contract.LabPromoteRequest) (contract.LabPromoteReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabPromoteReply{}, err
		}
		if err := labIDArg(r.Variant, "variant"); err != nil {
			return contract.LabPromoteReply{}, err
		}
		return expPromote(q, r.ExperimentId, r.Variant)
	})
	op("lab_rating_create", func(q *Request, r *contract.LabRatingCreateRequest) (contract.LabRatingCreateReply, error) {
		v, err := expRate(q, r)
		if err != nil {
			return contract.LabRatingCreateReply{}, err
		}
		return asT[contract.LabRatingCreateReply](v)
	})
	op("lab_rating_withdraw", func(q *Request, r *contract.LabRatingWithdrawRequest) (contract.LabRatingWithdrawReply, error) {
		return labMapReply(labRatingWithdraw(q, r.QuestionId, r.Reason))
	})
	op("lab_ratings_list", func(q *Request, r *contract.LabRatingsListRequest) (contract.LabRatingsListReply, error) {
		return labRatings(q, r.ExperimentId, r.State)
	})
	op("lab_rating_get", func(q *Request, r *contract.LabRatingGetRequest) (contract.LabRatingGetReply, error) {
		if err := labIDArg(r.QuestionId, "questionId"); err != nil {
			return contract.LabRatingGetReply{}, err
		}
		return labMapReply(labRating(q, r.QuestionId))
	})
	op("lab_score_correct", func(q *Request, r *contract.LabScoreCorrectRequest) (contract.LabScoreCorrectReply, error) {
		if err := labIDArg(r.ScoreId, "scoreId"); err != nil {
			return contract.LabScoreCorrectReply{}, err
		}
		return labMapReply(scoreCorrect(q, r))
	})
	op("lab_registry", func(q *Request, _ *contract.LabRegistryRequest) (contract.LabRegistryReply, error) {
		return labRegistry()
	})

}

// exampleRef: <id>@<version>.
func exampleRef(id string, version int64) string {
	return fmt.Sprintf("%s@%d", id, version)
}
