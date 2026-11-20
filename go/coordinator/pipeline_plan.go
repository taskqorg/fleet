package main

import (
	"context"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

const pipelinePlanVersion = 1

// pipelinePlanOfRun: a run's plan (contract.PipelinePlan, the run document's plan from getRun, nil for a run without
// one); a plan written as JSON (a fixture's map) is read into it once.
func pipelinePlanOfRun(run doc) *contract.PipelinePlan {
	switch p := run["plan"].(type) {
	case *contract.PipelinePlan:
		return p
	case nil:
		return nil
	default:
		plan, err := asT[contract.PipelinePlan](p)
		if err != nil {
			return nil
		}
		run["plan"] = &plan
		return &plan
	}
}

// pipelineEntries: the entries of a run's plan, in index order (aliases: an edit is the plan's).
func pipelineEntries(run doc) []contract.PipelinePlanEntry {
	if p := pipelinePlanOfRun(run); p != nil {
		return p.Entries
	}
	return nil
}

// pipelineEntry: entry index of a run's plan, nil out of range.
func pipelineEntry(run doc, index int) *contract.PipelinePlanEntry {
	entries := pipelineEntries(run)
	if index < 0 || index >= len(entries) {
		return nil
	}
	return &entries[index]
}

// pipelinePlanEntry: entry index as the record host_poll.go reads (a copy; nil out of range).
func pipelinePlanEntry(run doc, index int) doc {
	e := pipelineEntry(run, index)
	if e == nil {
		return nil
	}
	var out doc
	if json.Unmarshal(jsonBytes(e), &out) != nil {
		return nil
	}
	return out
}

func refExecutionID(e *contract.PipelinePlanEntry) string {
	if e.ExecutionRef == nil {
		return ""
	}
	return e.ExecutionRef.ExecutionId
}

func entryRefMode(e *contract.PipelinePlanEntry) contract.PipelineRefMode {
	if e.ExecutionRef == nil {
		return ""
	}
	return e.ExecutionRef.Mode
}

func dispatchPhase(e *contract.PipelinePlanEntry) contract.PipelineDispatchPhase {
	if e.Dispatch == nil {
		return ""
	}
	return e.Dispatch.Phase
}

// optString: *p, nil when absent (as a record's field compares).
func optString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// initPipelinePlan creates no execution. Static and guarded definitions share
// this same persisted model; there is no historical-row reconstruction path.
func initPipelinePlan(run doc) error {
	actor := firstString(run["directedBy"], mapOf(run["requestedBy"])["email"])
	if actor == "" {
		return fail(403, "Pipeline requires its original initiating actor")
	}
	definition := mapOf(run["definition"])
	entries, capabilities := []contract.PipelinePlanEntry{}, []any{}
	seen := map[string]bool{}
	for i, v := range anys(definition["steps"]) {
		step := mapOf(v)
		entries = append(entries, contract.PipelinePlanEntry{Id: astr(step["id"]), Index: int64(i), DefinitionSha256: sha256hex([]byte(canonical(step))), Activation: contract.PipelineActivationPending})
		kind, id := "", ""
		if step["kind"] == "api" {
			kind, id = "connection", astr(step["connection"])
		} else if step["kind"] == "agent" {
			kind, id = "profile", astr(step["profile"])
			if override := astr(mapOf(run["overrides"])["profile"]); override != "" {
				id = override
			}
		}
		if id != "" && !seen[kind+":"+id] {
			seen[kind+":"+id] = true
			capabilities = append(capabilities, doc{"kind": kind, "id": id})
		}
	}
	sort.Slice(capabilities, func(i, j int) bool {
		a, b := mapOf(capabilities[i]), mapOf(capabilities[j])
		if a["kind"] != b["kind"] {
			return astr(a["kind"]) < astr(b["kind"])
		}
		return astr(a["id"]) < astr(b["id"])
	})
	for _, input := range anys(mapOf(run["example"])["inputs"]) {
		if pipelineReservedInput(astr(mapOf(input)["path"])) {
			return fail(400, "Example input collides with reserved pipeline control namespace")
		}
	}
	run["planVersion"] = pipelinePlanVersion
	run["plan"] = &contract.PipelinePlan{DefinitionSha256: astr(run["definitionSha256"]), Revision: 0, Entries: entries}
	run["authority"] = doc{"actorEmail": actor, "projectId": run["projectId"], "requestedCapabilities": capabilities}
	return nil
}

func requirePipelinePlan(run doc) error {
	version, ok := intOK(run["planVersion"])
	if !ok || version != pipelinePlanVersion || len(pipelineEntries(run)) != len(anys(mapOf(run["definition"])["steps"])) {
		return pipelineFormatError()
	}
	return nil
}

func pipelineFormatError() error {
	return &HTTPError{Code: 409, Msg: "Unsupported pipeline run format; a clean start is required", Extra: doc{"reason": "pipeline_format_unsupported"}}
}

func pipelineFormatUnsupported(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Extra["reason"] == "pipeline_format_unsupported"
}

// pipelineDispatchKinds: the command kinds a pipeline plan dispatches (preparePipelineCommand, startScript); the
// poll preflight takes the GLOBAL lock for them (pollReadyCommands).
var pipelineDispatchKinds = []string{string(contract.CommandKindStart), string(contract.CommandKindScript)}

func pipelineDispatchKind(kind any) bool {
	k, _ := kind.(string)
	return slices.Contains(pipelineDispatchKinds, k)
}

// stopPipelineCommand settles a still pending command that will never be handed off, with the reason.
func stopPipelineCommand(q *Request, command doc, reason string) error {
	id, _ := command["id"].(string)
	_, err := commandStop(q, commandStopFilter{id: &id, statuses: []db.CommandStatus{db.CommandStatusPending}, detail: &reason})
	return err
}

type pipelineWriteKey struct {
	tx pgx.Tx
	id string
}

type pipelineWriteState struct {
	revision int64
	plans    []*contract.PipelinePlan
}

var pipelineWrites sync.Map

var pipelineBootID = newID()

const pipelineClaimObserved = "pipeline-claim-observed"

// pipelineAuthorityStamp holds the current authority rows through token mint or
// command handoff. A frozen project/profile is evidence, not permanent access.
func pipelineAuthorityStamp(q *Request, run doc, index int, connection, tool, host string) (string, []string, error) {
	// The caller has already resolved the run in its ordinary project scope.
	// Authority belongs to the retained initiating actor, even when a different
	// authorized operator requests recovery. Restore caller RLS before returning.
	type result struct {
		stamp       string
		connections []string
	}
	out, err := unscoped(q, func() (result, error) {
		stamp, connections, err := pipelineAuthorityStampWithin(q, run, index, connection, tool, host)
		return result{stamp, connections}, err
	})
	return out.stamp, out.connections, err
}

func pipelineAuthorityStampWithin(q *Request, run doc, index int, connection, tool, host string) (string, []string, error) {
	share := false
	resolve := resolveAccessTx
	if value, ok := txLockStates.Load(q.tx); ok && value.(*txLockState).write {
		share, resolve = true, resolveAccessShareTx
	}
	d := q.db()
	authority := mapOf(run["authority"])
	projectID := astr(run["projectId"])
	actor, err := resolve(q.ctx(), q.tx, astr(authority["actorEmail"]))
	if err != nil || actor == nil || !mayAccessProjectID(actor, projectID) {
		return "", nil, fail(403, "Pipeline initiating actor no longer has project access")
	}
	var projectHead *int32
	var projectVersion *int64
	if projectID != "" {
		var project db.PipelineProjectRow
		if share {
			var r db.PipelineProjectShareRow
			r, err = d.PipelineProjectShare(q.ctx(), projectID)
			project = db.PipelineProjectRow(r)
		} else {
			project, err = d.PipelineProject(q.ctx(), projectID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, fail(403, "Pipeline project is no longer available")
		}
		if err != nil {
			return "", nil, err
		}
		projectHead, projectVersion = &project.Head, &project.AccessVersion
	}
	// Global draft/static runs retain the existing full-access initiating-actor
	// boundary; they do not invent a project or borrow another actor's rights.
	stamp := doc{"actor": actor.Email, "accessVersion": actor.AccessVersion, "project": run["projectId"], "projectVersion": projectVersion, "projectHead": projectHead}
	connections := []string{}
	definition := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	if connection != "" {
		connections = append(connections, connection)
	} else if definition["kind"] == "agent" {
		profileID := astr(definition["profile"])
		if override := astr(mapOf(run["overrides"])["profile"]); override != "" {
			profileID = override
		}
		if share {
			_, err = d.PipelineProfileLiveShare(q.ctx(), profileID)
		} else {
			_, err = d.PipelineProfileLive(q.ctx(), profileID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, fail(403, "Pipeline profile is no longer available")
		}
		if err != nil {
			return "", nil, err
		}
		_, profile, err := stepProfile(q, definition, mapOf(run["overrides"]))
		if err != nil {
			return "", nil, err
		}
		step := mapOf(anys(run["steps"])[index])
		if native := astr(step["executionId"]); native != "" {
			same, err := sameAgentStep(q, native, definition, mapOf(run["overrides"]))
			if err != nil {
				return "", nil, err
			}
			if !same {
				return "", nil, fail(409, "Pipeline pinned profile changed before dispatch")
			}
		}
		stamp["profile"], stamp["profileSha256"] = profileID, sha256hex([]byte(canonical(profile)))
		connections = strs(profile["connections"])
	}
	sort.Strings(connections)
	connectionStamps := []any{}
	for _, name := range connections {
		var c db.PipelineConnectionGrantRow
		if share {
			var r db.PipelineConnectionGrantShareRow
			r, err = d.PipelineConnectionGrantShare(q.ctx(), "connection:"+name)
			c = db.PipelineConnectionGrantRow(r)
		} else {
			c, err = d.PipelineConnectionGrant(q.ctx(), "connection:"+name)
		}
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", nil, err
		}
		var access, tools any
		if found {
			if err := json.Unmarshal(c.Access, &access); err != nil {
				return "", nil, err
			}
			if err := json.Unmarshal(c.Tools, &tools); err != nil {
				return "", nil, err
			}
		}
		// connAllowed (connections.go) reads a connection record's holder and access.
		record := doc{"holder": nilIf(c.Holder), "access": access}
		grantee := doc{"directedBy": actor.Email, "projectId": projectID, "experimentId": run["experimentId"]}
		if !found || c.Intake != "all" || !connAllowed(record, grantee) || tool != "" && !inList(strs(tools), tool) {
			return "", nil, fail(403, "Pipeline connection or tool is no longer granted")
		}
		connectionStamps = append(connectionStamps, doc{"name": name, "access": access, "tools": tools, "intake": c.Intake})
	}
	stamp["connections"] = connectionStamps
	if host != "" {
		var h db.PipelineHostRow
		if share {
			var r db.PipelineHostShareRow
			r, err = d.PipelineHostShare(q.ctx(), "host:"+host)
			h = db.PipelineHostRow(r)
		} else {
			h, err = d.PipelineHost(q.ctx(), "host:"+host)
		}
		if errors.Is(err, pgx.ErrNoRows) || err == nil && h.State == "removed" {
			return "", nil, fail(403, "Pipeline host is no longer available")
		}
		if err != nil {
			return "", nil, err
		}
		holder, err := resolve(q.ctx(), q.tx, h.Holder)
		if err != nil || holder == nil || projectID != "" && !mayAccessProjectID(holder, projectID) {
			return "", nil, fail(403, "Pipeline host holder no longer has project access")
		}
		stamp["host"], stamp["holderAccessVersion"] = host, holder.AccessVersion
	}
	return sha256hex([]byte(canonical(stamp))), connections, nil
}

func pipelineStepIndex(run doc, executionID string) int {
	for i := range pipelineEntries(run) {
		if refExecutionID(pipelineEntry(run, i)) == executionID {
			return i
		}
	}
	return -1
}

// claimPipelineAPI mints once in the transaction that wins the persisted claim.
// Status/recent-task reads observe the existing job and never create a new effect.
// errFileTool: a pipeline api step names a file tool (file: upload|download), which only a host client can call.
var errFileTool = errors.New("file tool")

// claimPipelineAPI also answers the tool's idempotent flag from the minted spec (how an edge error reads).
func claimPipelineAPI(q *Request, runID, executionID, connection, tool string) (doc, string, bool, bool, error) {
	if _, err := q.db().PipelineRunLock(q.ctx(), runID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, false, err
	}
	run, err := getRun(q, runID)
	if err != nil || run == nil {
		return run, "", false, false, err
	}
	index := pipelineStepIndex(run, executionID)
	if run["state"] != "running" || index < 0 {
		return run, "", false, false, nil
	}
	entry := pipelineEntry(run, index)
	def := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	phase := dispatchPhase(entry)
	if def["kind"] != "api" || def["connection"] != connection {
		return run, "", false, false, fail(409, "Pipeline call does not match its declared API step")
	}
	observe := def["mode"] == "job" && tool != astr(def["tool"]) && (tool == jobTool(def, "job_status") || tool == jobTool(def, "recent_tasks"))
	if observe {
		c, err := connByName(q, connection)
		if err != nil {
			return run, "", false, false, err
		}
		meta := mapOf(providerTools(astr(c["provider"]))[tool])
		if meta["method"] != "GET" || meta["idempotent"] != true || phase != contract.PipelineDispatchPhaseHandedOff {
			return run, "", false, false, fail(409, "Pipeline observation is not a granted idempotent job read")
		}
	} else if phase != contract.PipelineDispatchPhaseReserved {
		return run, "", false, false, nil
	}
	stamp, _, err := pipelineAuthorityStamp(q, run, index, connection, tool, "")
	if err != nil {
		return run, "", false, false, err
	}
	m, err := mint(q, doc{"executionId": executionID, "directedBy": mapOf(run["authority"])["actorEmail"], "projectId": run["projectId"], "experimentId": run["experimentId"], "step": true}, []string{connection})
	if err != nil {
		return run, "", false, false, err
	}
	token := astr(m["token"])
	idempotent := false
	for _, c := range anys(m["connections"]) {
		for _, t := range anys(mapOf(c)["tools"]) {
			if mapOf(t)["name"] == tool {
				idempotent = mapOf(t)["idempotent"] == true
				if mapOf(t)["file"] != nil {
					return run, "", false, false, errFileTool // the coordinator calls JSON tools only
				}
			}
		}
	}
	if token == "" {
		return run, "", false, false, fail(403, "Pipeline token was not granted")
	}
	if !observe {
		pipelineHandOff(entry.Dispatch, stamp)
		if err := putRun(q, run); err != nil {
			return run, "", false, false, err
		}
	}
	return run, token, true, idempotent, nil
}

func preparePipelineCommand(q *Request, execution, command doc) error {
	if execution["pipelineRunId"] == nil {
		return nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return fail(409, "Pipeline command preparation requires its transaction fence")
	}
	run, err := getRun(q, astr(execution["pipelineRunId"]))
	if err != nil || run == nil {
		return err
	}
	index := pipelineStepIndex(run, astr(execution["pipelineStepId"]))
	if run["state"] != "running" || index < 0 {
		return fail(409, "Pipeline no longer permits command preparation")
	}
	step := mapOf(anys(run["steps"])[index])
	if step["executionId"] != execution["id"] {
		return fail(409, "Pipeline native execution identity changed")
	}
	entry := pipelineEntry(run, index)
	if dispatchPhase(entry) != contract.PipelineDispatchPhaseReserved {
		return fail(409, "Pipeline command was already prepared")
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, "", "", astr(command["hostId"])); err != nil {
		return err
	}
	d := entry.Dispatch
	d.Phase, d.NativeExecutionId, d.CommandId, d.HostId = contract.PipelineDispatchPhasePrepared, strp(execution, "id"), strp(command, "id"), strp(command, "hostId")
	return putRun(q, run)
}

// handoffPipelineCommand is the last authorization point before a host sees the
// command. Redelivery keeps its exact stored body and the executor's journal ID.
// Only the command the plan dispatched (an agent start or a script) is gated; the
// execution's later commands (stop, steer, input, teardown) go like any other.
func handoffPipelineCommand(q *Request, execution, command doc) (bool, error) {
	if execution["pipelineRunId"] == nil || !pipelineDispatchKind(command["kind"]) {
		return true, nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return false, nil // a stale poll preflight waits for the next correctly fenced poll
	}
	// A command that can never be handed off is stopped with its reason, never answered as an error: an error
	// fails the whole poll, so the host would get no other command and no update target. Its execution is left
	// to its engine and the lost-host sweep, as before.
	run, err := getRun(q, astr(execution["pipelineRunId"]))
	if pipelineFormatUnsupported(err) {
		return false, stopPipelineCommand(q, command, "pipeline run format unsupported")
	}
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, stopPipelineCommand(q, command, "pipeline run removed")
	}
	if executionEnds(astr(run["state"])) {
		return false, stopPipelineCommand(q, command, "pipeline run ended")
	}
	index := pipelineStepIndex(run, astr(execution["pipelineStepId"]))
	if run["state"] != "running" || index < 0 {
		return false, nil
	}
	step := mapOf(anys(run["steps"])[index])
	if step["executionId"] != execution["id"] {
		return false, stopPipelineCommand(q, command, "pipeline command execution differs")
	}
	entry := pipelineEntry(run, index)
	d := entry.Dispatch
	if d == nil || optString(d.CommandId) != command["id"] || optString(d.NativeExecutionId) != execution["id"] || optString(d.HostId) != command["hostId"] {
		return false, stopPipelineCommand(q, command, "pipeline command binding differs")
	}
	stamp, connections, err := pipelineAuthorityStamp(q, run, index, "", "", astr(command["hostId"]))
	if err != nil {
		return false, err
	}
	if d.Phase == contract.PipelineDispatchPhaseHandedOff {
		return true, nil // same immutable command, no token mint or new invocation
	}
	if d.Phase != contract.PipelineDispatchPhasePrepared {
		return false, nil
	}
	if len(connections) > 0 {
		grants, err := mint(q, doc{"executionId": execution["id"], "directedBy": mapOf(run["authority"])["actorEmail"], "projectId": run["projectId"], "experimentId": run["experimentId"], "pipelineRunId": run["id"]}, connections)
		if err != nil {
			return false, err
		}
		refused, ok := grants["refused"].([]string)
		if grants["token"] == nil || !ok || len(refused) > 0 {
			return false, fail(403, "Pipeline command connection grant was refused")
		}
		payload := mapOf(command["payload"])
		payload["runToken"], payload["connections"] = grants["token"], grants["connections"]
		if err := put(q, "command", command); err != nil {
			return false, err
		}
	}
	pipelineHandOff(d, stamp)
	return true, putRun(q, run)
}

// pipelineHandOff: a dispatch is handed off now by this coordinator, under authority stamp.
func pipelineHandOff(d *contract.PipelineDispatch, stamp string) {
	claim, boot, at := newID(), pipelineBootID, nowMs()
	d.Phase, d.ClaimId, d.OwnerBootId, d.HandedOffAtMs, d.AuthoritySha256 = contract.PipelineDispatchPhaseHandedOff, &claim, &boot, &at, &stamp
}

// guardPipelineWrite rejects a stale caller before any row is overwritten.
// Retried DB-only transactions restore their in-memory revision too; no external
// effect is performed by this helper or by a transaction retry.
func guardPipelineWrite(q *Request, run doc) error {
	if err := requirePipelinePlan(run); err != nil {
		return err
	}
	plan := pipelinePlanOfRun(run)
	want := plan.Revision
	stored, err := q.db().PipelineStoredPlan(q.ctx(), astr(run["id"]))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	next := want
	if err == nil && stored.IsRun {
		if stored.PlanVersion != strconv.Itoa(pipelinePlanVersion) || stored.Entries != stored.Steps {
			return pipelineFormatError()
		}
		if stored.Revision != want || stored.DefinitionSha256 != plan.DefinitionSha256 {
			return fail(409, "Pipeline plan changed before this transition")
		}
		next++
	}
	key := pipelineWriteKey{q.tx, astr(run["id"])}
	state := &pipelineWriteState{revision: want}
	previous, loaded := pipelineWrites.LoadOrStore(key, state)
	state = previous.(*pipelineWriteState)
	state.plans = append(state.plans, plan)
	if !loaded {
		onEnd(q.tx, func(committed bool) {
			if !committed {
				for _, p := range state.plans {
					p.Revision = state.revision
				}
			}
			pipelineWrites.Delete(key)
		})
	}
	plan.Revision = next
	return nil
}

func pipelinePlanSummary(run doc) contract.PipelinePlanSummary {
	var out contract.PipelinePlanSummary
	for i := range pipelineEntries(run) {
		e := pipelineEntry(run, i)
		out.Planned++
		switch e.Activation {
		case contract.PipelineActivationPending:
			out.Pending++
		case contract.PipelineActivationSelected:
			out.Selected++
			if entryRefMode(e) == contract.PipelineRefModeReused {
				out.ReusedExecutions++
			} else {
				out.FreshExecutions++
			}
		case contract.PipelineActivationNotSelected:
			out.NotSelected++
		case contract.PipelineActivationBlocked:
			out.Blocked++
		}
	}
	return out
}

func pipelineReservedInput(path string) bool {
	first, _, _ := strings.Cut(strings.ReplaceAll(path, "\\", "/"), "/")
	return strings.EqualFold(first, "__fleet_control") || strings.EqualFold(first, "__fleet_evaluation") || strings.EqualFold(first, "evaluation")
}

// selectPipelineStep is called after the current run was read under its write
// transaction. Its execution reservation and decision are persisted together.
func selectPipelineStep(q *Request, run doc, index int) error {
	entry := pipelineEntry(run, index)
	if entry == nil || entry.Activation != contract.PipelineActivationPending {
		return nil
	}
	definition := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	when, err := parsePipelineWhen(definition["when"])
	if err != nil {
		return err
	}
	decision := pipelineConditionDecision(nil, nil, nowMs())
	if when != nil {
		sourceID := when.Source.Step
		var source doc
		sourceIndex := -1
		for i, value := range anys(run["steps"])[:index] {
			if step := mapOf(value); step["id"] == sourceID {
				source, sourceIndex = step, i
				break
			}
		}
		reason := contract.PipelinePlanReasonSourceUnavailable
		if source != nil && source["state"] == "succeeded" && source["outputArtifactId"] != nil {
			if source["observation"] == "unconfirmed" {
				reason = contract.PipelinePlanReasonSourceUnconfirmed
			} else if err := pipelineOutputOwner(q, run, sourceIndex, astr(source["outputArtifactId"])); err != nil {
				return err
			} else {
				artifact, err := artifactRow(withTx(q), q.app, astr(source["outputArtifactId"]))
				if err != nil {
					return err
				}
				if sha256hex(jsonBytes(source["output"])) != artifact.ContentHash {
					reason = contract.PipelinePlanReasonSourceHashMismatch
				} else {
					decision = pipelineConditionDecision(when, source["output"], nowMs())
					decision.SourceArtifact = &contract.PipelineArtifactRef{ArtifactId: artifact.ID, Sha256: artifact.ContentHash}
					reason = ""
				}
			}
		}
		if reason != "" {
			decision = pipelineBlockedDecision(when, reason, nowMs())
		}
	}
	entry.Activation, entry.Decision = contract.PipelineActivation(decision.Outcome), &decision
	if entry.Activation == contract.PipelineActivationBlocked {
		entry.Problem = &contract.PipelinePlanProblem{Code: decision.Reason, Message: "Condition evidence is unavailable or invalid"}
		return finishRun(q, run, "failed", string(decision.Reason))
	}
	if entry.Activation == contract.PipelineActivationNotSelected {
		return putRun(q, run)
	}
	connection, tool := "", ""
	if definition["kind"] == "api" {
		connection, tool = astr(definition["connection"]), astr(definition["tool"])
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, connection, tool, ""); err != nil {
		entry.Activation = contract.PipelineActivationBlocked
		decision.Outcome, decision.Reason = contract.PipelineDecisionOutcomeBlocked, contract.PipelinePlanReasonAuthorityDenied
		entry.Problem = &contract.PipelinePlanProblem{Code: contract.PipelinePlanReasonAuthorityDenied, Message: "Current pipeline authority was denied"}
		return finishRun(q, run, "failed", "authority_denied")
	}
	row := stepRow(run, index)
	existing, err := q.db().PipelineExecutionExists(q.ctx(), row)
	if err != nil {
		return err
	}
	if existing {
		return fail(409, "Pipeline execution reservation identity conflict")
	}
	entry.ExecutionRef = &contract.PipelineExecutionRef{Mode: contract.PipelineRefModeFresh, ExecutionId: row, SourceRunId: astr(run["id"]), SourceStepId: entry.Id,
		SourceStepIndex: int64(index), SourceDefinitionSha256: astr(run["definitionSha256"]), SourceInputSha256: sha256hex([]byte("[]"))}
	entry.Dispatch = &contract.PipelineDispatch{Phase: contract.PipelineDispatchPhaseReserved}
	step := mapOf(anys(run["steps"])[index])
	step["state"] = "preparing"
	return putRun(q, run)
}

type pipelinePreparedInputs struct {
	files    []any
	bindings []contract.PipelineInputBinding
	example  []any
}

// materializePipelineInputs performs object I/O outside plan write locks. The
// caller later freezes these exact bindings using its expected plan revision.
func materializePipelineInputs(ctx context.Context, a *App, run doc, index int) (*pipelinePreparedInputs, error) {
	prepared := &pipelinePreparedInputs{files: []any{}, bindings: []contract.PipelineInputBinding{}, example: append([]any{}, anys(run["exampleInputs"])...)}
	if frozen := pipelineEntry(run, index).Inputs; frozen != nil {
		if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
			for _, binding := range frozen {
				artifact, err := artifactRow(withTx(q), a, binding.ArtifactId)
				if err != nil || artifact.State != "stored" || !same(artifact.ProjectID, run["projectId"]) || artifact.ContentHash != binding.Sha256 {
					return fail(409, "Frozen pipeline input is unavailable or changed")
				}
				prepared.files = append(prepared.files, doc{"artifactId": binding.ArtifactId, "path": binding.Path})
			}
			return nil
		}); err != nil {
			return nil, err
		}
		prepared.bindings = frozen
		return prepared, nil
	}
	if run["exampleInputs"] == nil {
		for _, value := range anys(mapOf(run["example"])["inputs"]) {
			input := mapOf(value)
			name := astr(input["path"])
			if pipelineReservedInput(name) || !pipelineInputPath(name) {
				return nil, fail(400, "Invalid or reserved pipeline input path")
			}
			var data []byte
			if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
				var err error
				data, err = exampleFileBytes(q, run["projectId"], input)
				return err
			}); err != nil {
				return nil, err
			}
			media := firstString(input["media_type"], "text/plain")
			id, _, err := saveObject(ctx, a, projectIDPtr(run["projectId"]), runScope(run), astr(run["id"]), "example/"+name, data, media)
			if err != nil {
				return nil, err
			}
			prepared.example = append(prepared.example, doc{"artifactId": id, "path": "example/" + name})
		}
	}
	prepared.files = append(prepared.files, prepared.example...)
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		files, err := pipelineOutputs(q, run, index)
		if err != nil {
			return err
		}
		for _, file := range files {
			prepared.files = append(prepared.files, file.doc())
		}
		seen := map[string]bool{}
		for _, value := range prepared.files {
			file := mapOf(value)
			name := astr(file["path"])
			if !pipelineInputPath(name) || pipelineReservedInput(name) || seen[name] {
				return fail(409, "Pipeline staged input path collision")
			}
			seen[name] = true
			artifact, err := artifactRow(withTx(q), a, astr(file["artifactId"]))
			if err != nil {
				return err
			}
			if artifact.State != "stored" || !same(artifact.ProjectID, run["projectId"]) {
				return fail(409, "Unavailable or foreign pipeline input")
			}
			binding := contract.PipelineInputBinding{Path: name, ArtifactId: astr(file["artifactId"]), Sha256: artifact.ContentHash}
			producer, _, _ := strings.Cut(name, "/")
			for i := range pipelineEntries(run)[:index] {
				entry := pipelineEntry(run, i)
				if entry.Id == producer && entry.Activation == contract.PipelineActivationSelected {
					ref := entry.ExecutionRef
					stepID := entry.Id
					binding.ProducerRunId, binding.ProducerStepId, binding.ProducerExecutionId = &ref.SourceRunId, &stepID, &ref.ExecutionId
					if mapOf(anys(run["steps"])[i])["state"] != "succeeded" {
						return fail(409, "Pipeline input producer has not succeeded")
					}
				}
			}
			prepared.bindings = append(prepared.bindings, binding)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(prepared.bindings, func(i, j int) bool { return prepared.bindings[i].Path < prepared.bindings[j].Path })
	if num(mapOf(run["definition"])["version"]) == 3 {
		entries := []any{}
		for i := range pipelineEntries(run)[:index] {
			entry := pipelineEntry(run, i)
			metadata := doc{"id": entry.Id, "index": i, "activation": entry.Activation}
			if entry.ExecutionRef != nil {
				metadata["executionRef"] = entry.ExecutionRef
			}
			step := mapOf(anys(run["steps"])[i])
			if native := astr(step["executionId"]); native != "" {
				if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
					raw, err := q.db().PipelineExecutionDefinitions(q.ctx(), db.PipelineExecutionDefinitionsParams{ID: native, ProjectID: projectIDPtr(run["projectId"])})
					if errors.Is(err, pgx.ErrNoRows) {
						return nil
					}
					if err != nil {
						return err
					}
					var pins any
					if err := json.Unmarshal(raw, &pins); err != nil {
						return err
					}
					if pins != nil {
						metadata["definitionPins"] = pins
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
			if step["state"] != nil {
				metadata["observation"] = firstString(step["observation"], "confirmed")
				metadata["executionState"] = step["state"]
			}
			for _, binding := range prepared.bindings {
				if binding.Path == entry.Id+"/output.json" {
					metadata["output"] = contract.PipelineArtifactRef{ArtifactId: binding.ArtifactId, Sha256: binding.Sha256}
				}
			}
			entries = append(entries, metadata)
		}
		manifest := doc{"schema": "fleet.pipeline-control.v1", "runId": run["id"], "consumerStepId": pipelineEntry(run, index).Id, "definitionSha256": run["definitionSha256"], "planRevision": pipelineRevision(run), "entries": entries, "inputs": prepared.bindings}
		id, hash, err := saveObject(ctx, a, projectIDPtr(run["projectId"]), runScope(run), stepRow(run, index), "__fleet_control/pipeline.json", jsonBytes(manifest), "application/json")
		if err != nil {
			return nil, err
		}
		prepared.files = append(prepared.files, doc{"path": "__fleet_control/pipeline.json", "artifactId": id})
		prepared.bindings = append(prepared.bindings, contract.PipelineInputBinding{Path: "__fleet_control/pipeline.json", ArtifactId: id, Sha256: hash})
		sort.Slice(prepared.bindings, func(i, j int) bool { return prepared.bindings[i].Path < prepared.bindings[j].Path })
	}
	return prepared, nil
}

func pipelineInputPath(value string) bool {
	return value != "" && path.IsAbs(value) == false && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, ":\\\x00")
}

func freezePipelineInputs(run doc, index int, inputs *pipelinePreparedInputs) {
	run["exampleInputs"] = inputs.example
	entry := pipelineEntry(run, index)
	entry.Inputs = inputs.bindings
	entry.ExecutionRef.SourceInputSha256 = sha256hex([]byte(canonical(inputs.bindings)))
}

func materializePipelineOutput(ctx context.Context, a *App, run doc, index int, output any) error {
	step := mapOf(anys(run["steps"])[index])
	if step["outputArtifactId"] != nil {
		return nil
	}
	id, _, err := saveObject(ctx, a, projectIDPtr(run["projectId"]), runScope(run), stepRow(run, index), "output.json", jsonBytes(output), "application/json")
	if err == nil {
		step["outputArtifactId"] = id
	}
	return err
}

func bindPipelineResult(q *Request, run doc) error {
	steps := anys(run["steps"])
	if len(steps) == 0 {
		return fail(409, "Pipeline has no final output")
	}
	index := len(steps) - 1
	step, entry := mapOf(steps[index]), pipelineEntry(run, index)
	if entry.Activation != contract.PipelineActivationSelected || step["state"] != "succeeded" || step["observation"] == "unconfirmed" {
		return fail(409, "Pipeline final output is not confirmed")
	}
	artifact, err := artifactRow(withTx(q), q.app, astr(step["outputArtifactId"]))
	if err != nil {
		return err
	}
	if err := pipelineOutputOwner(q, run, index, artifact.ID); err != nil {
		return err
	}
	if artifact.ContentHash != sha256hex(jsonBytes(step["output"])) {
		return fail(409, "Pipeline final output bytes changed")
	}
	inputs := append([]contract.PipelineInputBinding{}, entry.Inputs...)
	result := doc{"mode": "static", "stepId": entry.Id, "producerExecutionId": refExecutionID(entry), "artifact": doc{"artifactId": artifact.ID, "sha256": artifact.ContentHash}, "inputs": inputs, "selectedSources": []any{}}
	if num(mapOf(run["definition"])["version"]) == 3 {
		output := mapOf(step["output"])
		lineage := mapOf(output["fleetLineage"])
		if !keysOnly(output, "fleetLineage", "projectResult") || output["projectResult"] == nil || lineage == nil || !keysOnly(lineage, "schema", "selectedSources") || lineage["schema"] != "fleet.result-lineage.v1" || anys(lineage["selectedSources"]) == nil {
			return fail(409, "Finalizer result lineage is invalid")
		}
		for _, binding := range inputs {
			if binding.Path == "__fleet_control/pipeline.json" {
				control, err := artifactRow(withTx(q), q.app, binding.ArtifactId)
				if err != nil {
					return err
				}
				if control.State != "stored" || !same(control.ProjectID, run["projectId"]) || control.ExecutionID == nil || *control.ExecutionID != refExecutionID(entry) || control.ContentHash != binding.Sha256 {
					return fail(409, "Finalizer control manifest binding changed")
				}
				result["controlArtifact"] = doc{"artifactId": control.ID, "sha256": control.ContentHash}
			}
		}
		if result["controlArtifact"] == nil {
			return fail(409, "Finalizer control manifest is missing")
		}
		for _, source := range anys(lineage["selectedSources"]) {
			binding := mapOf(source)
			if binding == nil || !keysOnly(binding, "path", "artifactId", "sha256", "producerRunId", "producerStepId", "producerExecutionId") || astr(binding["producerExecutionId"]) == "" {
				return fail(409, "Finalizer selected source is not a producer input")
			}
			found := false
			for _, input := range inputs {
				found = found || canonical(input) == canonical(source)
			}
			if !found {
				return fail(409, "Finalizer selected source was not staged")
			}
		}
		result["mode"], result["selectedSources"] = "finalizer", lineage["selectedSources"]
	}
	run["canonicalResult"] = result
	return nil
}

func pipelinePlanShape(run doc) error {
	if err := requirePipelinePlan(run); err != nil {
		return err
	}
	for i := range pipelineEntries(run) {
		entry := pipelineEntry(run, i)
		definition := mapOf(anys(mapOf(run["definition"])["steps"])[i])
		if entry.Id != definition["id"] || entry.DefinitionSha256 != sha256hex([]byte(canonical(definition))) {
			return fmt.Errorf("pipeline plan entry %d does not match its definition", i)
		}
		ref := entry.ExecutionRef
		if entry.Activation == contract.PipelineActivationSelected {
			if ref == nil || ref.SourceStepId != entry.Id || ref.SourceStepIndex != int64(i) || !labSha.MatchString(ref.SourceDefinitionSha256) || !labSha.MatchString(ref.SourceInputSha256) {
				return fail(409, "Invalid selected execution reference")
			}
			if ref.Mode == contract.PipelineRefModeFresh && (ref.ExecutionId != stepRow(run, i) || ref.SourceRunId != run["id"] || entry.Dispatch == nil) {
				return fail(409, "Invalid fresh execution ownership")
			}
			if ref.Mode == contract.PipelineRefModeReused && entry.Dispatch != nil {
				return fail(409, "Reused execution cannot carry a new dispatch")
			}
		} else if ref != nil || entry.Dispatch != nil || entry.Inputs != nil {
			return fail(409, "Inactive plan entry carries execution facts")
		}
	}
	return nil
}

func pipelinePlanRead(q *Request, run doc) (doc, error) {
	if err := pipelinePlanShape(run); err != nil {
		return nil, err
	}
	out := doc{}
	for k, v := range run {
		if k != "steps" {
			out[k] = v
		}
	}
	plan := pipelinePlanOfRun(run)
	view := &contract.PipelinePlan{DefinitionSha256: plan.DefinitionSha256, Revision: plan.Revision, Entries: []contract.PipelinePlanEntry{}}
	for i := range plan.Entries {
		entry := plan.Entries[i]
		if entry.Activation == contract.PipelineActivationSelected {
			step := mapOf(anys(run["steps"])[i])
			execution := &contract.PipelineExecutionView{ExecutionId: refExecutionID(&entry), State: contract.ExecutionState(astr(step["state"])), Observation: contract.PipelineObservationConfirmed}
			if o, ok := step["observation"].(string); ok {
				execution.Observation = contract.PipelineObservation(o)
			}
			if id := astr(step["executionId"]); id != "" {
				execution.NativeExecutionId = &id
			}
			raw := func(v any) json.RawMessage {
				if v == nil {
					return nil
				}
				return jsonBytes(v)
			}
			execution.StartedAt, execution.EndedAt, execution.Usage, execution.Error = raw(step["startedAt"]), raw(step["finishedAt"]), raw(step["usage"]), raw(step["error"])
			if d, ok := numOK(step["durationMs"]); ok {
				execution.DurationMs = &d
			}
			if step["calls"] != nil {
				execution.Calls = []json.RawMessage{}
				for _, c := range anys(step["calls"]) {
					execution.Calls = append(execution.Calls, jsonBytes(c))
				}
			}
			entry.Execution = execution
			if id := astr(step["outputArtifactId"]); id != "" {
				artifact, err := artifactRow(withTx(q), q.app, id)
				if err != nil {
					return nil, err
				}
				if artifact.State == "stored" {
					if err := pipelineOutputOwner(q, run, i, id); err != nil {
						return nil, err
					}
					if artifact.ContentHash != sha256hex(jsonBytes(step["output"])) {
						return nil, fail(409, "Pipeline output bytes changed")
					}
					entry.Output = &contract.PipelineArtifactRef{ArtifactId: id, Sha256: artifact.ContentHash}
				}
			}
		}
		view.Entries = append(view.Entries, entry)
	}
	out["plan"], out["planSummary"] = view, pipelinePlanSummary(run)
	return out, nil
}

// pipelinePublicRead assembles only the current plan contract. Internal runner
// projections never leak an eager or fabricated steps array to callers.
func pipelinePublicRead(ctx context.Context, a *App, id string) (doc, error) {
	var out doc
	err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		run, err := getRun(q, id)
		if err != nil {
			return err
		}
		if run == nil {
			return fail(404, "Unknown pipeline run")
		}
		out, err = pipelinePlanRead(q, run)
		return err
	})
	return out, err
}

func pipelineResultValue(run doc) any {
	result := mapOf(run["canonicalResult"])
	for i := range pipelineEntries(run) {
		if pipelineEntry(run, i).Id == result["stepId"] {
			output := mapOf(anys(run["steps"])[i])["output"]
			if result["mode"] == "finalizer" {
				return mapOf(output)["projectResult"]
			}
			return output
		}
	}
	return nil
}

// A rerun is not permission to replay a possibly completed provider/native
// action. Only the existing command-bound local process proof can release an
// interrupted script suffix; the original observation stays unconfirmed.
func pipelineRecoveryFence(q *Request, run doc) error {
	for i := range pipelineEntries(run) {
		entry := pipelineEntry(run, i)
		if entry.Activation != contract.PipelineActivationSelected {
			continue
		}
		step := mapOf(anys(run["steps"])[i])
		if !unconfirmed(step) {
			continue
		}
		definition := mapOf(anys(mapOf(run["definition"])["steps"])[i])
		if definition["kind"] != "script" || entryRefMode(entry) != contract.PipelineRefModeFresh {
			return fail(409, "Unconfirmed external action cannot be replayed by rerun")
		}
		native := astr(step["executionId"])
		raw, err := q.db().PipelineHostJob(q.ctx(), native)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		// suffixProof (lab_retry.go) reads the host job's record.
		var execution doc
		if err := json.Unmarshal(raw, &execution); err != nil {
			return err
		}
		if execution == nil || execution["pipelineRunId"] != run["id"] || !same(execution["projectId"], run["projectId"]) {
			return fail(409, "Interrupted script ownership proof unavailable")
		}
		commands, err := all(q, "command", doc{"executionId": native})
		if err != nil {
			return err
		}
		command, err := suffixCommand(commands)
		if err != nil {
			return err
		}
		d := entry.Dispatch
		if d == nil || optString(d.CommandId) != command["id"] || optString(d.NativeExecutionId) != native || d.Phase != contract.PipelineDispatchPhaseHandedOff {
			return fail(409, "Interrupted script handoff proof unavailable")
		}
		events, err := eventsWhere(q, doc{"executionId": native, "type": "process.reconciled"})
		if err != nil {
			return err
		}
		matches := 0
		for _, event := range events {
			if suffixProof(execution, command, event, stepRow(run, i)) {
				matches++
			}
		}
		if matches != 1 {
			return fail(409, "Interrupted script has no unique command-bound process proof")
		}
	}
	return nil
}

// restorePipelineValue keeps existing map/slice aliases alive while restoring a
// transition snapshot. Numeric types are preserved; no JSON roundtrip is used.
func restorePipelineValue(current, snapshot any) any {
	switch source := snapshot.(type) {
	case *contract.PipelinePlan:
		target, _ := current.(*contract.PipelinePlan)
		if target == nil || target == source {
			target = &contract.PipelinePlan{}
		}
		target.DefinitionSha256, target.Revision = source.DefinitionSha256, source.Revision
		if len(target.Entries) != len(source.Entries) {
			target.Entries = make([]contract.PipelinePlanEntry, len(source.Entries))
		}
		for i := range source.Entries {
			restorePipelineEntry(&target.Entries[i], source.Entries[i])
		}
		return target
	case map[string]any:
		target := mapOf(current)
		if target == nil {
			target = doc{}
		}
		for key := range target {
			if _, ok := source[key]; !ok {
				delete(target, key)
			}
		}
		for key, value := range source {
			target[key] = restorePipelineValue(target[key], value)
		}
		return target
	case []any:
		target := anys(current)
		if len(target) != len(source) {
			target = make([]any, len(source))
		}
		for i, value := range source {
			target[i] = restorePipelineValue(target[i], value)
		}
		return target
	default:
		return snapshot
	}
}

// restorePipelineEntry sets *dst to a copy of src, keeping dst's dispatch and execution reference aliases alive.
func restorePipelineEntry(dst *contract.PipelinePlanEntry, src contract.PipelinePlanEntry) {
	if src.Decision != nil {
		d := *src.Decision
		d.Path = append([]string(nil), d.Path...)
		src.Decision = &d
	}
	if src.ExecutionRef != nil {
		r := *src.ExecutionRef
		if dst.ExecutionRef != nil {
			*dst.ExecutionRef, src.ExecutionRef = r, dst.ExecutionRef
		} else {
			src.ExecutionRef = &r
		}
	}
	if src.Dispatch != nil {
		x := *src.Dispatch
		if dst.Dispatch != nil {
			*dst.Dispatch, src.Dispatch = x, dst.Dispatch
		} else {
			src.Dispatch = &x
		}
	}
	if src.Inputs != nil {
		src.Inputs = append(make([]contract.PipelineInputBinding, 0, len(src.Inputs)), src.Inputs...)
	}
	if src.Problem != nil {
		p := *src.Problem
		src.Problem = &p
	}
	*dst = src
}

// runPipelineTransition retries DB work with the complete pre-transition
// snapshot, including a paid answer's already materialized output binding.
// Effects and object I/O must remain outside this callback.
func runPipelineTransition(ctx context.Context, a *App, run doc, fn func(*Request) error) error {
	snapshot := restorePipelineValue(nil, run)
	return runTx(ctx, a, nil, true, "", func(q *Request) error {
		cancelled, err := q.db().PipelineRunCancelled(q.ctx(), astr(run["id"]))
		if err != nil {
			return err
		}
		if cancelled {
			return fail(409, "Pipeline is cancelled")
		}
		onEnd(q.tx, func(committed bool) {
			if !committed {
				restorePipelineValue(run, snapshot)
			}
		})
		return fn(q)
	})
}
