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
	version, _ := numOK(d["version"])
	if version != 2 && version != 3 {
		return bad("version must be 2 or 3")
	}
	_, hasResultStep := d["resultStep"]
	if !keysOnly(d, "version", "id", "steps", "resultStep") || version == 2 && hasResultStep {
		return bad("root keys or resultStep version")
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
		if when, has := s["when"]; has {
			if version != 3 {
				return bad("when requires version 3")
			}
			if err := validatePipelineCondition(when, seen); err != nil {
				return bad("step " + id + ": " + err.Error())
			}
		}
		seen[id] = true
		kind := astr(s["kind"])
		keys, ok := allowed[kind]
		if !ok {
			return bad(fmt.Sprintf("step kind %v", s["kind"]))
		}
		if version == 3 {
			keys = append(append([]string{}, keys...), "when")
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
	if version == 3 {
		last := mapOf(steps[len(steps)-1])
		_, conditional := last["when"]
		if astr(d["resultStep"]) != astr(last["id"]) || last["kind"] != "script" || conditional {
			return bad("resultStep must name the last unconditional script step")
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

// putRun writes the current plan and only executions that have actually been selected.
func putRun(q *Request, run doc) error {
	if err := guardPipelineWrite(q, run); err != nil {
		return err
	}
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
	_, _, pipeID, pipeRev := definitionScalars(run["definitions"], nil)
	if pipeID == nil {
		pipeID = nilIf(astr(run["pipelineId"]))
	}
	dir := nilIf(firstString(run["directedBy"], mapOf(run["requestedBy"])["email"]))
	taskID := nilIf(firstString(run["deliveryId"], run["taskId"]))
	sessID := nilIf(astr(run["sessionId"]))
	inserted, err := executionInsert(q, executionRow(doc{"id": run["id"], "kind": "pipeline_run", "state": state, "observation": obs, "projectId": run["projectId"],
		"startedAt": run["startedAt"], "endedAt": run["finishedAt"], "data": rest, "configRef": cfg, "experimentId": run["experimentId"], "variant": run["variant"],
		"taskId": taskID, "sessionId": sessID, "directedBy": dir, "pipelineId": pipeID, "pipelineRevision": pipeRev, "parentExecutionId": run["parentExecutionId"]}))
	if err != nil {
		return err
	}
	if !inserted {
		if err := exec(q, `UPDATE execution SET observation=$2,project_id=$3,started_at=$4,ended_at=$5,data=$6::jsonb || CASE WHEN data ? 'projectAssessments' THEN jsonb_build_object('projectAssessments',data->'projectAssessments') ELSE '{}'::jsonb END,
		  config_ref=CASE WHEN variant IS NULL THEN $7 ELSE config_ref END,experiment_id=coalesce(experiment_id,$8),variant=coalesce(variant,$9),
		  task_id=coalesce(task_id,$10),session_id=coalesce(session_id,$11),directed_by=coalesce($12,directed_by),
		  pipeline_id=coalesce($13,pipeline_id),pipeline_revision=coalesce($14,pipeline_revision),parent_execution_id=coalesce(parent_execution_id,$15) WHERE id=$1`,
			run["id"], obs, projectOrNil(run["projectId"]), msTime(run["startedAt"]), msTime(run["finishedAt"]), jsonBytes(rest), jsonBytes(cfg), run["experimentId"], run["variant"], taskID, sessID, dir, pipeID, pipeRev, run["parentExecutionId"]); err != nil {
			return err
		}
		if _, err := executionTransition(q, astr(run["id"]), executionEvent{To: state, KeepEnd: true}); err != nil {
			return err
		}
	}
	if state == "running" {
		// A running run has its row on the kit (pipeline_work.go); a new one wakes the queue.
		if n, err := q.db().PipelineWorkEnsure(q.ctx(), astr(run["id"])); err != nil {
			return err
		} else if n > 0 {
			if err := kitNotify(q, "pipeline"); err != nil {
				return err
			}
		}
	}
	if run["definitions"] != nil {
		if err := exec(q, `UPDATE execution SET definitions=$2,pipeline_id=coalesce($3,pipeline_id),pipeline_revision=coalesce($4,pipeline_revision) WHERE id=$1`,
			run["id"], jsonBytes(run["definitions"]), pipeID, pipeRev); err != nil {
			return err
		}
	}
	for i, x := range anys(run["steps"]) {
		entry := pipelinePlanEntry(run, i)
		if entry == nil || entry["activation"] != "selected" || mapOf(entry["executionRef"])["mode"] == "reused" {
			continue
		}
		s := mapOf(x)
		st, sobs, err := runState(s)
		if err != nil {
			return err
		}
		data := doc{"index": i, "traceparent": run["traceparent"]}
		for k, v := range s {
			if k != "state" && k != "observation" {
				data[k] = v
			}
		}
		var ended any
		if executionEnds(st) {
			ended = msTime(s["finishedAt"])
		}
		row := stepRow(run, i)
		inserted, err := executionInsert(q, executionRow(doc{"id": row, "kind": "pipeline_step", "state": st, "observation": sobs, "projectId": run["projectId"], "parentExecutionId": run["id"],
			"startedAt": s["startedAt"], "endedAt": ended, "data": data, "taskId": taskID, "sessionId": sessID, "directedBy": dir, "pipelineId": pipeID, "pipelineRevision": pipeRev,
			"stepId": nilIf(astr(s["id"])), "experimentId": run["experimentId"], "variant": run["variant"]}))
		if err != nil {
			return err
		}
		if inserted {
			continue
		}
		if err := exec(q, `UPDATE execution SET observation=$2,project_id=$3,started_at=$4,ended_at=$5,data=$6,
		  task_id=coalesce(task_id,$7),session_id=coalesce(session_id,$8),directed_by=coalesce($9,directed_by),experiment_id=$10,variant=$11,
		  pipeline_id=coalesce($12,pipeline_id),pipeline_revision=coalesce($13,pipeline_revision),step_id=coalesce($14,step_id) WHERE id=$1`,
			row, sobs, projectOrNil(run["projectId"]), msTime(s["startedAt"]), ended, jsonBytes(data), taskID, sessID, dir, run["experimentId"], run["variant"], pipeID, pipeRev, nilIf(astr(s["id"]))); err != nil {
			return err
		}
		if _, err := executionTransition(q, row, executionEvent{To: st, KeepEnd: true}); err != nil {
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
	for _, run := range runs {
		if err := requirePipelinePlan(run); err != nil {
			return nil, err
		}
		for _, v := range pipelinePlanEntries(run) {
			entry := mapOf(v)
			if entry["activation"] == "selected" {
				ids = append(ids, astr(mapOf(entry["executionRef"])["executionId"]))
			}
		}
	}
	records, err := queryRows(q, "SELECT id AS step,parent_execution_id AS owner,project_id AS project,"+execJSON+" AS data FROM execution WHERE kind='pipeline_step' AND id=ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]doc{}
	for _, record := range records {
		byID[astr(record["step"])] = record
	}
	calls, err := queryRows(q, `SELECT e.parent_execution_id AS step,e.id,e.data->>'tool' AS tool,e.data->>'connection' AS connection,t.connection_cost AS cost,
  coalesce(t.connection_cost_basis,'unavailable') AS basis FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id
  WHERE e.kind='tool_run' AND e.parent_execution_id=ANY($1) ORDER BY e.seq`, ids)
	if err != nil {
		return nil, err
	}
	byStep := map[string][]any{}
	for _, c := range calls {
		key := astr(c["step"])
		byStep[key] = append(byStep[key], doc{"id": c["id"], "tool": c["tool"], "connection": c["connection"], "cost": c["cost"], "basis": c["basis"]})
	}
	for _, run := range runs {
		list := []any{}
		definition := anys(mapOf(run["definition"])["steps"])
		for i, v := range pipelinePlanEntries(run) {
			entry, def := mapOf(v), mapOf(definition[i])
			step := doc{"id": entry["id"], "kind": def["kind"]}
			if def["kind"] == "api" {
				step["tool"], step["connection"] = def["tool"], def["connection"]
			}
			if entry["activation"] == "selected" {
				ref := mapOf(entry["executionRef"])
				record := byID[astr(ref["executionId"])]
				if record == nil || record["owner"] != ref["sourceRunId"] || !same(record["project"], run["projectId"]) || mapOf(record["data"])["id"] != ref["sourceStepId"] {
					return nil, fail(409, "Missing or foreign selected pipeline execution")
				}
				for k, value := range mapOf(record["data"]) {
					if k != "index" {
						step[k] = value
					}
				}
				step["id"], step["kind"] = entry["id"], def["kind"]
				step["calls"] = append([]any{}, byStep[astr(ref["executionId"])]...)
				if ref["mode"] == "reused" {
					step["reusedFrom"] = ref["sourceRunId"]
				}
			}
			list = append(list, step)
		}
		run["steps"] = list
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
	if _, ok := executionTransitions[state]; !ok {
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
	reused := []string{}
	run := doc{"id": id, "traceparent": originTrace(q), "projectId": o["projectId"], "requestId": o["requestId"], "requestedBy": or("requestedBy", requester(q)), "definition": def,
		"definitionSha256": sha256hex([]byte(canonical(def))), "pipelineId": def["id"], "pipeline": o["file"], "example": or("example", doc{}), "overrides": or("overrides", doc{}),
		"state": "running", "startedAt": nowMs(), "durationMs": nil, "usage": nil, "result": nil, "steps": steps}
	// Caller lineage is server-derived, never a pipeline parameter.
	if strings.HasPrefix(q.agent, "execution:") {
		source := strings.TrimPrefix(q.agent, "execution:")
		parent, e := first(q, `SELECT e.project_id,coalesce(e.task_id,s.task_id) AS task_id,e.session_id,e.directed_by
			FROM execution e LEFT JOIN session s ON s.id=e.session_id WHERE e.id=$1`, source)
		if e != nil {
			return nil, e
		}
		if parent == nil || parent["project_id"] != run["projectId"] {
			return nil, fail(403, "Pipeline caller project mismatch")
		}
		if parent["task_id"] != nil {
			task, e := first(q, "SELECT project_id FROM task WHERE id=$1", parent["task_id"])
			if e != nil {
				return nil, e
			}
			if task == nil || task["project_id"] != run["projectId"] {
				return nil, fail(403, "Pipeline caller task project mismatch")
			}
		}
		if parent["session_id"] != nil {
			session, e := get(q, parent["session_id"], "session")
			if e != nil {
				return nil, e
			}
			if session["projectId"] != run["projectId"] || session["taskId"] != nil && session["taskId"] != parent["task_id"] {
				return nil, fail(403, "Pipeline caller session ownership mismatch")
			}
		}
		if strings.TrimSpace(astr(parent["directed_by"])) == "" {
			return nil, fail(403, "Pipeline caller has no initiating actor provenance")
		}
		run["parentExecutionId"], run["taskId"], run["sessionId"], run["directedBy"] = source, parent["task_id"], parent["session_id"], parent["directed_by"]
	}
	if o["experimentId"] != nil {
		run["experimentId"] = o["experimentId"]
	}
	if o["variant"] != nil {
		run["variant"] = o["variant"]
	}
	for k, v := range mapOf(o["extra"]) {
		run[k] = v
	}
	if err := taskAllowsWork(q, firstString(run["deliveryId"], run["taskId"])); err != nil {
		return nil, err
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
	if err := initPipelinePlan(run); err != nil {
		return nil, err
	}
	if len(anys(o["reuse"])) > 0 {
		old, err := getRun(q, astr(o["rerunOf"]))
		if err != nil || old == nil {
			return nil, fail(409, "Reused source run is unavailable")
		}
		verified, err := succeededPrefix(q, old, def)
		if err != nil {
			return nil, err
		}
		if len(verified) < len(anys(o["reuse"])) {
			return nil, fail(409, "Reused prefix proof changed")
		}
		for i := range anys(o["reuse"]) {
			sourceEntry := pipelinePlanEntry(old, i)
			entry := pipelinePlanEntry(run, i)
			definition := mapOf(anys(def["steps"])[i])
			when := mapOf(definition["when"])
			var output any
			if when != nil {
				for _, value := range steps[:i] {
					previous := mapOf(value)
					if previous["id"] == mapOf(when["source"])["step"] {
						output = previous["output"]
					}
				}
			}
			decision := pipelineConditionDecision(when, output, nowMs())
			if decision["outcome"] != sourceEntry["activation"] {
				return nil, fail(409, "Reused prefix guard proof differs")
			}
			if evidence := mapOf(sourceEntry["decision"])["sourceArtifact"]; evidence != nil {
				decision["sourceArtifact"] = evidence
			}
			entry["activation"], entry["decision"] = decision["outcome"], decision
			if entry["activation"] == "not_selected" {
				continue
			}
			ref := doc{}
			for k, v := range mapOf(sourceEntry["executionRef"]) {
				ref[k] = v
			}
			ref["mode"] = "reused"
			entry["executionRef"], entry["inputs"] = ref, sourceEntry["inputs"]
			step := mapOf(verified[i])
			step["reusedFrom"] = ref["sourceRunId"]
			steps[i] = step
			reused = append(reused, astr(entry["id"]))
		}
		run["reused"] = reused
	}
	return run, putRun(q, run)
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
func pipelineCancelIn(q *Request, runID string) (string, doc, error) {
	run, err := getRun(q, runID)
	if err != nil || run == nil || run["state"] != "running" {
		return "", nil, err
	}
	own := ""
	for i, value := range pipelinePlanEntries(run) {
		entry := mapOf(value)
		if entry["activation"] != "selected" || mapOf(entry["executionRef"])["mode"] == "reused" {
			continue
		}
		step := mapOf(anys(run["steps"])[i])
		if step["state"] != "running" && step["state"] != "preparing" && step["state"] != "waiting" {
			continue
		}
		dispatch := mapOf(entry["dispatch"])
		if dispatch["phase"] == "handed_off" {
			own = astr(step["executionId"])
			step["observation"], run["observation"] = "unconfirmed", "unconfirmed"
		} else if native := astr(step["executionId"]); native != "" {
			if _, err := commandStop(q, commandStopFilter{executionID: &native, statuses: []db.CommandStatus{db.CommandStatusPending}}); err != nil {
				return "", nil, err
			}
			if _, err := executionTransition(q, native, executionEvent{To: "cancelled", From: []string{"preparing", "waiting"}}); err != nil && !errors.Is(err, errNotFound) {
				return "", nil, err
			}
		}
		step["state"], step["finishedAt"] = "cancelled", nowMs()
	}
	return own, run, finishRun(q, run, "cancelled", "cancelled")
}

func finishRun(q *Request, run doc, state, problem string) error {
	if state == "succeeded" {
		if err := bindPipelineResult(q, run); err != nil {
			state, problem = "failed", "result_invalid"
		}
	}
	if state != "succeeded" {
		delete(run, "canonicalResult")
		code := "prior_step_failed"
		if state == "cancelled" {
			code = "run_cancelled"
		}
		for _, value := range pipelinePlanEntries(run) {
			entry := mapOf(value)
			if entry["activation"] == "pending" {
				entry["activation"], entry["problem"] = "blocked", doc{"code": code, "message": "Run ended before this entry could activate"}
			}
		}
	}
	run["state"], run["finishedAt"] = state, nowMs()
	st, _ := numOK(run["startedAt"])
	run["durationMs"] = nowMs() - int64(st)
	credits, used := 0.0, false
	outputs := doc{}
	var search doc
	var stepErr any
	for i, x := range anys(run["steps"]) {
		s := mapOf(x)
		if u := mapOf(s["usage"]); u != nil && mapOf(pipelinePlanEntry(run, i)["executionRef"])["mode"] != "reused" {
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
		if err := exec(q, "UPDATE execution SET problem=$2 WHERE id=$1", run["id"], problem); err != nil {
			return err
		}
	}
	return pipelineEnded(q, run)
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
		return fail(409, "Pipeline output has not been materialized")
	}
	artifact, err := artifactRow(withTx(q), q.app, astr(s["outputArtifactId"]))
	if err != nil {
		return err
	}
	if !pipelineOutputIdentity(artifact, run["projectId"], stepRow(run, i)) || artifact.ContentHash != sha256hex(jsonBytes(output)) {
		return fail(409, "Pipeline output identity or bytes changed")
	}
	dispatch := mapOf(pipelinePlanEntry(run, i)["dispatch"])
	if dispatch["phase"] != "handed_off" {
		return fail(409, "Pipeline output has no committed handoff")
	}
	dispatch["phase"], dispatch["settledAtMs"] = "settled", nowMs()
	for k, v := range extra {
		s[k] = v
	}
	finished := nowMs()
	s["output"], s["state"], s["finishedAt"] = output, "succeeded", finished
	if st, ok := numOK(s["startedAt"]); ok {
		s["durationMs"] = finished - int64(st)
	} else {
		s["durationMs"] = nil
	}
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
		entry := pipelinePlanEntry(run, j)
		if entry == nil || entry["activation"] != "selected" || s["state"] != "succeeded" || s["observation"] == "unconfirmed" {
			continue
		}
		ids := []string{astr(mapOf(entry["executionRef"])["executionId"])}
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
	if len(anys(mapOf(run["example"])["inputs"])) > 0 && run["exampleInputs"] == nil {
		return nil, fail(409, "Pipeline example inputs must be materialized before preparation")
	}
	out := append([]any{}, anys(run["exampleInputs"])...)
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
	dir := nilIf(firstString(run["directedBy"], mapOf(run["requestedBy"])["email"]))
	// A host command session like the others (actor program-worker): it holds the step's host job, not a conversation.
	s := doc{"id": sid, "createdAt": nowMs(), "pipelineRunId": run["id"], "actor": "program-worker"}
	if dir != nil {
		s["directedBy"] = dir
	}
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
	if dir != nil {
		e["directedBy"] = dir
	}
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
	return id, put(q, "command", doc{"id": newID(), "executionId": id, "sessionId": sid, "hostId": hostID, "kind": "script", "payload": payload, "status": "pending", "handedOut": false})
}

// pipelineClaimCall claims a provider call of a pipeline api step in q's transaction (claimPipelineAPI: a submit is handed
// off with the step, an observation of the job mints its own token): the token, or the line that answers the call
// without it. The token is revoked by the step that records the call.
func pipelineClaimCall(q *Request, run doc, row, connection, tool string) (string, bool, *contract.CallLine) {
	unavailable := &contract.CallLine{Tool: tool, Connection: connection, Outcome: contract.CallOutcomeError, ErrorCode: ptr("connection-unavailable")}
	if gatewayURL() == "" || !connName.MatchString(connection) {
		return "", false, unavailable
	}
	if err := exec(q, "SAVEPOINT pipeline_claim"); err != nil {
		unavailable.ErrorCode = ptr("pipeline-authority-denied")
		return "", false, unavailable
	}
	claimed, token, won, idempotent, err := claimPipelineAPI(q, astr(run["id"]), row, connection, tool)
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
	run["plan"] = claimed["plan"]
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

// recordJob: a finished job's usage as one tool_run child of the step.
func recordJob(q *Request, run doc, stepID, tool, jobID, connection string, usage doc) error {
	id := stepID + ":job:0"
	var pTask, pSession, pDir, pPipe, pStep *string
	var pPipeRev *int64
	if err := q.tx.QueryRow(q.ctx(), "SELECT task_id,session_id,directed_by,pipeline_id,pipeline_revision,step_id FROM execution WHERE id=$1", stepID).
		Scan(&pTask, &pSession, &pDir, &pPipe, &pPipeRev, &pStep); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	dir := nilIf(firstString(deref(pDir), run["directedBy"], mapOf(run["requestedBy"])["email"]))
	taskID := any(pTask)
	if pTask == nil {
		taskID = nilIf(firstString(run["deliveryId"], run["taskId"]))
	}
	jobNow := nowMs()
	if _, err := insertExecution(q, doc{
		"id": id, "kind": "tool_run", "state": "succeeded", "projectId": run["projectId"], "parentExecutionId": stepID,
		"experimentId": run["experimentId"], "variant": run["variant"],
		"taskId": taskID, "sessionId": pSession, "directedBy": dir,
		"pipelineId": pPipe, "pipelineRevision": pPipeRev, "stepId": pStep,
		"startedAt": jobNow, "endedAt": jobNow,
		"data": doc{"tool": tool, "requestId": jobID, "outcome": "ok", "errorCode": nil, "usage": usage, "connection": connection},
	}); err != nil {
		return err
	}
	var cost any
	if c, ok := numOK(usage["credits"]); ok {
		cost = doc{"unit": "credits", "credits": c, "connection": connection}
	}
	return writeConnectionCost(q, id, cost)
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
	apply    func(q *Request, run doc) error
}

// pipelineDrafts: the bytes of a finished draft-examples run's example files.
type pipelineDrafts [][]byte

// pipelineEvery: how often a waiting step looks again (a job poll, an agent's uploads) besides the wakes of its
// execution's end; FLEET_PIPELINE_TICK_MS, 0 = only on wakes.
var pipelineEvery = envMs("FLEET_PIPELINE_TICK_MS", 5000)

func pipelineRevision(run doc) int64 {
	r, _ := intOK(mapOf(run["plan"])["revision"])
	return r
}

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
func pipelineEnded(q *Request, run doc) error {
	if err := pipelineRevokeSteps(q, run); err != nil {
		return err
	}
	if id := astr(run["experimentId"]); id != "" {
		if err := labEnqueue(q, id); err != nil {
			return err
		}
	}
	return pipelineSoon(q, astr(run["id"]))
}

// pipelineRevokeSteps revokes every live call token of run's steps (a run that ended).
func pipelineRevokeSteps(q *Request, run doc) error {
	for i := range anys(run["steps"]) {
		if err := revokeTokens(q, stepRow(run, i)); err != nil {
			return err
		}
	}
	return nil
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
		if run["state"] != "running" || pipelineRevision(run) != r.revision {
			return pipelineSoon(q, c.Key)
		}
		return r.apply(q, run)
	}
	if run["state"] == "running" {
		index, err := pipelineNext(q, run)
		if err != nil {
			return err
		}
		if run["state"] == "running" {
			return pipelineStepAt(q, now, c, run, index)
		}
	}
	// An ended run: a draft-examples run that succeeded leaves its examples to read, then the row goes.
	if run["pipelineId"] == "draft-examples" && run["state"] == "succeeded" {
		files, err := pipelineOutputs(q, run, -1)
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
func pipelineNext(q *Request, run doc) (int, error) {
	for i, value := range pipelinePlanEntries(run) {
		entry := mapOf(value)
		if entry["activation"] == "not_selected" {
			continue
		}
		if entry["activation"] == "pending" {
			if err := selectPipelineStep(q, run, i); err != nil {
				return -1, err
			}
			if run["state"] != "running" {
				return -1, nil
			}
			if entry["activation"] == "not_selected" {
				continue
			}
		}
		if entry["activation"] == "blocked" {
			return -1, finishRun(q, run, "failed", "prior_step_failed")
		}
		if mapOf(anys(run["steps"])[i])["state"] == "succeeded" {
			continue
		}
		return i, nil
	}
	return -1, finishRun(q, run, "succeeded", "")
}

// pipelineStepAt moves step i of a running run: a preparing step leaves its inputs to materialize; a running api step
// fails as interrupted (sync) or leaves its job poll; a running agent or script step waits for its execution, then leaves
// its output to read.
func pipelineStepAt(q *Request, now time.Time, c *kitClaim, run doc, i int) error {
	a := q.app
	s := mapOf(anys(run["steps"])[i])
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	row := stepRow(run, i)
	kind := astr(def["kind"])
	runID := astr(run["id"])
	// A run keeps the definition it started with: a step of a kind no longer run (publication) fails, never runs as a
	// script; a job it had started under the step (the coordinator's own publication job) ends with it.
	if kind != "api" && kind != "agent" && kind != "script" {
		jobs, err := queryRows(q, "SELECT id FROM execution WHERE parent_execution_id=$1 AND state IN ('preparing','running','waiting') ORDER BY id FOR UPDATE", row)
		if err != nil {
			return err
		}
		problem := "step:unknown-kind"
		for _, j := range jobs {
			if _, err := executionTransition(q, astr(j["id"]), executionEvent{To: "failed", Problem: &problem}); err != nil {
				return err
			}
		}
		return failStep(q, run, i, "step:unknown-kind", "Step kind "+kind+" is no longer run")
	}
	if s["state"] == "preparing" {
		snapshot, revision := run, pipelineRevision(run)
		c.Call = func(ctx context.Context) any {
			prepared, err := materializePipelineInputs(ctx, a, snapshot, i)
			return pipelineRecord{revision: revision, apply: func(q *Request, run doc) error { return pipelinePrepared(q, c, run, i, prepared, err) }}
		}
		return nil
	}
	if kind != "api" && s["executionId"] == nil {
		return failStep(q, run, i, "unknown:start", "Coordinator stopped while starting the step; not replayed")
	}
	if kind == "api" && mapOf(pipelinePlanEntry(run, i)["dispatch"])["phase"] == "reserved" {
		// No committed handoff exists. Resume preparation with the same frozen
		// bindings and logical step; this is not replay of a provider call.
		s["state"] = "preparing"
		if err := putRun(q, run); err != nil {
			return err
		}
		return pipelineSoon(q, runID)
	}
	if kind == "api" && def["mode"] != "job" {
		// Handed off and not answered: the call's record was lost (a restart, a failed record). Never sent again.
		return failStep(q, run, i, "unknown:interrupted", "Coordinator restarted during the call; outcome unknown, not replayed")
	}
	if kind == "api" {
		connection, tool := astr(def["connection"]), jobTool(def, "job_status")
		token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
		snapshot, revision, jobID := run, pipelineRevision(run), s["jobId"]
		c.Call = func(ctx context.Context) any {
			line := refused
			if line == nil {
				l := pipelineGateway(ctx, a, row, token, connection, tool, doc{"jobId": jobID}, idempotent)
				line = &l
			}
			out, artifact, err := pipelinePolled(ctx, a, snapshot, i, *line)
			return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run doc) error {
				return pipelinePollRecord(q, now, run, i, *line, out, artifact, err)
			}}
		}
		return nil
	}
	// Agent or script step running: wait for its execution.
	e, err := maybe(q, s["executionId"], "execution")
	if err != nil {
		return err
	}
	if e == nil || !terminal(e) {
		return pipelineLater(q, runID) // an unconfirmed running state is not final: the host may still report the real outcome
	}
	if st := astr(e["state"]); st != "succeeded" {
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
		return failStep(q, run, i, problem, msg)
	}
	var pending int
	if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM artifact WHERE execution_id=$1 AND state='pending'", s["executionId"]).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return pipelineLater(q, runID) // uploads can finish after the completion receipt
	}
	arts, err := artifactsOf(q, astr(s["executionId"]))
	if err != nil {
		return err
	}
	snapshot, revision := run, pipelineRevision(run)
	c.Call = func(ctx context.Context) any {
		output, artifact, err := pipelineAgentOutput(ctx, a, snapshot, i, arts)
		return pipelineRecord{revision: revision, apply: func(q *Request, run doc) error {
			if err != nil {
				return err
			}
			step := mapOf(anys(run["steps"])[i])
			step["outputArtifactId"] = artifact
			u, err := creditsOf(q, astr(step["executionId"]))
			if err != nil {
				return err
			}
			if err := completeStep(q, run, i, output, doc{"usage": u}); err != nil {
				return err
			}
			return pipelineSoon(q, astr(run["id"]))
		}}
	}
	return nil
}

// pipelineStepOf: step i of run as a record.
func pipelineStepOf(run doc, i int) doc { return mapOf(anys(run["steps"])[i]) }

// pipelineAgentOutput: a finished agent or script step's output (its output.json, else its file list), saved as the
// step's output object. Outside any transaction.
func pipelineAgentOutput(ctx context.Context, a *App, run doc, i int, arts []doc) (any, any, error) {
	if _, err := connIngestPass(ctx, a); err != nil {
		return nil, nil, err
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
	if err := materializePipelineOutput(ctx, a, run, i, output); err != nil {
		return nil, nil, err
	}
	return output, pipelineStepOf(run, i)["outputArtifactId"], nil
}

// pipelinePrepared records a preparing step's materialized inputs and starts it: an api step is handed off and leaves
// its call; an agent or script step starts its execution (a start that fails fails the step from its preparing state).
func pipelinePrepared(q *Request, c *kitClaim, run doc, i int, prepared *pipelinePreparedInputs, inputsErr error) error {
	s := pipelineStepOf(run, i)
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	kind, connection, row := astr(def["kind"]), astr(def["connection"]), stepRow(run, i)
	if inputsErr != nil {
		return failStep(q, run, i, "infra:inputs", inputsErr.Error())
	}
	outputs := doc{}
	for _, x := range anys(run["steps"])[:i] {
		outputs[astr(mapOf(x)["id"])] = mapOf(x)["output"]
	}
	freezePipelineInputs(run, i, prepared)
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
		return failStep(q, run, i, "ref:"+re.path, "Missing reference "+re.path)
	} else if err != nil {
		return err
	}
	s["state"], s["startedAt"] = "running", nowMs()
	if kind == "api" {
		var submitTool doc
		if def["mode"] == "job" {
			conn, err := connByName(q, connection)
			if err != nil {
				return failStep(q, run, i, "step:job-tools", err.Error())
			}
			tools := providerTools(astr(conn["provider"]))
			submitTool = mapOf(tools[astr(def["tool"])])
			for _, name := range []string{jobTool(def, "job_status"), jobTool(def, "recent_tasks")} {
				tool := mapOf(tools[name])
				granted := false
				for _, t := range anys(conn["tools"]) {
					granted = granted || t == name
				}
				if tool["method"] != "GET" || tool["idempotent"] != true || !granted {
					return failStep(q, run, i, "step:job-tools", fmt.Sprintf("job read tool %s must be a granted idempotent GET", name))
				}
			}
		}
		s["arguments"] = resolved
		if err := putRun(q, run); err != nil {
			return err
		}
		return pipelineSubmit(q, c, run, i, resolved, submitTool)
	}
	// The start runs under a savepoint: one that fails leaves nothing of it, and the step fails from here.
	revision, dispatchBefore := pipelineRevision(run), clone(pipelinePlanEntry(run, i)["dispatch"])
	if err := exec(q, "SAVEPOINT pipeline_start"); err != nil {
		return err
	}
	err = func() error {
		if err := putRun(q, run); err != nil {
			return err
		}
		var execID string
		var err error
		if kind == "agent" {
			execID, err = startAgentStep(q, run, def, row, astr(resolved), prepared.files, mapOf(run["overrides"]))
		} else {
			host := astr(def["host"])
			if h := mapOf(run["overrides"])["host"]; h != nil {
				host = fmt.Sprint(h)
			}
			execID, err = startScript(q, run, row, astr(resolved), def["timeout_ms"], host, prepared.files)
		}
		if err != nil {
			return err
		}
		s["executionId"] = execID
		dispatch := mapOf(pipelinePlanEntry(run, i)["dispatch"])
		dispatch["nativeExecutionId"] = execID
		if kind == "script" {
			command, err := first(q, "SELECT "+convTables["command"].record()+" AS data FROM command WHERE execution_id=$1 AND kind='script' ORDER BY seq", execID)
			if err != nil {
				return err
			}
			cmd := mapOf(command["data"])
			if cmd == nil {
				return fail(409, "Pipeline script command was not reserved")
			}
			dispatch["phase"], dispatch["commandId"], dispatch["hostId"] = "prepared", cmd["id"], cmd["hostId"]
		}
		return putRun(q, run)
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
	delete(s, "executionId")
	mapOf(run["plan"])["revision"] = revision
	if dispatchBefore == nil {
		delete(pipelinePlanEntry(run, i), "dispatch")
	} else {
		pipelinePlanEntry(run, i)["dispatch"] = dispatchBefore
	}
	return failStep(q, run, i, problem, msg)
}

// pipelineSubmit hands an api step's call off (claimPipelineAPI, committed with this step) and leaves the call.
func pipelineSubmit(q *Request, c *kitClaim, run doc, i int, resolved any, submitTool doc) error {
	a := q.app
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	connection, tool, row := astr(def["connection"]), astr(def["tool"]), stepRow(run, i)
	token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
	if refused != nil && deref(refused.ErrorCode) == pipelineClaimObserved {
		return pipelineSoon(q, astr(run["id"])) // another owner holds the call
	}
	if refused != nil {
		return pipelineSubmitted(q, c, run, i, *refused, resolved, submitTool, nil)
	}
	snapshot, revision := run, pipelineRevision(run)
	c.Call = func(ctx context.Context) any {
		line := pipelineGateway(ctx, a, row, token, connection, tool, resolved, idempotent)
		out := pipelineSyncOutput(ctx, a, snapshot, i, line)
		return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run doc) error {
			return pipelineSubmitted(q, c, run, i, line, resolved, submitTool, out)
		}}
	}
	return nil
}

// pipelineSyncResult: a sync api step's answer read outside the transaction: its output (the mapping over the reply),
// its saved output object, or why neither.
type pipelineSyncResult struct {
	output, usage, reply, artifact any
	mapErr, saveErr                error
}

func pipelineSyncOutput(ctx context.Context, a *App, run doc, i int, line contract.CallLine) *pipelineSyncResult {
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	if line.Outcome != contract.CallOutcomeOk || def["mode"] == "job" {
		return nil
	}
	out := callOutput(line)
	r := &pipelineSyncResult{output: out, usage: mapOf(out)["usage"]}
	if m := def["output"]; m != nil {
		// The step's output is the mapping over the reply (${reply.<path>}); the reply's usage stays the step's usage.
		mapped, err := resolveRefs(m, nil, doc{"": out}, false)
		if err != nil {
			r.reply, r.mapErr = out, err // the paid answer stays inspectable on the failed step
			return r
		}
		r.output = mapped
	}
	r.saveErr = materializePipelineOutput(ctx, a, run, i, r.output)
	r.artifact = pipelineStepOf(run, i)["outputArtifactId"]
	return r
}

// pipelineSubmitted records an api step's call: a job submit without a clear answer leaves a listing of recent tasks
// (one created since the call started is adopted; otherwise unknown, never resent).
func pipelineSubmitted(q *Request, c *kitClaim, run doc, i int, line contract.CallLine, resolved any, submitTool doc, out *pipelineSyncResult) error {
	a := q.app
	s := pipelineStepOf(run, i)
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	connection, row := astr(def["connection"]), stepRow(run, i)
	job := def["mode"] == "job"
	code := deref(line.ErrorCode)
	if job && line.Outcome != contract.CallOutcomeOk && (line.Outcome == contract.CallOutcomeUnknown || http5xx.MatchString(code)) {
		tool := jobTool(def, "recent_tasks")
		token, idempotent, refused := pipelineClaimCall(q, run, row, connection, tool)
		revision := pipelineRevision(run)
		c.Call = func(ctx context.Context) any {
			recent := refused
			if recent == nil {
				l := pipelineGateway(ctx, a, row, token, connection, tool, doc{}, idempotent)
				recent = &l
			}
			return pipelineRecord{revision: revision, revoke: row, apply: func(q *Request, run doc) error {
				return pipelineAdopt(q, run, i, code, *recent, resolved, submitTool)
			}}
		}
		return nil
	}
	if line.Outcome != contract.CallOutcomeOk {
		return failStep(q, run, i, problemOf(string(line.Outcome), code), fmt.Sprintf("Connection call %s: %s", line.Outcome, code))
	}
	if job {
		return pipelineJobStarted(q, run, i, line)
	}
	if out.mapErr != nil {
		s["reply"] = out.reply
		return failStep(q, run, i, out.mapErr.Error(), "Output mapping: a reference to the reply is missing")
	}
	if out.saveErr != nil {
		return out.saveErr
	}
	s["outputArtifactId"] = out.artifact
	if err := completeStep(q, run, i, out.output, doc{"usage": out.usage}); err != nil {
		return err
	}
	return pipelineSoon(q, astr(run["id"]))
}

// pipelineAdopt: after a job submit without a clear answer, the one recent task created since the call started that no
// step owns and that matches the request is the step's job; otherwise the submit is unknown and never sent again.
func pipelineAdopt(q *Request, run doc, i int, code string, recent contract.CallLine, resolved any, submitTool doc) error {
	s := pipelineStepOf(run, i)
	listed := callOutput(recent)
	tasks := anys(listed)
	if tasks == nil {
		tasks = anys(mapOf(listed)["result"])
	}
	owned := map[string]bool{}
	rs, err := queryRows(q, "SELECT data->>'jobId' AS id FROM execution WHERE data ? 'jobId'")
	if err != nil {
		return err
	}
	for _, r := range rs {
		owned[astr(r["id"])] = true
	}
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
	if len(mine) != 1 {
		why := fmt.Sprintf("%d matching tasks", len(mine))
		if recent.Outcome != contract.CallOutcomeOk {
			why = "recent tasks unreadable"
		}
		return failStep(q, run, i, "unknown:submit-unconfirmed", fmt.Sprintf("Job submit %s; %s; not sent again", code, why))
	}
	s["adoptedAfter"] = code
	return pipelineJobStarted(q, run, i, contract.CallLine{Outcome: contract.CallOutcomeOk, Output: jsonBytes(doc{"jobId": mine[0]["id"]})})
}

// pipelineJobStarted: a job submit's answer names the job the step polls from now on.
func pipelineJobStarted(q *Request, run doc, i int, line contract.CallLine) error {
	s := pipelineStepOf(run, i)
	out := mapOf(callOutput(line))
	if _, ok := out["jobId"].(string); !ok {
		if r, ok := out["result"].(string); ok {
			out = doc{"jobId": r}
		}
	}
	jobID, ok := out["jobId"].(string)
	if !ok {
		return failStep(q, run, i, "step:no-job-id", "Job submit returned no jobId")
	}
	s["jobId"], s["polls"] = jobID, 0
	if err := putRun(q, run); err != nil {
		return err
	}
	if err := exec(q, "UPDATE execution SET data=data||jsonb_build_object('jobId',$2::text) WHERE id=$1", stepRow(run, i), jobID); err != nil {
		return err
	}
	return pipelineLater(q, astr(run["id"]))
}

// pipelinePolled reads a job poll's answer outside the transaction: the job's state, its output (Meshy's task shape
// mapped) and, once it succeeded, its saved output object.
func pipelinePolled(ctx context.Context, a *App, run doc, i int, line contract.CallLine) (doc, any, error) {
	if line.Outcome != contract.CallOutcomeOk {
		return nil, nil, nil
	}
	s := pipelineStepOf(run, i)
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
	if out["state"] != "succeeded" {
		return out, nil, nil
	}
	if out["output"] == nil {
		out["output"] = doc{}
	}
	if err := materializePipelineOutput(ctx, a, run, i, out["output"]); err != nil {
		return out, nil, err
	}
	return out, s["outputArtifactId"], nil
}

// pipelinePollRecord records one job poll: a succeeded job completes the step with its usage, a failed one fails it,
// anything else (an unknown poll included) polls again later.
func pipelinePollRecord(q *Request, now time.Time, run doc, i int, line contract.CallLine, out doc, artifact any, saveErr error) error {
	s := pipelineStepOf(run, i)
	def := mapOf(anys(mapOf(run["definition"])["steps"])[i])
	runID, row := astr(run["id"]), stepRow(run, i)
	if line.Outcome != contract.CallOutcomeOk {
		if line.Outcome == contract.CallOutcomeUnknown {
			return pipelineLater(q, runID)
		}
		code := deref(line.ErrorCode)
		return failStep(q, run, i, problemOf(string(line.Outcome), code), "Job poll failed: "+code)
	}
	p, _ := numOK(s["polls"])
	s["polls"] = p + 1
	switch out["state"] {
	case "succeeded":
		if saveErr != nil {
			return saveErr
		}
		s["outputArtifactId"] = artifact
		if err := completeStep(q, run, i, out["output"], doc{"usage": out["usage"]}); err != nil {
			return err
		}
		if u := mapOf(out["usage"]); u != nil {
			if err := recordJob(q, run, row, astr(def["tool"]), astr(s["jobId"]), astr(def["connection"]), u); err != nil {
				return err
			}
		}
		return pipelineSoon(q, runID)
	case "failed":
		code := out["error"]
		if code == nil {
			code = "job-failed"
		}
		return failStep(q, run, i, problemOf("error", fmt.Sprint(code)), "Job failed")
	}
	if err := putRun(q, run); err != nil {
		return err
	}
	return pipelineLater(q, runID)
}

// pipelineImportDrafts: a finished draft-examples run's examples become unapproved examples, each with its rating
// question; one that is refused is left out (savepoint). The run's row goes.
func pipelineImportDrafts(q *Request, run doc, drafts pipelineDrafts) error {
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
	return pipelineWorkDone(q, astr(run["id"]), true)
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
	longRoute("POST /api/pipelines/{id}/runs", func(q *Request) (any, error) {
		var out doc
		err := q.app.store.Tx(q.ctx(), scopeOf(q.person), true, "", func(tx pgx.Tx) error {
			q.tx = tx
			var err error
			out, err = pipelineRunCreate(q)
			return err
		})
		if err != nil {
			return nil, err
		}
		return pipelinePublicRead(q.ctx(), q.app, astr(out["id"]))
	})
	// pipeline_rerun: a finished run again on the current definition (or the caller's draft); mode failed reuses the steps
	// that succeeded and would run the same way.
	op("pipeline_rerun", func(q *Request, r *contract.PipelineRerunRequest) (contract.PipelineRerunReply, error) {
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
		out.RunId = astr(run["id"])
		for _, x := range anys(run["reused"]) {
			out.Reused = append(out.Reused, astr(x))
		}
		if d, err := asT[[]contract.DefinitionRef](run["definitions"]); err == nil && d != nil {
			out.Definitions = d
		}
		return out, nil
	})
	// pipeline_run_create: the route as an operation, plus draft (the caller's draft; architect only).
	op("pipeline_run_create", func(q *Request, r *contract.PipelineRunCreateRequest) (contract.PipelineRunCreateReply, error) {
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
		out.RunId = astr(run["id"])
		out.Definitions, err = asT[[]contract.DefinitionRef](run["definitions"])
		if out.Definitions == nil {
			out.Definitions = []contract.DefinitionRef{}
		}
		return out, err
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
	return budgetAnswerCheck(q, jsonBytes(mapOf(question["data"])["budgetAsk"]), answer)
}

func runAnswerAfter(q *Request, id string) error {
	row, err := first(q, "SELECT * FROM question WHERE id=$1", id)
	if err != nil || row == nil {
		return err
	}
	if err := labAnswerAfter(q, row); err != nil {
		return err
	}
	return budgetAnswerAfter(q, jsonBytes(mapOf(row["data"])["budgetAsk"]), astr(row["answer"]), strPtr(row["answered_by"]))
}

// runPipeline: the selected method's v2 pipeline with the query as the example ({query, prompt}); waits for the run.
// inv is pipeline (pipeline_run) or direct (direct_search); methodID nil selects the project's recommended method.
func runPipeline(q *Request, inv, projectID, rid, query string, methodID *string) (doc, error) {
	if rid == "" || len(rid) > 200 || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 2000 {
		return nil, fail(400, "Expected projectId, requestId, query and optional methodId")
	}
	var run doc
	old := false
	err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		signature := sha256hex([]byte(canonical(doc{"projectId": projectID, "query": query, "methodId": optText(methodID), "invocation": inv})))
		prev, err := runsWhere(q, "data->>'projectId'=$1 AND data->>'requestId'=$2", projectID, rid)
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
		p, err := projectContext(q, projectID)
		if err != nil {
			return err
		}
		c, err := catalogue(q, projectID)
		if err != nil {
			return err
		}
		want := deref(methodID)
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
		run, err = pipelineStart(q, doc{"file": method["pipeline"], "projectId": projectID, "requestId": rid, "example": doc{"query": query, "prompt": query},
			"requestedBy": requester(q), "extra": doc{"signature": signature, "invocation": inv, "selectedMethod": method["id"], "selectionSource": source, "context": p}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if old {
		return pipelinePublicRead(q.ctx(), q.app, astr(run["id"]))
	}
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
	op("pipeline_run", func(q *Request, r *contract.PipelineRunRequest) (contract.PipelineRunReply, error) {
		return replyAs[contract.PipelineRunReply](runPipeline(q, "pipeline", r.ProjectId, r.RequestId, r.Query, r.MethodId))
	})
	op("direct_search", func(q *Request, r *contract.DirectSearchRequest) (contract.DirectSearchReply, error) {
		return replyAs[contract.DirectSearchReply](runPipeline(q, "direct", r.ProjectId, r.RequestId, r.Query, r.MethodId))
	})
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
	if dir := nilIf(firstString(run["directedBy"], mapOf(run["requestedBy"])["email"])); dir != nil {
		s["directedBy"] = dir
	}
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
	if dir := nilIf(firstString(run["directedBy"], mapOf(run["requestedBy"])["email"])); dir != nil {
		execution["directedBy"] = dir
	}
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
	if err := pipelineRecoveryFence(q, old); err != nil {
		return nil, err
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
	if err := requirePipelinePlan(old); err != nil {
		return nil, err
	}
	reuse := []any{}
	oldSteps, newSteps := anys(old["steps"]), anys(def["steps"])
	oldDef := anys(mapOf(old["definition"])["steps"])
	for i := 0; i < len(oldSteps) && i < len(newSteps) && i < len(oldDef); i++ {
		e, entry := mapOf(oldSteps[i]), pipelinePlanEntry(old, i)
		if canonical(oldDef[i]) != canonical(newSteps[i]) {
			break
		}
		if entry["activation"] == "not_selected" {
			reuse = append(reuse, doc{"id": e["id"], "kind": e["kind"]})
			continue
		}
		if entry["activation"] != "selected" || e["state"] != "succeeded" || e["observation"] == "unconfirmed" || mapOf(entry["dispatch"])["phase"] != "settled" && mapOf(entry["executionRef"])["mode"] != "reused" {
			break
		}
		if err := pipelineOutputOwner(q, old, i, astr(e["outputArtifactId"])); err != nil {
			return nil, err
		}
		artifact, err := artifactRow(withTx(q), q.app, astr(e["outputArtifactId"]))
		if err != nil || artifact.ContentHash != sha256hex(jsonBytes(e["output"])) {
			return nil, fail(409, "Reused output bytes cannot be verified")
		}
		bindings := anys(entry["inputs"])
		if bindings == nil || sha256hex([]byte(canonical(bindings))) != mapOf(entry["executionRef"])["sourceInputSha256"] {
			return nil, fail(409, "Reused input identity cannot be verified")
		}
		for _, value := range bindings {
			binding := mapOf(value)
			a, err := artifactRow(withTx(q), q.app, astr(binding["artifactId"]))
			if err != nil || a.State != "stored" || !same(a.ProjectID, old["projectId"]) || a.ContentHash != binding["sha256"] {
				return nil, fail(409, "Reused input artifact is unavailable or changed")
			}
		}
		definition := mapOf(newSteps[i])
		connection, tool := "", ""
		if definition["kind"] == "api" {
			connection, tool = astr(definition["connection"]), astr(definition["tool"])
		}
		if _, _, err := pipelineAuthorityStamp(q, old, i, connection, tool, ""); err != nil {
			return nil, err
		}
		if definition["kind"] == "agent" {
			same, err := sameAgentStep(q, astr(e["executionId"]), definition, mapOf(old["overrides"]))
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, fail(409, "Reused agent configuration or model changed")
			}
		}
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
