// Pipeline format version 2 runner (docs/designs/fleet-lab.md "Pipeline format version 2").
// A run is an ordered chain of api, agent and script steps: the pipeline_run execution
// with one pipeline_step row `<run>:step:<i>` per step; each provider call is a tool_run child of its step. Every step
// leaves an output.json artifact. a run moves on the work kit (pipeline_work.go), one step per transaction; a sync api call
// interrupted by a restart is unknown and never replayed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"fleet/contract"

	"fleet/coordinator/db"
)

var (
	stepIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	pipelineFile   = regexp.MustCompile(`^[a-z0-9-]+\.json$`)
	infraCode      = regexp.MustCompile(`^(queue-full|no-usable-key|http-429|http-432|http-433|key-disabled-\d+|provider-unreachable|unreachable|unauthenticated|connection-unavailable|client-hash-mismatch)$`)
	pipeRefPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
	wholeRef       = regexp.MustCompile(`^\$\{([^}]+)\}$`)
	http5xx        = regexp.MustCompile(`^http-5\d\d$`)
	ownOutput      = regexp.MustCompile(`^(output/)?output\.json$`)
)

// runRecord: a pipeline run as the runner holds it (loadRun reads it, put writes it): its pipeline_run execution's record
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
}

// pipelineRequester: who asked for a run (a person, the operator, a Lab experiment's requester).
type pipelineRequester struct {
	Kind         string `json:"kind,omitempty"`
	Email        string `json:"email,omitempty"`
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
	Version    float64           `json:"version"`
	ID         string            `json:"id"`
	Steps      []pipelineDefStep `json:"steps"`
	ResultStep string            `json:"resultStep,omitempty"`
	raw        json.RawMessage
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
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Connection string          `json:"connection,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Mode       string          `json:"mode,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	StatusTool string          `json:"status_tool,omitempty"`
	RecentTool string          `json:"recent_tool,omitempty"`
	Profile    string          `json:"profile,omitempty"`
	Prompt     json.RawMessage `json:"prompt,omitempty"`
	Command    json.RawMessage `json:"command,omitempty"`
	TimeoutMs  json.RawMessage `json:"timeout_ms,omitempty"`
	Host       string          `json:"host,omitempty"`
	When       json.RawMessage `json:"when,omitempty"`
	raw        json.RawMessage
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

// Map adapters for the callers that still hold a run as its record (lab.go, lab_retry.go, lab_assessment.go,
// lab_pairwise.go, lab_variant.go, definitions.go): each converts once at the call. They go when those callers hold a
// *runRecord.

// runOfDoc: a run record held as a map as the typed run; its plan stays the map's (an edit through either is the other's).
func runOfDoc(d doc) (*runRecord, error) {
	plan := pipelinePlanOfRun(d)
	r, err := asT[runRecord](d)
	if err != nil {
		return nil, err
	}
	r.Plan = plan
	return &r, nil
}

// docOfRun: run as the map its callers read, sharing run's plan; nil for none.
func docOfRun(r *runRecord) doc {
	if r == nil {
		return nil
	}
	d := docOf(r)
	if r.Plan != nil {
		d["plan"] = r.Plan
	}
	return d
}

// getRun: loadRun as a map record.
func getRun(q *Request, id string) (doc, error) {
	r, err := loadRun(q, id)
	return docOfRun(r), err
}

// pipelineStart: startRun from the options as a map (lab.go; a draftDefinition and draftBase too); the run as a map
// record.
func pipelineStart(q *Request, o doc) (doc, error) {
	opts, err := asT[pipelineStartOptions](o)
	if err != nil {
		return nil, err
	}
	draft, err := asT[struct {
		Definition *pipelineDef `json:"draftDefinition"`
		Base       *int64       `json:"draftBase"`
	}](o)
	if err != nil {
		return nil, err
	}
	opts.draft = draft.Definition
	if draft.Base != nil {
		opts.draftBase, opts.hasDraftBase = *draft.Base, true
	}
	r, err := startRun(q, opts)
	return docOfRun(r), err
}

// pipelineOutputs: outputFiles of a map record.
func pipelineOutputs(q *Request, run doc, n int) ([]fileRef, error) {
	r, err := runOfDoc(run)
	if err != nil {
		return nil, err
	}
	return r.outputFiles(q, n)
}

// saveArtifact: an object of a run held as a map record (Lab judges).
func saveArtifact(q *Request, run doc, executionID, name string, b []byte, media string) (string, error) {
	r, err := runOfDoc(run)
	if err != nil {
		return "", err
	}
	return r.saveArtifact(q, executionID, name, b, media)
}

// succeededPrefix: runPrefix of a map record and definition; each reused step as its map record.
func succeededPrefix(q *Request, old, def doc) ([]any, error) {
	r, err := runOfDoc(old)
	if err != nil {
		return nil, err
	}
	d, err := asT[pipelineDef](def)
	if err != nil {
		return nil, err
	}
	steps, err := runPrefix(q, r, &d)
	out := []any{}
	for _, s := range steps {
		out = append(out, docOf(s))
	}
	return out, err
}

// stepRow: the step row id of a map record's step i.
func stepRow(run doc, i int) string {
	r, _ := runOfDoc(run)
	return r.row(i)
}

// pipelineOutputOwner: outputOwner of a map record.
func pipelineOutputOwner(q *Request, run doc, index int, aid string) error {
	r, err := runOfDoc(run)
	if err != nil {
		return err
	}
	return outputOwner(q, r, index, aid)
}

// pipelineRecoveryFence: recoveryFence of a map record.
func pipelineRecoveryFence(q *Request, run doc) error {
	r, err := runOfDoc(run)
	if err != nil {
		return err
	}
	return recoveryFence(q, r)
}

// pipelineResultValue: resultValue of a map record.
func pipelineResultValue(run doc) any {
	r, err := runOfDoc(run)
	if err != nil {
		return nil
	}
	return rawAny(r.resultValue())
}

// resolveRefs: resolveRefsRaw over decoded values (lab_retry.go).
func resolveRefs(v any, example any, outputs doc, quote bool) (any, error) {
	outs := map[string]json.RawMessage{}
	for k, o := range outputs {
		outs[k] = jsonBytes(o)
	}
	var ex json.RawMessage
	if example != nil {
		ex = jsonBytes(example)
	}
	out, err := resolveRefsRaw(jsonBytes(v), ex, outs, quote)
	if err != nil {
		return nil, err
	}
	return rawAny(out), nil
}

// validatePipeline: validatePipelineDef of a map (definitions.go); d itself when valid.
func validatePipeline(d doc) (doc, error) {
	if _, err := validatePipelineDef(jsonBytes(d)); err != nil {
		return nil, err
	}
	return d, nil
}

// loadPipeline: loadPipelineDef as a map.
func loadPipeline(q *Request, file string) (doc, error) {
	d, err := loadPipelineDef(q, file)
	if err != nil {
		return nil, err
	}
	return docOf(d.value()), nil
}

// problemOf: the execution problem for a failed call; code is the call's error code, "" when none.
func problemOf(outcome, c string) string {
	if outcome == "unknown" {
		if c == "" {
			c = "outcome"
		}
		return "unknown:" + c
	}
	if c != "" && infraCode.MatchString(c) {
		return "infra:" + c
	}
	if c == "" {
		c = "failed"
	}
	return "step:" + c
}

// validatePipelineDef: a pipeline definition's text checked field by field (pipelineRule) and decoded.
func validatePipelineDef(b []byte) (*pipelineDef, error) {
	bad := func(m string) (*pipelineDef, error) { return nil, fail(500, "Invalid pipeline: "+m) }
	d := pipelineRuleOf(b)
	var version float64
	if jsonKind(d["version"]) == 'n' || json.Unmarshal(d["version"], &version) != nil || version != 2 && version != 3 {
		return bad("version must be 2 or 3")
	}
	_, hasResultStep := d["resultStep"]
	if !ruleKeysOnly(d, "version", "id", "steps", "resultStep") || version == 2 && hasResultStep {
		return bad("root keys or resultStep version")
	}
	if id, _ := jsonString(d["id"]); !stepIDPattern.MatchString(id) {
		return bad("id")
	}
	steps := jsonList(d["steps"])
	if len(steps) == 0 {
		return bad("steps")
	}
	allowed := map[string][]string{"api": {"id", "kind", "connection", "tool", "arguments", "mode", "output", "status_tool", "recent_tool"}, "agent": {"id", "kind", "profile", "prompt"}, "script": {"id", "kind", "command", "timeout_ms", "host"}}
	seen := map[string]bool{}
	for _, x := range steps {
		s := pipelineRuleOf(x)
		id, _ := jsonString(s["id"])
		if s == nil || !stepIDPattern.MatchString(id) || seen[id] || id == "example" {
			return bad(fmt.Sprintf("step id %v", rawAny(s["id"])))
		}
		if when, has := s["when"]; has {
			if version != 3 {
				return bad("when requires version 3")
			}
			if err := validatePipelineCondition(when, seen); err != nil {
				return bad("step " + id + ": " + err.Error())
			}
		}
		seen[id] = true
		kind, _ := jsonString(s["kind"])
		keys, ok := allowed[kind]
		if !ok {
			return bad(fmt.Sprintf("step kind %v", rawAny(s["kind"])))
		}
		if version == 3 {
			keys = append(append([]string{}, keys...), "when")
		}
		for k := range s {
			if !ruleKeysOnly(pipelineRule{k: nil}, keys...) {
				return bad("step " + id + " key " + k)
			}
		}
		switch kind {
		case "api":
			_, isStr := jsonString(s["connection"])
			_, toolStr := jsonString(s["tool"])
			modeRaw, hasMode := s["mode"]
			mode, _ := jsonString(modeRaw)
			output, hasOutput := s["output"]
			if hasOutput && !onlyReplyRefs(output) {
				return bad("api step " + id + " output: only ${reply.<path>} references")
			}
			args := jsonKind(s["arguments"])
			if !isStr || !toolStr || args != '{' && args != '[' || hasMode && mode != "sync" && mode != "job" || hasOutput && (jsonKind(output) != '{' || mode == "job") {
				return bad("api step " + id)
			}
			for _, key := range []string{"status_tool", "recent_tool"} {
				if value, has := s[key]; has {
					name, ok := jsonString(value)
					if !ok || strings.TrimSpace(name) == "" || mode != "job" {
						return bad("api step " + id + " " + key + " requires job mode and a tool name")
					}
				}
			}
		case "agent":
			if _, ok := jsonString(s["profile"]); !ok {
				return bad("agent step " + id)
			}
			if _, ok := jsonString(s["prompt"]); !ok {
				return bad("agent step " + id)
			}
		case "script":
			t, has := s["timeout_ms"]
			var n float64
			isInt := jsonKind(t) != 'n' && json.Unmarshal(t, &n) == nil && n == float64(int64(n))
			if command, _ := jsonString(s["command"]); command == "" || has && !(isInt && n > 0) {
				return bad("script step " + id)
			}
		}
	}
	if version == 3 {
		last := pipelineRuleOf(steps[len(steps)-1])
		_, conditional := last["when"]
		resultStep, _ := jsonString(d["resultStep"])
		lastID, _ := jsonString(last["id"])
		lastKind, _ := jsonString(last["kind"])
		if resultStep != lastID || lastKind != "script" || conditional {
			return bad("resultStep must name the last unconditional script step")
		}
	}
	var def pipelineDef
	if err := json.Unmarshal(b, &def); err != nil {
		return bad("JSON")
	}
	return &def, nil
}

// loadPipelineDef: a pipeline definition's head; file is the catalogue's and runs' reference (<id>.json).
func loadPipelineDef(q *Request, file string) (*pipelineDef, error) {
	if !pipelineFile.MatchString(file) {
		return nil, fail(500, "Invalid pipeline reference")
	}
	raw, ok, err := headText(q, "pipeline", strings.TrimSuffix(file, ".json"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(404, "Unknown pipeline "+strings.TrimSuffix(file, ".json"))
	}
	if !json.Valid(raw) {
		return nil, fail(500, "Invalid pipeline: JSON")
	}
	return validatePipelineDef(raw)
}

type refError struct{ path string }

func (e *refError) Error() string { return "ref:" + e.path }

// shellQuote: a substituted value as one shell word (a string as it is, anything else as its JSON).
func shellQuote(raw json.RawMessage) string {
	s, ok := jsonString(raw)
	if !ok {
		s = string(jsonNorm(raw))
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// onlyReplyRefs: every reference in an output mapping names the reply.
func onlyReplyRefs(v json.RawMessage) bool {
	for _, m := range pipeRefPattern.FindAllStringSubmatch(string(jsonBytes(v)), -1) {
		if m[1] != "reply" && !strings.HasPrefix(m[1], "reply.") {
			return false
		}
	}
	return true
}

// resolveRefsRaw: ${example.<path>} and ${steps.<id>.output.<path>} (and ${reply.<path>} when outputs holds the reply
// under "", which is no step id) over a definition value v, walked as JSON (open values: arguments, prompts, commands).
// A whole-reference string takes the value as it is; inside a longer string the value is inlined as text. quote
// (scripts) applies to every substituted value. The result is normalized (jsonNorm).
func resolveRefsRaw(v, example json.RawMessage, outputs map[string]json.RawMessage, quote bool) (json.RawMessage, error) {
	out, err := resolveRefsIn(v, example, outputs, quote)
	if err != nil {
		return nil, err
	}
	return jsonNorm(out), nil
}

func resolveRefsIn(v, example json.RawMessage, outputs map[string]json.RawMessage, quote bool) (json.RawMessage, error) {
	look := func(path string) (json.RawMessage, error) {
		parts := strings.Split(path, ".")
		var cur json.RawMessage
		reply, hasReply := outputs[""]
		switch {
		case parts[0] == "example":
			cur, parts = example, parts[1:]
		case parts[0] == "reply" && hasReply:
			cur, parts = reply, parts[1:]
		case parts[0] == "steps" && len(parts) > 2 && parts[2] == "output":
			cur, parts = outputs[parts[1]], parts[3:]
		default:
			return nil, &refError{path}
		}
		for _, p := range parts {
			switch jsonKind(cur) {
			case '{':
				x, ok := pipelineRuleOf(cur)[p]
				if !ok {
					return nil, &refError{path}
				}
				cur = x
			case '[':
				l := jsonList(cur)
				var i int
				if _, err := fmt.Sscan(p, &i); err != nil || i < 0 || i >= len(l) || fmt.Sprint(i) != p {
					return nil, &refError{path}
				}
				cur = l[i]
			default:
				return nil, &refError{path}
			}
		}
		if isNull(cur) {
			if len(parts) == 0 && isNull(example) {
				return nil, &refError{path}
			}
			cur = json.RawMessage("null")
		}
		return cur, nil
	}
	switch jsonKind(v) {
	case '"':
		x, _ := jsonString(v)
		if m := wholeRef.FindStringSubmatch(x); m != nil && !quote {
			return look(m[1])
		}
		var ferr error
		out := pipeRefPattern.ReplaceAllStringFunc(x, func(s string) string {
			val, err := look(s[2 : len(s)-1])
			if err != nil {
				ferr = err
				return ""
			}
			if quote {
				return shellQuote(val)
			}
			if str, ok := jsonString(val); ok {
				return str
			}
			return string(jsonNorm(val))
		})
		return jsonBytes(out), ferr
	case '[':
		l := jsonList(v)
		out := make([]json.RawMessage, len(l))
		for i, e := range l {
			r, err := resolveRefsIn(e, example, outputs, quote)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return jsonBytes(out), nil
	case '{':
		m := pipelineRuleOf(v)
		out := make(pipelineRule, len(m))
		for k, e := range m {
			r, err := resolveRefsIn(e, example, outputs, quote)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return jsonBytes(out), nil
	}
	if len(v) == 0 {
		return json.RawMessage("null"), nil
	}
	return v, nil
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
	_, _, pipeID, pipeRev := definitionScalars(run.Definitions, nil)
	if pipeID == nil {
		pipeID = nilIf(run.PipelineID)
	}
	project, dir, taskID, sessID := agentOpt(run.project()), agentOpt(run.director()), agentOpt(firstString(run.DeliveryID, run.TaskID)), agentOpt(run.SessionID)
	pid, prev := strPtr(pipeID), nullInt(pipeRev)
	o := db.Observation(obs)
	inserted, err := executionInsert(q, db.ExecutionInsertParams{ID: run.ID, Kind: "pipeline_run", State: db.ExecutionState(state), Observation: &o, ProjectID: project,
		StartedAt: msColumn(run.StartedAt), EndedAt: msColumn(run.FinishedAt), Data: jsonBytes(rest), ConfigRef: jsonBytes(cfg), ExperimentID: agentOpt(run.ExperimentID),
		Variant: agentOpt(run.Variant), TaskID: taskID, SessionID: sessID, DirectedBy: dir, PipelineID: pid, PipelineRevision: prev, ParentExecutionID: agentOpt(run.ParentExecutionID)})
	if err != nil {
		return err
	}
	if !inserted {
		if err := q.db().PipelineRunUpdate(q.ctx(), db.PipelineRunUpdateParams{ID: run.ID, Observation: o, ProjectID: project,
			StartedAt: msColumn(run.StartedAt), EndedAt: msColumn(run.FinishedAt), Data: jsonBytes(rest), ConfigRef: jsonBytes(cfg), ExperimentID: agentOpt(run.ExperimentID),
			Variant: agentOpt(run.Variant), TaskID: taskID, SessionID: sessID, DirectedBy: dir, PipelineID: pid, PipelineRevision: prev,
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
			ParentExecutionID: &run.ID, StartedAt: msColumn(s.StartedAt), EndedAt: endedAt, Data: data, TaskID: taskID, SessionID: sessID, DirectedBy: dir, PipelineID: pid,
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

// loadRun: run id with its plan and steps; nil when there is none.
func loadRun(q *Request, id string) (*runRecord, error) {
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
	if q.person != nil {
		return pipelineRequester{Kind: "person", Email: q.person.Email}
	}
	return pipelineRequester{Kind: "operator"}
}

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

// pipelineCallerRow: the execution a pipeline is started from (lineage is server-derived, never a parameter).
type pipelineSessionOwner struct {
	ProjectID *string `json:"projectId"`
	TaskID    *string `json:"taskId"`
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
			session, e := kindGet[pipelineSessionOwner](q, *parent.SessionID, "session")
			if e != nil {
				return nil, e
			}
			if deref(session.ProjectID) != run.project() || session.TaskID != nil && deref(session.TaskID) != deref(parent.TaskID) {
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
		old, err := loadRun(q, o.RerunOf)
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
		if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { r, err = loadRun(q, runID); return }); err != nil {
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
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
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
	run, err := loadRun(q, runID)
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

func (run *runRecord) saveArtifact(q *Request, executionID, name string, b []byte, media string) (string, error) {
	return artifactSave(q, derefAny(run.ProjectID), run.scope(), executionID, name, b, media)
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

// fileRef: a staged file: its artifact and its path under the step's inputs.
type fileRef struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
}

func (f fileRef) doc() doc { return docOf(f) }

// artifactsOf: stored artifacts of an execution {id, path, sha256}.
func artifactsOf(q *Request, executionID string) ([]db.PipelineStoredArtifactsRow, error) {
	return q.db().PipelineStoredArtifacts(q.ctx(), &executionID)
}

// pipelineOutputIdentity rejects even a readable artifact owned by another step/project.
func pipelineOutputIdentity(x *Artifact, project any, owner string) bool {
	return x.State == "stored" && same(x.ProjectID, project) && x.ExecutionID != nil && *x.ExecutionID == owner && x.Name == "output.json" && labSha.MatchString(x.ContentHash)
}

// outputOwner follows immediate reuse lineage to the original step; readable foreign artifacts are not outputs.
func outputOwner(q *Request, run *runRecord, index int, aid string) error {
	seen := map[string]bool{}
	current := run
	for {
		id := current.ID
		if id == "" || seen[id] || index >= len(current.Steps) {
			return fail(409, "Invalid reused output lineage")
		}
		seen[id] = true
		step := current.Steps[index]
		if index >= len(run.Steps) || step.ID != run.Steps[index].ID || step.OutputArtifactID != aid {
			return fail(409, "Changed reused output identity")
		}
		previous := step.ReusedFrom
		if previous == "" {
			x, err := artifactRow(withTx(q), q.app, aid)
			if err != nil {
				return err
			}
			if !pipelineOutputIdentity(x, derefAny(run.ProjectID), current.row(index)) {
				return fail(409, "Invalid output artifact ownership")
			}
			return nil
		}
		var err error
		current, err = loadRun(q, previous)
		if err != nil {
			return err
		}
		if current == nil || current.project() != run.project() || (current.ProjectID == nil) != (run.ProjectID == nil) {
			return fail(409, "Missing reused output source")
		}
	}
}

// outputFiles: files of the first n steps under <step id>/ (n<0: every step). A step's own files win over the
// coordinator's output.json for it; dotfiles are left out.
func (run *runRecord) outputFiles(q *Request, n int) ([]fileRef, error) {
	steps := run.Steps
	if n < 0 {
		n = len(steps)
	}
	out := []fileRef{}
	for j := 0; j < n && j < len(steps); j++ {
		s := &steps[j]
		entry := run.entry(j)
		if entry == nil || entry.Activation != contract.PipelineActivationSelected || s.State != "succeeded" || s.unconfirmed() {
			continue
		}
		ids := []string{refExecutionID(entry)}
		if e := s.ExecutionID; e != "" {
			ids = append(ids, e)
		}
		order, files := []string{}, map[string]string{}
		if aid := s.OutputArtifactID; aid != "" {
			if err := outputOwner(q, run, j, aid); err != nil {
				return nil, err
			}
			k := s.ID + "/output.json"
			order, files[k] = append(order, k), aid
		}
		for _, id := range ids {
			as, err := artifactsOf(q, id)
			if err != nil {
				return nil, err
			}
			for _, a := range as {
				rel := strings.TrimPrefix(a.Path, "output/")
				if pipelineReservedInput(rel) {
					continue // server control is a consumer input, never a producer output
				}
				dot := false
				for _, p := range strings.Split(rel, "/") {
					dot = dot || strings.HasPrefix(p, ".")
				}
				if dot {
					continue
				}
				k := s.ID + "/" + rel
				if _, ok := files[k]; !ok {
					order = append(order, k)
				}
				files[k] = a.ID
			}
		}
		for _, k := range order {
			out = append(out, fileRef{files[k], k})
		}
	}
	return out, nil
}

// inputs: the files step i is staged with (the example's inputs, then the earlier steps' outputs).
func (run *runRecord) inputs(q *Request, i int) ([]fileRef, error) {
	if len(run.exampleInputs()) > 0 && run.ExampleInputs == nil {
		return nil, fail(409, "Pipeline example inputs must be materialized before preparation")
	}
	out := []fileRef{}
	if run.ExampleInputs != nil {
		out = append(out, *run.ExampleInputs...)
	}
	fs, err := run.outputFiles(q, i)
	if err != nil {
		return nil, err
	}
	return append(out, fs...), nil
}

// pipelineHostJob: a script step's host job (putExecution writes it), the step's child in the run's project (session)
// and experiment.
type pipelineHostJob struct {
	ID              string          `json:"id"`
	ExecutionID     string          `json:"executionId"`
	SessionID       string          `json:"sessionId"`
	HostID          string          `json:"hostId"`
	State           string          `json:"state"`
	CreatedAt       int64           `json:"createdAt"`
	Actor           string          `json:"actor"`
	PipelineStepID  string          `json:"pipelineStepId"`
	PipelineRunID   string          `json:"pipelineRunId"`
	ProjectSnapshot json.RawMessage `json:"projectSnapshot"`
	DirectedBy      string          `json:"directedBy,omitempty"`
	ExperimentID    string          `json:"experimentId,omitempty"`
}

// pipelineScriptCommand: the script command of a host job, as the host gets it.
type pipelineScriptCommand struct {
	ID          string                `json:"id"`
	ExecutionID string                `json:"executionId"`
	SessionID   string                `json:"sessionId"`
	HostID      string                `json:"hostId"`
	Kind        string                `json:"kind"`
	Payload     pipelineScriptPayload `json:"payload"`
	Status      string                `json:"status"`
	HandedOut   bool                  `json:"handedOut"`
}

type pipelineScriptPayload struct {
	Command   string          `json:"command"`
	Inputs    []fileRef       `json:"inputs"`
	TimeoutMs json.RawMessage `json:"timeout_ms,omitempty"`
}

// startScript: a script command for the executor, as its own session and execution.
func (run *runRecord) startScript(q *Request, stepExecutionID, command string, timeout json.RawMessage, hostID string, inputs []fileRef) (string, error) {
	sid := newID()
	dir := run.director()
	// A host command session like the others (actor program-worker): it holds the step's host job, not a conversation.
	s := agentSession{ID: sid, CreatedAt: nowMs(), PipelineRunID: run.ID, Actor: "program-worker", DirectedBy: dir, ProjectID: run.project()}
	if err := agentPut(q, "session", &s); err != nil {
		return "", err
	}
	if hostID == "" {
		hostID = q.app.hostID
	}
	id := newID()
	e := pipelineHostJob{ID: id, ExecutionID: id, SessionID: sid, HostID: hostID, State: "preparing", CreatedAt: nowMs(), Actor: "program-worker", PipelineStepID: stepExecutionID,
		PipelineRunID: run.ID, ProjectSnapshot: orNull(run.ProjectSnapshot), DirectedBy: dir, ExperimentID: run.ExperimentID}
	if err := putExecution(q, docOf(e)); err != nil { // a host job, the step's child in the run's project (session) and experiment
		return "", err
	}
	if inputs == nil {
		inputs = []fileRef{}
	}
	payload := pipelineScriptPayload{Command: command, Inputs: inputs}
	if !isNull(timeout) {
		payload.TimeoutMs = timeout
	}
	return id, kindPut(q, "command", pipelineScriptCommand{ID: newID(), ExecutionID: id, SessionID: sid, HostID: hostID, Kind: "script", Payload: payload, Status: "pending"})
}

// pipelineClaimCall claims a provider call of a pipeline api step in q's transaction (claimPipelineAPI: a submit is handed
// off with the step, an observation of the job mints its own token): the token, or the line that answers the call
// without it. The token is revoked by the step that records the call.
func pipelineClaimCall(q *Request, run *runRecord, row, connection, tool string) (string, bool, *contract.CallLine) {
	unavailable := &contract.CallLine{Tool: tool, Connection: connection, Outcome: contract.CallOutcomeError, ErrorCode: ptr("connection-unavailable")}
	if gatewayURL() == "" || !connName.MatchString(connection) {
		return "", false, unavailable
	}
	if err := exec(q, "SAVEPOINT pipeline_claim"); err != nil {
		unavailable.ErrorCode = ptr("pipeline-authority-denied")
		return "", false, unavailable
	}
	claimed, token, won, idempotent, err := claimPipelineAPI(q, run.ID, row, connection, tool)
	if err != nil {
		if e := exec(q, "ROLLBACK TO SAVEPOINT pipeline_claim"); e != nil {
			unavailable.ErrorCode = ptr("pipeline-authority-denied")
			return "", false, unavailable
		}
		unavailable.ErrorCode = ptr("pipeline-authority-denied")
		if errors.Is(err, errFileTool) {
			unavailable.ErrorCode = ptr("file-tool-unsupported")
		}
		return "", false, unavailable
	}
	if err := exec(q, "RELEASE SAVEPOINT pipeline_claim"); err != nil {
		return "", false, unavailable
	}
	if !won || token == "" {
		unavailable.ErrorCode = ptr(pipelineClaimObserved)
		return "", false, unavailable
	}
	// The claim wrote the run: the caller's record moves to its plan revision and dispatch.
	run.Plan = claimed.Plan
	return token, idempotent, nil
}

// pipelineGateway: one provider call through the gateway with a claimed token, outside any transaction; the gateway's
// spool line becomes the step's tool_run (ingested here, so the row exists when the step records the call).
func pipelineGateway(ctx context.Context, a *App, row, token, connection, tool string, args any, idempotent bool) contract.CallLine {
	writeSnapshot(ctx, a) // the gateway must see the token before the call
	line := gatewayCall(ctx, token, connection, tool, args, idempotent)
	// Revoked at once in its own write, even when the caller left or the record fails: a live token would keep the key
	// reachable. The recording step revokes again (idempotent), and a run's end revokes every token of its steps.
	if err := runTx(context.WithoutCancel(ctx), a, nil, true, "", func(q *Request) error { return revokeTokens(q, row) }); err != nil {
		log.Printf("revoke %s: %v", row, err)
	}
	if _, err := connIngestPass(context.WithoutCancel(ctx), a); err != nil {
		log.Printf("ingest %s: %v", connection, err)
	}
	return line
}

// pipelineToolRun: the tool_run of a finished job (insertExecution writes it).
type pipelineToolRun struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	State             string          `json:"state"`
	ProjectID         *string         `json:"projectId"`
	ParentExecutionID string          `json:"parentExecutionId"`
	ExperimentID      *string         `json:"experimentId"`
	Variant           *string         `json:"variant"`
	TaskID            *string         `json:"taskId"`
	SessionID         *string         `json:"sessionId"`
	DirectedBy        *string         `json:"directedBy"`
	PipelineID        *string         `json:"pipelineId"`
	PipelineRevision  *int64          `json:"pipelineRevision"`
	StepID            *string         `json:"stepId"`
	StartedAt         int64           `json:"startedAt"`
	EndedAt           int64           `json:"endedAt"`
	Data              pipelineJobCall `json:"data"`
}

type pipelineJobCall struct {
	Tool       string          `json:"tool"`
	RequestID  string          `json:"requestId"`
	Outcome    string          `json:"outcome"`
	ErrorCode  *string         `json:"errorCode"`
	Usage      json.RawMessage `json:"usage"`
	Connection string          `json:"connection"`
}

type pipelineCost struct {
	Unit       string  `json:"unit"`
	Credits    float64 `json:"credits"`
	Connection string  `json:"connection"`
}

// recordJob: a finished job's usage as one tool_run child of the step.
func (run *runRecord) recordJob(q *Request, stepID, tool, jobID, connection string, usage json.RawMessage) error {
	id := stepID + ":job:0"
	lineage, err := q.db().PipelineStepLineage(q.ctx(), stepID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	dir := agentOpt(firstString(deref(lineage.DirectedBy), run.director()))
	taskID := lineage.TaskID
	if taskID == nil {
		taskID = agentOpt(firstString(run.DeliveryID, run.TaskID))
	}
	jobNow := nowMs()
	if _, err := insertExecution(q, docOf(pipelineToolRun{
		ID: id, Kind: "tool_run", State: "succeeded", ProjectID: run.ProjectID, ParentExecutionID: stepID,
		ExperimentID: agentOpt(run.ExperimentID), Variant: agentOpt(run.Variant),
		TaskID: taskID, SessionID: lineage.SessionID, DirectedBy: dir,
		PipelineID: lineage.PipelineID, PipelineRevision: lineage.PipelineRevision, StepID: lineage.StepID,
		StartedAt: jobNow, EndedAt: jobNow,
		Data: pipelineJobCall{Tool: tool, RequestID: jobID, Outcome: "ok", Usage: usage, Connection: connection},
	})); err != nil {
		return err
	}
	var cost any
	var u struct {
		Credits *float64 `json:"credits"`
	}
	if lenientDecode(usage, &u) == nil && u.Credits != nil {
		cost = pipelineCost{Unit: "credits", Credits: *u.Credits, Connection: connection}
	}
	return writeConnectionCost(q, id, cost)
}

// creditsOf: an execution's measured provider credits as a step's usage; none without calls.
func creditsOf(q *Request, executionID string) (json.RawMessage, error) {
	r, err := q.db().PipelineStepCredits(q.ctx(), &executionID)
	if err != nil || r.Calls == 0 {
		return nil, err
	}
	return jsonBytes(pipelineCredits{Credits: r.Credits}), nil
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
	run, err := loadRun(q, c.Key)
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

// pipelineNative: a step's own execution (an agent run or host job) as the runner reads it.
type pipelineNative struct {
	State       string          `json:"state"`
	Observation string          `json:"observation"`
	Error       json.RawMessage `json:"error"`
}

// pipelineStepAt moves step i of a running run: a preparing step leaves its inputs to materialize; a running api step
// fails as interrupted (sync) or leaves its job poll; a running agent or script step waits for its execution, then leaves
// its output to read.
func pipelineStepAt(q *Request, now time.Time, c *kitClaim, run *runRecord, i int) error {
	a := q.app
	s, def := &run.Steps[i], run.def(i)
	row, kind, runID := run.row(i), def.Kind, run.ID
	// A run keeps the definition it started with: a step of a kind no longer run (publication) fails, never runs as a
	// script; a job it had started under the step (the coordinator's own publication job) ends with it.
	if kind != "api" && kind != "agent" && kind != "script" {
		jobs, err := q.db().PipelineOpenJobs(q.ctx(), &row)
		if err != nil {
			return err
		}
		problem := "step:unknown-kind"
		for _, j := range jobs {
			if _, err := executionTransition(q, j, executionEvent{To: "failed", Problem: &problem}); err != nil {
				return err
			}
		}
		return run.failStep(q, i, "step:unknown-kind", "Step kind "+kind+" is no longer run")
	}
	if s.State == "preparing" {
		snapshot, revision := run, run.revision()
		c.Call = func(ctx context.Context) any {
			prepared, err := materializePipelineInputs(ctx, a, snapshot, i)
			return pipelineRecord{revision: revision, apply: func(q *Request, run *runRecord) error { return pipelinePrepared(q, c, run, i, prepared, err) }}
		}
		return nil
	}
	if kind != "api" && s.ExecutionID == "" {
		return run.failStep(q, i, "unknown:start", "Coordinator stopped while starting the step; not replayed")
	}
	if kind == "api" && dispatchPhase(run.entry(i)) == contract.PipelineDispatchPhaseReserved {
		// No committed handoff exists. Resume preparation with the same frozen
		// bindings and logical step; this is not replay of a provider call.
		s.State = "preparing"
		if err := run.put(q); err != nil {
			return err
		}
		return pipelineSoon(q, runID)
	}
	if kind == "api" && def.Mode != "job" {
		// Handed off and not answered: the call's record was lost (a restart, a failed record). Never sent again.
		return run.failStep(q, i, "unknown:interrupted", "Coordinator restarted during the call; outcome unknown, not replayed")
	}
	if kind == "api" {
		connection, tool := def.Connection, def.jobTool("job_status")
		token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
		snapshot, revision, jobID := run, run.revision(), s.JobID
		c.Call = func(ctx context.Context) any {
			line := refused
			if line == nil {
				l := pipelineGateway(ctx, a, row, token, connection, tool, struct {
					JobID string `json:"jobId"`
				}{jobID}, idempotent)
				line = &l
			}
			out, artifact, err := pipelinePolled(ctx, a, snapshot, i, *line)
			return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run *runRecord) error {
				return pipelinePollRecord(q, now, run, i, *line, out, artifact, err)
			}}
		}
		return nil
	}
	// Agent or script step running: wait for its execution.
	e, err := kindMaybe[pipelineNative](q, s.ExecutionID, "execution")
	if err != nil {
		return err
	}
	if e == nil || !executionEnds(e.State) {
		return pipelineLater(q, runID) // an unconfirmed running state is not final: the host may still report the real outcome
	}
	if st := e.State; st != "succeeded" {
		problem, why := "step:cancelled", pipelineRuleOf(e.Error)
		if st != "cancelled" {
			code := "failed"
			if !isNull(why["code"]) {
				code = fmt.Sprint(rawAny(why["code"]))
			}
			problem = "step:" + code
			if e.Observation == "unconfirmed" { // the host lost the step: its outcome is unknown
				problem = "unknown:" + code
			}
		}
		msg, _ := jsonString(why["message"])
		if msg == "" {
			msg = "Step execution " + st
		}
		return run.failStep(q, i, problem, msg)
	}
	pending, err := q.db().PipelinePendingArtifacts(q.ctx(), &s.ExecutionID)
	if err != nil {
		return err
	}
	if pending > 0 {
		return pipelineLater(q, runID) // uploads can finish after the completion receipt
	}
	arts, err := artifactsOf(q, s.ExecutionID)
	if err != nil {
		return err
	}
	snapshot, revision := run, run.revision()
	c.Call = func(ctx context.Context) any {
		output, artifact, err := pipelineAgentOutput(ctx, a, snapshot, i, arts)
		return pipelineRecord{revision: revision, apply: func(q *Request, run *runRecord) error {
			if err != nil {
				return err
			}
			step := &run.Steps[i]
			step.OutputArtifactID = artifact
			u, err := creditsOf(q, step.ExecutionID)
			if err != nil {
				return err
			}
			if err := run.completeStep(q, i, output, u); err != nil {
				return err
			}
			return pipelineSoon(q, run.ID)
		}}
	}
	return nil
}

// pipelineAgentOutput: a finished agent or script step's output (its output.json, else its file list), saved as the
// step's output object. Outside any transaction.
func pipelineAgentOutput(ctx context.Context, a *App, run *runRecord, i int, arts []db.PipelineStoredArtifactsRow) (json.RawMessage, string, error) {
	if _, err := connIngestPass(ctx, a); err != nil {
		return nil, "", err
	}
	var output json.RawMessage
	for _, art := range arts {
		if ownOutput.MatchString(art.Path) {
			if b, err := artifactBytes(ctx, a, art.ID, 64<<20); err == nil {
				var v any
				if json.Unmarshal(b, &v) == nil && v != nil {
					output = jsonBytes(v)
				}
			}
			break
		}
	}
	if output == nil {
		files := []string{}
		for _, art := range arts {
			p := strings.TrimPrefix(art.Path, "output/")
			dot := false
			for _, x := range strings.Split(p, "/") {
				dot = dot || strings.HasPrefix(x, ".")
			}
			if !dot {
				files = append(files, p)
			}
		}
		output = jsonBytes(struct {
			Files []string `json:"files"`
		}{files})
	}
	if err := materializePipelineOutput(ctx, a, run, i, output); err != nil {
		return nil, "", err
	}
	return output, run.Steps[i].OutputArtifactID, nil
}

// pipelinePrepared records a preparing step's materialized inputs and starts it: an api step is handed off and leaves
// its call; an agent or script step starts its execution (a start that fails fails the step from its preparing state).
func pipelinePrepared(q *Request, c *kitClaim, run *runRecord, i int, prepared *pipelinePreparedInputs, inputsErr error) error {
	s, def := &run.Steps[i], run.def(i)
	kind, connection, row := def.Kind, def.Connection, run.row(i)
	if inputsErr != nil {
		return run.failStep(q, i, "infra:inputs", inputsErr.Error())
	}
	outputs := run.outputs(i)
	freezePipelineInputs(run, i, prepared)
	var resolved json.RawMessage
	var err error
	switch kind {
	case "api":
		resolved, err = resolveRefsRaw(def.withArguments(&run.Overrides), run.Example, outputs, false)
	case "agent":
		p := def.Prompt
		if !isNull(run.Overrides.Prompt) {
			p = run.Overrides.Prompt
		}
		resolved, err = resolveRefsRaw(p, run.Example, outputs, false)
	default:
		resolved, err = resolveRefsRaw(def.Command, run.Example, outputs, true)
	}
	if re, ok := err.(*refError); ok {
		now := nowMs()
		s.StartedAt = &now
		return run.failStep(q, i, "ref:"+re.path, "Missing reference "+re.path)
	} else if err != nil {
		return err
	}
	started := nowMs()
	s.State, s.StartedAt = "running", &started
	if kind == "api" {
		var submitTool *contract.GatewayTool
		if def.Mode == "job" {
			conn, err := connNamed(q, connection)
			if err != nil {
				return run.failStep(q, i, "step:job-tools", err.Error())
			}
			tools := gatewayProviders[conn.Provider].Tools
			if t, ok := tools[def.Tool]; ok {
				submitTool = &t
			}
			for _, name := range []string{def.jobTool("job_status"), def.jobTool("recent_tasks")} {
				tool, ok := tools[name]
				granted := false
				for _, t := range conn.Tools {
					granted = granted || t == name
				}
				if !ok || tool.Method != "GET" || !tool.Idempotent || !granted {
					return run.failStep(q, i, "step:job-tools", fmt.Sprintf("job read tool %s must be a granted idempotent GET", name))
				}
			}
		}
		s.Arguments = resolved
		if err := run.put(q); err != nil {
			return err
		}
		return pipelineSubmit(q, c, run, i, resolved, submitTool)
	}
	// The start runs under a savepoint: one that fails leaves nothing of it, and the step fails from here.
	revision, dispatchBefore := run.revision(), run.entry(i).Dispatch
	if dispatchBefore != nil {
		before := *dispatchBefore
		dispatchBefore = &before
	}
	if err := exec(q, "SAVEPOINT pipeline_start"); err != nil {
		return err
	}
	text, _ := jsonString(resolved)
	err = func() error {
		if err := run.put(q); err != nil {
			return err
		}
		var execID string
		var err error
		if kind == "agent" {
			execID, err = startAgentStep(q, run, def, row, text, prepared.files)
		} else {
			host := def.Host
			if h := run.Overrides.host(); h != "" {
				host = h
			}
			execID, err = run.startScript(q, row, text, def.TimeoutMs, host, prepared.files)
		}
		if err != nil {
			return err
		}
		s.ExecutionID = execID
		d := run.entry(i).Dispatch
		d.NativeExecutionId = &execID
		if kind == "script" {
			cmd, err := q.db().PipelineScriptCommand(q.ctx(), &execID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(409, "Pipeline script command was not reserved")
			}
			if err != nil {
				return err
			}
			d.Phase, d.CommandId, d.HostId = contract.PipelineDispatchPhasePrepared, &cmd.ID, nil
			if cmd.HostID != "" {
				d.HostId = &cmd.HostID
			}
		}
		return run.put(q)
	}()
	if err == nil {
		return exec(q, "RELEASE SAVEPOINT pipeline_start") // the step waits for its execution's end, which wakes the run
	}
	if e := exec(q, "ROLLBACK TO SAVEPOINT pipeline_start"); e != nil {
		return e
	}
	problem := "step:start"
	if he, ok := err.(*HTTPError); ok && (he.Code == 409 || he.Code == 503) || strings.Contains(strings.ToLower(err.Error()), "reachable store") {
		problem = "infra:start"
	}
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	s.ExecutionID = ""
	run.Plan.Revision = revision
	run.entry(i).Dispatch = dispatchBefore
	return run.failStep(q, i, problem, msg)
}

// pipelineSubmit hands an api step's call off (claimPipelineAPI, committed with this step) and leaves the call.
func pipelineSubmit(q *Request, c *kitClaim, run *runRecord, i int, resolved json.RawMessage, submitTool *contract.GatewayTool) error {
	a := q.app
	def := run.def(i)
	connection, tool, row := def.Connection, def.Tool, run.row(i)
	token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
	if refused != nil && deref(refused.ErrorCode) == pipelineClaimObserved {
		return pipelineSoon(q, run.ID) // another owner holds the call
	}
	if refused != nil {
		return pipelineSubmitted(q, c, run, i, *refused, resolved, submitTool, nil)
	}
	snapshot, revision := run, run.revision()
	c.Call = func(ctx context.Context) any {
		line := pipelineGateway(ctx, a, row, token, connection, tool, resolved, idempotent)
		out := pipelineSyncOutput(ctx, a, snapshot, i, line)
		return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run *runRecord) error {
			return pipelineSubmitted(q, c, run, i, line, resolved, submitTool, out)
		}}
	}
	return nil
}

// pipelineSyncResult: a sync api step's answer read outside the transaction: its output (the mapping over the reply),
// its saved output object, or why neither.
type pipelineSyncResult struct {
	output, usage, reply json.RawMessage
	artifact             string
	mapErr, saveErr      error
}

func pipelineSyncOutput(ctx context.Context, a *App, run *runRecord, i int, line contract.CallLine) *pipelineSyncResult {
	def := run.def(i)
	if line.Outcome != contract.CallOutcomeOk || def.Mode == "job" {
		return nil
	}
	out := json.RawMessage(jsonBytes(callOutput(line)))
	r := &pipelineSyncResult{output: out, usage: pipelineRuleOf(out)["usage"]}
	if m := def.Output; len(m) > 0 {
		// The step's output is the mapping over the reply (${reply.<path>}); the reply's usage stays the step's usage.
		mapped, err := resolveRefsRaw(m, nil, map[string]json.RawMessage{"": out}, false)
		if err != nil {
			r.reply, r.mapErr = out, err // the paid answer stays inspectable on the failed step
			return r
		}
		r.output = mapped
	}
	r.saveErr = materializePipelineOutput(ctx, a, run, i, r.output)
	r.artifact = run.Steps[i].OutputArtifactID
	return r
}

// pipelineSubmitted records an api step's call: a job submit without a clear answer leaves a listing of recent tasks
// (one created since the call started is adopted; otherwise unknown, never resent).
func pipelineSubmitted(q *Request, c *kitClaim, run *runRecord, i int, line contract.CallLine, resolved json.RawMessage, submitTool *contract.GatewayTool, out *pipelineSyncResult) error {
	a := q.app
	s, def := &run.Steps[i], run.def(i)
	connection, row := def.Connection, run.row(i)
	job := def.Mode == "job"
	code := deref(line.ErrorCode)
	if job && line.Outcome != contract.CallOutcomeOk && (line.Outcome == contract.CallOutcomeUnknown || http5xx.MatchString(code)) {
		tool := def.jobTool("recent_tasks")
		token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
		revision := run.revision()
		c.Call = func(ctx context.Context) any {
			recent := refused
			if recent == nil {
				l := pipelineGateway(ctx, a, row, token, connection, tool, struct{}{}, idempotent)
				recent = &l
			}
			return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run *runRecord) error {
				return pipelineAdopt(q, run, i, code, *recent, resolved, submitTool)
			}}
		}
		return nil
	}
	if line.Outcome != contract.CallOutcomeOk {
		return run.failStep(q, i, problemOf(string(line.Outcome), code), fmt.Sprintf("Connection call %s: %s", line.Outcome, code))
	}
	if job {
		return pipelineJobStarted(q, run, i, line)
	}
	if out.mapErr != nil {
		s.Reply = out.reply
		return run.failStep(q, i, out.mapErr.Error(), "Output mapping: a reference to the reply is missing")
	}
	if out.saveErr != nil {
		return out.saveErr
	}
	s.OutputArtifactID = out.artifact
	if err := run.completeStep(q, i, out.output, out.usage); err != nil {
		return err
	}
	return pipelineSoon(q, run.ID)
}

// pipelineAdopt: after a job submit without a clear answer, the one recent task created since the call started that no
// step owns and that matches the request is the step's job; otherwise the submit is unknown and never sent again.
func pipelineAdopt(q *Request, run *runRecord, i int, code string, recent contract.CallLine, resolved json.RawMessage, submitTool *contract.GatewayTool) error {
	s := &run.Steps[i]
	listed := json.RawMessage(jsonBytes(callOutput(recent)))
	tasks := jsonList(listed)
	if tasks == nil {
		tasks = jsonList(pipelineRuleOf(listed)["result"])
	}
	owned := map[string]bool{}
	rs, err := q.db().PipelineOwnedJobIDs(q.ctx())
	if err != nil {
		return err
	}
	for _, r := range rs {
		owned[r] = true
	}
	var started float64
	if s.StartedAt != nil {
		started = float64(*s.StartedAt)
	}
	mine := []string{}
	if recent.Outcome == contract.CallOutcomeOk {
		for _, x := range tasks {
			t := pipelineRuleOf(x)
			id, ok := jsonString(t["id"])
			var created float64
			if jsonKind(t["created_at"]) != '"' {
				json.Unmarshal(t["created_at"], &created)
			}
			if ok && id != "" && jobCreatedDuring(created, started, float64(nowMs())) && !owned[id] && jobMatchesRaw(t, pipelineRuleOf(resolved), submitTool) {
				mine = append(mine, id)
			}
		}
	}
	if len(mine) != 1 {
		why := fmt.Sprintf("%d matching tasks", len(mine))
		if recent.Outcome != contract.CallOutcomeOk {
			why = "recent tasks unreadable"
		}
		return run.failStep(q, i, "unknown:submit-unconfirmed", fmt.Sprintf("Job submit %s; %s; not sent again", code, why))
	}
	s.AdoptedAfter = code
	return pipelineJobStarted(q, run, i, contract.CallLine{Outcome: contract.CallOutcomeOk, Output: jsonBytes(struct {
		JobID string `json:"jobId"`
	}{mine[0]})})
}

// pipelineJobStarted: a job submit's answer names the job the step polls from now on.
func pipelineJobStarted(q *Request, run *runRecord, i int, line contract.CallLine) error {
	s := &run.Steps[i]
	out := pipelineRuleOf(jsonBytes(callOutput(line)))
	jobID, ok := jsonString(out["jobId"])
	if !ok {
		jobID, ok = jsonString(out["result"])
	}
	if !ok {
		return run.failStep(q, i, "step:no-job-id", "Job submit returned no jobId")
	}
	var polls int64
	s.JobID, s.Polls = jobID, &polls
	if err := run.put(q); err != nil {
		return err
	}
	if err := q.db().PipelineStepJobSet(q.ctx(), db.PipelineStepJobSetParams{RunID: run.ID, StepIndex: int32(i), JobID: jobID}); err != nil {
		return err
	}
	return pipelineLater(q, run.ID)
}

// pipelinePoll: a job poll's answer: the job's state, its output and usage, and its error.
type pipelinePoll struct {
	State  string          `json:"state"`
	Output json.RawMessage `json:"output"`
	Usage  json.RawMessage `json:"usage"`
	Error  json.RawMessage `json:"error"`
}

// meshyTask: Meshy's task shape (status SUCCEEDED | FAILED | CANCELED | EXPIRED | PENDING | IN_PROGRESS).
type meshyTask struct {
	Status          *string         `json:"status"`
	State           json.RawMessage `json:"state"`
	Result          json.RawMessage `json:"result"`
	ModelURLs       json.RawMessage `json:"model_urls"`
	ThumbnailURL    json.RawMessage `json:"thumbnail_url"`
	ImageURLs       json.RawMessage `json:"image_urls"`
	ConsumedCredits json.RawMessage `json:"consumed_credits"`
	TaskError       struct {
		Message json.RawMessage `json:"message"`
	} `json:"task_error"`
}

// meshyOutput: a Meshy job's output as the step keeps it.
type meshyOutput struct {
	JobID        string          `json:"jobId"`
	Request      json.RawMessage `json:"request"`
	Result       json.RawMessage `json:"result"`
	ModelURLs    json.RawMessage `json:"model_urls"`
	ThumbnailURL json.RawMessage `json:"thumbnail_url"`
	ImageURLs    json.RawMessage `json:"image_urls"`
	Receipt      json.RawMessage `json:"receipt"`
}

// pipelinePolled reads a job poll's answer outside the transaction: the job's state, its output (Meshy's task shape
// mapped) and, once it succeeded, its saved output object.
func pipelinePolled(ctx context.Context, a *App, run *runRecord, i int, line contract.CallLine) (*pipelinePoll, string, error) {
	if line.Outcome != contract.CallOutcomeOk {
		return nil, "", nil
	}
	s := &run.Steps[i]
	raw := json.RawMessage(jsonBytes(callOutput(line)))
	out := &pipelinePoll{}
	lenientDecode(raw, out)
	if jsonKind(raw) != '{' {
		out = &pipelinePoll{}
	}
	var m meshyTask
	lenientDecode(raw, &m)
	if jsonKind(raw) == '{' && m.Status != nil && isNull(m.State) {
		state := "running"
		if *m.Status == "SUCCEEDED" {
			state = "succeeded"
		} else if *m.Status == "FAILED" || *m.Status == "CANCELED" || *m.Status == "EXPIRED" {
			state = "failed"
		}
		urls := m.ModelURLs
		if isNull(urls) {
			urls = json.RawMessage("{}")
		}
		var usage json.RawMessage
		if jsonKind(m.ConsumedCredits) != '"' && !isNull(m.ConsumedCredits) {
			var c float64
			if json.Unmarshal(m.ConsumedCredits, &c) == nil {
				usage = jsonBytes(pipelineCredits{Credits: c})
			}
		}
		out = &pipelinePoll{State: state, Output: jsonBytes(meshyOutput{JobID: s.JobID, Request: orNull(s.Arguments), Result: orNull(m.Result), ModelURLs: urls,
			ThumbnailURL: orNull(m.ThumbnailURL), ImageURLs: orNull(m.ImageURLs), Receipt: raw}), Usage: usage, Error: m.TaskError.Message}
	}
	if out.State != "succeeded" {
		return out, "", nil
	}
	if isNull(out.Output) {
		out.Output = json.RawMessage("{}")
	}
	if err := materializePipelineOutput(ctx, a, run, i, out.Output); err != nil {
		return out, "", err
	}
	return out, s.OutputArtifactID, nil
}

// pipelinePollRecord records one job poll: a succeeded job completes the step with its usage, a failed one fails it,
// anything else (an unknown poll included) polls again later.
func pipelinePollRecord(q *Request, now time.Time, run *runRecord, i int, line contract.CallLine, out *pipelinePoll, artifact string, saveErr error) error {
	s, def := &run.Steps[i], run.def(i)
	runID, row := run.ID, run.row(i)
	if line.Outcome != contract.CallOutcomeOk {
		if line.Outcome == contract.CallOutcomeUnknown {
			return pipelineLater(q, runID)
		}
		code := deref(line.ErrorCode)
		return run.failStep(q, i, problemOf(string(line.Outcome), code), "Job poll failed: "+code)
	}
	var polls int64
	if s.Polls != nil {
		polls = *s.Polls
	}
	polls++
	s.Polls = &polls
	switch out.State {
	case "succeeded":
		if saveErr != nil {
			return saveErr
		}
		s.OutputArtifactID = artifact
		if err := run.completeStep(q, i, out.Output, out.Usage); err != nil {
			return err
		}
		if jsonKind(out.Usage) == '{' {
			if err := run.recordJob(q, row, def.Tool, s.JobID, def.Connection, out.Usage); err != nil {
				return err
			}
		}
		return pipelineSoon(q, runID)
	case "failed":
		code := "job-failed"
		if !isNull(out.Error) {
			code = fmt.Sprint(rawAny(out.Error))
		}
		return run.failStep(q, i, problemOf("error", code), "Job failed")
	}
	if err := run.put(q); err != nil {
		return err
	}
	return pipelineLater(q, runID)
}

// pipelineImportDrafts: a finished draft-examples run's examples become unapproved examples, each with its rating
// question; one that is refused is left out (savepoint). The run's row goes.
func pipelineImportDrafts(q *Request, run *runRecord, drafts pipelineDrafts) error {
	record := docOfRun(run) // importDraft (lab.go) reads the run as its record
	for _, b := range drafts {
		var ex any
		if json.Unmarshal(b, &ex) != nil {
			continue
		}
		if err := exec(q, "SAVEPOINT pipeline_draft"); err != nil {
			return err
		}
		if err := importDraft(q, record, ex); err != nil {
			if e := exec(q, "ROLLBACK TO SAVEPOINT pipeline_draft"); e != nil {
				return e
			}
			continue
		}
		if err := exec(q, "RELEASE SAVEPOINT pipeline_draft"); err != nil {
			return err
		}
	}
	return pipelineWorkDone(q, run.ID, true)
}

// callOutput: the provider's reply body of an in-process call, decoded; nil when there is none.
func callOutput(l contract.CallLine) any {
	var v any
	json.Unmarshal(l.Output, &v)
	return v
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
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
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
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
			defer func() { q.tx = nil }()
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
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
			defer func() { q.tx = nil }()
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
		ex, err := labExampleGet(q, id)
		if err != nil {
			return nil, err
		}
		approved := agentView[struct {
			State   string          `json:"state"`
			Example json.RawMessage `json:"example"`
		}](ex)
		if approved.State != "approved" {
			return nil, fail(409, "Example not approved")
		}
		example = pipelineRuleOf(approved.Example)
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

// Pipeline and Lab seams: the person gate on Lab and pipeline operations, long operations (their own transactions), the
// question answer hooks, pipeline_run and direct_search, and the agent step start.

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
func runAnswerChecks(q *Request, question doc, answer string) error {
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
	if err := labAnswerAfter(q, docOf(pipelineAnswered{ID: r.ID, Kind: r.Kind, Answer: r.Answer, AnsweredBy: r.AnsweredBy, Options: orNull(r.Options), Data: data})); err != nil {
		return err
	}
	var ask struct {
		BudgetAsk json.RawMessage `json:"budgetAsk"`
	}
	lenientDecode(data, &ask)
	return budgetAnswerAfter(q, orNull(ask.BudgetAsk), deref(r.Answer), r.AnsweredBy)
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
	err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
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

var skillID = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)

// projectIDPtr: a project id field as a pointer, nil when absent.
func projectIDPtr(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func init() {
	op("pipeline_run", func(q *Request, r *contract.PipelineRunRequest) (contract.PipelineRunReply, error) {
		return replyAs[contract.PipelineRunReply](runPipeline(q, "pipeline", r.ProjectId, r.RequestId, r.Query, r.MethodId))
	})
	op("direct_search", func(q *Request, r *contract.DirectSearchRequest) (contract.DirectSearchReply, error) {
		return replyAs[contract.DirectSearchReply](runPipeline(q, "direct", r.ProjectId, r.RequestId, r.Query, r.MethodId))
	})
}

// pipelineSkill: a skill's text as an execution payload carries it.
type pipelineSkill struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// profileSkills: the SKILL.md texts of a profile's skills (config/skills/<id>/SKILL.md), as an execution payload carries them.
func profileSkills(profile doc) ([]pipelineSkill, error) {
	skills := []pipelineSkill{}
	for _, x := range agentView[struct {
		Skills []json.RawMessage `json:"skills"`
	}](profile).Skills {
		id, _ := jsonString(x)
		if !skillID.MatchString(id) {
			return nil, fail(400, "Invalid skill")
		}
		text, err := os.ReadFile(filepath.Join(configDir(), "skills", id, "SKILL.md"))
		if err != nil {
			return nil, fail(400, "Unknown skill "+id)
		}
		skills = append(skills, pipelineSkill{ID: id, Text: string(text)})
	}
	return skills, nil
}

func artifactSave(q *Request, projectID any, scope, executionID, name string, b []byte, media string) (string, error) {
	id, _, err := saveObject(withTx(q), q.app, projectIDPtr(projectID), scope, executionID, name, b, media)
	return id, err
}

func artifactWriteObject(q *Request, projectID any, scope string, b []byte, media string) (string, string, error) {
	s, hash, err := writeObject(withTx(q), q.app, projectIDPtr(projectID), scope, b, media)
	if err != nil {
		return "", "", err
	}
	return hash, s.ID, nil
}

func recommend(q *Request, projectID, methodID string) (any, error) {
	sub := *q
	sub.body = jsonBytes(struct {
		ProjectID string `json:"projectId"`
		MethodID  string `json:"methodId"`
	}{projectID, methodID})
	return projectOps["recommendation_set"].h(&sub)
}

func projectKnown(q *Request, id string) bool { _, err := catalogue(q, id); return err == nil }

// Agent step: the step's trusted profile with the variant overrides, its skills, a session of the
// run, and an ordinary queued execution.
// stepProfile: the profile an agent step runs: its profile (or the override), with the variant's model and skills. The
// profile is its definition's open content.
func stepProfile(q *Request, step *pipelineDefStep, overrides *pipelineOverrides) (string, doc, error) {
	pid := step.Profile
	if overrides.Profile != "" {
		pid = overrides.Profile
	}
	profile, err := readTrustedProfile(q, pid)
	if err != nil {
		return pid, nil, err
	}
	if len(overrides.Model) > 0 {
		profile["model"] = rawAny(overrides.Model)
	}
	if len(overrides.Skills) > 0 {
		profile["skills"] = rawAny(overrides.Skills)
	}
	return pid, profile, nil
}

// pipelineAgentExtra: the pipeline fields of an agent step's execution.
type pipelineAgentExtra struct {
	PipelineStepID string `json:"pipelineStepId"`
	PipelineRunID  string `json:"pipelineRunId"`
	DirectedBy     string `json:"directedBy,omitempty"`
	ExperimentID   string `json:"experimentId,omitempty"`
}

func startAgentStep(q *Request, run *runRecord, step *pipelineDefStep, stepExecutionID, prompt string, inputs []fileRef) (string, error) {
	if id := run.ExperimentID; id != "" {
		requester, err := labRequester(q, id)
		if err != nil {
			return "", err
		}
		if run.RequestedBy.Email != requester.Email {
			return "", failCode(409, "Lab run requester provenance differs", "requester:conflict")
		}
	}
	overrides := &run.Overrides
	_, profile, err := stepProfile(q, step, overrides)
	if err != nil {
		return "", err
	}
	web := "connections"
	if len(overrides.WebRoute) > 0 {
		v, _ := jsonString(overrides.WebRoute)
		if v != "connections" && v != "builtin" {
			return "", fail(400, "web_route must be connections or builtin")
		}
		web = v
	}
	skills, err := profileSkills(profile)
	if err != nil {
		return "", err
	}
	s := agentSession{ID: newID(), CreatedAt: nowMs(), AgentConfig: jsonBytes(profile), PipelineRunID: run.ID, DirectedBy: run.director(), Account: overrides.Account,
		ProjectID: run.project()}
	if err := agentPut(q, "session", &s); err != nil {
		return "", err
	}
	if inputs == nil {
		inputs = []fileRef{}
	}
	payload := map[string]json.RawMessage{"inputs": jsonBytes(inputs), "skills": jsonBytes(skills), "webRoute": jsonBytes(web)}
	extra := docOf(pipelineAgentExtra{PipelineStepID: stepExecutionID, PipelineRunID: run.ID, DirectedBy: run.director(), ExperimentID: run.ExperimentID})
	e, err := agentEnqueue(q, &s, prompt, "pipeline:"+stepExecutionID, payload, extra, overrides.host(), nil)
	if err != nil {
		return "", err
	}
	return e.ID, nil
}

// jobTool names the status or listing tool of a job step's submit tool: Meshy multi_image_to_3d reads multi_job_status.
func (def *pipelineDefStep) jobTool(name string) string {
	override := map[string]string{"job_status": def.StatusTool, "recent_tasks": def.RecentTool}[name]
	if override != "" {
		return override
	}
	if strings.HasPrefix(def.Tool, "multi_") {
		return "multi_" + name
	}
	return name
}

// draftPipeline: the caller's runnable draft of a live pipeline (architect only): content and base revision.
func draftPipeline(q *Request, pid string) (*pipelineDef, int64, error) {
	if q.person != nil && !fullAccess(q.person) && !hasRole(q.person, "architect") {
		return nil, 0, fail(403, "A draft run is the architect's")
	}
	if _, ok, err := headText(q, "pipeline", pid); err != nil || !ok {
		if err == nil {
			err = fail(404, "Unknown pipeline "+pid)
		}
		return nil, 0, err
	}
	d, err := draftOf(q, "pipeline", pid)
	if err != nil || d == nil {
		if err == nil {
			err = fail(404, "No draft of "+pid)
		}
		return nil, 0, err
	}
	if ps := problems("pipeline", pid, d.Content); len(ps) > 0 {
		return nil, 0, fail(400, "The draft has problems: "+ps[0].Message)
	}
	var def pipelineDef
	return &def, d.Base, json.Unmarshal([]byte(d.Content), &def)
}

// pipelineRerun: loads finished run R and starts R2 from the definition to use (R's pipeline head, or the caller's draft).
// Mode all runs every step; mode failed reuses the longest prefix of R's steps where each succeeded, is the same step in
// the new definition, and (agent steps) would run the same profile content.
func pipelineRerun(q *Request, r *contract.PipelineRerunRequest) (*runRecord, error) {
	old, err := loadRun(q, r.RunId)
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

// runPrefix preserves immutable outputs/execution provenance, without charging copied work again: the steps of old a
// rerun on def reuses, each as the rerun's step (a not selected one as its id and kind).
func runPrefix(q *Request, old *runRecord, def *pipelineDef) ([]runStep, error) {
	if err := old.requirePlan(); err != nil {
		return nil, err
	}
	reuse := []runStep{}
	oldSteps, newSteps, oldDef := old.Steps, def.Steps, old.Definition.Steps
	for i := 0; i < len(oldSteps) && i < len(newSteps) && i < len(oldDef); i++ {
		e, entry := oldSteps[i], old.entry(i)
		if entry == nil || canonical(rawAny(oldDef[i].raw)) != canonical(rawAny(newSteps[i].raw)) {
			break
		}
		if entry.Activation == contract.PipelineActivationNotSelected {
			reuse = append(reuse, runStep{ID: e.ID, Kind: e.Kind})
			continue
		}
		if entry.Activation != contract.PipelineActivationSelected || e.State != "succeeded" || e.unconfirmed() || dispatchPhase(entry) != contract.PipelineDispatchPhaseSettled && entryRefMode(entry) != contract.PipelineRefModeReused {
			break
		}
		if err := outputOwner(q, old, i, e.OutputArtifactID); err != nil {
			return nil, err
		}
		artifact, err := artifactRow(withTx(q), q.app, e.OutputArtifactID)
		if err != nil || artifact.ContentHash != sha256hex(jsonNorm(e.Output)) {
			return nil, fail(409, "Reused output bytes cannot be verified")
		}
		bindings := entry.Inputs
		if bindings == nil || entry.ExecutionRef == nil || sha256hex([]byte(canonical(bindings))) != entry.ExecutionRef.SourceInputSha256 {
			return nil, fail(409, "Reused input identity cannot be verified")
		}
		for _, binding := range bindings {
			a, err := artifactRow(withTx(q), q.app, binding.ArtifactId)
			if err != nil || a.State != "stored" || !same(a.ProjectID, derefAny(old.ProjectID)) || a.ContentHash != binding.Sha256 {
				return nil, fail(409, "Reused input artifact is unavailable or changed")
			}
		}
		definition := &newSteps[i]
		connection, tool := "", ""
		if definition.Kind == "api" {
			connection, tool = definition.Connection, definition.Tool
		}
		if _, _, err := pipelineAuthorityStamp(q, old, i, connection, tool, ""); err != nil {
			return nil, err
		}
		if definition.Kind == "agent" {
			same, err := sameAgentStep(q, e.ExecutionID, definition, &old.Overrides)
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, fail(409, "Reused agent configuration or model changed")
			}
		}
		e.Calls, e.Usage = nil, nil
		if e.rest != nil {
			rest := recordRest{}
			for k, v := range e.rest {
				if k != "calls" && k != "usage" {
					rest[k] = v
				}
			}
			e.rest = rest
		}
		reuse = append(reuse, e)
	}
	return reuse, nil
}

// sameAgentStep: the profile an agent step would run now has the content the earlier step's execution pinned.
func sameAgentStep(q *Request, executionID string, step *pipelineDefStep, overrides *pipelineOverrides) (bool, error) {
	var pinned []contract.DefinitionRef
	var raw []byte
	raw, err := q.db().PipelineExecutionPins(q.ctx(), executionID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(raw, &pinned); err != nil || len(pinned) == 0 {
		return false, err
	}
	pid, profile, err := stepProfile(q, step, overrides)
	if err != nil {
		return false, err
	}
	now, err := definitionRef(q, "agent", pid, profile)
	return err == nil && now.ContentSha == pinned[0].ContentSha, err
}

// withArguments: a variant's arguments override for this api step, merged over the step's arguments before references
// are resolved (fleet-asset-pipelines §4, Variant arguments).
func (def *pipelineDefStep) withArguments(overrides *pipelineOverrides) json.RawMessage {
	extra := pipelineRuleOf(overrides.Arguments[def.ID])
	if extra == nil {
		return def.Arguments
	}
	out := pipelineRule{}
	for k, v := range pipelineRuleOf(def.Arguments) {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return jsonBytes(out)
}

// rerunExtra: what a rerun keeps of its source besides its example: its identity and its staged inputs (immutable even
// when the example contains no text files).
func rerunExtra(old *runRecord, identity string) (recordRest, error) {
	inputs := json.RawMessage("null")
	if old.ExampleInputs != nil {
		inputs = jsonBytes(*old.ExampleInputs)
	}
	return recordRest{"rerunIdentity": jsonBytes(identity), "exampleInputs": inputs}, nil
}

// jobMatchesRaw requires every declared input; output views never establish request identity.
func jobMatchesRaw(task, args pipelineRule, tool *contract.GatewayTool) bool {
	if tool == nil || deref(tool.AdoptMatch) == "" {
		return false
	}
	primary := *tool.AdoptMatch
	fields := []string{primary}
	for _, name := range tool.AdoptFields {
		if name == "" {
			return false
		}
		fields = append(fields, name)
	}
	for _, field := range fields {
		expected, sent := args[field]
		if !sent && field != primary {
			continue
		}
		actual, returned := task[field]
		if !sent || !returned || isNull(expected) || isNull(actual) || !bytes.Equal(jsonNorm(expected), jsonNorm(actual)) {
			return false
		}
	}
	switch jsonKind(args[primary]) {
	case '"':
		s, _ := jsonString(args[primary])
		return strings.TrimSpace(s) != ""
	case '[':
		input := jsonList(args[primary])
		if len(input) == 0 {
			return false
		}
		for _, value := range input {
			if s, ok := jsonString(value); !ok || strings.TrimSpace(s) == "" {
				return false
			}
		}
		return true
	}
	return false
}

// Meshy timestamps are Unix seconds; test adapters may return Unix milliseconds.
func jobCreatedDuring(created, started, finished float64) bool {
	if created >= 1e9 && created < 1e11 {
		created *= 1000
	}
	return created > 0 && created >= started-5000 && created <= finished+5000
}
