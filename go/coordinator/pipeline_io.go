// Pipeline definition validation, template reference resolution, input/output artifacts, credits, and reuse lineage.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

var (
	stepIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	pipelineFile   = regexp.MustCompile(`^[a-z0-9-]+\.json$`)
	pipeRefPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
	wholeRef       = regexp.MustCompile(`^\$\{([^}]+)\}$`)
	ownOutput      = regexp.MustCompile(`^(output/)?output\.json$`)
)

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

func (run *runRecord) saveArtifact(q *Request, executionID, name string, b []byte, media string) (string, error) {
	return artifactSave(q, derefAny(run.ProjectID), run.scope(), executionID, name, b, media)
}

// fileRef: a staged file: its artifact and its path under the step's inputs.
type fileRef struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
}

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
		current, err = getRun(q, previous)
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
	if _, err := insertExecution(q, pipelineToolRun{
		ID: id, Kind: "tool_run", State: "succeeded", ProjectID: run.ProjectID, ParentExecutionID: stepID,
		ExperimentID: agentOpt(run.ExperimentID), Variant: agentOpt(run.Variant),
		TaskID: taskID, SessionID: lineage.SessionID, DirectedBy: dir,
		PipelineID: lineage.PipelineID, PipelineRevision: lineage.PipelineRevision, StepID: lineage.StepID,
		StartedAt: jobNow, EndedAt: jobNow,
		Data: pipelineJobCall{Tool: tool, RequestID: jobID, Outcome: "ok", Usage: usage, Connection: connection},
	}); err != nil {
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

// pipelineImportDrafts: a finished draft-examples run's examples become unapproved examples, each with its rating
// question; one that is refused is left out (savepoint). The run's row goes.
func pipelineImportDrafts(q *Request, run *runRecord, drafts pipelineDrafts) error {
	for _, b := range drafts {
		var ex any
		if json.Unmarshal(b, &ex) != nil {
			continue
		}
		if err := exec(q, "SAVEPOINT pipeline_draft"); err != nil {
			return err
		}
		if err := importDraft(q, run, ex); err != nil {
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

// rerunExtra: what a rerun keeps of its source besides its example: its identity and its staged inputs (immutable even
// when the example contains no text files).
func rerunExtra(old *runRecord, identity string) (recordRest, error) {
	inputs := json.RawMessage("null")
	if old.ExampleInputs != nil {
		inputs = jsonBytes(*old.ExampleInputs)
	}
	return recordRest{"rerunIdentity": jsonBytes(identity), "exampleInputs": inputs}, nil
}
