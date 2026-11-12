// Pipeline format version 2 runner (docs/designs/fleet-lab.md "Pipeline format version 2").
// A run is an ordered chain of api, agent and script steps: the pipeline_run execution
// with one pipeline_step row `<run>:step:<i>` per step; each provider call is a tool_run child of its step. Every step
// leaves an output.json artifact. pipelineAdvance(runId) moves a run forward and is safe to call any time; a sync api call
// interrupted by a restart is unknown and never replayed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
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

func validatePipeline(d doc) (doc, error) {
	bad := func(m string) (doc, error) { return nil, fail(500, "Invalid pipeline: "+m) }
	if v, _ := numOK(d["version"]); v != 2 {
		return bad("version must be 2")
	}
	if !stepIDPattern.MatchString(astr(d["id"])) {
		return bad("id")
	}
	steps := anys(d["steps"])
	if len(steps) == 0 {
		return bad("steps")
	}
	allowed := map[string][]string{"api": {"id", "kind", "connection", "tool", "arguments", "mode", "output", "status_tool", "recent_tool"}, "agent": {"id", "kind", "profile", "prompt"}, "script": {"id", "kind", "command", "timeout_ms", "host"}}
	seen := map[string]bool{}
	for _, x := range steps {
		s := mapOf(x)
		id := astr(s["id"])
		if s == nil || !stepIDPattern.MatchString(id) || seen[id] || id == "example" {
			return bad(fmt.Sprintf("step id %v", s["id"]))
		}
		seen[id] = true
		kind := astr(s["kind"])
		keys, ok := allowed[kind]
		if !ok {
			return bad(fmt.Sprintf("step kind %v", s["kind"]))
		}
		for k := range s {
			if !keysOnly(doc{k: 1}, keys...) {
				return bad("step " + id + " key " + k)
			}
		}
		switch kind {
		case "api":
			_, isStr := s["connection"].(string)
			_, toolStr := s["tool"].(string)
			mode, hasMode := s["mode"]
			_, hasOutput := s["output"]
			if hasOutput && !onlyReplyRefs(s["output"]) {
				return bad("api step " + id + " output: only ${reply.<path>} references")
			}
			if !isStr || !toolStr || mapOf(s["arguments"]) == nil && anys(s["arguments"]) == nil || hasMode && mode != "sync" && mode != "job" || hasOutput && (mapOf(s["output"]) == nil || mode == "job") {
				return bad("api step " + id)
			}
			for _, key := range []string{"status_tool", "recent_tool"} {
				if value, has := s[key]; has {
					name, ok := value.(string)
					if !ok || strings.TrimSpace(name) == "" || mode != "job" {
						return bad("api step " + id + " " + key + " requires job mode and a tool name")
					}
				}
			}
		case "agent":
			if _, ok := s["profile"].(string); !ok {
				return bad("agent step " + id)
			}
			if _, ok := s["prompt"].(string); !ok {
				return bad("agent step " + id)
			}
		case "script":
			t, has := s["timeout_ms"]
			n, isInt := intOK(t)
			if astr(s["command"]) == "" || has && !(isInt && n > 0) {
				return bad("script step " + id)
			}
		}
	}
	return d, nil
}

// loadPipeline: a pipeline definition's head; file is the catalogue's and runs' reference (<id>.json).
func loadPipeline(q *Request, file string) (doc, error) {
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
	var d doc
	if json.Unmarshal(raw, &d) != nil {
		return nil, fail(500, "Invalid pipeline: JSON")
	}
	return validatePipeline(d)
}

type refError struct{ path string }

func (e *refError) Error() string { return "ref:" + e.path }

func shellQuote(x any) string {
	s, ok := x.(string)
	if !ok {
		s = string(jsonBytes(x))
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resolveRefs: ${example.<path>} and ${steps.<id>.output.<path>}. A whole-reference string takes the value as it is; inside
// a longer string the value is inlined as text. quote (scripts) applies to every substituted value.
// onlyReplyRefs: every reference in an output mapping names the reply.
func onlyReplyRefs(v any) bool {
	for _, m := range pipeRefPattern.FindAllStringSubmatch(string(jsonBytes(v)), -1) {
		if m[1] != "reply" && !strings.HasPrefix(m[1], "reply.") {
			return false
		}
	}
	return true
}

func hasReply(outputs doc) bool { _, ok := outputs[""]; return ok }

func resolveRefs(v any, example any, outputs doc, quote bool) (any, error) {
	look := func(path string) (any, error) {
		parts := strings.Split(path, ".")
		var cur any
		switch {
		case parts[0] == "example":
			cur, parts = example, parts[1:]
		case parts[0] == "reply" && hasReply(outputs):
			// An api step's own provider reply, only inside its output mapping; kept under "", which is no step id.
			cur, parts = outputs[""], parts[1:]
		case parts[0] == "steps" && len(parts) > 2 && parts[2] == "output":
			cur, parts = outputs[parts[1]], parts[3:]
		default:
			return nil, &refError{path}
		}
		for _, p := range parts {
			m := mapOf(cur)
			if m == nil {
				if l, ok := cur.([]any); ok {
					var i int
					if _, err := fmt.Sscan(p, &i); err == nil && i >= 0 && i < len(l) && fmt.Sprint(i) == p {
						cur = l[i]
						continue
					}
				}
				return nil, &refError{path}
			}
			x, ok := m[p]
			if !ok {
				return nil, &refError{path}
			}
			cur = x
		}
		if cur == nil && len(parts) == 0 && example == nil {
			return nil, &refError{path}
		}
		return cur, nil
	}
	switch x := v.(type) {
	case string:
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
			if str, ok := val.(string); ok {
				return str
			}
			return string(jsonBytes(val))
		})
		return out, ferr
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := resolveRefs(e, example, outputs, quote)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := doc{}
		for k, e := range x {
			r, err := resolveRefs(e, example, outputs, quote)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

// putRun writes the pipeline_run row and one pipeline_step row per step.
func putRun(q *Request, run doc) error {
	rest := doc{}
	for k, v := range run {
		if k != "steps" && k != "state" && k != "observation" {
			rest[k] = v
		}
	}
	state, obs, err := runState(run)
	if err != nil {
		return err
	}
	cfg := doc{"method": run["selectedMethod"], "definitionSha256": run["definitionSha256"]}
	if err := exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,started_at,ended_at,data,config_ref,experiment_id,variant)
	 VALUES($1,'pipeline_run',$2,$3,$4,$5,$6,$7,$8,$9,$10)
	 ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,
	  config_ref=CASE WHEN execution.variant IS NULL THEN excluded.config_ref ELSE execution.config_ref END,
	  experiment_id=coalesce(execution.experiment_id,excluded.experiment_id),variant=coalesce(execution.variant,excluded.variant)`,
		run["id"], state, obs, run["projectId"], msTime(run["startedAt"]), msTime(run["finishedAt"]), jsonBytes(rest), jsonBytes(cfg), run["experimentId"], run["variant"]); err != nil {
		return err
	}
	if run["definitions"] != nil {
		if err := exec(q, "UPDATE execution SET definitions=$2 WHERE id=$1", run["id"], jsonBytes(run["definitions"])); err != nil {
			return err
		}
	}
	for i, x := range anys(run["steps"]) {
		s := mapOf(x)
		st, sobs, err := runState(s)
		if err != nil {
			return err
		}
		data := doc{"index": i}
		for k, v := range s {
			if k != "state" && k != "observation" {
				data[k] = v
			}
		}
		if err := exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,parent_execution_id,started_at,data) VALUES($1,'pipeline_step',$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,started_at=excluded.started_at,data=excluded.data,
		  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
			stepRow(run, i), st, sobs, run["projectId"], run["id"], msTime(s["startedAt"]), jsonBytes(data)); err != nil {
			return err
		}
	}
	return nil
}

func withSteps(q *Request, runs []doc) ([]doc, error) {
	if len(runs) == 0 {
		return runs, nil
	}
	ids := []string{}
	for _, r := range runs {
		ids = append(ids, astr(r["id"]))
	}
	steps, err := queryRows(q, "SELECT parent_execution_id AS run,id AS step,"+execJSON+" AS data FROM execution WHERE kind='pipeline_step' AND parent_execution_id=ANY($1) ORDER BY (data->>'index')::int", ids)
	if err != nil {
		return nil, err
	}
	stepIDs := []string{}
	for _, s := range steps {
		stepIDs = append(stepIDs, astr(s["step"]))
	}
	// Each step's provider calls (tool_run children) with their cost and its basis (fleet-analytics.md, basis).
	calls, err := queryRows(q, `SELECT e.parent_execution_id AS step,e.id,e.data->>'tool' AS tool,e.data->>'connection' AS connection,t.connection_cost AS cost,
	 coalesce(t.connection_cost_basis,'unavailable') AS basis FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id
	 WHERE e.kind='tool_run' AND e.parent_execution_id=ANY($1) ORDER BY e.seq`, stepIDs)
	if err != nil {
		return nil, err
	}
	byStep := map[any][]any{}
	for _, c := range calls {
		byStep[c["step"]] = append(byStep[c["step"]], doc{"id": c["id"], "tool": c["tool"], "connection": c["connection"], "cost": c["cost"], "basis": c["basis"]})
	}
	for _, r := range runs {
		list := []any{}
		for _, s := range steps {
			if s["run"] == r["id"] {
				d := mapOf(s["data"])
				delete(d, "index")
				d["calls"] = append([]any{}, byStep[s["step"]]...)
				list = append(list, d)
			}
		}
		r["steps"] = list
	}
	return runs, nil
}

func getRun(q *Request, id string) (doc, error) {
	rs, err := rows(q, "SELECT "+execJSON+" FROM execution WHERE id=$1 AND "+runRows, id)
	if err != nil {
		return nil, err
	}
	rs, err = withSteps(q, rs)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

func runsWhere(q *Request, clause string, args ...any) ([]doc, error) {
	rs, err := rows(q, "SELECT "+execJSON+" FROM execution WHERE "+runRows+" AND "+clause+" ORDER BY seq", args...)
	if err != nil {
		return nil, err
	}
	return withSteps(q, rs)
}

// runRows: the pipeline runs a run record was written for (a Lab trial row exists before its run starts).
const runRows = "kind='pipeline_run' AND data ? 'pipelineId'"

// runState: the typed state and observation of a run or step record; unconfirmed only when the record says so.
func runState(r doc) (string, string, error) {
	state, obs := firstString(r["state"], "preparing"), "confirmed"
	if _, ok := stateOrder[state]; !ok {
		return "", "", fmt.Errorf("pipeline record %v: unknown state %q", r["id"], state)
	}
	if unconfirmed(r) {
		obs = "unconfirmed"
	}
	return state, obs, nil
}

func stepRow(run doc, i int) string { return fmt.Sprintf("%v:step:%d", run["id"], i) }

func requester(q *Request) doc {
	if q.person != nil {
		return doc{"kind": "person", "email": q.person.Email}
	}
	return doc{"kind": "operator"}
}

// pipelineStart writes a new run record (runner.start). o: id?, file, projectId, requestId, example, overrides, requestedBy,
// experimentId, variant, extra.
func pipelineStart(q *Request, o doc) (doc, error) {
	def := mapOf(o["draftDefinition"]) // a draft run: the caller's draft, validated
	var err error
	if def != nil {
		def, err = validatePipeline(def)
	} else {
		def, err = loadPipeline(q, astr(o["file"]))
	}
	if err != nil {
		return nil, err
	}
	id := astr(o["id"])
	if id == "" {
		id = newID()
	}
	steps := []any{}
	for _, x := range anys(def["steps"]) {
		s := mapOf(x)
		e := doc{"id": s["id"], "kind": s["kind"], "state": "preparing"}
		if s["kind"] == "api" {
			e["tool"], e["connection"] = s["tool"], s["connection"]
		}
		steps = append(steps, e)
	}
	or := func(k string, d any) any {
		if o[k] != nil {
			return o[k]
		}
		return d
	}
	// A rerun: the reused prefix of the earlier run's steps comes over as it ended (state, output, usage, artifacts by id).
	reused := []string{}
	for i, x := range anys(o["reuse"]) {
		if i < len(steps) {
			e := doc{}
			for k, v := range mapOf(x) {
				e[k] = v
			}
			e["reusedFrom"] = o["rerunOf"]
			steps[i] = e
			reused = append(reused, astr(e["id"]))
		}
	}
	run := doc{"id": id, "projectId": o["projectId"], "requestId": o["requestId"], "requestedBy": or("requestedBy", requester(q)), "definition": def,
		"definitionSha256": sha256hex(jsonBytes(def)), "pipelineId": def["id"], "pipeline": o["file"], "example": or("example", doc{}), "overrides": or("overrides", doc{}),
		"state": "running", "startedAt": nowMs(), "durationMs": nil, "usage": nil, "result": nil, "steps": steps}
	if o["experimentId"] != nil {
		run["experimentId"] = o["experimentId"]
	}
	if o["variant"] != nil {
		run["variant"] = o["variant"]
	}
	for k, v := range mapOf(o["extra"]) {
		run[k] = v
	}
	if o["rerunOf"] != nil {
		run["rerunOf"], run["reused"] = o["rerunOf"], reused
	}
	ref, err := definitionRef(q, "pipeline", astr(def["id"]), def)
	if err != nil {
		return nil, err
	}
	if base, ok := o["draftBase"].(int64); ok { // a draft run: on its base, whatever content it has (data.definition holds it)
		yes := true
		ref.Revision, ref.Draft = base, &yes
	}
	run["definitions"] = []contract.DefinitionRef{ref}
	if pid := astr(run["projectId"]); pid != "" {
		snapshot, err := projectContext(q, pid)
		if err != nil {
			return nil, err
		}
		run["projectSnapshot"] = snapshot
		run["definitions"] = append(run["definitions"].([]contract.DefinitionRef), contract.DefinitionRef{Kind: contract.DefinitionKindProject, Id: pid, Revision: int64(num(snapshot["revision"])), ContentSha: astr(snapshot["contentSha"])})
	}
	return run, putRun(q, run)
}

// Runner state of this process: per-run serial chains and in-flight sync calls.
var (
	runLocks sync.Map // run id -> *sync.Mutex
	inflight sync.Map // step row -> true
	runWG    sync.WaitGroup
)

func runLock(id string) *sync.Mutex {
	m, _ := runLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// pipelineAdvance moves a run forward in the background, serial per run, outside any caller's transaction.
func pipelineAdvance(a *App, runID string) {
	runWG.Add(1)
	go func() {
		defer runWG.Done()
		m := runLock(runID)
		m.Lock()
		defer m.Unlock()
		if err := advanceOnce(context.Background(), a, runID); err != nil {
			log.Printf("pipeline %s: %v", runID, err)
		}
	}()
}

// pipelineWait polls until the run leaves running (pipeline_run waits for a direct search).
func pipelineWait(ctx context.Context, a *App, runID string) (doc, error) {
	for {
		var r doc
		if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { r, err = getRun(q, runID); return }); err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fail(404, "Not found")
		}
		if r["state"] != "running" {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// pipelineOnExecution: the run whose step is this execution moves on (receipt hook; the tick is the backstop).
func pipelineOnExecution(a *App, executionID string) {
	var ids []string
	runTx(context.Background(), a, nil, false, "", func(q *Request) error {
		rs, err := queryRows(q, "SELECT parent_execution_id AS id FROM execution WHERE kind='pipeline_step' AND data->>'executionId'=$1", executionID)
		for _, r := range rs {
			ids = append(ids, astr(r["id"]))
		}
		return err
	})
	for _, id := range ids {
		pipelineAdvance(a, id)
	}
}

func pipelineTick(ctx context.Context, a *App) {
	var ids []string
	runTx(ctx, a, nil, false, "", func(q *Request) error {
		rs, err := queryRows(q, "SELECT id FROM execution WHERE "+runRows+" AND state='running' AND (data->'definition'->>'version')::int=2 ORDER BY seq")
		for _, r := range rs {
			ids = append(ids, astr(r["id"]))
		}
		return err
	})
	for _, id := range ids {
		m := runLock(id)
		if m.TryLock() { // a run already being advanced is skipped
			m.Unlock()
			pipelineAdvance(a, id)
		}
	}
}

// pipelineCancel: the run ends cancelled, later steps never start; returns the running step's own execution, which the
// caller stops. Serial with advance.
func pipelineCancel(ctx context.Context, a *App, runID string) (string, error) {
	m := runLock(runID)
	m.Lock()
	defer m.Unlock()
	own := ""
	var done doc
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		var err error
		own, done, err = pipelineCancelIn(q, runID)
		return err
	})
	if err == nil && done != nil {
		notifyFinished(a, done)
	}
	return own, err
}

// pipelineCancelIn is called under runLock and a write transaction. Caller handles post-commit notification/stop.
func pipelineCancelIn(q *Request, runID string) (string, doc, error) {
	run, err := getRun(q, runID)
	if err != nil || run == nil || run["state"] != "running" {
		return "", nil, err
	}
	own := ""
	for _, x := range anys(run["steps"]) {
		step := mapOf(x)
		if step["state"] == "running" || step["state"] == "preparing" {
			if step["state"] == "running" {
				own = astr(step["executionId"])
			}
			step["state"], step["finishedAt"] = "cancelled", nowMs()
			break
		}
	}
	return own, run, finishRun(q, run, "cancelled", "cancelled")
}

func notifyFinished(a *App, run doc) {
	go pipelineFinished(context.Background(), a, run)
}

func finishRun(q *Request, run doc, state, problem string) error {
	run["state"], run["finishedAt"] = state, nowMs()
	st, _ := numOK(run["startedAt"])
	run["durationMs"] = nowMs() - int64(st)
	credits, used := 0.0, false
	outputs := doc{}
	var search doc
	var stepErr any
	for _, x := range anys(run["steps"]) {
		s := mapOf(x)
		if u := mapOf(s["usage"]); u != nil {
			used = true
			c, _ := numOK(u["credits"])
			credits += c
		}
		if o, ok := s["output"]; ok {
			outputs[astr(s["id"])] = o
			if search == nil && anys(mapOf(o)["results"]) != nil {
				search = mapOf(o)
			}
		}
		if stepErr == nil && s["error"] != nil {
			stepErr = s["error"]
		}
	}
	if used {
		run["usage"] = doc{"credits": credits}
	} else {
		run["usage"] = nil
	}
	sources := []any{}
	result := doc{"outputs": outputs}
	if search != nil {
		for _, r := range anys(search["results"]) {
			x := mapOf(r)
			title := x["title"]
			if title == nil {
				title = x["url"]
			}
			sources = append(sources, doc{"url": x["url"], "title": title, "content": x["content"]})
		}
		result["native"] = search
	}
	result["sources"] = sources
	if problem != "" {
		if stepErr == nil {
			stepErr = doc{"code": problem}
		}
		result["error"] = stepErr
	}
	run["result"] = result
	if err := putRun(q, run); err != nil {
		return err
	}
	if problem != "" {
		return exec(q, "UPDATE execution SET problem=$2 WHERE id=$1", run["id"], problem)
	}
	return nil
}

func failStep(q *Request, run doc, i int, problem, message string) error {
	s := mapOf(anys(run["steps"])[i])
	s["state"] = "failed"
	lost := strings.HasPrefix(problem, "unknown:") // the outcome is unknown: failed for Fleet, unconfirmed
	if lost {
		s["observation"], run["observation"] = "unconfirmed", "unconfirmed"
	}
	s["problem"], s["error"], s["finishedAt"] = problem, doc{"code": problem, "message": message}, nowMs()
	if err := putRun(q, run); err != nil {
		return err
	}
	if err := exec(q, "UPDATE execution SET problem=$2 WHERE id=$1", stepRow(run, i), problem); err != nil {
		return err
	}
	return finishRun(q, run, "failed", problem)
}

func runScope(run doc) string {
	if run["experimentId"] != nil {
		return "lab"
	}
	return "prod"
}

func saveArtifact(q *Request, run doc, executionID, name string, b []byte, media string) (string, error) {
	return artifactSave(q, run["projectId"], runScope(run), executionID, name, b, media)
}

func completeStep(q *Request, run doc, i int, output any, extra doc) error {
	s := mapOf(anys(run["steps"])[i])
	if s["outputArtifactId"] == nil {
		id, err := saveArtifact(q, run, stepRow(run, i), "output.json", jsonBytes(output), "application/json")
		if err != nil {
			return err
		}
		s["outputArtifactId"] = id
	}
	for k, v := range extra {
		s[k] = v
	}
	s["output"], s["state"], s["finishedAt"] = output, "succeeded", nowMs()
	st, ok := numOK(s["startedAt"])
	if !ok {
		st = float64(nowMs())
	}
	s["durationMs"] = nowMs() - int64(st)
	return putRun(q, run)
}

type fileRef struct{ ArtifactID, Path string }

func (f fileRef) doc() doc { return doc{"artifactId": f.ArtifactID, "path": f.Path} }

// artifactsOf: stored artifacts of an execution {id, path, sha256}.
func artifactsOf(q *Request, executionID string) ([]doc, error) {
	return queryRows(q, "SELECT id,name AS path,content_hash AS sha256 FROM artifact WHERE execution_id=$1 AND state='stored' ORDER BY seq", executionID)
}

// pipelineOutputIdentity rejects even a readable artifact owned by another step/project.
func pipelineOutputIdentity(x *Artifact, project any, owner string) bool {
	return x.State == "stored" && same(x.ProjectID, project) && x.ExecutionID != nil && *x.ExecutionID == owner && x.Name == "output.json" && labSha.MatchString(x.ContentHash)
}

// pipelineOutputOwner follows immediate reuse lineage to the original step; readable foreign artifacts are not outputs.
func pipelineOutputOwner(q *Request, run doc, index int, aid string) error {
	seen := map[string]bool{}
	current := run
	for {
		id := astr(current["id"])
		if id == "" || seen[id] || index >= len(anys(current["steps"])) {
			return fail(409, "Invalid reused output lineage")
		}
		seen[id] = true
		step := mapOf(anys(current["steps"])[index])
		if step["id"] != mapOf(anys(run["steps"])[index])["id"] || step["outputArtifactId"] != aid {
			return fail(409, "Changed reused output identity")
		}
		previous := astr(step["reusedFrom"])
		if previous == "" {
			x, err := artifactRow(withTx(q), q.app, aid)
			if err != nil {
				return err
			}
			if !pipelineOutputIdentity(x, run["projectId"], stepRow(current, index)) {
				return fail(409, "Invalid output artifact ownership")
			}
			return nil
		}
		var err error
		current, err = getRun(q, previous)
		if err != nil {
			return err
		}
		if current == nil || current["projectId"] != run["projectId"] {
			return fail(409, "Missing reused output source")
		}
	}
}

// pipelineOutputs: files of the first n steps under <step id>/ (n<0: every step). A step's own files win over the
// coordinator's output.json for it; dotfiles are left out.
func pipelineOutputs(q *Request, run doc, n int) ([]fileRef, error) {
	steps := anys(run["steps"])
	if n < 0 {
		n = len(steps)
	}
	out := []fileRef{}
	for j := 0; j < n && j < len(steps); j++ {
		s := mapOf(steps[j])
		ids := []string{stepRow(run, j)}
		if e := astr(s["executionId"]); e != "" {
			ids = append(ids, e)
		}
		order, files := []string{}, map[string]string{}
		if aid := astr(s["outputArtifactId"]); aid != "" {
			if err := pipelineOutputOwner(q, run, j, aid); err != nil {
				return nil, err
			}
			k := astr(s["id"]) + "/output.json"
			order, files[k] = append(order, k), aid
		}
		for _, id := range ids {
			as, err := artifactsOf(q, id)
			if err != nil {
				return nil, err
			}
			for _, a := range as {
				rel := strings.TrimPrefix(astr(a["path"]), "output/")
				dot := false
				for _, p := range strings.Split(rel, "/") {
					dot = dot || strings.HasPrefix(p, ".")
				}
				if dot {
					continue
				}
				k := astr(s["id"]) + "/" + rel
				if _, ok := files[k]; !ok {
					order = append(order, k)
				}
				files[k] = astr(a["id"])
			}
		}
		for _, k := range order {
			out = append(out, fileRef{files[k], k})
		}
	}
	return out, nil
}

func inputsFor(q *Request, run doc, i int) ([]any, error) {
	out := []any{}
	ex := mapOf(run["example"])
	if inputs := anys(ex["inputs"]); len(inputs) > 0 {
		if run["exampleInputs"] == nil {
			list := []any{}
			for _, x := range inputs {
				f := mapOf(x)
				b, err := exampleFileBytes(q, run["projectId"], f)
				if err != nil {
					return nil, err
				}
				media := "text/plain"
				if f["media_type"] != nil {
					media = astr(f["media_type"])
				}
				id, err := saveArtifact(q, run, astr(run["id"]), "example/"+astr(f["path"]), b, media)
				if err != nil {
					return nil, err
				}
				list = append(list, doc{"artifactId": id, "path": "example/" + astr(f["path"])})
			}
			run["exampleInputs"] = list
			if err := putRun(q, run); err != nil {
				return nil, err
			}
		}
	}
	out = append(out, anys(run["exampleInputs"])...)
	fs, err := pipelineOutputs(q, run, i)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		out = append(out, f.doc())
	}
	return out, nil
}

// startScript: a script command for the executor, as its own session and execution.
func startScript(q *Request, run doc, stepExecutionID, command string, timeout any, hostID string, inputs []any) (string, error) {
	sid := newID()
	s := doc{"id": sid, "createdAt": nowMs(), "pipelineRunId": run["id"]}
	if run["projectId"] != nil {
		s["projectId"] = run["projectId"]
	}
	if err := put(q, "session", s); err != nil {
		return "", err
	}
	if hostID == "" {
		hostID = q.app.hostID
	}
	id := newID()
	e := doc{"id": id, "executionId": id, "sessionId": sid, "hostId": hostID, "state": "preparing", "createdAt": nowMs(), "actor": "program-worker", "pipelineStepId": stepExecutionID, "pipelineRunId": run["id"], "projectSnapshot": run["projectSnapshot"]}
	if run["experimentId"] != nil {
		e["experimentId"] = run["experimentId"]
	}
	if err := putExecution(q, e); err != nil { // a host job, the step's child in the run's project (session) and experiment
		return "", err
	}
	payload := doc{"command": command, "inputs": inputs}
	if timeout != nil {
		payload["timeout_ms"] = timeout
	}
	return id, put(q, "command", doc{"id": newID(), "executionId": id, "sessionId": sid, "hostId": hostID, "kind": "script", "payload": payload, "status": "pending"})
}

// connCallFor: one provider call with a token minted for the step row, revoked after the call; the gateway's spool line
// becomes the step's tool_run (ingested here, so the row exists when the step moves on).
func connCallFor(ctx context.Context, a *App, run doc, connection, executionID, tool string, args any) contract.CallLine {
	unavailable := contract.CallLine{Tool: tool, Connection: connection, Outcome: contract.CallOutcomeError, ErrorCode: ptr("connection-unavailable")}
	if gatewayURL() == "" || !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(connection) {
		return unavailable
	}
	var token string
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		by := mapOf(run["requestedBy"])["email"]
		m, err := mint(q, doc{"executionId": executionID, "directedBy": by, "projectId": run["projectId"], "experimentId": run["experimentId"], "step": true}, []string{connection})
		if err == nil {
			token, _ = m["token"].(string)
		}
		return err
	})
	if err != nil || token == "" {
		return unavailable
	}
	writeSnapshot(ctx, a) // the gateway must see the token before the call
	line := gatewayCall(ctx, token, connection, tool, args)
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		if err := revokeTokens(q, executionID); err != nil {
			return err
		}
		_, err := connIngest(q)
		return err
	}); err != nil {
		log.Printf("revoke %s: %v", executionID, err)
	}
	return line
}

// recordJob: a finished job's usage as one tool_run child of the step.
func recordJob(q *Request, run doc, stepID, tool, jobID, connection string, usage doc) error {
	id := stepID + ":job:0"
	if _, err := insertExecution(q, doc{"id": id, "kind": "tool_run", "state": "succeeded", "projectId": run["projectId"], "parentExecutionId": stepID, "experimentId": run["experimentId"], "variant": run["variant"],
		"data": doc{"tool": tool, "requestId": jobID, "outcome": "ok", "errorCode": nil, "usage": usage, "connection": connection}}); err != nil {
		return err
	}
	var cost any
	if c, ok := numOK(usage["credits"]); ok {
		cost = doc{"unit": "credits", "credits": c, "connection": connection}
	}
	return writeConnectionCost(q, id, run["projectId"], cost)
}

func creditsOf(q *Request, executionID string) (any, error) {
	var calls int
	var credits float64
	err := q.tx.QueryRow(q.ctx(), `SELECT count(*)::int,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.parent_execution_id=$1 AND e.kind='tool_run'`, executionID).Scan(&calls, &credits)
	if err != nil || calls == 0 {
		return nil, err
	}
	return doc{"credits": credits}, nil
}

func advanceOnce(ctx context.Context, a *App, runID string) error {
	for {
		var run doc
		if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { run, err = getRun(q, runID); return }); err != nil {
			return err
		}
		if run == nil || run["state"] != "running" || mapOf(run["definition"])["version"] != float64(2) {
			return nil
		}
		i := -1
		for j, x := range anys(run["steps"]) {
			if mapOf(x)["state"] != "succeeded" {
				i = j
				break
			}
		}
		if i < 0 {
			if err := runTx(ctx, a, nil, true, "", func(q *Request) error { return finishRun(q, run, "succeeded", "") }); err != nil {
				return err
			}
			notifyFinished(a, run)
			return nil
		}
		again, err := runStep(ctx, a, run, i)
		if err != nil {
			return err
		}
		if run["state"] != "running" {
			notifyFinished(a, run)
		}
		if !again {
			return nil
		}
	}
}

// runStep: true when the next step may start now.
func runStep(ctx context.Context, a *App, run doc, i int) (bool, error) {
	s := mapOf(anys(run["steps"])[i])
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	outputs := doc{}
	for _, x := range anys(run["steps"])[:i] {
		outputs[astr(mapOf(x)["id"])] = mapOf(x)["output"]
	}
	row := stepRow(run, i)
	kind := astr(def["kind"])
	tx := func(fn func(q *Request) error) error { return runTx(ctx, a, nil, true, "", fn) }
	failNow := func(problem, message string) (bool, error) {
		return false, tx(func(q *Request) error { return failStep(q, run, i, problem, message) })
	}
	connection := astr(def["connection"])

	if s["state"] == "preparing" {
		var resolved any
		var err error
		switch kind {
		case "api":
			resolved, err = resolveRefs(withArguments(def, mapOf(run["overrides"])), run["example"], outputs, false)
		case "agent":
			p := def["prompt"]
			if o := mapOf(run["overrides"]); o != nil && o["prompt"] != nil {
				p = o["prompt"]
			}
			resolved, err = resolveRefs(p, run["example"], outputs, false)
		default:
			resolved, err = resolveRefs(def["command"], run["example"], outputs, true)
		}
		if re, ok := err.(*refError); ok {
			s["startedAt"] = nowMs()
			return failNow("ref:"+re.path, "Missing reference "+re.path)
		} else if err != nil {
			return false, err
		}
		s["state"], s["startedAt"] = "running", nowMs()
		if kind == "api" {
			var submitTool doc
			if def["mode"] == "job" {
				err := runTx(ctx, a, nil, false, "", func(q *Request) error {
					c, err := connByName(q, connection)
					if err != nil {
						return err
					}
					tools := providerTools(astr(c["provider"]))
					submitTool = mapOf(tools[astr(def["tool"])])
					for _, name := range []string{jobTool(def, "job_status"), jobTool(def, "recent_tasks")} {
						tool := mapOf(tools[name])
						granted := false
						for _, t := range anys(c["tools"]) {
							granted = granted || t == name
						}
						if tool["method"] != "GET" || tool["idempotent"] != true || !granted {
							return fmt.Errorf("job read tool %s must be a granted idempotent GET", name)
						}
					}
					return nil
				})
				if err != nil {
					return failNow("step:job-tools", err.Error())
				}
			}
			s["arguments"] = resolved
			if err := tx(func(q *Request) error { return putRun(q, run) }); err != nil {
				return false, err
			}
			inflight.Store(row, true)
			defer inflight.Delete(row)
			tool := astr(def["tool"])
			line := connCallFor(ctx, a, run, connection, row, tool, resolved)
			job := def["mode"] == "job"
			// A job submit without a clear answer: a task created since the call started is adopted; otherwise unknown, never resent.
			code := deref(line.ErrorCode)
			if job && line.Outcome != contract.CallOutcomeOk && (line.Outcome == contract.CallOutcomeUnknown || http5xx.MatchString(code)) {
				recent := connCallFor(ctx, a, run, connection, row, jobTool(def, "recent_tasks"), doc{})
				listed := callOutput(recent)
				tasks := anys(listed)
				if tasks == nil {
					tasks = anys(mapOf(listed)["result"])
				}
				owned := map[string]bool{}
				runTx(ctx, a, nil, false, "", func(q *Request) error {
					rs, err := queryRows(q, "SELECT data->>'jobId' AS id FROM execution WHERE data ? 'jobId'")
					for _, r := range rs {
						owned[astr(r["id"])] = true
					}
					return err
				})
				started, _ := numOK(s["startedAt"])
				mine := []doc{}
				if recent.Outcome == contract.CallOutcomeOk {
					for _, x := range tasks {
						t := mapOf(x)
						id, ok := t["id"].(string)
						c, _ := numOK(t["created_at"])
						if ok && id != "" && jobCreatedDuring(c, started, float64(nowMs())) && !owned[id] && jobMatches(t, mapOf(resolved), submitTool) {
							mine = append(mine, t)
						}
					}
				}
				if len(mine) == 1 {
					line.Outcome, line.Output = contract.CallOutcomeOk, jsonBytes(doc{"jobId": mine[0]["id"]})
					s["adoptedAfter"] = code
				} else {
					why := fmt.Sprintf("%d matching tasks", len(mine))
					if recent.Outcome != contract.CallOutcomeOk {
						why = "recent tasks unreadable"
					}
					return failNow("unknown:submit-unconfirmed", fmt.Sprintf("Job submit %s; %s; not sent again", code, why))
				}
			}
			if line.Outcome != contract.CallOutcomeOk {
				return failNow(problemOf(string(line.Outcome), code), fmt.Sprintf("Connection call %s: %s", line.Outcome, code))
			}
			if job {
				out := mapOf(callOutput(line))
				if _, ok := out["jobId"].(string); !ok {
					if r, ok := out["result"].(string); ok {
						out = doc{"jobId": r}
					}
				}
				jobID, ok := out["jobId"].(string)
				if !ok {
					return failNow("step:no-job-id", "Job submit returned no jobId")
				}
				s["jobId"], s["polls"] = jobID, 0
				return false, tx(func(q *Request) error {
					if err := putRun(q, run); err != nil {
						return err
					}
					return exec(q, "UPDATE execution SET data=data||jsonb_build_object('jobId',$2::text) WHERE id=$1", row, jobID)
				})
			}
			out := callOutput(line)
			if m := def["output"]; m != nil {
				// The step's output is the mapping over the reply (${reply.<path>}); the reply's usage stays the step's usage.
				usage := mapOf(out)["usage"]
				mapped, err := resolveRefs(m, nil, doc{"": out}, false)
				if err != nil {
					s["reply"] = out // the paid answer stays inspectable on the failed step
					return failNow(err.Error(), "Output mapping: a reference to the reply is missing")
				}
				return true, tx(func(q *Request) error { return completeStep(q, run, i, mapped, doc{"usage": usage}) })
			}
			return true, tx(func(q *Request) error {
				return completeStep(q, run, i, out, doc{"usage": mapOf(out)["usage"]})
			})
		}
		var execID string
		err = tx(func(q *Request) error {
			inputs, err := inputsFor(q, run, i)
			if err != nil {
				return err
			}
			if kind == "agent" {
				execID, err = startAgentStep(q, run, def, row, astr(resolved), inputs, mapOf(run["overrides"]))
			} else {
				host := astr(def["host"])
				if h := mapOf(run["overrides"])["host"]; h != nil {
					host = fmt.Sprint(h)
				}
				execID, err = startScript(q, run, row, astr(resolved), def["timeout_ms"], host, inputs)
			}
			if err != nil {
				return err
			}
			s["executionId"] = execID
			return putRun(q, run)
		})
		if err != nil {
			problem := "step:start"
			if he, ok := err.(*HTTPError); ok && (he.Code == 409 || he.Code == 503) || strings.Contains(strings.ToLower(err.Error()), "reachable store") {
				problem = "infra:start"
			}
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			// Re-read: the failed transaction left nothing; the step fails from its preparing state.
			delete(s, "executionId")
			return failNow(problem, msg)
		}
		return false, nil
	}
	if kind != "api" && s["executionId"] == nil {
		return failNow("unknown:start", "Coordinator stopped while starting the step; not replayed")
	}
	if kind == "api" && def["mode"] != "job" {
		if _, busy := inflight.Load(row); busy {
			return false, nil
		}
		return failNow("unknown:interrupted", "Coordinator restarted during the call; outcome unknown, not replayed")
	}
	if kind == "api" {
		if _, busy := inflight.LoadOrStore(row, true); busy {
			return false, nil
		}
		defer inflight.Delete(row)
		line := connCallFor(ctx, a, run, connection, row, jobTool(def, "job_status"), doc{"jobId": s["jobId"]})
		p, _ := numOK(s["polls"])
		s["polls"] = p + 1
		if line.Outcome != contract.CallOutcomeOk {
			if line.Outcome == contract.CallOutcomeUnknown {
				return false, nil
			}
			code := deref(line.ErrorCode)
			return failNow(problemOf(string(line.Outcome), code), "Job poll failed: "+code)
		}
		out := mapOf(callOutput(line))
		// Meshy's task shape: status SUCCEEDED | FAILED | CANCELED | EXPIRED | PENDING | IN_PROGRESS.
		if st, ok := out["status"].(string); ok && out["state"] == nil {
			state := "running"
			if st == "SUCCEEDED" {
				state = "succeeded"
			} else if st == "FAILED" || st == "CANCELED" || st == "EXPIRED" {
				state = "failed"
			}
			urls := out["model_urls"]
			if urls == nil {
				urls = doc{}
			}
			var usage any
			if c, ok := numOK(out["consumed_credits"]); ok {
				usage = doc{"credits": c}
			}
			out = doc{"state": state, "output": doc{"jobId": s["jobId"], "request": s["arguments"], "result": out["result"], "model_urls": urls, "thumbnail_url": out["thumbnail_url"], "image_urls": out["image_urls"], "receipt": out}, "usage": usage, "error": mapOf(out["task_error"])["message"]}
		}
		switch out["state"] {
		case "succeeded":
			result := out["output"]
			if result == nil {
				result = doc{}
			}
			return true, tx(func(q *Request) error {
				if err := completeStep(q, run, i, result, doc{"usage": out["usage"]}); err != nil {
					return err
				}
				if u := mapOf(out["usage"]); u != nil {
					return recordJob(q, run, row, astr(def["tool"]), astr(s["jobId"]), connection, u)
				}
				return nil
			})
		case "failed":
			code := out["error"]
			if code == nil {
				code = "job-failed"
			}
			return failNow(problemOf("error", fmt.Sprint(code)), "Job failed")
		}
		return false, tx(func(q *Request) error { return putRun(q, run) })
	}
	// Agent or script step running: wait for its execution.
	var e doc
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { e, err = maybe(q, s["executionId"], "execution"); return }); err != nil || e == nil {
		return false, err
	}
	st := astr(e["state"])
	if !terminal(e) {
		return false, nil // an unconfirmed running state is not final: the host may still report the real outcome
	}
	if st != "succeeded" {
		problem := "step:cancelled"
		if st != "cancelled" {
			code := mapOf(e["error"])["code"]
			if code == nil {
				code = "failed"
			}
			problem = fmt.Sprint("step:", code)
			if unconfirmed(e) { // the host lost the step: its outcome is unknown
				problem = fmt.Sprint("unknown:", code)
			}
		}
		msg := astr(mapOf(e["error"])["message"])
		if msg == "" {
			msg = "Step execution " + st
		}
		return failNow(problem, msg)
	}
	var arts []doc
	var pending int
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM artifact WHERE execution_id=$1 AND state='pending'", s["executionId"]).Scan(&pending); err != nil {
			return err
		}
		var err error
		arts, err = artifactsOf(q, astr(s["executionId"]))
		return err
	}); err != nil {
		return false, err
	}
	if pending > 0 {
		return false, nil // uploads can finish after the completion receipt
	}
	if err := tx(func(q *Request) error { _, err := connIngest(q); return err }); err != nil {
		return false, err
	}
	var output any
	for _, art := range arts {
		if ownOutput.MatchString(astr(art["path"])) {
			if b, err := artifactBytes(ctx, a, astr(art["id"]), 64<<20); err == nil {
				if json.Unmarshal(b, &output) != nil {
					output = nil
				}
			}
			break
		}
	}
	if output == nil {
		files := []any{}
		for _, art := range arts {
			p := strings.TrimPrefix(astr(art["path"]), "output/")
			dot := false
			for _, x := range strings.Split(p, "/") {
				dot = dot || strings.HasPrefix(x, ".")
			}
			if !dot {
				files = append(files, p)
			}
		}
		output = doc{"files": files}
	}
	return true, tx(func(q *Request) error {
		u, err := creditsOf(q, astr(s["executionId"]))
		if err != nil {
			return err
		}
		return completeStep(q, run, i, output, doc{"usage": u})
	})
}

// callOutput: the provider's reply body of an in-process call, decoded; nil when there is none.
func callOutput(l contract.CallLine) any {
	var v any
	json.Unmarshal(l.Output, &v)
	return v
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
	gatedLongRoute("POST /api/pipelines/{id}/runs", func(q *Request) (any, error) {
		var out doc
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
			var err error
			out, err = pipelineRunCreate(q)
			return err
		})
		if err == nil && out["state"] == "running" {
			pipelineAdvance(q.app, astr(out["id"]))
		}
		return out, err
	})
	// pipeline_rerun: a finished run again on the current definition (or the caller's draft); mode failed reuses the steps
	// that succeeded and would run the same way.
	longOp("pipeline_rerun", typedOp(func(q *Request, r *contract.PipelineRerunRequest) (contract.PipelineRerunReply, error) {
		out := contract.PipelineRerunReply{Reused: []string{}, Definitions: []contract.DefinitionRef{}}
		var run doc
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
		if run["state"] == "running" {
			pipelineAdvance(q.app, astr(run["id"]))
		}
		out.RunId = astr(run["id"])
		for _, x := range anys(run["reused"]) {
			out.Reused = append(out.Reused, astr(x))
		}
		if d, err := asT[[]contract.DefinitionRef](run["definitions"]); err == nil && d != nil {
			out.Definitions = d
		}
		return out, nil
	}))
	// pipeline_run_create: the route as an operation, plus draft (the caller's draft; architect only).
	longOp("pipeline_run_create", typedOp(func(q *Request, r *contract.PipelineRunCreateRequest) (contract.PipelineRunCreateReply, error) {
		var out contract.PipelineRunCreateReply
		var run doc
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
			defer func() { q.tx = nil }()
			b := doc{"requestId": r.RequestId}
			if r.ProjectId != nil {
				b["projectId"] = *r.ProjectId
			}
			if r.ExampleArtifactId != nil {
				b["exampleArtifactId"] = *r.ExampleArtifactId
			}
			if r.Example != nil {
				b["example"] = rawAny(jsonBytes(r.Example))
			}
			if r.Overrides != nil {
				b["overrides"] = rawAny(jsonBytes(r.Overrides))
			}
			var draft doc
			var base int64
			// A repeated requestId answers its run, even after the draft was saved or changed.
			if old, err := runsWhere(q, "data->>'requestId'=$1 AND data->>'pipeline'=$2", r.RequestId, r.PipelineId+".json"); err != nil {
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
		if run["state"] == "running" {
			pipelineAdvance(q.app, astr(run["id"]))
		}
		out.RunId = astr(run["id"])
		out.Definitions, err = asT[[]contract.DefinitionRef](run["definitions"])
		if out.Definitions == nil {
			out.Definitions = []contract.DefinitionRef{}
		}
		return out, err
	}))
	onStart(func(ctx context.Context, a *App) {
		// On start, running v2 runs move on (an interrupted sync call becomes unknown, never replayed).
		pipelineTick(ctx, a)
		every(ctx, "pipelines", envMs("FLEET_PIPELINE_TICK_MS", 5000), func(ctx context.Context) error { pipelineTick(ctx, a); labReconcileRunning(ctx, a); return nil })
	})
}

func pipelineRunCreate(q *Request) (doc, error) {
	return pipelineRunFrom(q, q.r.PathValue("id"), body(q), nil, 0)
}

// pipelineRunFrom: a generic run of pipeline id from body b (requestId, projectId, exampleArtifactId, example, overrides);
// draft, when set, is the caller's draft content to run instead of the head.
func pipelineRunFrom(q *Request, pipelineID string, b doc, draft doc, draftBase int64) (doc, error) {
	{
		rid, ok := b["requestId"].(string)
		if !ok || rid == "" || len(rid) > 200 || !keysOnly(b, "requestId", "projectId", "exampleArtifactId", "example", "overrides") {
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
		sig := doc{}
		for k, v := range b {
			sig[k] = v
		}
		// The request's identity includes the content it ran: the head's, or the draft's (fleet-definition-editors.md).
		sig["contentSha"] = sha256Text(string(head))
		if draft != nil {
			t, _ := canonicalText(jsonBytes(draft))
			sig["contentSha"], sig["draft"] = sha256Text(t), true
		}
		signature := sha256hex([]byte(canonical(sig)))
		old, err := runsWhere(q, "data->>'requestId'=$1 AND data->>'pipeline'=$2", rid, file)
		if err != nil {
			return nil, err
		}
		if len(old) > 0 {
			if old[0]["signature"] != signature {
				return nil, fail(409, "Request identity conflict")
			}
			return old[0], nil
		}
		example := mapOf(b["example"])
		if example == nil {
			example = doc{}
		}
		if id, ok := b["exampleArtifactId"].(string); ok && id != "" {
			ex, err := labExampleGet(q, id)
			if err != nil {
				return nil, err
			}
			if ex["state"] != "approved" {
				return nil, fail(409, "Example not approved")
			}
			example = mapOf(ex["example"])
		}
		delete(example, "references") // a run never carries the known-good answers
		overrides := mapOf(b["overrides"])
		if overrides == nil {
			overrides = doc{}
		}
		// Fix: overrides take the Lab variant keys only; host, account and profile pick machines and credentials: owner only.
		if !keysOnly(overrides, labOverrides...) {
			return nil, fail(400, "overrides are "+strings.Join(labOverrides, ", "))
		}
		if q.person != nil && !fullAccess(q.person) && (overrides["host"] != nil || overrides["account"] != nil || overrides["profile"] != nil) {
			return nil, fail(403, "Only the owner overrides host, account or profile")
		}
		start := doc{"file": file, "projectId": b["projectId"], "requestId": rid, "example": example, "overrides": overrides, "extra": doc{"signature": signature}}
		if draft != nil {
			start["draftDefinition"], start["draftBase"] = draft, draftBase
		}
		run, err := pipelineStart(q, start)
		if err != nil {
			return nil, err
		}
		return run, nil
	}
}

// Pipeline and Lab seams: the person gate on Lab and pipeline operations, long operations (their own transactions), the
// question answer hooks, pipeline_run and direct_search, and the agent step start.

// longOperations: operations that open their own transactions (q.tx nil). runOp (ops.go) runs them without one.
var longOperations = map[string]handler{}

// Answer checks run before a question's answer is recorded and the after steps run after, in the same transaction:
// Lab, then budgets. question is the row (snake_case columns); in after, its answer column holds the answer.
func runAnswerChecks(q *Request, question doc, answer string) error {
	if err := labAnswerCheck(q, question, answer); err != nil {
		return err
	}
	if err := keyAnswerCheck(q, question, answer); err != nil {
		return err
	}
	return budgetAnswerCheck(question, answer, q.person)
}

func runAnswerAfter(q *Request, id string) error {
	row, err := first(q, "SELECT * FROM question WHERE id=$1", id)
	if err != nil || row == nil {
		return err
	}
	if err := labAnswerAfter(q, row); err != nil {
		return err
	}
	return budgetAnswerAfter(q, row)
}

// gatedOp: an operation behind the same person gate as every other operation.
func gatedOp(name string, read bool, h handler) {
	operation(name, read, func(q *Request) (any, error) {
		if err := q.app.guardOp(q, name, q.body); err != nil {
			return nil, err
		}
		return h(q)
	})
}

// longOp: a long operation: POST /api/ops/<name> without a surrounding transaction, listed among the tools, and run by
// runOp without one.
func longOp(name string, h handler) {
	gated := func(q *Request) (any, error) {
		if err := q.app.guardOp(q, name, q.body); err != nil {
			return nil, err
		}
		return h(q)
	}
	longRoute("POST /api/ops/"+name, gated)
	longOperations[name] = gated
	operation(name, false, func(q *Request) (any, error) {
		return nil, fail(500, "long operation "+name+" ran inside a transaction")
	})
}

// runPipeline: the selected method's v2 pipeline with the query as the example ({query, prompt}); waits for the run.
func runPipeline(q *Request, invocation string) (any, error) {
	b := body(q)
	if invocation == "direct" {
		b["invocation"] = "direct"
	}
	rid, _ := b["requestId"].(string)
	query, isStr := b["query"].(string)
	_, methodOK := b["methodId"].(string)
	if !keysOnly(b, "projectId", "requestId", "query", "methodId", "invocation") || rid == "" || len(rid) > 200 || !isStr || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 2000 || b["methodId"] != nil && !methodOK {
		return nil, fail(400, "Expected projectId, requestId, query and optional methodId")
	}
	inv := astr(b["invocation"])
	if inv == "" {
		inv = "pipeline"
	}
	var run doc
	old := false
	err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		signature := sha256hex([]byte(canonical(doc{"projectId": b["projectId"], "query": query, "methodId": b["methodId"], "invocation": inv})))
		prev, err := runsWhere(q, "data->>'projectId'=$1 AND data->>'requestId'=$2", astr(b["projectId"]), rid)
		if err != nil {
			return err
		}
		if len(prev) > 0 {
			if prev[0]["signature"] != signature {
				return fail(409, "Request identity conflict")
			}
			run, old = prev[0], true
			return nil
		}
		if inv != "direct" && inv != "pipeline" {
			return fail(400, "Invalid invocation")
		}
		p, err := projectContext(q, astr(b["projectId"]))
		if err != nil {
			return err
		}
		c, err := catalogue(q, astr(b["projectId"]))
		if err != nil {
			return err
		}
		want := astr(b["methodId"])
		source := c["recommendationSource"]
		if want == "" {
			want = astr(c["recommendedMethod"])
		} else {
			source = "explicit"
		}
		var method doc
		for _, m := range anys(c["methods"]) {
			if mapOf(m)["id"] == want {
				method = mapOf(m)
			}
		}
		if method == nil {
			return fail(400, "Selected method unavailable for project")
		}
		run, err = pipelineStart(q, doc{"file": method["pipeline"], "projectId": b["projectId"], "requestId": rid, "example": doc{"query": query, "prompt": query},
			"requestedBy": requester(q), "extra": doc{"signature": signature, "invocation": inv, "selectedMethod": method["id"], "selectionSource": source, "context": p}})
		return err
	})
	if err != nil || old {
		return run, err
	}
	pipelineAdvance(q.app, astr(run["id"]))
	return pipelineWait(q.ctx(), q.app, astr(run["id"]))
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
	for _, inv := range []string{"pipeline", "direct"} {
		inv := inv
		name := map[string]string{"pipeline": "pipeline_run", "direct": "direct_search"}[inv]
		var h handler = func(q *Request) (any, error) { return runPipeline(q, inv) }
		if inv == "pipeline" {
			h = shaped[contract.PipelineRunRequest, contract.PipelineRunReply](h)
		} else {
			h = shaped[contract.DirectSearchRequest, contract.DirectSearchReply](h)
		}
		longOp(name, h)
		projectOps[name] = registeredOp{false, h}
	}
	gatedLongRoute("POST /api/pipeline-runs", func(q *Request) (any, error) { return runPipeline(q, "pipeline") })

}

// profileSkills: the SKILL.md texts of a profile's skills (config/skills/<id>/SKILL.md), as an execution payload carries them.
func profileSkills(profile doc) ([]any, error) {
	skills := []any{}
	for _, x := range anys(profile["skills"]) {
		id, _ := x.(string)
		if !skillID.MatchString(id) {
			return nil, fail(400, "Invalid skill")
		}
		text, err := os.ReadFile(filepath.Join(configDir(), "skills", id, "SKILL.md"))
		if err != nil {
			return nil, fail(400, "Unknown skill "+id)
		}
		skills = append(skills, doc{"id": id, "text": string(text)})
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
	sub.body = jsonBytes(doc{"projectId": projectID, "methodId": methodID})
	return projectOps["recommendation_set"].h(&sub)
}

func projectKnown(q *Request, id string) bool { _, err := catalogue(q, id); return err == nil }

// Agent step: the step's trusted profile with the variant overrides, its skills, a session of the
// run, and an ordinary queued execution.
// stepProfile: the profile an agent step runs: its profile (or the override), with the variant's model and skills.
func stepProfile(q *Request, step, overrides doc) (string, doc, error) {
	pid := astr(step["profile"])
	if overrides["profile"] != nil {
		pid = astr(overrides["profile"])
	}
	profile, err := readTrustedProfile(q, pid)
	if err != nil {
		return pid, nil, err
	}
	if v, ok := overrides["model"]; ok {
		profile["model"] = v
	}
	if v, ok := overrides["skills"]; ok {
		profile["skills"] = v
	}
	return pid, profile, nil
}

func startAgentStep(q *Request, run, step doc, stepExecutionID, prompt string, inputs []any, overrides doc) (string, error) {
	if id := astr(run["experimentId"]); id != "" {
		requester, err := labRequester(q, id)
		if err != nil {
			return "", err
		}
		if astr(mapOf(run["requestedBy"])["email"]) != requester.Email {
			return "", failCode(409, "Lab run requester provenance differs", "requester:conflict")
		}
	}

	_, profile, err := stepProfile(q, step, overrides)
	if err != nil {
		return "", err
	}
	web := "connections"
	if v, ok := overrides["web_route"]; ok {
		if v != "connections" && v != "builtin" {
			return "", fail(400, "web_route must be connections or builtin")
		}
		web = astr(v)
	}
	skills, err := profileSkills(profile)
	if err != nil {
		return "", err
	}
	s := doc{"id": newID(), "createdAt": nowMs(), "agentConfig": profile, "pipelineRunId": run["id"]}
	if overrides["account"] != nil {
		s["account"] = overrides["account"]
	}
	if run["projectId"] != nil {
		s["projectId"] = run["projectId"]
	}
	if err := put(q, "session", s); err != nil {
		return "", err
	}
	execution := doc{"pipelineStepId": stepExecutionID, "pipelineRunId": run["id"]}
	if run["experimentId"] != nil {
		execution["experimentId"] = run["experimentId"]
	}
	host := ""
	if overrides["host"] != nil {
		host = fmt.Sprint(overrides["host"])
	}
	e, err := enqueue(q, s, prompt, "pipeline:"+stepExecutionID, doc{"inputs": inputs, "skills": skills, "webRoute": web}, execution, host)
	if err != nil {
		return "", err
	}
	return astr(e["id"]), nil
}

// jobTool names the status or listing tool of a job step's submit tool: Meshy multi_image_to_3d reads multi_job_status.
func jobTool(def doc, name string) string {
	field := map[string]string{"job_status": "status_tool", "recent_tasks": "recent_tool"}[name]
	if override := astr(def[field]); override != "" {
		return override
	}
	if strings.HasPrefix(astr(def["tool"]), "multi_") {
		return "multi_" + name
	}
	return name
}

// draftPipeline: the caller's runnable draft of a live pipeline (architect only): content and base revision.
func draftPipeline(q *Request, pid string) (doc, int64, error) {
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
	var def doc
	return def, d.Base, json.Unmarshal([]byte(d.Content), &def)
}

// pipelineRerun: loads finished run R and starts R2 from the definition to use (R's pipeline head, or the caller's draft).
// Mode all runs every step; mode failed reuses the longest prefix of R's steps where each succeeded, is the same step in
// the new definition, and (agent steps) would run the same profile content.
func pipelineRerun(q *Request, r *contract.PipelineRerunRequest) (doc, error) {
	old, err := getRun(q, r.RunId)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, fail(404, "Unknown run")
	}
	if st := astr(old["state"]); st == "running" || st == "preparing" || st == "waiting" {
		return nil, &HTTPError{Code: 409, Msg: "The run has not finished", Extra: map[string]any{"reason": contract.ConflictReasonWrongState}}
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
	if prev, err := runsWhere(q, "data->>'requestId'=$1 AND data->>'rerunOf'=$2", rid, r.RunId); err != nil {
		return nil, err
	} else if len(prev) > 0 {
		if astr(prev[0]["rerunIdentity"]) != identity {
			return nil, fail(409, "Request identity conflict")
		}
		return prev[0], nil
	}
	// The overrides that pick machines and credentials stay the owner's, as on pipeline_run_create.
	if ov := mapOf(old["overrides"]); q.person != nil && !fullAccess(q.person) && (ov["host"] != nil || ov["account"] != nil || ov["profile"] != nil) {
		return nil, fail(403, "Only the owner reruns a run that overrides host, account or profile")
	}
	pid := astr(old["pipelineId"])
	var def doc
	var base int64
	if draft {
		if def, base, err = draftPipeline(q, pid); err != nil {
			return nil, err
		}
	} else if def, err = loadPipeline(q, pid+".json"); err != nil {
		return nil, err
	}
	reuse := []any{}
	if r.Mode == contract.RerunModeFailed {
		var err error
		reuse, err = succeededPrefix(q, old, def)
		if err != nil {
			return nil, err
		}
	}
	o := doc{"file": pid + ".json", "projectId": old["projectId"], "requestId": rid, "example": old["example"], "overrides": old["overrides"], "rerunOf": r.RunId, "reuse": reuse,
		"extra": rerunInputs(old, identity)}
	if draft {
		o["draftDefinition"], o["draftBase"] = def, base
	}
	return pipelineStart(q, o)
}

// succeededPrefix preserves immutable outputs/execution provenance, without charging copied work again.
func succeededPrefix(q *Request, old, def doc) ([]any, error) {
	reuse := []any{}
	oldSteps, newSteps := anys(old["steps"]), anys(def["steps"])
	oldDef := anys(mapOf(old["definition"])["steps"])
	for i := 0; i < len(oldSteps) && i < len(newSteps) && i < len(oldDef); i++ {
		e := mapOf(oldSteps[i])
		if e["state"] != "succeeded" || canonical(oldDef[i]) != canonical(newSteps[i]) {
			break
		}
		if astr(mapOf(newSteps[i])["kind"]) == "agent" {
			same, err := sameAgentStep(q, astr(e["executionId"]), mapOf(newSteps[i]), mapOf(old["overrides"]))
			if err != nil {
				return nil, err
			}
			if !same {
				break
			}
		}
		// The step's work came from R: its output and execution stay, its spend is R's (not counted again here).
		c := doc{}
		for k, v := range e {
			if k != "calls" && k != "usage" {
				c[k] = v
			}
		}
		reuse = append(reuse, c)
	}
	return reuse, nil
}

// sameAgentStep: the profile an agent step would run now has the content the earlier step's execution pinned.
func sameAgentStep(q *Request, executionID string, step, overrides doc) (bool, error) {
	var pinned []contract.DefinitionRef
	var raw []byte
	if err := q.tx.QueryRow(q.ctx(), "SELECT definitions FROM execution WHERE id=$1", executionID).Scan(&raw); err != nil {
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
func withArguments(def, overrides doc) any {
	extra := mapOf(mapOf(overrides["arguments"])[astr(def["id"])])
	base := mapOf(def["arguments"])
	if extra == nil {
		return def["arguments"]
	}
	out := doc{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// Reruns retain immutable staged inputs even when the example contains no text files.
func rerunInputs(old doc, identity string) doc {
	return doc{"rerunIdentity": identity, "exampleInputs": old["exampleInputs"]}
}

// jobMatches requires every declared input; output views never establish request identity.
func jobMatches(task, args, tool doc) bool {
	primary := astr(tool["adoptMatch"])
	if primary == "" {
		return false
	}
	fields := []string{primary}
	for _, f := range anys(tool["adoptFields"]) {
		name, ok := f.(string)
		if !ok || name == "" {
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
		if !sent || !returned || expected == nil || actual == nil || !bytes.Equal(jsonBytes(expected), jsonBytes(actual)) {
			return false
		}
	}
	switch input := args[primary].(type) {
	case string:
		return strings.TrimSpace(input) != ""
	case []any:
		if len(input) == 0 {
			return false
		}
		for _, value := range input {
			if s, ok := value.(string); !ok || strings.TrimSpace(s) == "" {
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
