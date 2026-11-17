package main

import (
	"context"
	"errors"
	"fleet/contract"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

const pipelinePlanVersion = 1

func pipelinePlanEntries(run doc) []any { return anys(mapOf(run["plan"])["entries"]) }

func pipelinePlanEntry(run doc, index int) doc {
	entries := pipelinePlanEntries(run)
	if index < 0 || index >= len(entries) {
		return nil
	}
	return mapOf(entries[index])
}

// initPipelinePlan creates no execution. Static and guarded definitions share
// this same persisted model; there is no historical-row reconstruction path.
func initPipelinePlan(run doc) error {
	actor := firstString(run["directedBy"], mapOf(run["requestedBy"])["email"])
	if actor == "" {
		return fail(403, "Pipeline requires its original initiating actor")
	}
	definition := mapOf(run["definition"])
	entries, capabilities := []any{}, []any{}
	seen := map[string]bool{}
	for i, v := range anys(definition["steps"]) {
		step := mapOf(v)
		entries = append(entries, doc{"id": step["id"], "index": i, "definitionSha256": sha256hex([]byte(canonical(step))), "activation": "pending"})
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
	run["plan"] = doc{"definitionSha256": run["definitionSha256"], "revision": 0, "entries": entries}
	run["authority"] = doc{"actorEmail": actor, "projectId": run["projectId"], "requestedCapabilities": capabilities}
	return nil
}

func requirePipelinePlan(run doc) error {
	version, ok := intOK(run["planVersion"])
	if !ok || version != pipelinePlanVersion || len(pipelinePlanEntries(run)) != len(anys(mapOf(run["definition"])["steps"])) {
		return &HTTPError{Code: 409, Msg: "Unsupported pipeline run format; a clean start is required", Extra: doc{"reason": "pipeline_format_unsupported"}}
	}
	return nil
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
	return exec(q, `UPDATE document SET data=data||jsonb_build_object('status','stopped','stopDetail',$2::text), updated_at=now() WHERE kind='command' AND id=$1 AND data->>'status'='pending'`, command["id"], reason)
}

type pipelineWriteKey struct {
	tx pgx.Tx
	id string
}

type pipelineWriteState struct {
	revision int64
	plans    []doc
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
	share := ""
	resolve := resolveAccessTx
	if value, ok := txLockStates.Load(q.tx); ok && value.(*txLockState).write {
		share, resolve = " FOR SHARE", resolveAccessShareTx
	}
	authority := mapOf(run["authority"])
	projectID := astr(run["projectId"])
	actor, err := resolve(q.ctx(), q.tx, astr(authority["actorEmail"]))
	if err != nil || actor == nil || !mayAccessProjectID(actor, projectID) {
		return "", nil, fail(403, "Pipeline initiating actor no longer has project access")
	}
	project := doc{}
	if projectID != "" {
		project, err = first(q, "SELECT head,access_version FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NULL"+share, projectID)
		if err != nil {
			return "", nil, err
		}
		if project == nil {
			return "", nil, fail(403, "Pipeline project is no longer available")
		}
	}
	// Global draft/static runs retain the existing full-access initiating-actor
	// boundary; they do not invent a project or borrow another actor's rights.
	stamp := doc{"actor": actor.Email, "accessVersion": actor.AccessVersion, "project": run["projectId"], "projectVersion": project["access_version"], "projectHead": project["head"]}
	connections := []string{}
	definition := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	if connection != "" {
		connections = append(connections, connection)
	} else if definition["kind"] == "agent" {
		profileID := astr(definition["profile"])
		if override := astr(mapOf(run["overrides"])["profile"]); override != "" {
			profileID = override
		}
		row, err := first(q, "SELECT id FROM definition WHERE kind='agent' AND id=$1 AND retired_at IS NULL"+share, profileID)
		if err != nil {
			return "", nil, err
		}
		if row == nil {
			return "", nil, fail(403, "Pipeline profile is no longer available")
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
		row, err := first(q, "SELECT data FROM document WHERE id=$1 AND kind='api-connection'"+share, "connection:"+name)
		if err != nil {
			return "", nil, err
		}
		c := mapOf(row["data"])
		grantee := doc{"directedBy": actor.Email, "projectId": projectID, "experimentId": run["experimentId"]}
		if c == nil || c["intake"] != "all" || !connAllowed(c, grantee) || tool != "" && !inList(strs(c["tools"]), tool) {
			return "", nil, fail(403, "Pipeline connection or tool is no longer granted")
		}
		connectionStamps = append(connectionStamps, doc{"name": name, "access": c["access"], "tools": c["tools"], "intake": c["intake"]})
	}
	stamp["connections"] = connectionStamps
	if host != "" {
		row, err := first(q, "SELECT data FROM document WHERE id=$1 AND kind='host'"+share, "host:"+host)
		if err != nil {
			return "", nil, err
		}
		h := mapOf(row["data"])
		if h == nil || hostRemoved(h) {
			return "", nil, fail(403, "Pipeline host is no longer available")
		}
		holder, err := resolve(q.ctx(), q.tx, astr(h["holder"]))
		if err != nil || holder == nil || projectID != "" && !mayAccessProjectID(holder, projectID) {
			return "", nil, fail(403, "Pipeline host holder no longer has project access")
		}
		stamp["host"], stamp["holderAccessVersion"] = host, holder.AccessVersion
	}
	return sha256hex([]byte(canonical(stamp))), connections, nil
}

func pipelineStepIndex(run doc, executionID string) int {
	for i := range pipelinePlanEntries(run) {
		if mapOf(pipelinePlanEntry(run, i)["executionRef"])["executionId"] == executionID {
			return i
		}
	}
	return -1
}

// claimPipelineAPI mints once in the transaction that wins the persisted claim.
// Status/recent-task reads observe the existing job and never create a new effect.
// claimPipelineAPI also answers the tool's idempotent flag from the minted spec (how an edge error reads).
func claimPipelineAPI(q *Request, runID, executionID, connection, tool string) (doc, string, bool, bool, error) {
	if _, err := first(q, "SELECT id FROM execution WHERE id=$1 FOR UPDATE", runID); err != nil {
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
	entry := pipelinePlanEntry(run, index)
	def := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	dispatch := mapOf(entry["dispatch"])
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
		if meta["method"] != "GET" || meta["idempotent"] != true || dispatch["phase"] != "handed_off" {
			return run, "", false, false, fail(409, "Pipeline observation is not a granted idempotent job read")
		}
	} else if dispatch["phase"] != "reserved" {
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
			}
		}
	}
	if token == "" {
		return run, "", false, false, fail(403, "Pipeline token was not granted")
	}
	if !observe {
		dispatch["phase"], dispatch["claimId"], dispatch["ownerBootId"] = "handed_off", newID(), pipelineBootID
		dispatch["handedOffAtMs"], dispatch["authoritySha256"] = nowMs(), stamp
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
	dispatch := mapOf(pipelinePlanEntry(run, index)["dispatch"])
	if dispatch["phase"] != "reserved" {
		return fail(409, "Pipeline command was already prepared")
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, "", "", astr(command["hostId"])); err != nil {
		return err
	}
	dispatch["phase"], dispatch["nativeExecutionId"], dispatch["commandId"] = "prepared", execution["id"], command["id"]
	dispatch["hostId"] = command["hostId"]
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
	if terminalTyped[astr(run["state"])] {
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
	dispatch := mapOf(pipelinePlanEntry(run, index)["dispatch"])
	if dispatch["commandId"] != command["id"] || dispatch["nativeExecutionId"] != execution["id"] || dispatch["hostId"] != command["hostId"] {
		return false, stopPipelineCommand(q, command, "pipeline command binding differs")
	}
	stamp, connections, err := pipelineAuthorityStamp(q, run, index, "", "", astr(command["hostId"]))
	if err != nil {
		return false, err
	}
	if dispatch["phase"] == "handed_off" {
		return true, nil // same immutable command, no token mint or new invocation
	}
	if dispatch["phase"] != "prepared" {
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
	dispatch["phase"], dispatch["claimId"], dispatch["ownerBootId"] = "handed_off", newID(), pipelineBootID
	dispatch["handedOffAtMs"], dispatch["authoritySha256"] = nowMs(), stamp
	return true, putRun(q, run)
}

// guardPipelineWrite rejects a stale caller before any row is overwritten.
// Retried DB-only transactions restore their in-memory revision too; no external
// effect is performed by this helper or by a transaction retry.
func guardPipelineWrite(q *Request, run doc) error {
	if err := requirePipelinePlan(run); err != nil {
		return err
	}
	plan := mapOf(run["plan"])
	want, _ := intOK(plan["revision"])
	stored, err := first(q, "SELECT data FROM execution WHERE id=$1 FOR UPDATE", run["id"])
	if err != nil {
		return err
	}
	next := want
	if stored != nil {
		data := mapOf(stored["data"])
		if data["pipelineId"] != nil {
			if err := requirePipelinePlan(data); err != nil {
				return err
			}
			old := mapOf(data["plan"])
			revision, _ := intOK(old["revision"])
			if revision != want || old["definitionSha256"] != plan["definitionSha256"] {
				return fail(409, "Pipeline plan changed before this transition")
			}
			next++
		}
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
					p["revision"] = state.revision
				}
			}
			pipelineWrites.Delete(key)
		})
	}
	plan["revision"] = next
	return nil
}

func pipelinePlanSummary(run doc) doc {
	out := doc{"planned": 0, "pending": 0, "selected": 0, "notSelected": 0, "blocked": 0, "freshExecutions": 0, "reusedExecutions": 0}
	inc := func(key string) { n, _ := intOK(out[key]); out[key] = n + 1 }
	for _, value := range pipelinePlanEntries(run) {
		e := mapOf(value)
		inc("planned")
		key := astr(e["activation"])
		if key == "not_selected" {
			key = "notSelected"
		}
		inc(key)
		if e["activation"] == "selected" {
			if mapOf(e["executionRef"])["mode"] == "reused" {
				inc("reusedExecutions")
			} else {
				inc("freshExecutions")
			}
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
	entry := pipelinePlanEntry(run, index)
	if entry == nil || entry["activation"] != "pending" {
		return nil
	}
	definition := mapOf(anys(mapOf(run["definition"])["steps"])[index])
	when := mapOf(definition["when"])
	decision := pipelineConditionDecision(nil, nil, nowMs())
	if when != nil {
		sourceID := astr(mapOf(when["source"])["step"])
		var source doc
		sourceIndex := -1
		for i, value := range anys(run["steps"])[:index] {
			if step := mapOf(value); step["id"] == sourceID {
				source, sourceIndex = step, i
				break
			}
		}
		reason := "source_unavailable"
		if source != nil && source["state"] == "succeeded" && source["outputArtifactId"] != nil {
			if source["observation"] == "unconfirmed" {
				reason = "source_unconfirmed"
			} else if err := pipelineOutputOwner(q, run, sourceIndex, astr(source["outputArtifactId"])); err != nil {
				return err
			} else {
				artifact, err := artifactRow(withTx(q), q.app, astr(source["outputArtifactId"]))
				if err != nil {
					return err
				}
				if sha256hex(jsonBytes(source["output"])) != artifact.ContentHash {
					reason = "source_hash_mismatch"
				} else {
					decision = pipelineConditionDecision(when, source["output"], nowMs())
					decision["sourceArtifact"] = doc{"artifactId": artifact.ID, "sha256": artifact.ContentHash}
					reason = ""
				}
			}
		}
		if reason != "" {
			decision = doc{"kind": when["kind"], "outcome": "blocked", "reason": reason, "evaluatedAtMs": nowMs(), "ruleSha256": sha256hex([]byte(canonical(when))), "sourceStepId": sourceID, "path": mapOf(when["source"])["path"]}
		}
	}
	entry["decision"], entry["activation"] = decision, decision["outcome"]
	if entry["activation"] == "blocked" {
		entry["problem"] = doc{"code": decision["reason"], "message": "Condition evidence is unavailable or invalid"}
		return finishRun(q, run, "failed", astr(decision["reason"]))
	}
	if entry["activation"] == "not_selected" {
		return putRun(q, run)
	}
	connection, tool := "", ""
	if definition["kind"] == "api" {
		connection, tool = astr(definition["connection"]), astr(definition["tool"])
	}
	if _, _, err := pipelineAuthorityStamp(q, run, index, connection, tool, ""); err != nil {
		entry["activation"] = "blocked"
		decision["outcome"], decision["reason"] = "blocked", "authority_denied"
		entry["problem"] = doc{"code": "authority_denied", "message": "Current pipeline authority was denied"}
		return finishRun(q, run, "failed", "authority_denied")
	}
	row := stepRow(run, index)
	existing, err := first(q, "SELECT id FROM execution WHERE id=$1", row)
	if err != nil {
		return err
	}
	if existing != nil {
		return fail(409, "Pipeline execution reservation identity conflict")
	}
	entry["executionRef"] = doc{"mode": "fresh", "executionId": row, "sourceRunId": run["id"], "sourceStepId": entry["id"], "sourceStepIndex": index, "sourceDefinitionSha256": run["definitionSha256"], "sourceInputSha256": sha256hex([]byte("[]"))}
	entry["dispatch"] = doc{"phase": "reserved"}
	step := mapOf(anys(run["steps"])[index])
	step["state"] = "preparing"
	return putRun(q, run)
}

type pipelinePreparedInputs struct {
	files    []any
	bindings []any
	example  []any
}

// materializePipelineInputs performs object I/O outside plan write locks. The
// caller later freezes these exact bindings using its expected plan revision.
func materializePipelineInputs(ctx context.Context, a *App, run doc, index int) (*pipelinePreparedInputs, error) {
	prepared := &pipelinePreparedInputs{files: []any{}, bindings: []any{}, example: append([]any{}, anys(run["exampleInputs"])...)}
	if frozen := anys(pipelinePlanEntry(run, index)["inputs"]); frozen != nil {
		if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
			for _, value := range frozen {
				binding := mapOf(value)
				artifact, err := artifactRow(withTx(q), a, astr(binding["artifactId"]))
				if err != nil || artifact.State != "stored" || !same(artifact.ProjectID, run["projectId"]) || artifact.ContentHash != binding["sha256"] {
					return fail(409, "Frozen pipeline input is unavailable or changed")
				}
				prepared.files = append(prepared.files, doc{"artifactId": binding["artifactId"], "path": binding["path"]})
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
			binding := doc{"path": name, "artifactId": file["artifactId"], "sha256": artifact.ContentHash}
			producer, _, _ := strings.Cut(name, "/")
			for i, v := range pipelinePlanEntries(run)[:index] {
				entry := mapOf(v)
				if entry["id"] == producer && entry["activation"] == "selected" {
					ref := mapOf(entry["executionRef"])
					binding["producerRunId"], binding["producerStepId"], binding["producerExecutionId"] = ref["sourceRunId"], entry["id"], ref["executionId"]
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
	sort.Slice(prepared.bindings, func(i, j int) bool {
		return astr(mapOf(prepared.bindings[i])["path"]) < astr(mapOf(prepared.bindings[j])["path"])
	})
	if num(mapOf(run["definition"])["version"]) == 3 {
		entries := []any{}
		for i, v := range pipelinePlanEntries(run)[:index] {
			entry := mapOf(v)
			metadata := doc{"id": entry["id"], "index": i, "activation": entry["activation"]}
			if entry["executionRef"] != nil {
				metadata["executionRef"] = entry["executionRef"]
			}
			step := mapOf(anys(run["steps"])[i])
			if native := astr(step["executionId"]); native != "" {
				if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
					row, err := first(q, "SELECT definitions FROM execution WHERE id=$1 AND project_id IS NOT DISTINCT FROM $2", native, run["projectId"])
					if err != nil {
						return err
					}
					if row != nil && row["definitions"] != nil {
						metadata["definitionPins"] = row["definitions"]
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
			for _, value := range prepared.bindings {
				binding := mapOf(value)
				if binding["path"] == astr(entry["id"])+"/output.json" {
					metadata["output"] = doc{"artifactId": binding["artifactId"], "sha256": binding["sha256"]}
				}
			}
			entries = append(entries, metadata)
		}
		manifest := doc{"schema": "fleet.pipeline-control.v1", "runId": run["id"], "consumerStepId": pipelinePlanEntry(run, index)["id"], "definitionSha256": run["definitionSha256"], "planRevision": mapOf(run["plan"])["revision"], "entries": entries, "inputs": prepared.bindings}
		id, hash, err := saveObject(ctx, a, projectIDPtr(run["projectId"]), runScope(run), stepRow(run, index), "__fleet_control/pipeline.json", jsonBytes(manifest), "application/json")
		if err != nil {
			return nil, err
		}
		prepared.files = append(prepared.files, doc{"path": "__fleet_control/pipeline.json", "artifactId": id})
		prepared.bindings = append(prepared.bindings, doc{"path": "__fleet_control/pipeline.json", "artifactId": id, "sha256": hash})
		sort.Slice(prepared.bindings, func(i, j int) bool {
			return astr(mapOf(prepared.bindings[i])["path"]) < astr(mapOf(prepared.bindings[j])["path"])
		})
	}
	return prepared, nil
}

func pipelineInputPath(value string) bool {
	return value != "" && path.IsAbs(value) == false && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, ":\\\x00")
}

func freezePipelineInputs(run doc, index int, inputs *pipelinePreparedInputs) {
	run["exampleInputs"] = inputs.example
	entry := pipelinePlanEntry(run, index)
	entry["inputs"] = inputs.bindings
	mapOf(entry["executionRef"])["sourceInputSha256"] = sha256hex([]byte(canonical(inputs.bindings)))
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
	step, entry := mapOf(steps[index]), pipelinePlanEntry(run, index)
	if entry["activation"] != "selected" || step["state"] != "succeeded" || step["observation"] == "unconfirmed" {
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
	inputs := append([]any{}, anys(entry["inputs"])...)
	result := doc{"mode": "static", "stepId": entry["id"], "producerExecutionId": mapOf(entry["executionRef"])["executionId"], "artifact": doc{"artifactId": artifact.ID, "sha256": artifact.ContentHash}, "inputs": inputs, "selectedSources": []any{}}
	if num(mapOf(run["definition"])["version"]) == 3 {
		output := mapOf(step["output"])
		lineage := mapOf(output["fleetLineage"])
		if !keysOnly(output, "fleetLineage", "projectResult") || output["projectResult"] == nil || lineage == nil || !keysOnly(lineage, "schema", "selectedSources") || lineage["schema"] != "fleet.result-lineage.v1" || anys(lineage["selectedSources"]) == nil {
			return fail(409, "Finalizer result lineage is invalid")
		}
		for _, v := range inputs {
			binding := mapOf(v)
			if binding["path"] == "__fleet_control/pipeline.json" {
				control, err := artifactRow(withTx(q), q.app, astr(binding["artifactId"]))
				if err != nil {
					return err
				}
				if control.State != "stored" || !same(control.ProjectID, run["projectId"]) || control.ExecutionID == nil || *control.ExecutionID != astr(mapOf(entry["executionRef"])["executionId"]) || control.ContentHash != binding["sha256"] {
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
	for i, value := range pipelinePlanEntries(run) {
		entry := mapOf(value)
		definition := mapOf(anys(mapOf(run["definition"])["steps"])[i])
		if entry["id"] != definition["id"] || entry["definitionSha256"] != sha256hex([]byte(canonical(definition))) {
			return fmt.Errorf("pipeline plan entry %d does not match its definition", i)
		}
		ref := mapOf(entry["executionRef"])
		if entry["activation"] == "selected" {
			if ref == nil || ref["sourceStepId"] != entry["id"] || num(ref["sourceStepIndex"]) != float64(i) || !labSha.MatchString(astr(ref["sourceDefinitionSha256"])) || !labSha.MatchString(astr(ref["sourceInputSha256"])) {
				return fail(409, "Invalid selected execution reference")
			}
			if ref["mode"] == "fresh" && (ref["executionId"] != stepRow(run, i) || ref["sourceRunId"] != run["id"] || entry["dispatch"] == nil) {
				return fail(409, "Invalid fresh execution ownership")
			}
			if ref["mode"] == "reused" && entry["dispatch"] != nil {
				return fail(409, "Reused execution cannot carry a new dispatch")
			}
		} else if ref != nil || entry["dispatch"] != nil || entry["inputs"] != nil {
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
	plan := mapOf(run["plan"])
	view := doc{"definitionSha256": plan["definitionSha256"], "revision": plan["revision"], "entries": []any{}}
	for i, value := range pipelinePlanEntries(run) {
		entry := doc{}
		for k, v := range mapOf(value) {
			entry[k] = v
		}
		if entry["activation"] == "selected" {
			step := mapOf(anys(run["steps"])[i])
			execution := doc{"executionId": mapOf(entry["executionRef"])["executionId"], "state": step["state"], "observation": "confirmed"}
			if step["observation"] != nil {
				execution["observation"] = step["observation"]
			}
			if id := astr(step["executionId"]); id != "" {
				execution["nativeExecutionId"] = id
			}
			for _, k := range []string{"startedAt", "finishedAt", "durationMs", "usage", "calls", "error"} {
				if step[k] != nil {
					if k == "finishedAt" {
						execution["endedAt"] = step[k]
					} else {
						execution[k] = step[k]
					}
				}
			}
			entry["execution"] = execution
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
					entry["output"] = doc{"artifactId": id, "sha256": artifact.ContentHash}
				}
			}
		}
		view["entries"] = append(anys(view["entries"]), entry)
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
	for i, value := range pipelinePlanEntries(run) {
		if mapOf(value)["id"] == result["stepId"] {
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
	for i, value := range pipelinePlanEntries(run) {
		entry := mapOf(value)
		if entry["activation"] != "selected" {
			continue
		}
		step := mapOf(anys(run["steps"])[i])
		if !unconfirmed(step) {
			continue
		}
		definition := mapOf(anys(mapOf(run["definition"])["steps"])[i])
		ref := mapOf(entry["executionRef"])
		if definition["kind"] != "script" || ref["mode"] != "fresh" {
			return fail(409, "Unconfirmed external action cannot be replayed by rerun")
		}
		native := astr(step["executionId"])
		execution, err := one(q, "SELECT "+execJSON+" || jsonb_build_object('projectId',project_id) FROM execution WHERE id=$1 AND kind='host_job'", native)
		if err != nil {
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
		dispatch := mapOf(entry["dispatch"])
		if dispatch["commandId"] != command["id"] || dispatch["nativeExecutionId"] != native || dispatch["phase"] != "handed_off" {
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

// runPipelineTransition retries DB work with the complete pre-transition
// snapshot, including a paid answer's already materialized output binding.
// Effects and object I/O must remain outside this callback.
func runPipelineTransition(ctx context.Context, a *App, run doc, fn func(*Request) error) error {
	snapshot := restorePipelineValue(nil, run)
	return runTx(ctx, a, nil, true, "", func(q *Request) error {
		cancelled, err := has(q, "SELECT 1 FROM execution WHERE id=$1 AND state='cancelled'", run["id"])
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
