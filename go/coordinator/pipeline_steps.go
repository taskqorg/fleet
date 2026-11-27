// Pipeline step execution: agent, script, and gateway/API dispatch, polling, and handoff.
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

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

var (
	infraCode = regexp.MustCompile(`^(queue-full|no-usable-key|http-429|http-432|http-433|key-disabled-\d+|provider-unreachable|unreachable|unauthenticated|connection-unavailable|client-hash-mismatch)$`)
	http5xx   = regexp.MustCompile(`^http-5\d\d$`)
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

// pipelineHostJob: a script step's host job (executionPutRaw writes it), the step's child in the run's project and
// experiment. A host job has no session, so the project is its own field: the files the script saves belong to the
// run's project (artifactReserve reads the execution's), which the next step's inputs require.
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
	ProjectID       string          `json:"projectId,omitempty"`
	ProjectSnapshot json.RawMessage `json:"projectSnapshot"`
	DirectedBy      string          `json:"directedBy,omitempty"`
	ExperimentID    string          `json:"experimentId,omitempty"`
}

type pipelineScriptPayload struct {
	Command   string          `json:"command"`
	Inputs    []fileRef       `json:"inputs"`
	TimeoutMs json.RawMessage `json:"timeout_ms,omitempty"`
}

// scriptHost: the host for a script step that names none, chosen as dispatch chooses for agent work: held by someone
// who may run the project's work, online, accepting work, able to run scripts, not overloaded or full, in host order with the default host first; a spare host
// only when no other host qualifies (fleet-people-access.md §7). With none, the default host, as before.
func scriptHost(q *Request, projectID, director string) (string, error) {
	k, err := loadDispatchContext(q)
	if err != nil {
		return "", err
	}
	queued := map[string]int64{}
	rows, err := q.db().DispQueuedHostJobsByHost(q.ctx())
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		queued[r.HostID] = int64(r.N)
	}
	now := nowMs()
	order := append([]string{q.app.hostID}, k.hostIDs...)
	for _, spare := range []bool{false, true} {
		for _, id := range order {
			h, ok := k.hosts[id]
			switch {
			case !ok || k.spare(id) != spare || deref(h.Intake) == "paused":
			case !q.app.holderMayRun(deref(h.Holder), projectID, director): // the same trust gate as dispatch
			case !(float64(now)-k.seen[id] < onlineMs) || !inList(k.caps[id], "script"):
			case hostOverloadReason(id, k.loads[id], now) != "":
			// Queued steps take no slot (work-allocation B7), but a host full with running and queued work is skipped, so a
			// burst of unpinned steps spreads over the hosts instead of queueing on the first.
			case hostConcurrencyLimit(h, k.loads[id], now) > 0 && k.running[id]+queued[id] >= hostConcurrencyLimit(h, k.loads[id], now):
			default:
				return id, nil
			}
		}
	}
	return q.app.hostID, nil
}

// startScript: a script command for the executor, as its own execution: a host job (actor program-worker) of the run,
// with no session (a host job is no conversation).
func (run *runRecord) startScript(q *Request, stepExecutionID, command string, timeout json.RawMessage, hostID string, inputs []fileRef) (string, error) {
	dir := run.director()
	if hostID == "" {
		h, err := scriptHost(q, run.project(), dir)
		if err != nil {
			return "", err
		}
		hostID = h
	}
	id := newID()
	e := pipelineHostJob{ID: id, ExecutionID: id, HostID: hostID, State: "preparing", CreatedAt: nowMs(), Actor: "program-worker", PipelineStepID: stepExecutionID,
		PipelineRunID: run.ID, ProjectID: run.project(), ProjectSnapshot: orNull(run.ProjectSnapshot), DirectedBy: dir, ExperimentID: run.ExperimentID}
	if err := executionWrite(q, e); err != nil { // a host job, the step's child in the run's project and experiment
		return "", err
	}
	if inputs == nil {
		inputs = []fileRef{}
	}
	payload := pipelineScriptPayload{Command: command, Inputs: inputs}
	if !isNull(timeout) {
		payload.TimeoutMs = timeout
	}
	return id, commandPut(q, &agentCommand{ID: newID(), ExecutionID: id, HostID: hostID, Kind: "script", Payload: jsonBytes(payload), Status: "pending", HandedOut: ptr(false)})
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
	if err := a.store.Write(context.WithoutCancel(ctx), nil, []string{GLOBAL}, func(q *Request) error { return revokeTokens(q, row) }); err != nil {
		log.Printf("revoke %s: %v", row, err)
	}
	if _, err := connIngestPass(context.WithoutCancel(ctx), a); err != nil {
		log.Printf("ingest %s: %v", connection, err)
	}
	return line
}

const (
	stepProblemDetailMax = 180
	stepProblemMax       = 220
)

func stepProblemWithDetail(base, detail string) string {
	d := compactProblemDetail(detail, stepProblemDetailMax)
	if d == "" || d == base || strings.HasSuffix(base, ":"+d) {
		return compactProblemDetail(base, stepProblemMax)
	}
	return compactProblemDetail(base+": "+d, stepProblemMax)
}

func nativeStepFailure(state, obs, nativeProblem string, errRaw json.RawMessage, fallbackEventProblem string) (string, string) {
	why := pipelineRuleOf(errRaw)
	errStr, _ := jsonString(errRaw)
	whyMsg, _ := jsonString(why["message"])
	if state == "cancelled" {
		return "step:cancelled", firstString(whyMsg, errStr, "Step execution cancelled")
	}
	code := "failed"
	if why != nil && !isNull(why["code"]) {
		if c := strings.TrimSpace(fmt.Sprint(rawAny(why["code"]))); c != "" {
			code = c
		}
	}
	detail := firstString(strings.TrimSpace(nativeProblem), whyMsg, errStr, strings.TrimSpace(fallbackEventProblem))
	msg := compactProblemDetail(firstString(whyMsg, errStr, strings.TrimSpace(nativeProblem), strings.TrimSpace(fallbackEventProblem), "Step execution "+state), 500)

	if np := strings.TrimSpace(nativeProblem); strings.HasPrefix(np, "infra:") || strings.HasPrefix(np, "unknown:") || strings.HasPrefix(np, "step:") {
		if obs == "unconfirmed" && !strings.HasPrefix(np, "unknown:") {
			_, rest, _ := strings.Cut(np, ":")
			return compactProblemDetail("unknown:"+rest, stepProblemMax), msg
		}
		return compactProblemDetail(np, stepProblemMax), msg
	}

	prefix := "step:"
	switch {
	case obs == "unconfirmed":
		prefix = "unknown:"
	case infraCode.MatchString(code):
		prefix = "infra:"
	}
	return stepProblemWithDetail(prefix+code, detail), msg
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
	execRow, err := q.db().ExecutionByID(q.ctx(), s.ExecutionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pipelineLater(q, runID)
	}
	if err != nil {
		return err
	}
	eState, eObs := string(execRow.State), string(execRow.Observation)
	if !executionEnds(eState) {
		return pipelineLater(q, runID) // an unconfirmed running state is not final: the host may still report the real outcome
	}
	if eState != "succeeded" {
		data, err := recordRaw(execRow.Data)
		if err != nil {
			return err
		}
		nativeProblem := deref(execRow.Problem)
		errRaw := data["error"]
		if eState == "cancelled" && isNull(errRaw) && data.text("stopDetail") != "" {
			errRaw = data["stopDetail"] // who or what stopped it (a refused start, a deletion) is the step's message
		}
		var eventDetail string
		if eState != "cancelled" && nativeProblem == "" && isNull(errRaw) {
			evs, err := executionEventRecords(q, s.ExecutionID)
			if err != nil {
				return err
			}
			for _, ev := range evs {
				if p := eventProblem(ev.text("type"), ev.object("data"), execRow.Kind == db.ExecutionKindHostJob); p != "" {
					eventDetail = p
					break
				}
			}
		}
		problem, msg := nativeStepFailure(eState, eObs, nativeProblem, errRaw, eventDetail)
		return run.failStep(q, i, problem, msg)
	}
	pending, err := q.db().PipelinePendingArtifacts(q.ctx(), &s.ExecutionID)
	if err != nil {
		return err
	}
	if pending > 0 {
		return pipelineLater(q, runID) // uploads can finish after the completion receipt
	}
	var arts []db.PipelineStoredArtifactsRow
	var review *pipelineReview
	if mode, _ := jsonString(run.def(i).Output); mode == pipelineReviewReply {
		replies, err := replyText(q, s.ExecutionID)
		if err != nil {
			return err
		}
		review = &pipelineReview{}
		if len(replies) == 0 || json.Unmarshal([]byte(replies[len(replies)-1]), review) != nil ||
			strings.TrimSpace(review.Verdict) == "" || review.Findings == nil || strings.TrimSpace(review.Report) == "" {
			return run.failStep(q, i, "output_invalid", "Review reply must contain a verdict, a findings array and a Markdown report")
		}
	} else {
		arts, err = artifactsOf(q, s.ExecutionID)
		if err != nil {
			return err
		}
	}
	snapshot, revision := run, run.revision()
	c.Call = func(ctx context.Context) any {
		output, artifact, err := pipelineAgentOutput(ctx, a, snapshot, i, arts, review)
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

// pipelinePrepared records a preparing step's materialized inputs and starts it: an api step is handed off and leaves
// its call; an agent or script step starts its execution (a start that fails fails the step from its preparing state).
func pipelinePrepared(q *Request, c *kitClaim, run *runRecord, i int, prepared *pipelinePreparedInputs, inputsErr error) error {
	s, def := &run.Steps[i], run.def(i)
	kind, connection, row := def.Kind, def.Connection, run.row(i)
	if inputsErr != nil {
		return run.failStep(q, i, stepProblemWithDetail("infra:inputs", inputsErr.Error()), inputsErr.Error())
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
				if !ok && name == def.jobTool("recent_tasks") && def.RecentTool == "" {
					continue // a provider with no task listing (Gemini interactions): an unclear submit is never adopted
				}
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
	return run.failStep(q, i, stepProblemWithDetail(problem, msg), msg)
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
		if !def.hasRecentTool(q, connection) {
			return run.failStep(q, i, "unknown:submit-unconfirmed", fmt.Sprintf("Job submit %s; the provider lists no tasks to confirm it; not sent again", code))
		}
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
		return run.failStepDetail(q, i, problemOf(string(line.Outcome), code), fmt.Sprintf("Connection call %s: %s", line.Outcome, code), callErrorDetail(line.Output))
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
		jobID, ok = jsonString(out["id"]) // a Gemini interaction
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

// geminiInteraction: a Gemini Interactions API resource (status in_progress | requires_action | completed | failed |
// cancelled | incomplete); the step keeps the whole reply as its output.
type geminiInteraction struct {
	Object string          `json:"object"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Usage  struct {
		TotalTokens *float64 `json:"total_tokens"`
	} `json:"usage"`
}

// geminiInteractionPoll maps a Gemini interaction to a job poll's answer (the whole reply is the output); nil when the
// reply is not an interaction.
func geminiInteractionPoll(raw json.RawMessage) *pipelinePoll {
	var g geminiInteraction
	lenientDecode(raw, &g)
	if jsonKind(raw) != '{' || g.Object != "interaction" || g.Status == "" {
		return nil
	}
	state := "running"
	switch g.Status {
	case "completed":
		state = "succeeded"
	case "failed", "cancelled", "incomplete":
		state = "failed"
	}
	var problem json.RawMessage
	if state == "failed" {
		problem = jsonBytes("interaction-" + g.Status)
		if !isNull(g.Error) {
			problem = g.Error
		}
	}
	var usage json.RawMessage
	if state == "succeeded" && g.Usage.TotalTokens != nil {
		usage = jsonBytes(pipelineUnits{Unit: "tokens", Amount: *g.Usage.TotalTokens})
	}
	return &pipelinePoll{State: state, Output: raw, Usage: usage, Error: problem}
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
	if g := geminiInteractionPoll(raw); g != nil {
		out = g
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
		return run.failStepDetail(q, i, problemOf(string(line.Outcome), code), "Job poll failed: "+code, callErrorDetail(line.Output))
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

// callOutput: the provider's reply body of an in-process call, decoded; nil when there is none.
func callOutput(l contract.CallLine) any {
	var v any
	json.Unmarshal(l.Output, &v)
	return v
}

var skillID = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)

// pipelineSkill: a skill's text as an execution payload carries it.
type pipelineSkill struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// profileSkills: the SKILL.md texts of a profile's skills (config/skills/<id>/SKILL.md), as an execution payload carries them.
func profileSkills(profile any) ([]pipelineSkill, error) {
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

// Agent step: the step's trusted profile with the variant overrides, its skills, a session of the
// run, and an ordinary queued execution.
// stepProfile: the profile an agent step runs: its profile (or the override), with the variant's model and skills. The
// profile is its definition's open content.
func stepProfile(q *Request, step *pipelineDefStep, overrides *pipelineOverrides) (string, rawObject, error) {
	pid := step.Profile
	if overrides.Profile != "" {
		pid = overrides.Profile
	}
	profile, err := readTrustedProfile(q, pid)
	if err != nil {
		return pid, nil, err
	}
	if len(overrides.Model) > 0 {
		profile["model"] = openJSON(overrides.Model)
	}
	if len(overrides.Skills) > 0 {
		profile["skills"] = openJSON(overrides.Skills)
	}
	if len(overrides.Candidate) > 0 {
		profile["harnessCandidate"] = openJSON(overrides.Candidate)
	}
	return pid, profile, nil
}

// pipelineAgentExtra: the pipeline fields of an agent step's execution.
type pipelineAgentExtra struct {
	PipelineStepID string `json:"pipelineStepId"`
	PipelineRunID  string `json:"pipelineRunId"`
	DirectedBy     string `json:"directedBy,omitempty"`
	ExperimentID   string `json:"experimentId,omitempty"`
	// StepPublishes: the step publishes to the project's forge (its profile's publishing, or the step's publishes),
	// so dispatch keeps execution.forge for it (publishes); every other pipeline agent step goes without.
	StepPublishes bool `json:"stepPublishes,omitempty"`
}

// stepPublishes: the agent step publishes: the pipeline step says so, or its profile does (publishing). A Lab trial
// never publishes (publishes in dispatch.go).
func stepPublishes(step *pipelineDefStep, profile rawObject) bool {
	p, _ := jsonAs[struct {
		Publishing bool `json:"publishing"`
	}](jsonBytes(profile))
	return step.Publishes || p.Publishing
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
	if err := sessionPut(q, &s); err != nil {
		return "", err
	}
	if inputs == nil {
		inputs = []fileRef{}
	}
	payload := map[string]json.RawMessage{"inputs": jsonBytes(inputs), "skills": jsonBytes(skills), "webRoute": jsonBytes(web)}
	extra := pipelineAgentExtra{PipelineStepID: stepExecutionID, PipelineRunID: run.ID, DirectedBy: run.director(), ExperimentID: run.ExperimentID,
		StepPublishes: stepPublishes(step, profile)}
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

// hasRecentTool: whether the job step's connection provider has the task listing that confirms an unclear submit.
func (def *pipelineDefStep) hasRecentTool(q *Request, connection string) bool {
	if def.RecentTool != "" {
		return true
	}
	conn, err := connNamed(q, connection)
	if err != nil {
		return true // the listing call then fails and the submit stays unknown, as before
	}
	_, ok := gatewayProviders[conn.Provider].Tools[def.jobTool("recent_tasks")]
	return ok
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
