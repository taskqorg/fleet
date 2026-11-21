// Pipeline run creation, work-kit stepping, advance, cancel, rerun, and completion.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// pipelineStartOptions: what a new run is started from (startRun): its id (else a new one), the pipeline file or the
// caller's draft, project, request, example, overrides, requester, experiment and variant; extra is written over the
// run's fields; a rerun names its source and how many of its steps it reuses.
type pipelineStartOptions struct {
	ID           string             `json:"id,omitempty"`
	File         string             `json:"file,omitempty"`
	ProjectID    *string            `json:"projectId,omitempty"`
	RequestID    string             `json:"requestId,omitempty"`
	Example      json.RawMessage    `json:"example,omitempty"`
	Overrides    json.RawMessage    `json:"overrides,omitempty"`
	RequestedBy  *pipelineRequester `json:"requestedBy,omitempty"`
	ExperimentID string             `json:"experimentId,omitempty"`
	Variant      string             `json:"variant,omitempty"`
	Extra        recordRest         `json:"extra,omitempty"`
	RerunOf      string             `json:"rerunOf,omitempty"`
	Reuse        []json.RawMessage  `json:"reuse,omitempty"`
	draft        *pipelineDef
	draftBase    int64
	hasDraftBase bool
}

// pipelineProjectSnapshot: the fields of a project context a run's definition reference reads.
type pipelineProjectSnapshot struct {
	Revision   float64 `json:"revision"`
	ContentSha string  `json:"contentSha"`
}

// startRun writes a new run record (runner.start).
func startRun(q *Request, o pipelineStartOptions) (*runRecord, error) {
	def := o.draft // a draft run: the caller's draft, validated
	var err error
	if def != nil {
		def, err = validatePipelineDef(jsonBytes(def))
	} else {
		def, err = loadPipelineDef(q, o.File)
	}
	if err != nil {
		return nil, err
	}
	id := o.ID
	if id == "" {
		id = newID()
	}
	steps := []runStep{}
	for _, s := range def.Steps {
		e := runStep{ID: s.ID, Kind: s.Kind, State: "preparing"}
		if s.Kind == "api" {
			e.Tool, e.Connection = s.Tool, s.Connection
		}
		steps = append(steps, e)
	}
	reqBy := requester(q)
	if o.RequestedBy != nil {
		reqBy = *o.RequestedBy
	}
	example, overrides := o.Example, pipelineOverrides{}
	if isNull(example) {
		example = json.RawMessage("{}")
	}
	if !isNull(o.Overrides) {
		if err := json.Unmarshal(o.Overrides, &overrides); err != nil {
			return nil, err
		}
	}
	started := nowMs()
	run := &runRecord{ID: id, Traceparent: originTrace(q), ProjectID: o.ProjectID, RequestID: o.RequestID, RequestedBy: reqBy, Definition: *def,
		DefinitionSha256: sha256hex([]byte(canonical(def.value()))), PipelineID: def.ID, Pipeline: o.File, Example: example, Overrides: overrides,
		State: "running", StartedAt: &started, Steps: steps}
	// Caller lineage is server-derived, never a pipeline parameter.
	if strings.HasPrefix(q.agent, "execution:") {
		source := strings.TrimPrefix(q.agent, "execution:")
		parent, e := q.db().PipelineCaller(q.ctx(), source)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		if e != nil || deref(parent.ProjectID) != run.project() || parent.ProjectID == nil && run.ProjectID != nil {
			return nil, fail(403, "Pipeline caller project mismatch")
		}
		if parent.TaskID != nil {
			project, e := q.db().PipelineTaskProject(q.ctx(), *parent.TaskID)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return nil, e
			}
			if e != nil || deref(project) != run.project() || project == nil && run.ProjectID != nil {
				return nil, fail(403, "Pipeline caller task project mismatch")
			}
		}
		if parent.SessionID != nil {
			session, e := sessionByID(q, *parent.SessionID)
			if e != nil {
				return nil, e
			}
			if session.ProjectID != run.project() || (session.TaskID != "" && session.TaskID != deref(parent.TaskID)) {
				return nil, fail(403, "Pipeline caller session ownership mismatch")
			}
		}
		if strings.TrimSpace(deref(parent.DirectedBy)) == "" {
			return nil, fail(403, "Pipeline caller has no initiating actor provenance")
		}
		run.ParentExecutionID, run.TaskID, run.SessionID, run.DirectedBy = source, deref(parent.TaskID), deref(parent.SessionID), deref(parent.DirectedBy)
	}
	run.ExperimentID, run.Variant = o.ExperimentID, o.Variant
	if err := run.merge(o.Extra); err != nil {
		return nil, err
	}
	if err := taskAllowsWork(q, firstString(run.DeliveryID, run.TaskID)); err != nil {
		return nil, err
	}
	reused := []string{}
	if o.RerunOf != "" {
		run.RerunOf, run.Reused = o.RerunOf, &reused
	}
	ref, err := definitionRef(q, "pipeline", def.ID, def.value())
	if err != nil {
		return nil, err
	}
	if o.hasDraftBase { // a draft run: on its base, whatever content it has (data.definition holds it)
		yes := true
		ref.Revision, ref.Draft = o.draftBase, &yes
	}
	run.Definitions = []contract.DefinitionRef{ref}
	if pid := run.project(); pid != "" {
		ctx, err := projectContext(q, pid)
		if err != nil {
			return nil, err
		}
		snapshot := json.RawMessage(jsonBytes(ctx))
		var head pipelineProjectSnapshot
		lenientDecode(snapshot, &head)
		run.ProjectSnapshot = snapshot
		run.Definitions = append(run.Definitions, contract.DefinitionRef{Kind: contract.DefinitionKindProject, Id: pid, Revision: int64(head.Revision), ContentSha: head.ContentSha})
	}
	if err := run.initPlan(); err != nil {
		return nil, err
	}
	if len(o.Reuse) > 0 {
		old, err := getRun(q, o.RerunOf)
		if err != nil || old == nil {
			return nil, fail(409, "Reused source run is unavailable")
		}
		verified, err := runPrefix(q, old, def)
		if err != nil {
			return nil, err
		}
		if len(verified) < len(o.Reuse) {
			return nil, fail(409, "Reused prefix proof changed")
		}
		for i := range o.Reuse {
			sourceEntry := old.entry(i)
			entry := run.entry(i)
			when, err := def.Steps[i].when()
			if err != nil {
				return nil, err
			}
			var output any
			if when != nil {
				for _, previous := range run.Steps[:i] {
					if previous.ID == when.Source.Step {
						output = rawAny(previous.Output)
					}
				}
			}
			decision := pipelineConditionDecision(when, output, nowMs())
			if contract.PipelineActivation(decision.Outcome) != sourceEntry.Activation {
				return nil, fail(409, "Reused prefix guard proof differs")
			}
			if sourceEntry.Decision != nil && sourceEntry.Decision.SourceArtifact != nil {
				evidence := *sourceEntry.Decision.SourceArtifact
				decision.SourceArtifact = &evidence
			}
			entry.Activation, entry.Decision = contract.PipelineActivation(decision.Outcome), &decision
			if entry.Activation == contract.PipelineActivationNotSelected {
				continue
			}
			ref := &contract.PipelineExecutionRef{}
			if sourceEntry.ExecutionRef != nil {
				*ref = *sourceEntry.ExecutionRef
			}
			ref.Mode = contract.PipelineRefModeReused
			entry.ExecutionRef, entry.Inputs = ref, sourceEntry.Inputs
			step := verified[i]
			step.ReusedFrom = ref.SourceRunId
			run.Steps[i] = step
			reused = append(reused, entry.Id)
		}
		run.Reused = &reused
	}
	return run, run.put(q)
}

// pipelineWait polls until the run leaves running (pipeline_run waits for a direct search).
func pipelineWait(ctx context.Context, a *App, runID string) (recordRest, error) {
	for {
		var r *runRecord
		if err := a.store.Read(ctx, nil, func(q *Request) (err error) { r, err = getRun(q, runID); return }); err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fail(404, "Not found")
		}
		if r.State != "running" {
			return pipelinePublicRead(ctx, a, runID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// pipelineOnEnded: an agent or script step's own execution ended (its executionTransition): its run's step is due.
func pipelineOnEnded(q *Request, executionID string) error {
	runID, err := q.db().PipelineRunOfExecution(q.ctx(), executionID)
	if err != nil || runID == "" {
		return err
	}
	return kitWake(q, "pipeline", runID, time.Now())
}

// pipelineCancel: the run ends cancelled, later steps never start; returns the running step's own execution, which the
// caller stops. Its GLOBAL fence orders it with the run's steps on the kit.
func pipelineCancel(ctx context.Context, a *App, runID string) (string, error) {
	own := ""
	err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		var err error
		own, _, err = pipelineCancelIn(q, runID)
		return err
	})
	return own, err
}

// pipelineCancelIn uses GLOBAL to fence durable transitions. A driver that
// already holds a snapshot rechecks the stored run before writing it back.
// The caller handles post-commit notification and a handed-off native stop.
func pipelineCancelIn(q *Request, runID string) (string, *runRecord, error) {
	run, err := getRun(q, runID)
	if err != nil || run == nil || run.State != "running" {
		return "", nil, err
	}
	own := ""
	for i := range run.entries() {
		entry := run.entry(i)
		if entry.Activation != contract.PipelineActivationSelected || entryRefMode(entry) == contract.PipelineRefModeReused {
			continue
		}
		step := &run.Steps[i]
		if step.State != "running" && step.State != "preparing" && step.State != "waiting" {
			continue
		}
		if dispatchPhase(entry) == contract.PipelineDispatchPhaseHandedOff {
			own = step.ExecutionID
			step.Observation, run.Observation = "unconfirmed", "unconfirmed"
		} else if native := step.ExecutionID; native != "" {
			if _, err := commandStop(q, commandStopFilter{executionID: &native, statuses: []db.CommandStatus{db.CommandStatusPending}}); err != nil {
				return "", nil, err
			}
			if _, err := executionTransition(q, native, executionEvent{To: "cancelled", From: []string{"preparing", "waiting"}}); err != nil && !errors.Is(err, errNotFound) {
				return "", nil, err
			}
		}
		now := nowMs()
		step.State, step.FinishedAt = "cancelled", &now
	}
	return own, run, run.finish(q, "cancelled", "cancelled")
}

// finish ends the run in state (problem: why it did not succeed) and writes its result.
func (run *runRecord) finish(q *Request, state, problem string) error {
	if state == "succeeded" {
		if err := bindPipelineResult(q, run); err != nil {
			state, problem = "failed", "result_invalid"
		}
	}
	if state != "succeeded" {
		run.CanonicalResult = nil
		delete(run.rest, "canonicalResult")
		code := contract.PipelinePlanReasonPriorStepFailed
		if state == "cancelled" {
			code = contract.PipelinePlanReasonRunCancelled
		}
		for i := range run.entries() {
			if entry := run.entry(i); entry.Activation == contract.PipelineActivationPending {
				entry.Activation, entry.Problem = contract.PipelineActivationBlocked, &contract.PipelinePlanProblem{Code: code, Message: "Run ended before this entry could activate"}
			}
		}
	}
	finished := nowMs()
	run.State, run.FinishedAt = state, &finished
	var started int64
	if run.StartedAt != nil {
		started = *run.StartedAt
	}
	duration := nowMs() - started
	run.DurationMs = &duration
	credits, used := 0.0, false
	result := &pipelineResult{Outputs: map[string]json.RawMessage{}, Sources: []pipelineSource{}}
	var search json.RawMessage
	var stepErr json.RawMessage
	for i := range run.Steps {
		s := &run.Steps[i]
		if jsonKind(s.Usage) == '{' && entryRefMode(run.entry(i)) != contract.PipelineRefModeReused {
			used = true
			var u struct {
				Credits *float64 `json:"credits"`
			}
			if lenientDecode(s.Usage, &u) == nil && u.Credits != nil {
				credits += *u.Credits
			}
		}
		if s.hasOutput() {
			result.Outputs[s.ID] = s.Output
			if search == nil && jsonKind(s.Output) == '{' && jsonKind(pipelineRuleOf(s.Output)["results"]) == '[' {
				search = s.Output
			}
		}
		if stepErr == nil && !isNull(s.Error) {
			stepErr = s.Error
		}
	}
	run.Usage = nil
	if used {
		run.Usage = &pipelineCredits{Credits: credits}
	}
	if search != nil {
		for _, r := range jsonList(pipelineRuleOf(search)["results"]) {
			x := pipelineRuleOf(r)
			title := x["title"]
			if isNull(title) {
				title = x["url"]
			}
			result.Sources = append(result.Sources, pipelineSource{URL: x["url"], Title: title, Content: x["content"]})
		}
		result.Native = search
	}
	if problem != "" {
		if stepErr == nil {
			stepErr = jsonBytes(struct {
				Code string `json:"code"`
			}{problem})
		}
		result.Error = stepErr
	}
	run.Result = result
	if err := run.put(q); err != nil {
		return err
	}
	if problem != "" {
		if err := q.db().PipelineProblemSet(q.ctx(), db.PipelineProblemSetParams{ID: run.ID, Problem: problem}); err != nil {
			return err
		}
	}
	return pipelineEnded(q, run)
}

// failStep fails step i with problem and message, and the run with it.
func (run *runRecord) failStep(q *Request, i int, problem, message string) error {
	s := &run.Steps[i]
	s.State = "failed"
	lost := strings.HasPrefix(problem, "unknown:") // the outcome is unknown: failed for Fleet, unconfirmed
	if lost {
		s.Observation, run.Observation = "unconfirmed", "unconfirmed"
	}
	now := nowMs()
	s.Problem, s.Error, s.FinishedAt = problem, jsonBytes(pipelineStepError{Code: problem, Message: message}), &now
	if err := run.put(q); err != nil {
		return err
	}
	if err := q.db().PipelineProblemSet(q.ctx(), db.PipelineProblemSetParams{ID: run.row(i), Problem: problem}); err != nil {
		return err
	}
	return run.finish(q, "failed", problem)
}

// completeStep: step i succeeded with output (its saved object already bound) and usage.
func (run *runRecord) completeStep(q *Request, i int, output, usage json.RawMessage) error {
	s := &run.Steps[i]
	if s.OutputArtifactID == "" {
		return fail(409, "Pipeline output has not been materialized")
	}
	artifact, err := artifactRow(withTx(q), q.app, s.OutputArtifactID)
	if err != nil {
		return err
	}
	if !pipelineOutputIdentity(artifact, derefAny(run.ProjectID), run.row(i)) || artifact.ContentHash != sha256hex(jsonNorm(output)) {
		return fail(409, "Pipeline output identity or bytes changed")
	}
	d := run.entry(i).Dispatch
	if d == nil || d.Phase != contract.PipelineDispatchPhaseHandedOff {
		return fail(409, "Pipeline output has no committed handoff")
	}
	settled := nowMs()
	d.Phase, d.SettledAtMs = contract.PipelineDispatchPhaseSettled, &settled
	s.Usage = usage
	finished := nowMs()
	s.Output, s.State, s.FinishedAt = jsonNorm(output), "succeeded", &finished
	s.DurationMs = nil
	if s.StartedAt != nil {
		d := finished - *s.StartedAt
		s.DurationMs = &d
	}
	return run.put(q)
}

// The runner on the work kit (pipeline_work.go). One step of a run is one transaction under GLOBAL: it selects the next
// plan entry and moves its step as far as the database alone can. Work outside the database (the inputs' objects, a
// provider call, the output's object, a job poll, a finished agent's output) is a Call the step leaves; the kit runs it
// after the commit, outside any transaction, and its pipelineRecord is applied by the next step to the run read again,
// only while the plan revision is the one the call was made at (else the step looks again). What a crash between them
// leaves: inputs or an output object saved but not bound (saved again, the old object unbound); a sync api call or a job
// submit handed off (committed before the call) but not answered, which the next step fails as unknown:interrupted and
// never sends again; a poll not recorded, which polls again.

// pipelineRecord: what a call outside the step's transaction learned; revoke names the step row whose call token is
// revoked whatever happens to the record.
type pipelineRecord struct {
	revision int64
	revoke   string
	apply    func(q *Request, run *runRecord) error
}

// pipelineDrafts: the bytes of a finished draft-examples run's example files.
type pipelineDrafts [][]byte

// pipelineEvery: how often a waiting step looks again (a job poll, an agent's uploads) besides the wakes of its
// execution's end; FLEET_PIPELINE_TICK_MS, 0 = only on wakes.
var pipelineEvery = envMs("FLEET_PIPELINE_TICK_MS", 5000)

// pipelineSoon: the run's next step is due now and the queue is woken.
func pipelineSoon(q *Request, runID string) error {
	if err := kitSchedule(q, "pipeline", runID, time.Now()); err != nil {
		return err
	}
	return kitNotify(q, "pipeline")
}

// pipelineLater: the run waits for a wake, or its next look pipelineEvery after this write (a call may have taken
// longer than that since the pass began).
func pipelineLater(q *Request, runID string) error {
	now := time.Now()
	at := now.Add(100 * 365 * 24 * time.Hour)
	if pipelineEvery > 0 {
		at = now.Add(pipelineEvery)
	}
	return kitSchedule(q, "pipeline", runID, at)
}

// pipelineEnded: a run that ended in q's transaction: its experiment looks again and its own row runs once more for what
// follows an end (draft examples), then goes.
func pipelineEnded(q *Request, run *runRecord) error {
	for i := range run.Steps { // every live call token of its steps
		if err := revokeTokens(q, run.row(i)); err != nil {
			return err
		}
	}
	if id := run.ExperimentID; id != "" {
		if err := labEnqueue(q, id); err != nil {
			return err
		}
	}
	return pipelineSoon(q, run.ID)
}

// pipelineWorkDone: run id leaves the queue. A run that ended revoked its steps' tokens then (pipelineEnded); one
// that is gone or cannot be read has them looked up by its step rows' prefix.
func pipelineWorkDone(q *Request, runID string, ended bool) error {
	if !ended {
		n, err := q.db().PipelineRevokeRunTokens(q.ctx(), db.PipelineRevokeRunTokensParams{At: float64(nowMs()), RunID: runID})
		if err != nil {
			return err
		}
		if n > 0 {
			publish(q)
		}
	}
	return q.db().PipelineWorkDelete(q.ctx(), runID)
}

// pipelineWorkStep: one step of run c.Key (the pipeline queue's step, in the kit's transaction).
func pipelineWorkStep(q *Request, now time.Time, c *kitClaim) error {
	if err := acquireTxLock(q, GLOBAL); err != nil {
		return err
	}
	if _, err := q.db().PipelineRunLock(q.ctx(), c.Key); errors.Is(err, pgx.ErrNoRows) {
		return pipelineWorkDone(q, c.Key, false)
	} else if err != nil {
		return err
	}
	run, err := getRun(q, c.Key)
	if pipelineFormatUnsupported(err) || err == nil && run == nil {
		return pipelineWorkDone(q, c.Key, false) // a historical plan never runs again
	}
	if err != nil {
		return err
	}
	switch r := c.Result.(type) {
	case pipelineDrafts:
		return pipelineImportDrafts(q, run, r)
	case pipelineRecord:
		if r.revoke != "" {
			if err := revokeTokens(q, r.revoke); err != nil {
				return err
			}
		}
		if run.State != "running" || run.revision() != r.revision {
			return pipelineSoon(q, c.Key)
		}
		return r.apply(q, run)
	}
	if run.State == "running" {
		index, err := pipelineNext(q, run)
		if err != nil {
			return err
		}
		if run.State == "running" {
			return pipelineStepAt(q, now, c, run, index)
		}
	}
	// An ended run: a draft-examples run that succeeded leaves its examples to read, then the row goes.
	if run.PipelineID == "draft-examples" && run.State == "succeeded" {
		files, err := run.outputFiles(q, -1)
		if err != nil {
			return err
		}
		a := q.app
		c.Call = func(ctx context.Context) any {
			out := pipelineDrafts{}
			for _, f := range files {
				if !draftExample.MatchString(f.Path) {
					continue
				}
				if b, err := artifactBytes(ctx, a, f.ArtifactID, 64<<20); err == nil {
					out = append(out, b)
				}
			}
			return out
		}
		return nil
	}
	return pipelineWorkDone(q, c.Key, true)
}

// pipelineNext selects the plan entries up to the first step still to run and returns its index; a run with none left
// ends.
func pipelineNext(q *Request, run *runRecord) (int, error) {
	for i := range run.entries() {
		entry := run.entry(i)
		if entry.Activation == contract.PipelineActivationNotSelected {
			continue
		}
		if entry.Activation == contract.PipelineActivationPending {
			if err := run.selectStep(q, i); err != nil {
				return -1, err
			}
			if run.State != "running" {
				return -1, nil
			}
			if entry.Activation == contract.PipelineActivationNotSelected {
				continue
			}
		}
		if entry.Activation == contract.PipelineActivationBlocked {
			return -1, run.finish(q, "failed", "prior_step_failed")
		}
		if run.Steps[i].State == "succeeded" {
			continue
		}
		return i, nil
	}
	return -1, run.finish(q, "succeeded", "")
}

// pipelineRunFrom: a generic run of pipeline id from body b (requestId, projectId, exampleArtifactId, example, overrides);
// draft, when set, is the caller's draft content to run instead of the head.
func pipelineRunFrom(q *Request, pipelineID string, b pipelineRule, draft *pipelineDef, draftBase int64) (*runRecord, error) {
	rid, ok := jsonString(b["requestId"])
	if !ok || rid == "" || len(rid) > 200 || !ruleKeysOnly(b, "requestId", "projectId", "exampleArtifactId", "example", "overrides") {
		return nil, fail(400, "Expected requestId and optional projectId, exampleArtifactId or example, overrides")
	}
	file := pipelineID + ".json"
	// The live definition must exist (a draft of a retired or deleted pipeline does not run).
	head, ok, err := headText(q, "pipeline", pipelineID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(404, "Unknown pipeline "+pipelineID)
	}
	sig := pipelineRule{}
	for k, v := range b {
		sig[k] = v
	}
	// The request's identity includes the content it ran: the head's, or the draft's (fleet-definition-editors.md).
	sig["contentSha"] = jsonBytes(sha256Text(string(head)))
	if draft != nil {
		t, _ := canonicalText(jsonBytes(draft.value()))
		sig["contentSha"], sig["draft"] = jsonBytes(sha256Text(t)), jsonBytes(true)
	}
	signature := sha256hex([]byte(canonical(rawAny(jsonBytes(sig)))))
	old, err := runsOf(q)(q.db().PipelineRunsByRequest(q.ctx(), db.PipelineRunsByRequestParams{RequestID: rid, Pipeline: file}))
	if err != nil {
		return nil, err
	}
	if len(old) > 0 {
		if old[0].Signature != signature {
			return nil, fail(409, "Request identity conflict")
		}
		return old[0], nil
	}
	example := pipelineRuleOf(b["example"])
	if example == nil {
		example = pipelineRule{}
	}
	if id, ok := jsonString(b["exampleArtifactId"]); ok && id != "" {
		ex, err := labExampleRead(q, id)
		if err != nil {
			return nil, err
		}
		if ex.state != "approved" {
			return nil, fail(409, "Example not approved")
		}
		example = pipelineRuleOf(ex.reply.Example)
		if example == nil {
			example = pipelineRule{}
		}
	}
	delete(example, "references") // a run never carries the known-good answers
	overridesRaw := b["overrides"]
	overrides := pipelineRuleOf(overridesRaw)
	if overrides == nil {
		overrides, overridesRaw = pipelineRule{}, json.RawMessage("{}")
	}
	// Fix: overrides take the Lab variant keys only; host, account and profile pick machines and credentials: owner only.
	if !ruleKeysOnly(overrides, labOverrides...) {
		return nil, fail(400, "overrides are "+strings.Join(labOverrides, ", "))
	}
	if q.person != nil && !fullAccess(q.person) && (!isNull(overrides["host"]) || !isNull(overrides["account"]) || !isNull(overrides["profile"])) {
		return nil, fail(403, "Only the owner overrides host, account or profile")
	}
	var projectID *string
	if p, ok := jsonString(b["projectId"]); ok {
		projectID = &p
	}
	start := pipelineStartOptions{File: file, ProjectID: projectID, RequestID: rid, Example: jsonBytes(example), Overrides: jsonBytes(overrides),
		Extra: recordRest{"signature": jsonBytes(signature)}}
	if draft != nil {
		start.draft, start.draftBase, start.hasDraftBase = draft, draftBase, true
	}
	return startRun(q, start)
}

// pipelineSignature: the identity of a pipeline_run or direct_search request (fields in key order).
type pipelineSignature struct {
	Invocation string  `json:"invocation"`
	MethodID   *string `json:"methodId"`
	ProjectID  string  `json:"projectId"`
	Query      string  `json:"query"`
}

// pipelineCatalogue: the methods of a project's catalogue and its recommendation.
type pipelineCatalogue struct {
	RecommendationSource json.RawMessage `json:"recommendationSource"`
	RecommendedMethod    string          `json:"recommendedMethod"`
	Methods              []struct {
		ID       json.RawMessage `json:"id"`
		Pipeline json.RawMessage `json:"pipeline"`
	} `json:"methods"`
}

// runPipeline: the selected method's v2 pipeline with the query as the example ({query, prompt}); waits for the run.
// inv is pipeline (pipeline_run) or direct (direct_search); methodID nil selects the project's recommended method.
func runPipeline(q *Request, inv, projectID, rid, query string, methodID *string) (recordRest, error) {
	if rid == "" || len(rid) > 200 || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 2000 {
		return nil, fail(400, "Expected projectId, requestId, query and optional methodId")
	}
	var run *runRecord
	old := false
	err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
		signature := sha256hex([]byte(canonical(pipelineSignature{ProjectID: projectID, Query: query, MethodID: methodID, Invocation: inv})))
		prev, err := runsOf(q)(q.db().PipelineRunsByProjectRequest(q.ctx(), db.PipelineRunsByProjectRequestParams{ProjectID: projectID, RequestID: rid}))
		if err != nil {
			return err
		}
		if len(prev) > 0 {
			if prev[0].Signature != signature {
				return fail(409, "Request identity conflict")
			}
			run, old = prev[0], true
			return nil
		}
		p, err := projectContext(q, projectID)
		if err != nil {
			return err
		}
		cd, err := catalogue(q, projectID)
		if err != nil {
			return err
		}
		c := agentView[pipelineCatalogue](cd)
		want := deref(methodID)
		source := c.RecommendationSource
		if want == "" {
			want = c.RecommendedMethod
		} else {
			source = jsonBytes("explicit")
		}
		found := false
		var methodRaw json.RawMessage
		var file string
		for _, m := range c.Methods {
			if id, ok := jsonString(m.ID); ok && id == want {
				found, methodRaw = true, m.ID
				file, _ = jsonString(m.Pipeline)
			}
		}
		if !found {
			return fail(400, "Selected method unavailable for project")
		}
		example := jsonBytes(struct {
			Prompt string `json:"prompt"`
			Query  string `json:"query"`
		}{query, query})
		reqBy := requester(q)
		run, err = startRun(q, pipelineStartOptions{File: file, ProjectID: &projectID, RequestID: rid, Example: example, RequestedBy: &reqBy,
			Extra: recordRest{"signature": jsonBytes(signature), "invocation": jsonBytes(inv), "selectedMethod": methodRaw, "selectionSource": orNull(source), "context": jsonBytes(p)}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if old {
		return pipelinePublicRead(q.ctx(), q.app, run.ID)
	}
	return pipelineWait(q.ctx(), q.app, run.ID)
}

// pipelineRerun: loads finished run R and starts R2 from the definition to use (R's pipeline head, or the caller's draft).
// Mode all runs every step; mode failed reuses the longest prefix of R's steps where each succeeded, is the same step in
// the new definition, and (agent steps) would run the same profile content.
func pipelineRerun(q *Request, r *contract.PipelineRerunRequest) (*runRecord, error) {
	old, err := getRun(q, r.RunId)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, fail(404, "Unknown run")
	}
	if st := old.State; st == "running" || st == "preparing" || st == "waiting" {
		return nil, conflictWithReason("The run has not finished", string(contract.ConflictReasonWrongState))
	}
	if r.Mode != contract.RerunModeAll && r.Mode != contract.RerunModeFailed {
		return nil, fail(400, "mode: failed or all")
	}
	draft := r.Draft != nil && *r.Draft
	identity := string(r.Mode) + fmt.Sprint(draft)
	rid := deref(r.RequestId)
	if rid == "" {
		rid = "rerun:" + newID()
	}
	// A repeated requestId answers its rerun (even after the draft changed); a different mode or draft is a conflict.
	if prev, err := runsOf(q)(q.db().PipelineRunsByRerun(q.ctx(), db.PipelineRunsByRerunParams{RequestID: rid, RerunOf: r.RunId})); err != nil {
		return nil, err
	} else if len(prev) > 0 {
		if prev[0].RerunIdentity != identity {
			return nil, fail(409, "Request identity conflict")
		}
		return prev[0], nil
	}
	if err := recoveryFence(q, old); err != nil {
		return nil, err
	}
	// The overrides that pick machines and credentials stay the owner's, as on pipeline_run_create.
	if q.person != nil && !fullAccess(q.person) && old.Overrides.machines() {
		return nil, fail(403, "Only the owner reruns a run that overrides host, account or profile")
	}
	pid := old.PipelineID
	var def *pipelineDef
	var base int64
	if draft {
		if def, base, err = draftPipeline(q, pid); err != nil {
			return nil, err
		}
	} else if def, err = loadPipelineDef(q, pid+".json"); err != nil {
		return nil, err
	}
	reuse := []runStep{}
	if r.Mode == contract.RerunModeFailed {
		var err error
		reuse, err = runPrefix(q, old, def)
		if err != nil {
			return nil, err
		}
	}
	extra, err := rerunExtra(old, identity)
	if err != nil {
		return nil, err
	}
	o := pipelineStartOptions{File: pid + ".json", ProjectID: old.ProjectID, RequestID: rid, Example: old.Example, Overrides: jsonBytes(old.Overrides), RerunOf: r.RunId,
		Reuse: make([]json.RawMessage, len(reuse)), Extra: extra}
	if draft {
		o.draft, o.draftBase, o.hasDraftBase = def, base, true
	}
	return startRun(q, o)
}
