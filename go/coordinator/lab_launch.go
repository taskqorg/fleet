// Lab launch, trial creation, work-kit step, and trial settlement/completion (labWorkStep, labRecord).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// expLaunch: draft to running, refused without a goal and a hypothesis; the record is frozen from here on.
func expLaunch(q *Request, id string) error {
	e, err := q.db().LabExperiment(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "Unknown experiment")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(deref(e.Goal)) == "" || strings.TrimSpace(deref(e.Hypothesis)) == "" {
		return fail(400, "Launch needs the experiment's goal and hypothesis")
	}
	return expSetState(q, id, []string{"draft"}, "running", nil) // the experiment_record trigger stamps launched_at
}

// labRun: a Lab run (a trial, a judge run or a loop strategy run) as the Lab reads it, decoded once from its record
// (labRunFacts). run is the typed run record the pipelines.go functions take.
type labRun struct {
	run                *runRecord
	ID                 string          `json:"id"`
	Kind               string          `json:"kind"`
	ExperimentID       string          `json:"experimentId"`
	ProjectID          *string         `json:"projectId"`
	State              string          `json:"state"`
	Observation        string          `json:"observation"`
	Cell               *labCell        `json:"cell"`
	Judge              *labJudge       `json:"judge"`
	LoopStrategy       *labLoopMark    `json:"loopStrategy"`
	Variant            *string         `json:"variant"`
	Problem            json.RawMessage `json:"problem"`
	StartedAt          json.RawMessage `json:"startedAt"`
	FinishedAt         json.RawMessage `json:"finishedAt"`
	DurationMs         *float64        `json:"durationMs"`
	CanonicalResult    json.RawMessage `json:"canonicalResult"`
	ProjectAssessments json.RawMessage `json:"projectAssessments"`
	Recovery           *labRecovery    `json:"recovery"`
	Overrides          json.RawMessage `json:"overrides"`
	DefinitionSha256   string          `json:"definitionSha256"`
	Usage              json.RawMessage `json:"usage"`
	Steps              []labRunStep    `json:"steps"`
	Definition         json.RawMessage `json:"definition"`
	Definitions        json.RawMessage `json:"definitions"`
	Example            json.RawMessage `json:"example"`
	ExampleInputs      json.RawMessage `json:"exampleInputs"`
	RerunOf            string          `json:"rerunOf"`
	ProjectSnapshot    json.RawMessage `json:"projectSnapshot"`
}

// labRunStep: a run step as the Lab reads it.
type labRunStep struct {
	ID               string          `json:"id"`
	ExecutionID      string          `json:"executionId"`
	State            string          `json:"state"`
	Observation      string          `json:"observation"`
	Arguments        json.RawMessage `json:"arguments"`
	Output           json.RawMessage `json:"output"`
	OutputArtifactID string          `json:"outputArtifactId"`
}

// labRunFacts: a run record (getRun) as the Lab reads it; nil stays nil.
func labRunFacts(run *runRecord) *labRun {
	if run == nil {
		return nil
	}
	// The run's fields as the Lab reads them; its open values as decoded JSON writes them (object keys sorted, numbers
	// as float64), as the trial evidence hashes them. The Lab's own keys on the run (cell, judge, recovery, ...) are the
	// record's remainder, decoded once.
	var r labRun
	run.restAs(&r)
	r.Problem, r.ProjectAssessments = labSortedRaw(r.Problem), labSortedRaw(r.ProjectAssessments)
	r.ID, r.ExperimentID, r.ProjectID, r.State, r.Observation = run.ID, run.ExperimentID, run.ProjectID, run.State, run.Observation
	r.DefinitionSha256, r.RerunOf = run.DefinitionSha256, run.RerunOf
	if run.StartedAt != nil {
		r.StartedAt = labSortedRaw(jsonBytes(*run.StartedAt))
	}
	if run.FinishedAt != nil {
		r.FinishedAt = labSortedRaw(jsonBytes(*run.FinishedAt))
	}
	if run.DurationMs != nil {
		d := float64(*run.DurationMs)
		r.DurationMs = &d
	}
	if run.CanonicalResult != nil {
		r.CanonicalResult = labSortedRaw(jsonBytes(run.CanonicalResult))
	}
	r.Usage, r.Overrides = labSortedRaw(jsonBytes(run.Usage)), labSortedRaw(jsonBytes(run.Overrides))
	r.Definition = jsonBytes(run.Definition)
	if len(run.Definitions) > 0 {
		r.Definitions = labSortedRaw(jsonBytes(run.Definitions))
	}
	if len(run.Example) > 0 {
		r.Example = labSortedRaw(run.Example)
	}
	if run.ExampleInputs != nil {
		r.ExampleInputs = labSortedRaw(jsonBytes(run.ExampleInputs))
	}
	if len(run.ProjectSnapshot) > 0 {
		r.ProjectSnapshot = labSortedRaw(run.ProjectSnapshot)
	}
	r.Steps = make([]labRunStep, 0, len(run.Steps))
	for _, s := range run.Steps {
		r.Steps = append(r.Steps, labRunStep{ID: s.ID, ExecutionID: s.ExecutionID, State: s.State, Observation: s.Observation,
			Arguments: labSortedRaw(s.Arguments), Output: labSortedRaw(s.Output), OutputArtifactID: s.OutputArtifactID})
	}
	r.run = run
	return &r
}

// labSortedRaw: raw as decoded JSON writes it (object keys sorted); absent stays absent.
func labSortedRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return jsonBytes(rawValue(raw))
}

// labRunRead: run id (getRun); nil when there is none.
func labRunRead(q *Request, id string) (*labRun, error) {
	run, err := getRun(q, id)
	return labRunFacts(run), err
}

// inputs: the run's example input artifacts.
func (t *labRun) inputs() []labExampleInput {
	var l []labExampleInput
	json.Unmarshal(t.ExampleInputs, &l)
	return l
}

// output: the run's result value (pipelineResultValue), its JSON.
func (t *labRun) output() json.RawMessage { return labNull(t.run.resultValue()) }

// outputObject: the run's result when it is an object, else nil.
func (t *labRun) outputObject() json.RawMessage {
	if _, ok := labRawObject(t.output()); ok {
		return t.output()
	}
	return nil
}

func (t *labRun) terminal() bool    { return executionEnds(t.State) }
func (t *labRun) unconfirmed() bool { return t.Observation == "unconfirmed" }

func expRuns(q *Request, id string) ([]*labRun, error) {
	rs, err := q.db().LabRunIDs(q.ctx(), &id)
	if err != nil {
		return nil, err
	}
	out := []*labRun{}
	for _, r := range rs {
		run, err := getRun(q, r.ID)
		if err != nil {
			return nil, err
		}
		if run != nil {
			t := labRunFacts(run)
			t.Variant, t.Problem = r.Variant, jsonBytes(r.Problem)
			out = append(out, t)
		}
	}
	return out, nil
}

func expTrials(runs []*labRun) []*labRun {
	out := []*labRun{}
	for _, r := range runs {
		if r.Cell != nil {
			out = append(out, r)
		}
	}
	return out
}

// latest attempt per cell, in first-seen order.
func expLatest(trials []*labRun) ([]string, map[string]*labRun) {
	order, m, attempts := []string{}, map[string]*labRun{}, map[string]int64{}
	for _, t := range trials {
		c := t.cell()
		k := c.key()
		oa, ok := attempts[k]
		if !ok {
			order = append(order, k)
		}
		if !ok || c.Attempt > oa {
			m[k], attempts[k] = t, c.Attempt
		}
	}
	return order, m
}

// cell: the run's cell; a run without one (a judge or strategy run) has the zero cell.
func (t *labRun) cell() labCell {
	if t.Cell == nil {
		return labCell{}
	}
	return *t.Cell
}

func comparable(t *labRun) bool { return t.State == "succeeded" && !t.unconfirmed() }

// labRunStart: a Lab run to start (labStartRun): its request, pipeline, example, variant and what the run records
// besides; a suffix retry also reuses the source run's confirmed prefix.
type labRunStart struct {
	RequestID string
	File      string
	Example   any
	Variant   *labVariant
	Extra     labRunExtra
	Reuse     []runStep
	RerunOf   string
}

// labExecutionRow: the execution row of a Lab run, reserved before the run starts (insertExecution).
type labExecutionRow struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	State        string  `json:"state"`
	ProjectID    string  `json:"projectId"`
	ExperimentID string  `json:"experimentId"`
	Variant      *string `json:"variant"`
	RequestID    string  `json:"requestId"`
	ConfigRef    *struct {
		ConfigRef string `json:"config_ref"`
	} `json:"configRef"`
	Data struct {
		Recovery *labRecovery `json:"recovery"`
	} `json:"data"`
}

// labStartRun reserves exactly once; an existing request never reinitializes a run. It is the Lab's one start of a
// pipeline run: the execution row (insertExecution) and the run (pipelineStart) from its options.
func labStartRun(q *Request, exp *labExperiment, s labRunStart) (string, error) {
	requester, err := labRequester(q, exp.ID)
	if err != nil {
		return "", err
	}
	id := newID()
	row := labExecutionRow{ID: id, Kind: "pipeline_run", State: "preparing", ProjectID: exp.ProjectID, ExperimentID: exp.ID, RequestID: s.RequestID}
	row.Data.Recovery = s.Extra.Recovery
	var overrides json.RawMessage
	if v := s.Variant; v != nil {
		if v.Label != "" {
			row.Variant = &v.Label
		}
		row.ConfigRef = &struct {
			ConfigRef string `json:"config_ref"`
		}{v.ConfigRef}
		if v.Overrides != nil {
			overrides = jsonBytes(v.Overrides)
		}
	}
	ok, err := insertExecution(q, row)
	if err != nil || !ok {
		return "", err
	}
	s.Extra.Priority = "background"
	extra, err := recordOf(s.Extra)
	if err != nil {
		return "", err
	}
	reuse := []json.RawMessage{}
	for _, step := range s.Reuse {
		reuse = append(reuse, jsonBytes(step))
	}
	project := exp.ProjectID
	opts := pipelineStartOptions{ID: id, File: s.File, ProjectID: &project, RequestID: s.RequestID, Example: jsonBytes(s.Example), Overrides: overrides, ExperimentID: exp.ID,
		Variant: deref(row.Variant), RequestedBy: &pipelineRequester{Kind: "lab", ExperimentID: exp.ID, Email: requester.Email}, Extra: extra, Reuse: reuse, RerunOf: s.RerunOf}
	_, err = startRun(q, opts)
	return id, err
}

// labJudgeExample: a judge run's example: the prompt, and the candidate trials of a pairwise judge or the form of a
// one-trial judge.
type labJudgeExample struct {
	Prompt     string   `json:"prompt"`
	Candidates []string `json:"candidates,omitempty"`
	Form       string   `json:"form,omitempty"`
}

// labStrategyExample: a loop strategy run's example: its input files.
type labStrategyExample struct {
	Inputs []labStrategyInput `json:"inputs"`
}

// exampleOf: the pinned example, refused when its content changed.
func exampleOf(q *Request, pin *labPin) (labExample, error) {
	if pin == nil {
		return labExample{}, fail(404, "Unknown example")
	}
	ex, err := labExampleRead(q, pin.ArtifactID)
	if err != nil {
		return labExample{}, err
	}
	hash, _ := q.db().LabArtifactHash(q.ctx(), pin.ArtifactID)
	if hash != pin.Hash {
		return labExample{}, fail(409, fmt.Sprintf("Example %v changed since it was pinned", pin.ID))
	}
	return ex.example, nil
}

func expLaunchTrial(q *Request, exp *labExperiment, pin *labPin, variant *labVariant, repeat, attempt int64) (string, error) {
	iteration := variant.Iteration
	cell := labCell{Iteration: &iteration, Example: pin.ID, Variant: variant.Label, Repeat: repeat, Attempt: attempt}
	full, err := exampleOf(q, pin)
	if err != nil {
		return "", err
	}
	rid := "lab:" + sha256hex(jsonBytes([]any{exp.ID, iteration, pin.ID, variant.Label, repeat, attempt}))
	return labStartRun(q, exp, labRunStart{RequestID: rid, File: variantPipelineID(exp, variant) + ".json", Example: full.runnable(), Variant: variant, Extra: labRunExtra{Cell: &cell}})
}

// labPending: a script check of one trial, read in the step's transaction and run outside it.
type labPending struct {
	checks           []labCheck
	files            []labFile
	trial            *labRun
	evaluator, input string
	snapshot         labAssessmentSnap
}

// labChecked: the script checks' results by trial, or why they could not run.
type labChecked struct {
	checked map[string]*labCheckRun
	err     error
}

// labLaterEvery: how often a running experiment looks again besides its trials' ends (budgets, the requester's access):
// FLEET_PIPELINE_TICK_MS, 0 = only on wakes.
var labLaterEvery = envMs("FLEET_PIPELINE_TICK_MS", 5000)

// labWorkStep moves experiment c.Key (the lab queue's step, in the kit's transaction under GLOBAL and the experiment's
// lock): trials whose script checks are due leave the checks to run outside the transaction (the next step records
// their scores); otherwise the experiment's budgets, evaluations, launches and end are written. A running experiment
// looks again later; any other leaves the queue until a producer enqueues it again.
func labWorkStep(q *Request, now time.Time, c *kitClaim) error {
	id := c.Key
	if err := acquireTxLock(q, GLOBAL, "experiment:"+id); err != nil {
		return err
	}
	checked := map[string]*labCheckRun{}
	if r, ok := c.Result.(labChecked); ok {
		if r.err != nil {
			return r.err
		}
		checked = r.checked
	} else {
		todo, err := labPendingChecks(q, id)
		if err != nil {
			return err
		}
		if len(todo) > 0 {
			a := q.app
			c.Call = func(ctx context.Context) any {
				checked := map[string]*labCheckRun{}
				for id, p := range todo {
					r, err := runChecks(ctx, p.checks, p.files)
					if err == nil {
						err = materializeLabAssessments(ctx, a, p.trial.run, r, p.evaluator, p.input)
					}
					if err != nil {
						return labChecked{err: err}
					}
					snapshot := p.snapshot
					r.AssessmentSnapshot = &snapshot
					checked[id] = r
				}
				return labChecked{checked: checked}
			}
			return nil
		}
	}
	if err := labRecord(q, id, checked); err != nil {
		return err
	}
	state, err := q.db().LabExperimentState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && state != "running" {
		return q.db().LabWorkDelete(q.ctx(), id)
	}
	if err != nil {
		return err
	}
	if _, err := q.db().LabWorkEnsure(q.ctx(), id); err != nil {
		return err
	}
	at := now.Add(100 * 365 * 24 * time.Hour) // only on wakes
	if labLaterEvery > 0 {
		at = time.Now().Add(labLaterEvery)
	}
	return kitSchedule(q, "lab", id, at)
}

// labTrialMeta: what a script check reads of its trial (__fleet_evaluation/trial.json); fields in canonical (sorted)
// key order.
type labTrialMeta struct {
	CanonicalResult json.RawMessage              `json:"canonicalResult"`
	EvaluatorSha256 string                       `json:"evaluatorSha256"`
	Evidence        labEvidence                  `json:"evidence"`
	ExecutionState  string                       `json:"executionState"`
	ExperimentID    string                       `json:"experimentId"`
	Inputs          []labFileBinding             `json:"inputs"`
	Observation     string                       `json:"observation"`
	PlanSummary     contract.PipelinePlanSummary `json:"planSummary"`
	Resources       labTrialResources            `json:"resources"`
	RunID           string                       `json:"runId"`
	Schema          string                       `json:"schema"`
	TrialID         string                       `json:"trialId"`
	VariantID       string                       `json:"variantId"`
}

// labTrialResources: a trial's usage and duration; fields in canonical key order.
type labTrialResources struct {
	DurationMs *float64        `json:"durationMs"`
	Usage      json.RawMessage `json:"usage"`
}

// labPendingChecks: the latest comparable trials of experiment id whose script checks have not scored them, with what
// the checks read.
func labPendingChecks(q *Request, id string) (map[string]labPending, error) {
	todo := map[string]labPending{}
	err := func() error {
		exp, err := expRow(q, id)
		if err != nil || exp.State == "draft" {
			return err
		}
		hasScript := false
		for _, e := range exp.Evaluators {
			hasScript = hasScript || e.Kind == "script"
		}
		if !hasScript {
			return nil
		}
		cur, err := scoresCurrent(q, id)
		if err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		trials := expTrials(runs)
		_, latest := expLatest(trials)
		for _, t := range trials {
			if l := latest[t.cell().key()]; l == nil || l.ID != t.ID {
				continue
			}
			if !comparable(t) {
				continue
			}
			done := true
			for _, e := range exp.Evaluators {
				if e.Kind != "script" {
					continue
				}
				name := e.name()
				has := false
				for _, s := range cur {
					has = has || s.Evaluator == name && s.first() == t.ID
				}
				if !has {
					done = false
					break
				}
			}
			if done {
				continue
			}
			full, err := exampleOf(q, exp.pin(t.cell().Example))
			if err != nil {
				return err
			}
			files, err := trialFiles(q, t, full)
			if err != nil {
				return err
			}
			// Script checks only (never a judge or an agent): the known-good result under references/, for comparison.
			for _, f := range full.References {
				b, err := labFileBytes(q, t.ProjectID, f)
				if err != nil {
					return err
				}
				files = append(files, labFile{"references/" + f.Path, b})
			}
			for _, f := range files {
				first, _, _ := strings.Cut(f.path, "/")
				if strings.EqualFold(first, "__fleet_evaluation") || strings.EqualFold(first, "evaluation") {
					return fail(409, "Evaluator input collides with reserved namespace")
				}
			}
			evidence, evidenceFiles, err := labSelectedEvidence(q, t)
			if err != nil {
				return err
			}
			files = append(files, evidenceFiles...)
			result := t.CanonicalResult
			if result == nil {
				result = json.RawMessage("null")
			}
			metadata := labTrialMeta{Schema: "fleet.lab-trial.v1", EvaluatorSha256: labEvaluatorIdentity(full.Checks, files), TrialID: t.ID, RunID: t.ID, ExperimentID: id,
				VariantID: t.cell().Variant, PlanSummary: planSummary(t.run.entries()), CanonicalResult: result, Evidence: evidence, ExecutionState: t.State,
				Observation: firstString(t.Observation, "confirmed"), Resources: labTrialResources{Usage: t.Usage, DurationMs: t.DurationMs}, Inputs: labFileBindings(files)}
			files = append(files, labFile{"__fleet_evaluation/trial.json", jsonBytes(metadata)})
			evaluator, input := labAssessmentInputs(t.run, full.Checks, files)
			todo[t.ID] = labPending{full.Checks, files, t, evaluator, input, labAssessmentSnapshot(t.run, full)}
		}
		return nil
	}()
	return todo, err
}

// labRecord writes one look at experiment id: its requester, budgets, the evaluations (checked: the script checks just
// run), the trials to launch and its end. A launched trial's run is on the kit from its first write.
func labRecord(q *Request, id string, checked map[string]*labCheckRun) error {
	var launched []string
	exp, err := expRow(q, id)
	if err != nil || exp.State == "draft" {
		return err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return err
	}
	trials := expTrials(runs)
	cur, err := scoresCurrent(q, id)
	if err != nil {
		return err
	}
	if exp.State != "running" {
		_, err := expEvaluate(q, exp, trials, runs, cur, &launched, checked)
		return err
	}
	if _, err := labRequester(q, id); err != nil {
		var reason *codedError
		if errors.As(err, &reason) && strings.HasPrefix(reason.code, "requester:") {
			state := "stopped"
			if reason.code == "requester:access-revoked" {
				state = "paused"
			}
			_, err := experimentTransition(q, id, experimentEvent{To: state, StopReason: &reason.code})
			return err
		}
		return err
	}
	if strings.HasPrefix(deref(exp.StopReason), "requester:") {
		if err := q.db().LabExperimentStopReasonClear(q.ctx(), id); err != nil {
			return err
		}
	}
	order, last := expLatest(trials)
	repeats := exp.Repeats
	total := len(exp.Examples) * len(exp.Variants) * int(repeats)
	missing := total - len(order)
	s, err := expSpend(q, id)
	if err != nil {
		return err
	}
	budgets := []labBudget{}
	if missing > 0 || exp.Kind == "loop" {
		budgets = listOrEmpty(exp.Budgets)
	}
	state, changed := "running", false
	var reason *string
	reached := func(b labBudget) bool {
		return b.FiredAt == nil && spendOf(s, b.Unit) >= b.Limit
	}
	for i := range budgets {
		b := &budgets[i]
		if b.Action == "stop" && reached(*b) {
			at := nowMs()
			r := "budget:" + b.Unit
			b.FiredAt, changed, state, reason = &at, true, "stopped", &r
			break
		}
	}
	if state == "running" {
		for i := range budgets {
			b := &budgets[i]
			if b.Action == "stop" || !reached(*b) {
				continue
			}
			at := nowMs()
			b.FiredAt, changed = &at, true
			if b.Action == "pause" {
				state = "paused"
				if _, err := askRow(q, "operator_decision", fmt.Sprintf("Lab budget reached: %v %v of %v. Running trials finish; resume launches or stop the experiment?", b.Unit, spendOf(s, b.Unit), b.Limit),
					exp.ProjectID, []any{"resume", "stop"}, "lab", labBudgetAsk{ExperimentID: id, Budget: b.Unit}); err != nil {
					return err
				}
				break
			}
		}
	}
	if changed || state != "running" {
		if err := q.db().LabExperimentBudgetsSet(q.ctx(), db.LabExperimentBudgetsSetParams{ID: id, Budgets: jsonBytes(budgets)}); err != nil {
			return err
		}
		if _, err := experimentTransition(q, id, experimentEvent{To: state, StopReason: reason}); err != nil {
			return err
		}
	}
	evalExp := *exp
	evalExp.State = state
	evaluated, err := expEvaluate(q, &evalExp, trials, runs, cur, &launched, checked)
	if err != nil {
		return err
	}
	if state != "running" {
		return nil
	}
	// A trials budget that has not fired caps this pass's launches, so it is reached exactly.
	room := math.Inf(1)
	for _, b := range budgets {
		if b.Unit == "trials" && b.Action != "warn" && b.FiredAt == nil {
			room = math.Min(room, b.Limit-s.Trials.Value)
		}
	}
launch:
	for i := range exp.Examples {
		for j := range exp.Variants {
			p, v := &exp.Examples[i], &exp.Variants[j]
			for r := int64(0); r < repeats; r++ {
				if _, ok := last[cellKey(p.ID, v.Label, r)]; ok {
					continue
				}
				if room <= 0 {
					break launch
				}
				rid, err := expLaunchTrial(q, exp, p, v, r, 0)
				if err != nil {
					return err
				}
				if rid != "" {
					launched = append(launched, rid)
					room--
				}
			}
		}
	}
	allFinal := true
	for _, k := range order {
		allFinal = allFinal && last[k].terminal()
	}
	if len(launched) == 0 && len(order) == total && allFinal && evaluated {
		if exp.Kind == "loop" {
			// Evaluators may have just inserted the final scores in this transaction.
			fresh, err := scoresCurrent(q, id)
			if err != nil {
				return err
			}
			return loopAdvance(q, exp, trials, runs, fresh, s, &launched)
		}
		complete := "complete"
		_, err = experimentTransition(q, id, experimentEvent{To: "done", From: []string{"running"}, StopReason: &complete})
		return err
	}
	return nil
}

// labBudgetAsk: a budget question's data (labAnswerAfter reads it back as labQuestionData).
type labBudgetAsk struct {
	ExperimentID string `json:"experimentId"`
	Budget       string `json:"budget,omitempty"`
}

func expSetState(q *Request, id string, from []string, to string, reason any) error {
	_, err := experimentTransition(q, id, experimentEvent{To: to, From: from, Strict: true, StopReason: strPtr(reason)})
	return err
}

func labReadTx(q *Request, fn func(q *Request) error) error {
	return q.app.store.Read(q.ctx(), q, fn)
}

func expGetTx(q *Request, id string) (out labExperimentView, err error) {
	err = labReadTx(q, func(q *Request) error { out, err = expGet(q, id); return err })
	return
}

func expAction(q *Request, id, action string) (labExperimentView, error) {
	var err error
	switch action {
	case "launch":
		err = q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error { return expLaunch(q, id) })
	case "stop":
		err = q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error { return expSetState(q, id, []string{"running", "paused"}, "stopped", "owner") })
	case "resume":
		err = q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error { return expSetState(q, id, []string{"paused"}, "running", nil) })
	default:
		return labExperimentView{}, fail(400, "action is launch, stop or resume")
	}
	if err != nil {
		return labExperimentView{}, err
	}
	if err := labRunNow(q.ctx(), q.app, id); err != nil {
		return labExperimentView{}, err
	}
	out, err := expGetTx(q, id)
	if err != nil || action != "launch" {
		return out, err
	}
	// Drift warns on launch, never refuses (registry source_status of the pinned examples' use cases).
	ucs := map[string]bool{}
	if err := labReadTx(q, func(q *Request) error {
		for _, p := range out.Examples {
			ex, err := labExampleRead(q, deref(p.ArtifactId))
			if err != nil {
				return err
			}
			ucs[ex.example.UseCaseID] = true
		}
		return nil
	}); err != nil {
		return labExperimentView{}, err
	}
	warnings := []string{}
	reg, _ := useCases()
	for _, u := range reg {
		if ucs[u.Id] && u.SourceStatus != "current" {
			warnings = append(warnings, fmt.Sprintf("%v: source %v", u.Id, u.SourceStatus))
		}
	}
	if out.Rest == nil {
		out.Rest = map[string]json.RawMessage{}
	}
	out.Rest["warnings"] = jsonBytes(warnings)
	return out, nil
}

func expCellAction(q *Request, id, cell, action string) (labExperimentView, error) {
	var exp *labExperiment
	var t *labRun
	if err := labReadTx(q, func(q *Request) error {
		var err error
		if exp, err = expRow(q, id); err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		_, last := expLatest(expTrials(runs))
		t = last[cell]
		return nil
	}); err != nil {
		return labExperimentView{}, err
	}
	if t == nil {
		return labExperimentView{}, fail(404, "Unknown cell")
	}
	if action == "cancel" {
		if t.terminal() {
			return labExperimentView{}, fail(409, "Trial already finished")
		}
		own, err := pipelineCancel(q.ctx(), q.app, t.ID)
		if err != nil {
			return labExperimentView{}, err
		}
		if own != "" {
			if err := stopExecution(q.ctx(), q.app, own); err != nil && !isNotFound(err) { // the run is cancelled already
				return labExperimentView{}, err
			}
		}
	} else {
		if !t.terminal() {
			return labExperimentView{}, fail(409, "Trial still running; cancel it first")
		}
		if exp.State == "draft" || exp.State == "stopped" {
			return labExperimentView{}, fail(409, fmt.Sprintf("Experiment is %v", exp.State))
		}
		c := t.cell()
		if err := q.app.store.Write(q.ctx(), q, []string{GLOBAL, "experiment:" + id}, func(q *Request) error {
			runs, err := expRuns(q, id)
			if err != nil {
				return err
			}
			_, latest := expLatest(expTrials(runs))
			if latest[cell] == nil || latest[cell].ID != t.ID {
				return fail(409, "Cell source changed; read it again")
			}
			if err := recoveryFence(q, latest[cell].run); err != nil {
				return err
			}
			if _, err := experimentTransition(q, id, experimentEvent{To: "running", From: []string{"done"}}); err != nil {
				return err
			}
			pin, variant := exp.pin(c.Example), exp.variant(c.Variant)
			if pin == nil || variant == nil {
				return fail(404, "Unknown example")
			}
			_, err = expLaunchTrial(q, exp, pin, variant, c.Repeat, c.Attempt+1)
			return err
		}); err != nil {
			return labExperimentView{}, err
		}
	}
	if err := labRunNow(q.ctx(), q.app, id); err != nil {
		return labExperimentView{}, err
	}
	return expGetTx(q, id)
}

// Lock the experiment row FOR SHARE before person/person_grant FOR SHARE, matching setPeople's table lock order
// (experiment -> person) without acquiring GLOBAL after experiment:<id>.
func labRequester(q *Request, id string) (*Person, error) {
	exp, err := q.db().LabExperimentForShare(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	email := deref(exp.RequestedBy)
	if email == "" {
		return nil, failCode(409, "Lab requester is unknown; retained history cannot authorize new work", "requester:unknown")
	}
	if exp.RequesterRemovedAt != nil {
		return nil, failCode(409, "Lab requester was removed; this experiment cannot transfer identity", "requester:removed")
	}
	p, err := unscoped(q, func() (*Person, error) { return resolveAccessShareTx(q.ctx(), q.tx, email) })
	if err != nil {
		var denied *HTTPError
		if errors.As(err, &denied) && denied.Code == 403 {
			return nil, failCode(409, "Lab requester is no longer present", "requester:removed")
		}
		return nil, err
	}
	if (!fullAccess(p) && !hasRole(p, "architect")) || !mayAccessProjectID(p, exp.ProjectID) {
		return nil, failCode(403, "Lab requester lacks current role or project access", "requester:access-revoked")
	}
	return p, nil
}
