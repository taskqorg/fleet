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

// initPlan creates no execution. Static and guarded definitions share
// this same persisted model; there is no historical-row reconstruction path.
func (run *runRecord) initPlan() error {
	actor := run.director()
	if actor == "" {
		return fail(403, "Pipeline requires its original initiating actor")
	}
	entries, capabilities := []contract.PipelinePlanEntry{}, []contract.PipelineCapabilityRef{}
	seen := map[string]bool{}
	for i := range run.Definition.Steps {
		step := run.def(i)
		entries = append(entries, contract.PipelinePlanEntry{Id: step.ID, Index: int64(i), DefinitionSha256: step.sha(), Activation: contract.PipelineActivationPending})
		kind, id := contract.PipelineCapabilityKind(""), ""
		if step.Kind == "api" {
			kind, id = contract.PipelineCapabilityKindConnection, step.Connection
		} else if step.Kind == "agent" {
			kind, id = contract.PipelineCapabilityKindProfile, step.Profile
			if override := run.Overrides.Profile; override != "" {
				id = override
			}
		}
		if id != "" && !seen[string(kind)+":"+id] {
			seen[string(kind)+":"+id] = true
			capabilities = append(capabilities, contract.PipelineCapabilityRef{Kind: kind, Id: id})
		}
	}
	sort.Slice(capabilities, func(i, j int) bool {
		a, b := capabilities[i], capabilities[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Id < b.Id
	})
	for _, input := range run.exampleInputs() {
		if p, _ := jsonString(input["path"]); pipelineReservedInput(p) {
			return fail(400, "Example input collides with reserved pipeline control namespace")
		}
	}
	run.PlanVersion = pipelinePlanVersion
	run.Plan = &contract.PipelinePlan{DefinitionSha256: run.DefinitionSha256, Revision: 0, Entries: entries}
	run.Authority = &contract.PipelineAuthority{ActorEmail: actor, ProjectId: run.ProjectID, RequestedCapabilities: capabilities}
	return nil
}

// actor: the initiating actor the run's authority retains ("" without one).
func (run *runRecord) actor() string {
	if run.Authority == nil {
		return ""
	}
	return run.Authority.ActorEmail
}

func (run *runRecord) requirePlan() error {
	if run.PlanVersion != pipelinePlanVersion || len(run.entries()) != len(run.Definition.Steps) {
		return pipelineFormatError()
	}
	return nil
}

// conflictWithReason: a 409 whose body names its reason (HTTPError's extra fields).
func conflictWithReason(msg, reason string) error {
	return &HTTPError{Code: 409, Msg: msg, Extra: reasonExtra{reason}}
}

func pipelineFormatError() error {
	return conflictWithReason("Unsupported pipeline run format; a clean start is required", "pipeline_format_unsupported")
}

func pipelineFormatUnsupported(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	r, _ := he.extraReason()
	return r == "pipeline_format_unsupported"
}

// pipelineDispatchKinds: the command kinds a pipeline plan dispatches (preparePipelineCommand, startScript); the
// poll preflight takes the GLOBAL lock for them (pollReadyCommands).
var pipelineDispatchKinds = []string{string(contract.CommandKindStart), string(contract.CommandKindScript)}

func pipelineDispatchKind(kind string) bool { return slices.Contains(pipelineDispatchKinds, kind) }

// stopPipelineCommand settles a still pending command that will never be handed off, with the reason.
func stopPipelineCommand(q *Request, command any, reason string) error {
	id := agentView[agentCommand](command).ID
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

// pipelineStamp: what a dispatch's authority hash covers (fields in key order): the initiating actor and its access,
// the project, an agent step's profile, each connection's grant and a host's holder.
type pipelineStamp struct {
	AccessVersion       string                    `json:"accessVersion"`
	Actor               string                    `json:"actor"`
	Connections         []pipelineConnectionStamp `json:"connections"`
	HolderAccessVersion *string                   `json:"holderAccessVersion,omitempty"`
	Host                *string                   `json:"host,omitempty"`
	Profile             *string                   `json:"profile,omitempty"`
	ProfileSha256       *string                   `json:"profileSha256,omitempty"`
	Project             *string                   `json:"project"`
	ProjectHead         *int32                    `json:"projectHead"`
	ProjectVersion      *int64                    `json:"projectVersion"`
}

type pipelineConnectionStamp struct {
	Access any    `json:"access"`
	Intake string `json:"intake"`
	Name   string `json:"name"`
	Tools  any    `json:"tools"`
}

// pipelineAuthorityStamp holds the current authority rows through token mint or
// command handoff. A frozen project/profile is evidence, not permanent access.
func pipelineAuthorityStamp(q *Request, run *runRecord, index int, connection, tool, host string) (string, []string, error) {
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

func pipelineAuthorityStampWithin(q *Request, run *runRecord, index int, connection, tool, host string) (string, []string, error) {
	share := false
	resolve := resolveAccessTx
	if value, ok := txLockStates.Load(q.tx); ok && value.(*txLockState).write {
		share, resolve = true, resolveAccessShareTx
	}
	d := q.db()
	actorEmail := run.actor()
	projectID := run.project()
	actor, err := resolve(q.ctx(), q.tx, actorEmail)
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
	stamp := pipelineStamp{Actor: actor.Email, AccessVersion: actor.AccessVersion, Project: run.ProjectID, ProjectVersion: projectVersion, ProjectHead: projectHead}
	connections := []string{}
	definition := run.def(index)
	if connection != "" {
		connections = append(connections, connection)
	} else if definition.Kind == "agent" {
		profileID := definition.Profile
		if override := run.Overrides.Profile; override != "" {
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
		_, profile, err := stepProfile(q, definition, &run.Overrides)
		if err != nil {
			return "", nil, err
		}
		if native := run.Steps[index].ExecutionID; native != "" {
			same, err := sameAgentStep(q, native, definition, &run.Overrides)
			if err != nil {
				return "", nil, err
			}
			if !same {
				return "", nil, fail(409, "Pipeline pinned profile changed before dispatch")
			}
		}
		sha := sha256hex([]byte(canonical(profile)))
		stamp.Profile, stamp.ProfileSha256 = &profileID, &sha
		connections = jsonStrings(agentView[struct {
			Connections json.RawMessage `json:"connections"`
		}](profile).Connections)
	}
	sort.Strings(connections)
	stamp.Connections = []pipelineConnectionStamp{}
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
		var grant connAccess
		allowed := found && json.Unmarshal(orNull(c.Access), &grant) == nil &&
			connAllowedFor(apiConnection{Holder: c.Holder, Access: grant}, mintRun{DirectedBy: &actor.Email, ProjectID: &projectID, ExperimentID: agentOpt(run.ExperimentID)})
		if !found || c.Intake != "all" || !allowed || tool != "" && !inList(strs(tools), tool) {
			return "", nil, fail(403, "Pipeline connection or tool is no longer granted")
		}
		stamp.Connections = append(stamp.Connections, pipelineConnectionStamp{Name: name, Access: access, Tools: tools, Intake: c.Intake})
	}
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
		stamp.Host, stamp.HolderAccessVersion = &host, &holder.AccessVersion
	}
	return sha256hex([]byte(canonical(stamp))), connections, nil
}

func pipelineStepIndex(run *runRecord, executionID string) int {
	for i := range run.entries() {
		if refExecutionID(run.entry(i)) == executionID {
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
func claimPipelineAPI(q *Request, runID, executionID, connection, tool string) (*runRecord, string, bool, bool, error) {
	if _, err := q.db().PipelineRunLock(q.ctx(), runID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, false, err
	}
	run, err := getRun(q, runID)
	if err != nil || run == nil {
		return run, "", false, false, err
	}
	index := pipelineStepIndex(run, executionID)
	if run.State != "running" || index < 0 {
		return run, "", false, false, nil
	}
	entry, def := run.entry(index), run.def(index)
	phase := dispatchPhase(entry)
	if def.Kind != "api" || def.Connection != connection {
		return run, "", false, false, fail(409, "Pipeline call does not match its declared API step")
	}
	observe := def.Mode == "job" && tool != def.Tool && (tool == def.jobTool("job_status") || tool == def.jobTool("recent_tasks"))
	if observe {
		c, err := connNamed(q, connection)
		if err != nil {
			return run, "", false, false, err
		}
		meta, ok := gatewayProviders[c.Provider].Tools[tool]
		if !ok || meta.Method != "GET" || !meta.Idempotent || phase != contract.PipelineDispatchPhaseHandedOff {
			return run, "", false, false, fail(409, "Pipeline observation is not a granted idempotent job read")
		}
	} else if phase != contract.PipelineDispatchPhaseReserved {
		return run, "", false, false, nil
	}
	stamp, _, err := pipelineAuthorityStamp(q, run, index, connection, tool, "")
	if err != nil {
		return run, "", false, false, err
	}
	g, err := mintGrantFor(q, mintRun{ExecutionID: &executionID, DirectedBy: agentOpt(run.actor()), ProjectID: run.ProjectID, ExperimentID: agentOpt(run.ExperimentID), Step: true}, []string{connection})
	if err != nil {
		return run, "", false, false, err
	}
	token := deref(g.Token)
	idempotent := false
	for _, c := range g.Connections {
		for _, t := range c.Tools {
			if t.Name == tool {
				idempotent = t.Idempotent
				if t.File != nil {
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
		if err := run.put(q); err != nil {
			return run, "", false, false, err
		}
	}
	return run, token, true, idempotent, nil
}

// pipelineNativeRecord: the pipeline fields of a step's own execution record.
type pipelineNativeRecord struct {
	ID             string `json:"id"`
	PipelineRunID  string `json:"pipelineRunId"`
	PipelineStepID string `json:"pipelineStepId"`
}

func preparePipelineCommand(q *Request, execution, command any) error {
	e, cmd := agentView[pipelineNativeRecord](execution), agentView[agentCommand](command)
	if e.PipelineRunID == "" {
		return nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return fail(409, "Pipeline command preparation requires its transaction fence")
	}
	run, err := getRun(q, e.PipelineRunID)
	if err != nil || run == nil {
		return err
	}
	index := pipelineStepIndex(run, e.PipelineStepID)
	if run.State != "running" || index < 0 {
		return fail(409, "Pipeline no longer permits command preparation")
	}
	if run.Steps[index].ExecutionID != e.ID {
		return fail(409, "Pipeline native execution identity changed")
	}
	entry := run.entry(index)
	if dispatchPhase(entry) != contract.PipelineDispatchPhaseReserved {
		return fail(409, "Pipeline command was already prepared")
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, "", "", cmd.HostID); err != nil {
		return err
	}
	d := entry.Dispatch
	d.Phase, d.NativeExecutionId, d.CommandId, d.HostId = contract.PipelineDispatchPhasePrepared, agentOpt(e.ID), agentOpt(cmd.ID), agentOpt(cmd.HostID)
	return run.put(q)
}

// handoffPipelineCommand is the last authorization point before a host sees the
// command. Redelivery keeps its exact stored body and the executor's journal ID.
// Only the command the plan dispatched (an agent start or a script) is gated; the
// execution's later commands (stop, steer, input, teardown) go like any other.
func handoffPipelineCommand(q *Request, execution, command any) (bool, error) {
	e, cmd := agentView[pipelineNativeRecord](execution), agentView[agentCommand](command)
	if e.PipelineRunID == "" || !pipelineDispatchKind(cmd.Kind) {
		return true, nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return false, nil // a stale poll preflight waits for the next correctly fenced poll
	}
	// A command that can never be handed off is stopped with its reason, never answered as an error: an error
	// fails the whole poll, so the host would get no other command and no update target. Its execution is left
	// to its engine and the lost-host sweep, as before.
	run, err := getRun(q, e.PipelineRunID)
	if pipelineFormatUnsupported(err) {
		return false, stopPipelineCommand(q, command, "pipeline run format unsupported")
	}
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, stopPipelineCommand(q, command, "pipeline run removed")
	}
	if executionEnds(run.State) {
		return false, stopPipelineCommand(q, command, "pipeline run ended")
	}
	index := pipelineStepIndex(run, e.PipelineStepID)
	if run.State != "running" || index < 0 {
		return false, nil
	}
	if run.Steps[index].ExecutionID != e.ID {
		return false, stopPipelineCommand(q, command, "pipeline command execution differs")
	}
	entry := run.entry(index)
	d := entry.Dispatch
	if d == nil || deref(d.CommandId) != cmd.ID || deref(d.NativeExecutionId) != e.ID || deref(d.HostId) != cmd.HostID {
		return false, stopPipelineCommand(q, command, "pipeline command binding differs")
	}
	stamp, connections, err := pipelineAuthorityStamp(q, run, index, "", "", cmd.HostID)
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
		grants, err := mintGrantFor(q, mintRun{ExecutionID: agentOpt(e.ID), DirectedBy: agentOpt(run.actor()), ProjectID: run.ProjectID, ExperimentID: agentOpt(run.ExperimentID), PipelineRunID: &run.ID}, connections)
		if err != nil {
			return false, err
		}
		if grants.Token == nil || len(grants.Refused) > 0 {
			return false, fail(403, "Pipeline command connection grant was refused")
		}
		// The poll's command carries no payload: the stored one gets the token.
		if cmd, err = recGet[agentCommand](q, recCommand, cmd.ID); err != nil {
			return false, err
		}
		payload := pipelineRuleOf(cmd.Payload)
		if payload == nil {
			payload = pipelineRule{}
		}
		payload["runToken"], payload["connections"] = jsonBytes(*grants.Token), jsonBytes(grants.Connections)
		cmd.Payload = jsonBytes(payload)
		if err := recWrite(q, recCommand, cmd); err != nil {
			return false, err
		}
	}
	pipelineHandOff(d, stamp)
	return true, run.put(q)
}

// pipelineHandOff: a dispatch is handed off now by this coordinator, under authority stamp.
func pipelineHandOff(d *contract.PipelineDispatch, stamp string) {
	claim, boot, at := newID(), pipelineBootID, nowMs()
	d.Phase, d.ClaimId, d.OwnerBootId, d.HandedOffAtMs, d.AuthoritySha256 = contract.PipelineDispatchPhaseHandedOff, &claim, &boot, &at, &stamp
}

// guardPipelineWrite rejects a stale caller before any row is overwritten.
// Retried DB-only transactions restore their in-memory revision too; no external
// effect is performed by this helper or by a transaction retry.
func guardPipelineWrite(q *Request, run *runRecord) error {
	if err := run.requirePlan(); err != nil {
		return err
	}
	plan := run.Plan
	want := plan.Revision
	stored, err := q.db().PipelineStoredPlan(q.ctx(), run.ID)
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
	key := pipelineWriteKey{q.tx, run.ID}
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

// planSummary: what a plan's entries are (pending, selected fresh or reused, not selected, blocked).
func planSummary(entries []contract.PipelinePlanEntry) contract.PipelinePlanSummary {
	var out contract.PipelinePlanSummary
	for i := range entries {
		e := &entries[i]
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

// selectStep is called after the current run was read under its write
// transaction. Its execution reservation and decision are persisted together.
func (run *runRecord) selectStep(q *Request, index int) error {
	entry := run.entry(index)
	if entry == nil || entry.Activation != contract.PipelineActivationPending {
		return nil
	}
	definition := run.def(index)
	when, err := definition.when()
	if err != nil {
		return err
	}
	decision := pipelineConditionDecision(nil, nil, nowMs())
	if when != nil {
		sourceID := when.Source.Step
		var source *runStep
		sourceIndex := -1
		for i := range run.Steps[:index] {
			if run.Steps[i].ID == sourceID {
				source, sourceIndex = &run.Steps[i], i
				break
			}
		}
		reason := contract.PipelinePlanReasonSourceUnavailable
		if source != nil && source.State == "succeeded" && source.OutputArtifactID != "" {
			if source.unconfirmed() {
				reason = contract.PipelinePlanReasonSourceUnconfirmed
			} else if err := outputOwner(q, run, sourceIndex, source.OutputArtifactID); err != nil {
				return err
			} else {
				artifact, err := artifactRow(withTx(q), q.app, source.OutputArtifactID)
				if err != nil {
					return err
				}
				if sha256hex(jsonNorm(source.Output)) != artifact.ContentHash {
					reason = contract.PipelinePlanReasonSourceHashMismatch
				} else {
					decision = pipelineConditionDecision(when, rawAny(source.Output), nowMs())
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
		return run.finish(q, "failed", string(decision.Reason))
	}
	if entry.Activation == contract.PipelineActivationNotSelected {
		return run.put(q)
	}
	connection, tool := "", ""
	if definition.Kind == "api" {
		connection, tool = definition.Connection, definition.Tool
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, connection, tool, ""); err != nil {
		entry.Activation = contract.PipelineActivationBlocked
		decision.Outcome, decision.Reason = contract.PipelineDecisionOutcomeBlocked, contract.PipelinePlanReasonAuthorityDenied
		entry.Problem = &contract.PipelinePlanProblem{Code: contract.PipelinePlanReasonAuthorityDenied, Message: "Current pipeline authority was denied"}
		return run.finish(q, "failed", "authority_denied")
	}
	row := run.row(index)
	existing, err := q.db().PipelineExecutionExists(q.ctx(), row)
	if err != nil {
		return err
	}
	if existing {
		return fail(409, "Pipeline execution reservation identity conflict")
	}
	entry.ExecutionRef = &contract.PipelineExecutionRef{Mode: contract.PipelineRefModeFresh, ExecutionId: row, SourceRunId: run.ID, SourceStepId: entry.Id,
		SourceStepIndex: int64(index), SourceDefinitionSha256: run.DefinitionSha256, SourceInputSha256: sha256hex([]byte("[]"))}
	entry.Dispatch = &contract.PipelineDispatch{Phase: contract.PipelineDispatchPhaseReserved}
	run.Steps[index].State = "preparing"
	return run.put(q)
}

type pipelinePreparedInputs struct {
	files    []fileRef
	bindings []contract.PipelineInputBinding
	example  []fileRef
}

// pipelineControlEntry: one earlier plan entry as a step's control manifest shows it.
type pipelineControlEntry struct {
	ID             string                         `json:"id"`
	Index          int                            `json:"index"`
	Activation     contract.PipelineActivation    `json:"activation"`
	ExecutionRef   *contract.PipelineExecutionRef `json:"executionRef,omitempty"`
	DefinitionPins json.RawMessage                `json:"definitionPins,omitempty"`
	Observation    string                         `json:"observation,omitempty"`
	ExecutionState string                         `json:"executionState,omitempty"`
	Output         *contract.PipelineArtifactRef  `json:"output,omitempty"`
}

// pipelineControl: a version 3 step's control manifest (__fleet_control/pipeline.json).
type pipelineControl struct {
	Schema           string                          `json:"schema"`
	RunID            string                          `json:"runId"`
	ConsumerStepID   string                          `json:"consumerStepId"`
	DefinitionSha256 string                          `json:"definitionSha256"`
	PlanRevision     int64                           `json:"planRevision"`
	Entries          []pipelineControlEntry          `json:"entries"`
	Inputs           []contract.PipelineInputBinding `json:"inputs"`
}

// materializePipelineInputs performs object I/O outside plan write locks. The
// caller later freezes these exact bindings using its expected plan revision.
func materializePipelineInputs(ctx context.Context, a *App, run *runRecord, index int) (*pipelinePreparedInputs, error) {
	prepared := &pipelinePreparedInputs{files: []fileRef{}, bindings: []contract.PipelineInputBinding{}, example: []fileRef{}}
	if run.ExampleInputs != nil {
		prepared.example = append(prepared.example, *run.ExampleInputs...)
	}
	project := derefAny(run.ProjectID)
	if frozen := run.entry(index).Inputs; frozen != nil {
		if err := a.store.Read(ctx, nil, func(q *Request) error {
			prepared.files = prepared.files[:0]
			for _, binding := range frozen {
				artifact, err := artifactRow(withTx(q), a, binding.ArtifactId)
				if err != nil || artifact.State != "stored" || !same(artifact.ProjectID, project) || artifact.ContentHash != binding.Sha256 {
					return fail(409, "Frozen pipeline input is unavailable or changed")
				}
				prepared.files = append(prepared.files, fileRef{ArtifactID: binding.ArtifactId, Path: binding.Path})
			}
			return nil
		}); err != nil {
			return nil, err
		}
		prepared.bindings = frozen
		return prepared, nil
	}
	if run.ExampleInputs == nil {
		for _, input := range run.exampleInputs() {
			name, _ := jsonString(input["path"])
			if pipelineReservedInput(name) || !pipelineInputPath(name) {
				return nil, fail(400, "Invalid or reserved pipeline input path")
			}
			var data []byte
			if err := a.store.Read(ctx, nil, func(q *Request) error {
				var err error
				var f labExampleFile
				json.Unmarshal(jsonBytes(input), &f)
				data, err = labFileBytes(q, run.ProjectID, f)
				return err
			}); err != nil {
				return nil, err
			}
			media, _ := jsonString(input["media_type"])
			id, _, err := saveObject(ctx, a, run.ProjectID, run.scope(), run.ID, "example/"+name, data, firstString(media, "text/plain"))
			if err != nil {
				return nil, err
			}
			prepared.example = append(prepared.example, fileRef{ArtifactID: id, Path: "example/" + name})
		}
	}
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		prepared.files = append([]fileRef(nil), prepared.example...)
		prepared.bindings = nil
		files, err := run.outputFiles(q, index)
		if err != nil {
			return err
		}
		prepared.files = append(prepared.files, files...)
		seen := map[string]bool{}
		for _, file := range prepared.files {
			name := file.Path
			if !pipelineInputPath(name) || pipelineReservedInput(name) || seen[name] {
				return fail(409, "Pipeline staged input path collision")
			}
			seen[name] = true
			artifact, err := artifactRow(withTx(q), a, file.ArtifactID)
			if err != nil {
				return err
			}
			if artifact.State != "stored" || !same(artifact.ProjectID, project) {
				return fail(409, "Unavailable or foreign pipeline input")
			}
			binding := contract.PipelineInputBinding{Path: name, ArtifactId: file.ArtifactID, Sha256: artifact.ContentHash}
			producer, _, _ := strings.Cut(name, "/")
			for i := range run.entries()[:index] {
				entry := run.entry(i)
				if entry.Id == producer && entry.Activation == contract.PipelineActivationSelected {
					ref := entry.ExecutionRef
					stepID := entry.Id
					binding.ProducerRunId, binding.ProducerStepId, binding.ProducerExecutionId = &ref.SourceRunId, &stepID, &ref.ExecutionId
					if run.Steps[i].State != "succeeded" {
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
	if run.Definition.Version == 3 {
		entries := []pipelineControlEntry{}
		for i := range run.entries()[:index] {
			entry := run.entry(i)
			metadata := pipelineControlEntry{ID: entry.Id, Index: i, Activation: entry.Activation, ExecutionRef: entry.ExecutionRef}
			step := &run.Steps[i]
			if native := step.ExecutionID; native != "" {
				if err := a.store.Read(ctx, nil, func(q *Request) error {
					raw, err := q.db().PipelineExecutionDefinitions(q.ctx(), db.PipelineExecutionDefinitionsParams{ID: native, ProjectID: run.ProjectID})
					if errors.Is(err, pgx.ErrNoRows) {
						return nil
					}
					if err != nil {
						return err
					}
					if !json.Valid(raw) {
						return fmt.Errorf("execution %s: invalid definitions", native)
					}
					if !isNull(raw) {
						metadata.DefinitionPins = raw
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
			if step.State != "" {
				metadata.Observation = firstString(step.Observation, "confirmed")
				metadata.ExecutionState = step.State
			}
			for _, binding := range prepared.bindings {
				if binding.Path == entry.Id+"/output.json" {
					metadata.Output = &contract.PipelineArtifactRef{ArtifactId: binding.ArtifactId, Sha256: binding.Sha256}
				}
			}
			entries = append(entries, metadata)
		}
		manifest := pipelineControl{Schema: "fleet.pipeline-control.v1", RunID: run.ID, ConsumerStepID: run.entry(index).Id, DefinitionSha256: run.DefinitionSha256,
			PlanRevision: run.revision(), Entries: entries, Inputs: prepared.bindings}
		id, hash, err := saveObject(ctx, a, run.ProjectID, run.scope(), run.row(index), "__fleet_control/pipeline.json", jsonNorm(jsonBytes(manifest)), "application/json")
		if err != nil {
			return nil, err
		}
		prepared.files = append(prepared.files, fileRef{Path: "__fleet_control/pipeline.json", ArtifactID: id})
		prepared.bindings = append(prepared.bindings, contract.PipelineInputBinding{Path: "__fleet_control/pipeline.json", ArtifactId: id, Sha256: hash})
		sort.Slice(prepared.bindings, func(i, j int) bool { return prepared.bindings[i].Path < prepared.bindings[j].Path })
	}
	return prepared, nil
}

func pipelineInputPath(value string) bool {
	return value != "" && path.IsAbs(value) == false && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, ":\\\x00")
}

func freezePipelineInputs(run *runRecord, index int, inputs *pipelinePreparedInputs) {
	example := append([]fileRef{}, inputs.example...)
	run.ExampleInputs = &example
	entry := run.entry(index)
	entry.Inputs = inputs.bindings
	entry.ExecutionRef.SourceInputSha256 = sha256hex([]byte(canonical(inputs.bindings)))
}

func materializePipelineOutput(ctx context.Context, a *App, run *runRecord, index int, output json.RawMessage) error {
	step := &run.Steps[index]
	if step.OutputArtifactID != "" {
		return nil
	}
	id, _, err := saveObject(ctx, a, run.ProjectID, run.scope(), run.row(index), "output.json", jsonNorm(output), "application/json")
	if err == nil {
		step.OutputArtifactID = id
	}
	return err
}

// pipelineLineage: a finalizer output's result lineage.
type pipelineLineage struct {
	FleetLineage  json.RawMessage `json:"fleetLineage"`
	ProjectResult json.RawMessage `json:"projectResult"`
}

func bindPipelineResult(q *Request, run *runRecord) error {
	steps := run.Steps
	if len(steps) == 0 {
		return fail(409, "Pipeline has no final output")
	}
	index := len(steps) - 1
	step, entry := &steps[index], run.entry(index)
	if entry.Activation != contract.PipelineActivationSelected || step.State != "succeeded" || step.unconfirmed() {
		return fail(409, "Pipeline final output is not confirmed")
	}
	artifact, err := artifactRow(withTx(q), q.app, step.OutputArtifactID)
	if err != nil {
		return err
	}
	if err := outputOwner(q, run, index, artifact.ID); err != nil {
		return err
	}
	if artifact.ContentHash != sha256hex(jsonNorm(step.Output)) {
		return fail(409, "Pipeline final output bytes changed")
	}
	inputs := append([]contract.PipelineInputBinding{}, entry.Inputs...)
	result := &contract.PipelineCanonicalResult{Mode: "static", StepId: entry.Id, ProducerExecutionId: refExecutionID(entry), Artifact: contract.PipelineArtifactRef{ArtifactId: artifact.ID, Sha256: artifact.ContentHash},
		Inputs: inputs, SelectedSources: []contract.PipelineInputBinding{}}
	if run.Definition.Version == 3 {
		output := pipelineRuleOf(step.Output)
		lineage := pipelineRuleOf(output["fleetLineage"])
		schema, _ := jsonString(lineage["schema"])
		if !ruleKeysOnly(output, "fleetLineage", "projectResult") || isNull(output["projectResult"]) || lineage == nil || !ruleKeysOnly(lineage, "schema", "selectedSources") || schema != "fleet.result-lineage.v1" || jsonKind(lineage["selectedSources"]) != '[' {
			return fail(409, "Finalizer result lineage is invalid")
		}
		for _, binding := range inputs {
			if binding.Path == "__fleet_control/pipeline.json" {
				control, err := artifactRow(withTx(q), q.app, binding.ArtifactId)
				if err != nil {
					return err
				}
				if control.State != "stored" || !same(control.ProjectID, derefAny(run.ProjectID)) || control.ExecutionID == nil || *control.ExecutionID != refExecutionID(entry) || control.ContentHash != binding.Sha256 {
					return fail(409, "Finalizer control manifest binding changed")
				}
				result.ControlArtifact = &contract.PipelineArtifactRef{ArtifactId: control.ID, Sha256: control.ContentHash}
			}
		}
		if result.ControlArtifact == nil {
			return fail(409, "Finalizer control manifest is missing")
		}
		selected := []contract.PipelineInputBinding{}
		for _, source := range jsonList(lineage["selectedSources"]) {
			binding := pipelineRuleOf(source)
			producer, _ := jsonString(binding["producerExecutionId"])
			if binding == nil || !ruleKeysOnly(binding, "path", "artifactId", "sha256", "producerRunId", "producerStepId", "producerExecutionId") || producer == "" {
				return fail(409, "Finalizer selected source is not a producer input")
			}
			found := false
			for _, input := range inputs {
				found = found || canonical(input) == canonical(rawAny(source))
			}
			if !found {
				return fail(409, "Finalizer selected source was not staged")
			}
			var b contract.PipelineInputBinding
			if err := json.Unmarshal(source, &b); err != nil {
				return fail(409, "Finalizer selected source is not a producer input")
			}
			selected = append(selected, b)
		}
		result.Mode, result.SelectedSources = "finalizer", selected
	}
	run.CanonicalResult = result
	return nil
}

func (run *runRecord) planShape() error {
	if err := run.requirePlan(); err != nil {
		return err
	}
	for i := range run.entries() {
		entry, definition := run.entry(i), run.def(i)
		if entry.Id != definition.ID || entry.DefinitionSha256 != definition.sha() {
			return fmt.Errorf("pipeline plan entry %d does not match its definition", i)
		}
		ref := entry.ExecutionRef
		if entry.Activation == contract.PipelineActivationSelected {
			if ref == nil || ref.SourceStepId != entry.Id || ref.SourceStepIndex != int64(i) || !labSha.MatchString(ref.SourceDefinitionSha256) || !labSha.MatchString(ref.SourceInputSha256) {
				return fail(409, "Invalid selected execution reference")
			}
			if ref.Mode == contract.PipelineRefModeFresh && (ref.ExecutionId != run.row(i) || ref.SourceRunId != run.ID || entry.Dispatch == nil) {
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

// planRead: the run as its readers get it: its record without the runner's steps, its plan with each selected entry's
// execution and verified output, and the plan's summary.
func (run *runRecord) planRead(q *Request) (recordRest, error) {
	if err := run.planShape(); err != nil {
		return nil, err
	}
	out, err := recordOf(run)
	if err != nil {
		return nil, err
	}
	delete(out, "steps")
	plan := run.Plan
	view := &contract.PipelinePlan{DefinitionSha256: plan.DefinitionSha256, Revision: plan.Revision, Entries: []contract.PipelinePlanEntry{}}
	for i := range plan.Entries {
		entry := plan.Entries[i]
		if entry.Activation == contract.PipelineActivationSelected {
			step := &run.Steps[i]
			execution := &contract.PipelineExecutionView{ExecutionId: refExecutionID(&entry), State: contract.ExecutionState(step.State), Observation: contract.PipelineObservationConfirmed}
			if step.Observation != "" {
				execution.Observation = contract.PipelineObservation(step.Observation)
			}
			if id := step.ExecutionID; id != "" {
				execution.NativeExecutionId = &id
			}
			ms := func(v *int64) json.RawMessage {
				if v == nil {
					return nil
				}
				return jsonBytes(*v)
			}
			execution.StartedAt, execution.EndedAt = ms(step.StartedAt), ms(step.FinishedAt)
			if !isNull(step.Usage) {
				execution.Usage = step.Usage
			}
			if !isNull(step.Error) {
				execution.Error = step.Error
			}
			if step.DurationMs != nil {
				d := float64(*step.DurationMs)
				execution.DurationMs = &d
			}
			if step.Calls != nil {
				execution.Calls = []json.RawMessage{}
				for _, c := range step.Calls {
					execution.Calls = append(execution.Calls, jsonBytes(c))
				}
			}
			entry.Execution = execution
			if id := step.OutputArtifactID; id != "" {
				artifact, err := artifactRow(withTx(q), q.app, id)
				if err != nil {
					return nil, err
				}
				if artifact.State == "stored" {
					if err := outputOwner(q, run, i, id); err != nil {
						return nil, err
					}
					if artifact.ContentHash != sha256hex(jsonNorm(step.Output)) {
						return nil, fail(409, "Pipeline output bytes changed")
					}
					entry.Output = &contract.PipelineArtifactRef{ArtifactId: id, Sha256: artifact.ContentHash}
				}
			}
		}
		view.Entries = append(view.Entries, entry)
	}
	out["plan"], out["planSummary"] = jsonBytes(view), jsonBytes(planSummary(run.entries()))
	return out, nil
}

// pipelinePublicRead assembles only the current plan contract. Internal runner
// projections never leak an eager or fabricated steps array to callers.
func pipelinePublicRead(ctx context.Context, a *App, id string) (recordRest, error) {
	var out recordRest
	err := a.store.Read(ctx, nil, func(q *Request) error {
		run, err := getRun(q, id)
		if err != nil {
			return err
		}
		if run == nil {
			return fail(404, "Unknown pipeline run")
		}
		out, err = run.planRead(q)
		return err
	})
	return out, err
}

// resultValue: the run's canonical result value (a finalizer's project result, else its result step's output).
func (run *runRecord) resultValue() json.RawMessage {
	result := run.CanonicalResult
	if result == nil {
		return nil
	}
	for i := range run.entries() {
		if run.entry(i).Id == result.StepId {
			output := run.Steps[i].Output
			if result.Mode == "finalizer" {
				return pipelineRuleOf(output)["projectResult"]
			}
			return output
		}
	}
	return nil
}

// pipelineHostJobRecord: the fields of a script step's host job its recovery proof reads.
type pipelineHostJobRecord struct {
	PipelineRunID string  `json:"pipelineRunId"`
	ProjectID     *string `json:"projectId"`
}

// pipelineEventFilter: the events of one execution of one type (eventsWhere).
type pipelineEventFilter struct {
	ExecutionID string `json:"executionId"`
	Type        string `json:"type"`
}

// A rerun is not permission to replay a possibly completed provider/native
// action. Only the existing command-bound local process proof can release an
// interrupted script suffix; the original observation stays unconfirmed.
func recoveryFence(q *Request, run *runRecord) error {
	for i := range run.entries() {
		entry := run.entry(i)
		if entry.Activation != contract.PipelineActivationSelected {
			continue
		}
		step := &run.Steps[i]
		if !step.unconfirmed() {
			continue
		}
		definition := run.def(i)
		if definition.Kind != "script" || entryRefMode(entry) != contract.PipelineRefModeFresh {
			return fail(409, "Unconfirmed external action cannot be replayed by rerun")
		}
		native := step.ExecutionID
		raw, err := q.db().PipelineHostJob(q.ctx(), native)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		var job pipelineHostJobRecord
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		var execution *labProofExecution
		if json.Unmarshal(raw, &execution) != nil {
			execution = nil // a record that is no host job proves nothing
		}
		if execution == nil || job.PipelineRunID != run.ID || !same(derefAny(job.ProjectID), derefAny(run.ProjectID)) {
			return fail(409, "Interrupted script ownership proof unavailable")
		}
		commands, err := recAll[rawObject](q, recCommand, agentFilter{ExecutionID: &native})
		if err != nil {
			return err
		}
		scripts := []labSuffixCommand{}
		for _, c := range commands {
			scripts = append(scripts, agentView[labSuffixCommand](c))
		}
		ci, err := suffixScript(scripts)
		if err != nil {
			return err
		}
		command := &scripts[ci]
		d := entry.Dispatch
		if d == nil || deref(d.CommandId) != command.ID || d.CommandId == nil || deref(d.NativeExecutionId) != native || d.Phase != contract.PipelineDispatchPhaseHandedOff {
			return fail(409, "Interrupted script handoff proof unavailable")
		}
		events, err := eventsWhere(q, pipelineEventFilter{ExecutionID: native, Type: "process.reconciled"})
		if err != nil {
			return err
		}
		matches := 0
		for _, event := range events {
			if suffixProven(execution, command, labRecordOf[labProofEvent](event), run.row(i)) {
				matches++
			}
		}
		if matches != 1 {
			return fail(409, "Interrupted script has no unique command-bound process proof")
		}
	}
	return nil
}
