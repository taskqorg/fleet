// Pipeline format version 2 runner (docs/designs/fleet-lab.md "Pipeline format version 2").
// A run is an ordered chain of api, agent and script steps: the pipeline_run execution
// with one pipeline_step row `<run>:step:<i>` per step; each provider call is a tool_run child of its step. Every step
// leaves an output.json artifact. a run moves on the work kit (pipeline_work.go), one step per transaction; a sync api call
// interrupted by a restart is unknown and never replayed.
//
// Core run/definition types, op/route registrations, and run/plan/step persistence live here; run lifecycle lives in
// pipeline_run.go, step dispatch in pipeline_steps.go, and definition/ref/artifact I/O and lineage in pipeline_io.go.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/mcpserve"
)

// runRecord: a pipeline run as the runner holds it (getRun reads it, put writes it): its pipeline_run execution's record
// (execution.data, its state and observation), its plan (pipeline_run and pipeline_step rows) and its steps (each a
// pipeline_step execution's record with its step facts). Keys no field names (a Lab trial's cell, recovery, priority)
// stay in rest, never read here. A kit claim, its call and the step after it share one *runRecord.
type runRecord struct {
	ID                string                            `json:"id"`
	Traceparent       string                            `json:"traceparent,omitempty"`
	ProjectID         *string                           `json:"projectId"`
	RequestID         string                            `json:"requestId,omitempty"`
	RequestedBy       pipelineRequester                 `json:"requestedBy"`
	Definition        pipelineDef                       `json:"definition"`
	DefinitionSha256  string                            `json:"definitionSha256"`
	PipelineID        string                            `json:"pipelineId"`
	Pipeline          string                            `json:"pipeline,omitempty"`
	Example           json.RawMessage                   `json:"example,omitempty"`
	Overrides         pipelineOverrides                 `json:"overrides"`
	State             string                            `json:"state"`
	Observation       string                            `json:"observation,omitempty"`
	StartedAt         *int64                            `json:"startedAt,omitempty"`
	FinishedAt        *int64                            `json:"finishedAt,omitempty"`
	DurationMs        *int64                            `json:"durationMs"`
	Usage             *pipelineCredits                  `json:"usage"`
	Result            *pipelineResult                   `json:"result"`
	Steps             []runStep                         `json:"steps"`
	Definitions       []contract.DefinitionRef          `json:"definitions,omitempty"`
	ProjectSnapshot   json.RawMessage                   `json:"projectSnapshot,omitempty"`
	PlanVersion       int64                             `json:"planVersion,omitempty"`
	Plan              *contract.PipelinePlan            `json:"plan,omitempty"`
	Authority         *contract.PipelineAuthority       `json:"authority,omitempty"`
	CanonicalResult   *contract.PipelineCanonicalResult `json:"canonicalResult,omitempty"`
	ExampleInputs     *[]fileRef                        `json:"exampleInputs,omitempty"`
	InputRefs         []pipelineInputRef                `json:"inputRefs,omitempty"` // inputs by reference as asked, and how each was staged (pipeline_refs.go)
	ParentExecutionID string                            `json:"parentExecutionId,omitempty"`
	TaskID            string                            `json:"taskId,omitempty"`
	DeliveryID        string                            `json:"deliveryId,omitempty"`
	SessionID         string                            `json:"sessionId,omitempty"`
	DirectedBy        string                            `json:"directedBy,omitempty"`
	ExperimentID      string                            `json:"experimentId,omitempty"`
	Variant           string                            `json:"variant,omitempty"`
	Signature         string                            `json:"signature,omitempty"`
	Invocation        string                            `json:"invocation,omitempty"`
	SelectedMethod    string                            `json:"selectedMethod,omitempty"`
	SelectionSource   string                            `json:"selectionSource,omitempty"`
	Context           json.RawMessage                   `json:"context,omitempty"`
	RerunOf           string                            `json:"rerunOf,omitempty"`
	Reused            *[]string                         `json:"reused,omitempty"`
	RerunIdentity     string                            `json:"rerunIdentity,omitempty"`
	failure           json.RawMessage                   // why a run that failed before any step did (finish), as a step error
	rest              recordRest
}

func (r *runRecord) UnmarshalJSON(b []byte) error {
	type plain runRecord
	return restDecode(b, (*plain)(r), &r.rest)
}

func (r runRecord) MarshalJSON() ([]byte, error) {
	type plain runRecord
	return restEncode(plain(r), r.rest)
}

// restAs: the run record's fields that no runRecord field names (an area's own keys on the run, such as the Lab's
// cell and recovery), decoded once into v.
func (r *runRecord) restAs(v any) error {
	if len(r.rest) == 0 {
		return nil
	}
	return json.Unmarshal(jsonBytes(r.rest), v)
}

// runStep: one step of a run: its plan entry's step record (the pipeline_step execution's record and step facts) as the
// runner projection shows it.
type runStep struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Connection       string          `json:"connection,omitempty"`
	State            string          `json:"state,omitempty"`
	Observation      string          `json:"observation,omitempty"`
	ExecutionID      string          `json:"executionId,omitempty"`
	OutputArtifactID string          `json:"outputArtifactId,omitempty"`
	JobID            string          `json:"jobId,omitempty"`
	Polls            *int64          `json:"polls,omitempty"`
	Problem          string          `json:"problem,omitempty"`
	StartedAt        *int64          `json:"startedAt,omitempty"`
	FinishedAt       *int64          `json:"finishedAt,omitempty"`
	DurationMs       *int64          `json:"durationMs,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
	Usage            json.RawMessage `json:"usage,omitempty"`
	Error            json.RawMessage `json:"error,omitempty"`
	Reply            json.RawMessage `json:"reply,omitempty"`
	AdoptedAfter     string          `json:"adoptedAfter,omitempty"`
	ReusedFrom       string          `json:"reusedFrom,omitempty"`
	Calls            []pipelineCall  `json:"calls,omitempty"`
	placementWait    string
	rest             recordRest
}

func (s *runStep) UnmarshalJSON(b []byte) error {
	type plain runStep
	return restDecode(b, (*plain)(s), &s.rest)
}

func (s runStep) MarshalJSON() ([]byte, error) {
	type plain runStep
	return restEncode(plain(s), s.rest)
}

func (s *runStep) unconfirmed() bool { return s.Observation == "unconfirmed" }

// hasOutput: the step recorded an output (null included).
func (s *runStep) hasOutput() bool { return len(s.Output) > 0 }

// pipelineCall: one provider call of a step (its tool_run) with its connection cost.
type pipelineCall struct {
	ID         string          `json:"id"`
	Tool       *string         `json:"tool"`
	Connection *string         `json:"connection"`
	Cost       json.RawMessage `json:"cost"`
	Basis      string          `json:"basis"`
}

// pipelineStepError: a failed step's error.
type pipelineStepError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Detail: the provider's own error reply to a failed connection call, bounded and redacted (callErrorDetail), so a
	// person or agent sees why (a quota name, a retry delay) without the gateway's log.
	Detail string `json:"detail,omitempty"`
}

// pipelineRequester: who asked for a run (a person, the operator, a Lab experiment's requester).
type pipelineRequester struct {
	Kind         string `json:"kind,omitempty"`
	Email        string `json:"email,omitempty"`
	App          string `json:"app,omitempty"` // kind app: the project app (fleet-project-apps.md)
	ExperimentID string `json:"experimentId,omitempty"`
}

// pipelineOverrides: a run's variant overrides (labOverrides); values the runner passes on stay as written.
type pipelineOverrides struct {
	Profile   string                     `json:"profile,omitempty"`
	Model     json.RawMessage            `json:"model,omitempty"`
	Skills    json.RawMessage            `json:"skills,omitempty"`
	Prompt    json.RawMessage            `json:"prompt,omitempty"`
	WebRoute  json.RawMessage            `json:"web_route,omitempty"`
	Account   string                     `json:"account,omitempty"`
	Host      json.RawMessage            `json:"host,omitempty"`
	Arguments map[string]json.RawMessage `json:"arguments,omitempty"`
	// Candidate: a proposed CLI pin {name, asset} the agent step runs instead of its release's (fleet-host-updates.md
	// step 4); the executor stages it with the release's own checks.
	Candidate json.RawMessage `json:"candidate,omitempty"`
	rest      recordRest
}

func (o *pipelineOverrides) UnmarshalJSON(b []byte) error {
	type plain pipelineOverrides
	return restDecode(b, (*plain)(o), &o.rest)
}

func (o pipelineOverrides) MarshalJSON() ([]byte, error) {
	type plain pipelineOverrides
	return restEncode(plain(o), o.rest)
}

// host: the host override as text ("" for none).
func (o *pipelineOverrides) host() string {
	if isNull(o.Host) {
		return ""
	}
	if s, ok := jsonString(o.Host); ok {
		return s
	}
	return string(bytes.TrimSpace(o.Host))
}

// machines: the overrides pick a host, an account or a profile (the owner's to set).
func (o *pipelineOverrides) machines() bool {
	return !isNull(o.Host) || o.Account != "" || o.Profile != "" || !isNull(o.rest["account"]) || !isNull(o.rest["profile"])
}

type pipelineCredits struct {
	Credits float64 `json:"credits"`
}

// pipelineResult: a finished run's result: each step's output, the search answer's sources and the error that ended it.
type pipelineResult struct {
	Outputs map[string]json.RawMessage `json:"outputs"`
	Native  json.RawMessage            `json:"native,omitempty"`
	Sources []pipelineSource           `json:"sources"`
	Error   json.RawMessage            `json:"error,omitempty"`
}

type pipelineSource struct {
	URL     json.RawMessage `json:"url"`
	Title   json.RawMessage `json:"title"`
	Content json.RawMessage `json:"content"`
}

// pipelineDef: a pipeline definition (version 2 or 3, validatePipelineDef); raw is its text, which the run keeps and its
// hashes read.
type pipelineDef struct {
	Version     float64           `json:"version"`
	ID          string            `json:"id"`
	Description string            `json:"description,omitempty"`
	Input       string            `json:"input,omitempty"`
	Output      string            `json:"output,omitempty"`
	Steps       []pipelineDefStep `json:"steps"`
	ResultStep  string            `json:"resultStep,omitempty"`
	raw         json.RawMessage
}

func (d *pipelineDef) UnmarshalJSON(b []byte) error {
	type plain pipelineDef
	d.raw = append(json.RawMessage(nil), b...)
	return lenientDecode(b, (*plain)(d))
}

func (d pipelineDef) MarshalJSON() ([]byte, error) {
	if d.raw != nil {
		return d.raw, nil
	}
	type plain pipelineDef
	return json.Marshal(plain(d))
}

// value: the definition as decoded JSON (its hashes and definition reference read that).
func (d *pipelineDef) value() any { return rawAny(d.raw) }

// pipelineDefStep: one step of a definition (contract.PipelineStep's fields); raw is its text.
type pipelineDefStep struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Description string          `json:"description,omitempty"`
	Connection  string          `json:"connection,omitempty"`
	Tool        string          `json:"tool,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	StatusTool  string          `json:"status_tool,omitempty"`
	RecentTool  string          `json:"recent_tool,omitempty"`
	Profile     string          `json:"profile,omitempty"`
	Prompt      json.RawMessage `json:"prompt,omitempty"`
	Command     json.RawMessage `json:"command,omitempty"`
	TimeoutMs   json.RawMessage `json:"timeout_ms,omitempty"`
	Host        string          `json:"host,omitempty"`
	When        json.RawMessage `json:"when,omitempty"`
	// Publishes: the agent step publishes to the project's forge though its profile does not say so (publishing).
	Publishes bool `json:"publishes,omitempty"`
	raw       json.RawMessage
}

func (s *pipelineDefStep) UnmarshalJSON(b []byte) error {
	type plain pipelineDefStep
	s.raw = append(json.RawMessage(nil), b...)
	return lenientDecode(b, (*plain)(s))
}

func (s pipelineDefStep) MarshalJSON() ([]byte, error) {
	if s.raw != nil {
		return s.raw, nil
	}
	type plain pipelineDefStep
	return json.Marshal(plain(s))
}

// sha: the step's definition hash (its plan entry's definitionSha256).
func (s *pipelineDefStep) sha() string { return sha256hex([]byte(canonical(rawAny(s.raw)))) }

// when: the step's condition (nil when it has none).
func (s *pipelineDefStep) when() (*pipelineWhen, error) { return parsePipelineWhen(rawAny(s.When)) }

// lenientDecode: b into v; a field of another JSON type reads as zero (validatePipelineDef refuses such a definition).
func lenientDecode(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		if te := (*json.UnmarshalTypeError)(nil); !errors.As(err, &te) {
			return err
		}
	}
	return nil
}

// recordOf: v's JSON object field by field.
func recordOf(v any) (recordRest, error) {
	m := recordRest{}
	if err := json.Unmarshal(jsonBytes(v), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// jsonNorm: a JSON value as encoding/json writes it decoded (object keys sorted, numbers as float64): output objects
// are saved and their hashes checked in this form, whatever text the database returned.
func jsonNorm(raw json.RawMessage) []byte { return jsonBytes(rawAny(raw)) }

func (r *runRecord) entries() []contract.PipelinePlanEntry {
	if r.Plan == nil {
		return nil
	}
	return r.Plan.Entries
}

// entry: entry index of the plan, nil out of range (aliases: an edit is the plan's).
func (r *runRecord) entry(index int) *contract.PipelinePlanEntry {
	entries := r.entries()
	if index < 0 || index >= len(entries) {
		return nil
	}
	return &entries[index]
}

func (r *runRecord) def(i int) *pipelineDefStep { return &r.Definition.Steps[i] }

func (r *runRecord) row(i int) string { return fmt.Sprintf("%s:step:%d", r.ID, i) }

func (r *runRecord) revision() int64 {
	if r.Plan != nil {
		return r.Plan.Revision
	}
	return 0
}

func (r *runRecord) project() string { return deref(r.ProjectID) }

func (r *runRecord) unconfirmed() bool { return r.Observation == "unconfirmed" }

// director: the person the run acts for (its caller's director, else its requester).
func (r *runRecord) director() string { return firstString(r.DirectedBy, r.RequestedBy.Email) }

func (r *runRecord) scope() string {
	if r.ExperimentID != "" {
		return "lab"
	}
	return "prod"
}

// exampleInputs: the example's input files as written.
func (r *runRecord) exampleInputs() []pipelineRule {
	var ex struct {
		Inputs []json.RawMessage `json:"inputs"`
	}
	lenientDecode(r.Example, &ex)
	out := []pipelineRule{}
	for _, x := range ex.Inputs {
		out = append(out, pipelineRuleOf(x))
	}
	return out
}

// outputs: the outputs of the steps before i by step id, as references read them.
func (r *runRecord) outputs(i int) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, s := range r.Steps[:i] {
		out[s.ID] = s.Output
	}
	return out
}

// merge: extra's keys over the run's own (a caller's extra fields, a rerun's frozen inputs).
func (r *runRecord) merge(extra recordRest) error {
	if len(extra) == 0 {
		return nil
	}
	m, err := recordOf(r)
	if err != nil {
		return err
	}
	for k, v := range extra {
		m[k] = v
	}
	plan := r.Plan
	var out runRecord
	if err := json.Unmarshal(jsonBytes(m), &out); err != nil {
		return err
	}
	out.Plan = plan
	*r = out
	return nil
}

// pipelineConfigRef: a run row's config_ref (the selected method and the definition's hash).
type pipelineConfigRef struct {
	DefinitionSha256 string  `json:"definitionSha256"`
	Method           *string `json:"method"`
}

// msColumn: a millisecond time as a column value.
func msColumn(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

// put writes the current plan and only executions that have actually been selected.
func (run *runRecord) put(q *Request) error {
	if err := guardPipelineWrite(q, run); err != nil {
		return err
	}
	rest, err := recordOf(run)
	if err != nil {
		return err
	}
	for _, k := range []string{"steps", "state", "observation", "plan"} {
		delete(rest, k)
	}
	state, obs, err := runState(run.ID, run.State, run.Observation)
	if err != nil {
		return err
	}
	cfg := pipelineConfigRef{Method: agentOpt(run.SelectedMethod), DefinitionSha256: run.DefinitionSha256}
	_, _, pipeID, pipeRev := definitionScalars(run.Definitions, "")
	if pipeID == nil {
		pipeID = nilIf(run.PipelineID)
	}
	project, dir, taskID, sessID := agentOpt(run.project()), agentOpt(run.director()), agentOpt(firstString(run.DeliveryID, run.TaskID)), agentOpt(run.SessionID)
	pid, prev := strPtr(pipeID), nullInt(pipeRev)
	botID, err := executionBotAttr(q, run.ParentExecutionID, run.SessionID)
	if err != nil {
		return err
	}
	botPtr := strPtr(botID)
	o := db.Observation(obs)
	inserted, err := executionInsert(q, db.ExecutionInsertParams{ID: run.ID, Kind: "pipeline_run", State: db.ExecutionState(state), Observation: &o, ProjectID: project,
		StartedAt: msColumn(run.StartedAt), EndedAt: msColumn(run.FinishedAt), Data: jsonBytes(rest), ConfigRef: jsonBytes(cfg), ExperimentID: agentOpt(run.ExperimentID),
		Variant: agentOpt(run.Variant), TaskID: taskID, BotID: botPtr, SessionID: sessID, DirectedBy: dir, PipelineID: pid, PipelineRevision: prev, ParentExecutionID: agentOpt(run.ParentExecutionID)})
	if err != nil {
		return err
	}
	if run.RequestedBy.Kind == "app" && run.RequestedBy.App != "" {
		if err := q.db().ExecutionSetApp(q.ctx(), db.ExecutionSetAppParams{ID: run.ID, AppID: &run.RequestedBy.App}); err != nil {
			return err
		}
	}
	if !inserted {
		if err := q.db().PipelineRunUpdate(q.ctx(), db.PipelineRunUpdateParams{ID: run.ID, Observation: o, ProjectID: project,
			StartedAt: msColumn(run.StartedAt), EndedAt: msColumn(run.FinishedAt), Data: jsonBytes(rest), ConfigRef: jsonBytes(cfg), ExperimentID: agentOpt(run.ExperimentID),
			Variant: agentOpt(run.Variant), TaskID: taskID, BotID: botPtr, SessionID: sessID, DirectedBy: dir, PipelineID: pid, PipelineRevision: prev,
			ParentExecutionID: agentOpt(run.ParentExecutionID)}); err != nil {
			return err
		}
		if _, err := executionTransition(q, run.ID, executionEvent{To: state, KeepEnd: true}); err != nil {
			return err
		}
	}
	if err := putPipelinePlan(q, run.ID, run.Plan); err != nil {
		return err
	}
	if state == "running" {
		// A running run has its row on the kit (pipeline_work.go); a new one wakes the queue.
		if n, err := q.db().PipelineWorkEnsure(q.ctx(), run.ID); err != nil {
			return err
		} else if n > 0 {
			if err := kitNotify(q, "pipeline"); err != nil {
				return err
			}
		}
	}
	if run.Definitions != nil {
		if err := q.db().PipelineRunDefinitionsSet(q.ctx(), db.PipelineRunDefinitionsSetParams{ID: run.ID, Definitions: jsonBytes(run.Definitions),
			PipelineID: pid, PipelineRevision: prev}); err != nil {
			return err
		}
	}
	for i := range run.Steps {
		entry := run.entry(i)
		if entry == nil || entry.Activation != contract.PipelineActivationSelected || entryRefMode(entry) == contract.PipelineRefModeReused {
			continue
		}
		s := &run.Steps[i]
		st, sobs, err := runState(s.ID, s.State, s.Observation)
		if err != nil {
			return err
		}
		facts, data, err := pipelineStepFactsOf(run, i)
		if err != nil {
			return err
		}
		var endedAt *time.Time
		if executionEnds(st) {
			endedAt = msColumn(s.FinishedAt)
		}
		row := run.row(i)
		so := db.Observation(sobs)
		inserted, err := executionInsert(q, db.ExecutionInsertParams{ID: row, Kind: "pipeline_step", State: db.ExecutionState(st), Observation: &so, ProjectID: project,
			ParentExecutionID: &run.ID, StartedAt: msColumn(s.StartedAt), EndedAt: endedAt, Data: data, TaskID: taskID, BotID: botPtr, SessionID: sessID, DirectedBy: dir, PipelineID: pid,
			PipelineRevision: prev, StepID: agentOpt(s.ID), ExperimentID: agentOpt(run.ExperimentID), Variant: agentOpt(run.Variant)})
		if err != nil {
			return err
		}
		if err := q.db().PipelineStepRecordSet(q.ctx(), facts); err != nil {
			return err
		}
		if inserted {
			continue
		}
		if err := q.db().PipelineStepUpdate(q.ctx(), db.PipelineStepUpdateParams{ID: row, Observation: so, ProjectID: project,
			StartedAt: msColumn(s.StartedAt), EndedAt: endedAt, Data: data, TaskID: taskID, SessionID: sessID, DirectedBy: dir,
			ExperimentID: agentOpt(run.ExperimentID), Variant: agentOpt(run.Variant), PipelineID: pid, PipelineRevision: prev, StepID: agentOpt(s.ID)}); err != nil {
			return err
		}
		if _, err := executionTransition(q, row, executionEvent{To: st, KeepEnd: true}); err != nil {
			return err
		}
	}
	return nil
}

// pipelineStepFactsOf: step i's facts (pipeline_step's step columns: what only the pipeline code reads) and the rest of
// its record (the execution's data, with its index and the run's trace).
func pipelineStepFactsOf(run *runRecord, i int) (db.PipelineStepRecordSetParams, []byte, error) {
	s := run.Steps[i]
	f := db.PipelineStepRecordSetParams{RunID: run.ID, StepIndex: int32(i), Polls: s.Polls, StartedAtMs: s.StartedAt, FinishedAtMs: s.FinishedAt, DurationMs: s.DurationMs}
	for _, t := range []struct {
		v   string
		col **string
	}{{s.ExecutionID, &f.NativeExecutionID}, {s.OutputArtifactID, &f.OutputArtifactID}, {s.JobID, &f.JobID}, {s.Problem, &f.StepProblem}} {
		if t.v != "" {
			v := t.v
			*t.col = &v
		}
	}
	for _, v := range []struct {
		raw json.RawMessage
		col *[]byte
	}{{s.Arguments, &f.Arguments}, {s.Output, &f.Output}, {s.Usage, &f.Usage}, {s.Error, &f.Error}} {
		if len(v.raw) > 0 {
			*v.col = jsonBytes(v.raw)
		}
	}
	s.State, s.Observation, s.ExecutionID, s.OutputArtifactID, s.JobID, s.Problem = "", "", "", "", "", ""
	s.Polls, s.StartedAt, s.FinishedAt, s.DurationMs = nil, nil, nil, nil
	s.Arguments, s.Output, s.Usage, s.Error = nil, nil, nil, nil
	data, err := recordOf(s)
	if err != nil {
		return f, nil, err
	}
	for _, k := range []string{"state", "observation", "executionId", "outputArtifactId", "jobId", "problem", "polls", "startedAt", "finishedAt", "durationMs", "arguments", "output", "usage", "error"} {
		delete(data, k)
	}
	if _, ok := data["traceparent"]; !ok {
		data["traceparent"] = jsonBytes(run.Traceparent)
	}
	data["index"] = jsonBytes(i)
	return f, jsonBytes(data), nil
}

// withPipelineStepFacts puts a step row's facts (PipelineSteps) back into its record.
func withPipelineStepFacts(data recordRest, r db.PipelineStepsRow) {
	for k, p := range map[string]*string{"executionId": r.NativeExecutionID, "outputArtifactId": r.OutputArtifactID, "jobId": r.JobID, "problem": r.StepProblem} {
		if p != nil {
			data[k] = jsonBytes(*p)
		}
	}
	for k, p := range map[string]*int64{"polls": r.Polls, "startedAt": r.StartedAtMs, "finishedAt": r.FinishedAtMs, "durationMs": r.DurationMs} {
		if p != nil {
			data[k] = jsonBytes(*p)
		}
	}
	for k, b := range map[string][]byte{"arguments": r.Arguments, "output": r.Output, "usage": r.Usage, "error": r.Error} {
		if b != nil {
			data[k] = b
		}
	}
}

// putPipelinePlan writes plan p of run id to pipeline_run and its entries to pipeline_step (an entry's inputs: none is
// not yet frozen, an empty list is frozen empty).
func putPipelinePlan(q *Request, id string, p *contract.PipelinePlan) error {
	if p == nil {
		return nil
	}
	d := q.db()
	if err := d.PipelinePlanHeadSet(q.ctx(), db.PipelinePlanHeadSetParams{RunID: id, DefinitionSha256: p.DefinitionSha256, Revision: p.Revision}); err != nil {
		return err
	}
	for i := range p.Entries {
		e := &p.Entries[i]
		row := db.PipelinePlanStepSetParams{RunID: id, StepIndex: int32(i), StepID: e.Id, DefinitionSha256: e.DefinitionSha256, Activation: string(e.Activation)}
		if e.Decision != nil {
			row.Decision = jsonBytes(e.Decision)
		}
		if r := e.ExecutionRef; r != nil {
			mode, index := string(r.Mode), int32(r.SourceStepIndex)
			row.RefMode, row.RefExecutionID, row.RefSourceRunID, row.RefSourceStepID = &mode, &r.ExecutionId, &r.SourceRunId, &r.SourceStepId
			row.RefSourceStepIndex, row.RefSourceDefinitionSha256, row.RefSourceInputSha256 = &index, &r.SourceDefinitionSha256, &r.SourceInputSha256
		}
		if x := e.Dispatch; x != nil {
			phase := string(x.Phase)
			row.DispatchPhase, row.DispatchClaimID, row.DispatchOwnerBootID, row.DispatchHandedOffAtMs = &phase, x.ClaimId, x.OwnerBootId, x.HandedOffAtMs
			row.DispatchAuthoritySha256, row.DispatchNativeExecutionID, row.DispatchCommandID = x.AuthoritySha256, x.NativeExecutionId, x.CommandId
			row.DispatchHostID, row.DispatchSettledAtMs = x.HostId, x.SettledAtMs
		}
		if e.Inputs != nil {
			row.Inputs = jsonBytes(e.Inputs)
		}
		if e.Problem != nil {
			code := string(e.Problem.Code)
			row.ProblemCode, row.ProblemMessage = &code, &e.Problem.Message
		}
		if err := d.PipelinePlanStepSet(q.ctx(), row); err != nil {
			return err
		}
	}
	return d.PipelinePlanStepsTrim(q.ctx(), db.PipelinePlanStepsTrimParams{RunID: id, Entries: int32(len(p.Entries))})
}

// pipelinePlanOf: the plan of one run from its stored head and entries (in index order).
func pipelinePlanOf(head db.PipelineRun, steps []db.PipelineStep) (*contract.PipelinePlan, error) {
	p := &contract.PipelinePlan{DefinitionSha256: head.DefinitionSha256, Revision: head.Revision, Entries: []contract.PipelinePlanEntry{}}
	for _, s := range steps {
		e := contract.PipelinePlanEntry{Id: s.StepID, Index: int64(s.StepIndex), DefinitionSha256: s.DefinitionSha256, Activation: contract.PipelineActivation(s.Activation)}
		if s.Decision != nil {
			var decision contract.PipelineConditionDecision
			if err := json.Unmarshal(s.Decision, &decision); err != nil {
				return nil, err
			}
			e.Decision = &decision
		}
		if s.RefMode != nil {
			e.ExecutionRef = &contract.PipelineExecutionRef{Mode: contract.PipelineRefMode(*s.RefMode), ExecutionId: deref(s.RefExecutionID), SourceRunId: deref(s.RefSourceRunID),
				SourceStepId: deref(s.RefSourceStepID), SourceDefinitionSha256: deref(s.RefSourceDefinitionSha256), SourceInputSha256: deref(s.RefSourceInputSha256)}
			if s.RefSourceStepIndex != nil {
				e.ExecutionRef.SourceStepIndex = int64(*s.RefSourceStepIndex)
			}
		}
		if s.DispatchPhase != nil {
			e.Dispatch = &contract.PipelineDispatch{Phase: contract.PipelineDispatchPhase(*s.DispatchPhase), ClaimId: s.DispatchClaimID, OwnerBootId: s.DispatchOwnerBootID,
				HandedOffAtMs: s.DispatchHandedOffAtMs, AuthoritySha256: s.DispatchAuthoritySha256, NativeExecutionId: s.DispatchNativeExecutionID,
				CommandId: s.DispatchCommandID, HostId: s.DispatchHostID, SettledAtMs: s.DispatchSettledAtMs}
		}
		if s.Inputs != nil {
			e.Inputs = []contract.PipelineInputBinding{}
			if err := json.Unmarshal(s.Inputs, &e.Inputs); err != nil {
				return nil, err
			}
		}
		if s.ProblemCode != nil {
			e.Problem = &contract.PipelinePlanProblem{Code: contract.PipelinePlanReason(*s.ProblemCode), Message: deref(s.ProblemMessage)}
		}
		p.Entries = append(p.Entries, e)
	}
	return p, nil
}

// withPipelinePlans sets each run's plan from its stored rows; a run without them keeps none (a format
// requirePlan refuses).
func withPipelinePlans(q *Request, runs []*runRecord) error {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	heads, err := q.db().PipelinePlanHeads(q.ctx(), ids)
	if err != nil {
		return err
	}
	steps, err := q.db().PipelinePlanSteps(q.ctx(), ids)
	if err != nil {
		return err
	}
	byRun := map[string][]db.PipelineStep{}
	for _, s := range steps {
		byRun[s.RunID] = append(byRun[s.RunID], s)
	}
	plans := map[string]*contract.PipelinePlan{}
	for _, h := range heads {
		if plans[h.RunID], err = pipelinePlanOf(h, byRun[h.RunID]); err != nil {
			return err
		}
	}
	for _, run := range runs {
		if p, ok := plans[run.ID]; ok {
			run.Plan = p
		}
	}
	return nil
}

func withSteps(q *Request, runs []*runRecord) ([]*runRecord, error) {
	if len(runs) == 0 {
		return runs, nil
	}
	if err := withPipelinePlans(q, runs); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, run := range runs {
		if err := run.requirePlan(); err != nil {
			return nil, err
		}
		for i := range run.entries() {
			if entry := run.entry(i); entry.Activation == contract.PipelineActivationSelected {
				ids = append(ids, refExecutionID(entry))
			}
		}
	}
	records, err := q.db().PipelineSteps(q.ctx(), ids)
	if err != nil {
		return nil, err
	}
	type stepRecord struct {
		owner, project *string
		step           runStep // the step's record, as the runner projection shows it
	}
	byID := map[string]stepRecord{}
	for _, r := range records {
		var data recordRest
		if err := json.Unmarshal(r.Record, &data); err != nil {
			return nil, err
		}
		withPipelineStepFacts(data, r)
		delete(data, "index")
		var s runStep
		if err := json.Unmarshal(jsonBytes(data), &s); err != nil {
			return nil, err
		}
		if s.State == "running" {
			s.placementWait = r.PlacementWait
		}
		byID[r.ID] = stepRecord{r.ParentExecutionID, r.ProjectID, s}
	}
	calls, err := q.db().PipelineStepCalls(q.ctx(), ids)
	if err != nil {
		return nil, err
	}
	byStep := map[string][]pipelineCall{}
	for _, c := range calls {
		call := pipelineCall{ID: c.ID, Tool: agentOpt(c.Tool), Connection: agentOpt(c.Connection), Cost: c.Cost, Basis: c.Basis}
		if c.Cost != nil && !json.Valid(c.Cost) {
			return nil, fmt.Errorf("pipeline call %s: invalid cost", c.ID)
		}
		byStep[c.Step] = append(byStep[c.Step], call)
	}
	for _, run := range runs {
		list := make([]runStep, 0, len(run.entries()))
		for i := range run.entries() {
			entry, def := run.entry(i), run.def(i)
			step := runStep{ID: entry.Id, Kind: def.Kind}
			if def.Kind == "api" {
				step.Tool, step.Connection = def.Tool, def.Connection
			}
			if entry.Activation == contract.PipelineActivationSelected && entry.ExecutionRef == nil {
				return nil, fail(409, "Missing or foreign selected pipeline execution")
			}
			if entry.Activation == contract.PipelineActivationSelected {
				ref := entry.ExecutionRef
				record, found := byID[ref.ExecutionId]
				if !found || record.owner == nil || *record.owner != ref.SourceRunId || deref(record.project) != run.project() || record.step.ID != ref.SourceStepId {
					return nil, fail(409, "Missing or foreign selected pipeline execution")
				}
				s := record.step
				if s.Tool == "" {
					s.Tool = step.Tool
				}
				if s.Connection == "" {
					s.Connection = step.Connection
				}
				s.ID, s.Kind = entry.Id, def.Kind
				s.Calls = append([]pipelineCall{}, byStep[ref.ExecutionId]...)
				if ref.Mode == contract.PipelineRefModeReused {
					s.ReusedFrom = ref.SourceRunId
				}
				step = s
			}
			list = append(list, step)
		}
		run.Steps = list
	}
	return runs, nil
}

// getRun: run id with its plan and steps; nil when there is none.
func getRun(q *Request, id string) (*runRecord, error) {
	raw, err := q.db().PipelineRun(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var run runRecord
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, err
	}
	rs, err := withSteps(q, []*runRecord{&run})
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

// runsOf: run records (PipelineRun... queries) with their steps.
func runsOf(q *Request) func([][]byte, error) ([]*runRecord, error) {
	return func(raws [][]byte, err error) ([]*runRecord, error) {
		if err != nil {
			return nil, err
		}
		runs := make([]*runRecord, 0, len(raws))
		for _, raw := range raws {
			var run runRecord
			if err := json.Unmarshal(raw, &run); err != nil {
				return nil, err
			}
			runs = append(runs, &run)
		}
		return withSteps(q, runs)
	}
}

// runState: the typed state and observation of a run or step record; unconfirmed only when the record says so.
func runState(id, state, observation string) (string, string, error) {
	state, obs := firstString(state, "preparing"), "confirmed"
	if _, ok := executionTransitions[state]; !ok {
		return "", "", fmt.Errorf("pipeline record %v: unknown state %q", id, state)
	}
	if observation == "unconfirmed" {
		obs = "unconfirmed"
	}
	return state, obs, nil
}

func requester(q *Request) pipelineRequester {
	if q.caller != nil {
		return pipelineRequester{Kind: "app", App: q.caller.ID}
	}
	if q.person != nil {
		return pipelineRequester{Kind: "person", Email: q.person.Email}
	}
	return pipelineRequester{Kind: "operator"}
}

// derefAny: an optional column as the value a record holds (nil when NULL).
func derefAny[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// textOf: a record's string field as a column value ("" kept, nil for anything else).
func textOf(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// msTimePtr: a millisecond time as a column value (msTime).
func msTimePtr(v any) *time.Time {
	if t, ok := msTime(v).(time.Time); ok {
		return &t
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strOr(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func init() {
	// Generic v2 run: an example (an approved example artifact, references withheld, or an inline example) and overrides.
	longRoute("POST /api/pipelines/{id}/runs", func(q *Request) (any, error) {
		var out *runRecord
		err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
			var err error
			out, err = pipelineRunFrom(q, q.r.PathValue("id"), recOf(body(q)), nil, 0)
			return err
		})
		if err != nil {
			return nil, err
		}
		return pipelinePublicRead(q.ctx(), q.app, out.ID)
	})
	// pipeline_rerun: a finished run again on the current definition (or the caller's draft); mode failed reuses the steps
	// that succeeded and would run the same way.
	op("pipeline_rerun", func(q *Request, r *contract.PipelineRerunRequest) (contract.PipelineRerunReply, error) {
		out := contract.PipelineRerunReply{Reused: []string{}, Definitions: []contract.DefinitionRef{}}
		var run *runRecord
		err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
			var err error
			run, err = pipelineRerun(q, r)
			return err
		})
		if err != nil {
			return out, err
		}
		out.RunId = run.ID
		if run.Reused != nil {
			out.Reused = append(out.Reused, *run.Reused...)
		}
		if run.Definitions != nil {
			out.Definitions = run.Definitions
		}
		return out, nil
	})
	// pipeline_run_create: the route as an operation, plus draft (the caller's draft; architect only).
	op("pipeline_run_create", func(q *Request, r *contract.PipelineRunCreateRequest) (contract.PipelineRunCreateReply, error) {
		var out contract.PipelineRunCreateReply
		var run *runRecord
		err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
			b := pipelineRule{"requestId": jsonBytes(r.RequestId)}
			if r.ProjectId != nil {
				b["projectId"] = jsonBytes(*r.ProjectId)
			}
			if r.ExampleArtifactId != nil {
				b["exampleArtifactId"] = jsonBytes(*r.ExampleArtifactId)
			}
			if r.Example != nil {
				b["example"] = jsonBytes(r.Example)
			}
			if r.Overrides != nil {
				b["overrides"] = jsonBytes(r.Overrides)
			}
			var draft *pipelineDef
			var base int64
			// A repeated requestId answers its run, even after the draft was saved or changed.
			if old, err := runsOf(q)(q.db().PipelineRunsByRequest(q.ctx(), db.PipelineRunsByRequestParams{RequestID: r.RequestId, Pipeline: r.PipelineId + ".json"})); err != nil {
				return err
			} else if len(old) > 0 && r.Draft != nil && *r.Draft {
				run = old[0]
				return nil
			}
			if r.Draft != nil && *r.Draft {
				var err error
				if draft, base, err = draftPipeline(q, r.PipelineId); err != nil {
					return err
				}
			}
			var err error
			run, err = pipelineRunFrom(q, r.PipelineId, b, draft, base)
			return err
		})
		if err != nil {
			return out, err
		}
		out.RunId = run.ID
		out.Definitions = run.Definitions
		if out.Definitions == nil {
			out.Definitions = []contract.DefinitionRef{}
		}
		return out, nil
	})
}

// longOperations: operations that open their own transactions (q.tx nil). runOp (ops.go) runs them without one.
var longOperations = map[string]handler{}

// pipelineQuestionData: the budget ask a question carries.
type pipelineQuestionData struct {
	Data struct {
		BudgetAsk json.RawMessage `json:"budgetAsk"`
	} `json:"data"`
}

// Answer checks run before a question's answer is recorded and the after steps run after, in the same transaction:
// Lab, then budgets. question is the row (snake_case columns); in after, its answer column holds the answer.
func runAnswerChecks(q *Request, question json.RawMessage, answer string) error {
	if err := labAnswerCheck(q, question, answer); err != nil {
		return err
	}
	if err := keyAnswerCheck(q, question, answer); err != nil {
		return err
	}
	return budgetAnswerCheck(q, orNull(agentView[pipelineQuestionData](question).Data.BudgetAsk), answer)
}

// pipelineAnswered: an answered question's row as labAnswerAfter (lab.go) reads it.
type pipelineAnswered struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Answer     *string         `json:"answer"`
	AnsweredBy *string         `json:"answered_by"`
	Options    json.RawMessage `json:"options"`
	Data       json.RawMessage `json:"data"`
}

func runAnswerAfter(q *Request, id string) error {
	r, err := q.db().PipelineQuestion(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !json.Valid(r.Data) || r.Options != nil && !json.Valid(r.Options) {
		return fmt.Errorf("question %s: invalid record", id)
	}
	data := json.RawMessage(r.Data)
	if err := problemAnswerAfter(q, id, data); err != nil {
		return err
	}
	if err := labAnswerAfter(q, jsonBytes(pipelineAnswered{ID: r.ID, Kind: r.Kind, Answer: r.Answer, AnsweredBy: r.AnsweredBy, Options: orNull(r.Options), Data: data})); err != nil {
		return err
	}
	var ask struct {
		BudgetAsk json.RawMessage `json:"budgetAsk"`
	}
	lenientDecode(data, &ask)
	return budgetAnswerAfter(q, orNull(ask.BudgetAsk), deref(r.Answer), r.AnsweredBy)
}

// projectIDPtr: a project id field as a pointer, nil when absent.
func projectIDPtr(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// Leave room below the 16 MiB frame limit for the request ID and transport
// metadata. Measure the wrapped JSON because MCP quotes the response as text.
const maxPipelineReplyBytes = 15 << 20

func boundPipelineReply(run contract.PipelineRunReply) (contract.PipelineRunReply, int, error) {
	wire, err := json.Marshal(mcpserve.Result(nil, run))
	if err != nil {
		return contract.PipelineRunReply{}, 0, err
	}
	if len(wire) <= maxPipelineReplyBytes {
		return run, len(wire), nil
	}
	summary := contract.PipelineRunReply{
		Id: run.Id, State: run.State, Observation: run.Observation, PlanVersion: run.PlanVersion,
		PlanSummary: run.PlanSummary, ProjectId: run.ProjectId, PipelineId: run.PipelineId,
		RequestId: run.RequestId, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		DurationMs: run.DurationMs, Truncated: ptr(true),
	}
	if run.CanonicalResult != nil {
		summary.ResultArtifact = &run.CanonicalResult.Artifact
	}
	wire, err = json.Marshal(mcpserve.Result(nil, summary))
	if err != nil {
		return contract.PipelineRunReply{}, 0, err
	}
	if len(wire) > maxPipelineReplyBytes {
		return contract.PipelineRunReply{}, 0, fail(413, "Pipeline run summary exceeds the response size limit")
	}
	return summary, len(wire), nil
}

func init() {
	op("pipeline_run", func(q *Request, r *contract.PipelineRunRequest) (contract.PipelineRunReply, error) {
		run, err := replyAs[contract.PipelineRunReply](runPipeline(q, "pipeline", r.ProjectId, r.RequestId, r.Query, r.MethodId, r.Inputs, r.Notify, r.Wait == nil || *r.Wait))
		if err != nil {
			return contract.PipelineRunReply{}, err
		}
		run, _, err = boundPipelineReply(run)
		return run, err
	})
	op("direct_search", func(q *Request, r *contract.DirectSearchRequest) (contract.DirectSearchReply, error) {
		return replyAs[contract.DirectSearchReply](runPipeline(q, "direct", r.ProjectId, r.RequestId, r.Query, r.MethodId, nil, nil, true))
	})
}

func recommend(q *Request, projectID, methodID string) (any, error) {
	sub := *q
	sub.body = jsonBytes(struct {
		ProjectID string `json:"projectId"`
		MethodID  string `json:"methodId"`
	}{projectID, methodID})
	return projectOps["recommendation_set"].h(&sub)
}

func projectKnown(q *Request, id string) bool { _, err := catalogueBase(q, id); return err == nil }
