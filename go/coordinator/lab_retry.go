package main

// Local-suffix retry is one Lab action, not outcome reconciliation. The original unknown exit stays unknown.
import (
	"encoding/json"
	"fleet/contract"
	"fmt"
	"sort"
	"strings"
)

type suffixSnapshot struct {
	exp, source, variant doc
	reuse                []any
	inputs               []any
	facts                doc
	artifacts            map[string]*Artifact
	expected             map[string]string
	proofs               []any
}

func suffixConflict(reason string) error { return fail(409, "Local suffix retry: "+reason) }
func suffixIdentity(exp, cell, source, request string) doc {
	return doc{"experimentId": exp, "cell": cell, "sourceRunId": source, "requestId": request}
}
func suffixKey(identity doc) string {
	return "lab-suffix:" + sha256hex([]byte(canonical([]any{identity["experimentId"], identity["requestId"]})))
}

// Called only after expRow succeeds in the caller's ordinary project scope. No cached result bypasses authorization.
func suffixReplay(q *Request, identity doc) (string, error) {
	rs, err := rows(q, "SELECT data || jsonb_build_object('id',id,'experimentId',experiment_id,'kind',kind) FROM execution WHERE request_id=$1", suffixKey(identity))
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "", nil
	}
	r := rs[0]
	got := mapOf(mapOf(r["recovery"])["identity"])
	if r["kind"] != "pipeline_run" || len(got) != 4 || !same(got, identity) || r["experimentId"] != identity["experimentId"] || astr(r["id"]) == "" {
		return "", suffixConflict("request identity conflict")
	}
	return astr(r["id"]), nil
}

// Exact command/event identity is required in addition to the trusted host's resolved recorded ownership.
func suffixProof(execution, command, event doc, stepID string) bool {
	if execution == nil || command == nil || event == nil {
		return false
	}
	if execution["pipelineStepId"] != stepID || execution["actor"] != "program-worker" || command["kind"] != "script" || command["status"] != "unknown" {
		return false
	}
	xid, cid, sid, hid := astr(execution["id"]), astr(command["id"]), astr(execution["sessionId"]), astr(execution["hostId"])
	if xid == "" || cid == "" || sid == "" || hid == "" || command["executionId"] != xid || command["sessionId"] != sid || command["hostId"] != hid {
		return false
	}
	seq, ok := intOK(event["seq"])
	if !ok || seq < 0 || event["id"] != fmt.Sprintf("event:%s:%s-%d", xid, cid, seq) || event["executionId"] != xid || event["sessionId"] != sid || event["type"] != "process.reconciled" {
		return false
	}
	data := mapOf(event["data"])
	count, ok := intOK(data["ownedProcesses"])
	return ok && count > 0 && data["resolved"] == true && data["launchWindowUnresolved"] == false && data["survived"] == false && data["fleetNormalized"] == nil
}

// A stop command neither launches work nor supplies absence proof. Any other second command is ambiguous.
func suffixCommand(commands []doc) (doc, error) {
	var script doc
	for _, command := range commands {
		switch command["kind"] {
		case "stop":
			continue
		case "script":
			if script != nil {
				return nil, suffixConflict("additional launch command")
			}
			script = command
		default:
			return nil, suffixConflict("unexpected execution command")
		}
	}
	if script == nil {
		return nil, suffixConflict("missing script command")
	}
	return script, nil
}

// Frozen example inputs may retain their original owner through an earlier suffix recovery.
func suffixExampleOwner(q *Request, source doc, a *Artifact) error {
	current := source
	seen := map[string]bool{}
	for current != nil {
		id := astr(current["id"])
		if id == "" || seen[id] || !same(current["projectId"], source["projectId"]) || !same(current["example"], source["example"]) {
			break
		}
		seen[id] = true
		found := false
		for _, x := range anys(current["exampleInputs"]) {
			ref := mapOf(x)
			if ref["artifactId"] == a.ID && ref["path"] == a.Name {
				found = true
			}
		}
		if !found {
			break
		}
		if a.ExecutionID != nil && *a.ExecutionID == id {
			return nil
		}
		previous := astr(current["rerunOf"])
		if previous == "" {
			break
		}
		var err error
		current, err = getRun(q, previous)
		if err != nil {
			return err
		}
	}
	return suffixConflict("example input owner changed")
}

// No artifact content is read here. The same function rederives all immutable facts under the write lock.
func suffixRead(q *Request, identity doc, lock bool) (*suffixSnapshot, error) {
	id, cell, sourceID := astr(identity["experimentId"]), astr(identity["cell"]), astr(identity["sourceRunId"])
	exp, err := expRow(q, id)
	if err != nil {
		return nil, err
	}
	if exp["state"] == "draft" || exp["state"] == "stopped" {
		return nil, suffixConflict("experiment is not eligible")
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return nil, err
	}
	_, latest := expLatest(expTrials(runs))
	source := latest[cell]
	if source == nil || source["id"] != sourceID || comparable(source) {
		return nil, suffixConflict("source is not the latest eligible trial")
	}
	scores, err := scoresCurrent(q, id)
	if err != nil {
		return nil, err
	}
	for _, score := range scores {
		for _, trial := range trialIDsOf(score) {
			if trial == sourceID {
				return nil, suffixConflict("source is scored")
			}
		}
	}
	c := mapOf(source["cell"])
	variant := findVariant(exp, c["variant"])
	pin := findPin(exp, c["example"])
	overrides := mapOf(variant["overrides"])
	if overrides == nil {
		overrides = doc{}
	}
	if variant == nil || pin == nil || !same(source["overrides"], overrides) {
		return nil, suffixConflict("variant or example changed")
	}
	def, err := loadPipeline(q, variantPipelineID(exp, variant)+".json")
	if err != nil {
		return nil, err
	}
	if !same(def, source["definition"]) || sha256hex(jsonBytes(def)) != source["definitionSha256"] {
		return nil, suffixConflict("frozen definition changed")
	}
	ref, err := definitionRef(q, "pipeline", astr(def["id"]), def)
	if err != nil {
		return nil, err
	}
	refs := []any{ref}
	if pid := astr(source["projectId"]); pid != "" {
		frozen := mapOf(source["projectSnapshot"])
		current, err := projectContext(q, pid)
		if err != nil {
			return nil, err
		}
		if frozen == nil || frozen["contentSha"] != current["contentSha"] || num(frozen["revision"]) != num(current["revision"]) {
			return nil, suffixConflict("frozen project revision changed or unavailable")
		}
		refs = append(refs, contract.DefinitionRef{Kind: contract.DefinitionKindProject, Id: pid, Revision: int64(num(frozen["revision"])), ContentSha: astr(frozen["contentSha"])})
	}
	if !same(source["definitions"], refs) {
		return nil, suffixConflict("frozen pipeline revision changed")
	}
	reuse, err := succeededPrefix(q, source, def)
	if err != nil {
		return nil, err
	}
	steps := anys(source["steps"])
	definitions := anys(def["steps"])
	if len(reuse) == 0 || len(reuse) >= len(steps) || len(steps) != len(definitions) {
		return nil, suffixConflict("requires a nonempty confirmed prefix and local suffix")
	}
	for _, x := range reuse {
		if unconfirmed(mapOf(x)) {
			return nil, suffixConflict("prefix outcome is unconfirmed")
		}
	}
	snap := &suffixSnapshot{exp: exp, source: source, variant: variant, reuse: reuse, artifacts: map[string]*Artifact{}, expected: map[string]string{}, proofs: []any{}}
	// Include every original prefix coordinator output, even when a child file overrides its staging path.
	for i, x := range reuse {
		s := mapOf(x)
		aid := astr(s["outputArtifactId"])
		if aid == "" {
			return nil, suffixConflict("prefix output artifact missing")
		}
		if err := pipelineOutputOwner(q, source, i, aid); err != nil {
			return nil, err
		}
		snap.expected[aid] = sha256hex(jsonBytes(s["output"]))
	}
	fs, err := pipelineOutputs(q, source, len(reuse))
	if err != nil {
		return nil, err
	}
	snap.inputs = append([]any{}, anys(source["exampleInputs"])...)
	for _, f := range fs {
		snap.inputs = append(snap.inputs, f.doc())
	}
	// Example input artifacts must still represent the frozen runnable example's file content.
	exampleFiles := anys(mapOf(source["example"])["inputs"])
	if len(exampleFiles) != len(anys(source["exampleInputs"])) {
		return nil, suffixConflict("frozen example input references missing")
	}
	for i, x := range exampleFiles {
		f, ref := mapOf(x), mapOf(anys(source["exampleInputs"])[i])
		aid := astr(ref["artifactId"])
		if aid == "" || ref["path"] != "example/"+astr(f["path"]) {
			return nil, suffixConflict("example input path changed")
		}
		hash := astr(f["sha256"])
		if hash == "" {
			hash = sha256hex([]byte(astr(f["text"])))
		}
		snap.expected[aid] = hash
	}
	pinID := astr(pin["artifact_id"])
	if pinID == "" {
		return nil, suffixConflict("missing pinned example")
	}
	snap.expected[pinID] = astr(pin["hash"])
	for _, x := range snap.inputs {
		aid := astr(mapOf(x)["artifactId"])
		if aid == "" {
			return nil, suffixConflict("missing input artifact")
		}
		if _, ok := snap.expected[aid]; !ok {
			snap.expected[aid] = ""
		}
	}
	artifactFacts := doc{}
	for aid, expected := range snap.expected {
		a, err := artifactRow(withTx(q), q.app, aid)
		if err != nil {
			return nil, suffixConflict("artifact unavailable")
		}
		if a.State != "stored" || !labSha.MatchString(a.ContentHash) || (expected != "" && a.ContentHash != expected) {
			return nil, suffixConflict("artifact content identity changed")
		}
		// The pinned example can be shared lab data. All run inputs/outputs belong to this source project.
		if aid != pinID && !same(a.ProjectID, source["projectId"]) {
			return nil, suffixConflict("foreign artifact")
		}
		if strings.HasPrefix(a.Name, "example/") {
			if err := suffixExampleOwner(q, source, a); err != nil {
				return nil, err
			}
		}
		snap.artifacts[aid] = a
		artifactFacts[aid] = a.view()
	}
	for i := len(reuse); i < len(steps); i++ {
		s, d := mapOf(steps[i]), mapOf(definitions[i])
		if d["kind"] != "script" {
			return nil, suffixConflict("suffix is not script-only")
		}
		xid := astr(s["executionId"])
		if xid == "" {
			if s["state"] != "preparing" {
				return nil, suffixConflict("missing suffix execution")
			}
			continue
		}
		if lock {
			if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "execution:"+xid); err != nil {
				return nil, err
			}
		}
		e, err := get(q, xid, "execution")
		if err != nil {
			return nil, err
		}
		owned, err := has(q, "SELECT 1 FROM execution WHERE id=$1 AND project_id=$2 AND kind='host_job'", xid, source["projectId"])
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, suffixConflict("wrong execution project")
		}
		commands, err := all(q, "command", doc{"executionId": xid})
		if err != nil {
			return nil, err
		}
		command, err := suffixCommand(commands)
		if err != nil {
			return nil, err
		}
		session, err := get(q, e["sessionId"], "session")
		if err != nil {
			return nil, err
		}
		if e["pipelineRunId"] != sourceID || session["pipelineRunId"] != sourceID || session["projectId"] != source["projectId"] {
			return nil, suffixConflict("execution/session source binding changed")
		}
		outputs := doc{}
		for _, old := range steps[:i] {
			prev := mapOf(old)
			outputs[astr(prev["id"])] = prev["output"]
		}
		resolved, err := resolveRefs(d["command"], source["example"], outputs, true)
		if err != nil {
			return nil, suffixConflict("frozen script cannot be resolved")
		}
		payload := mapOf(command["payload"])
		if !same(payload["command"], resolved) || !same(payload["timeout_ms"], d["timeout_ms"]) {
			return nil, suffixConflict("original script command changed")
		}

		events, err := eventsWhere(q, doc{"executionId": xid, "type": "process.reconciled"})
		if err != nil {
			return nil, err
		}
		var proof doc
		for _, event := range events {
			if suffixProof(e, command, event, stepRow(source, i)) {
				if proof != nil {
					return nil, suffixConflict("ambiguous absence proof")
				}
				proof = event
			}
		}
		if proof == nil {
			return nil, suffixConflict("required execution, command, event or receipt link is missing")
		}
		if e["state"] == "succeeded" && !unconfirmed(e) {
			return nil, suffixConflict("suffix is already confirmed successful")
		}
		// Only the first pending suffix command has inputs; later steps were never launched.
		if !same(mapOf(command["payload"])["inputs"], snap.inputs) {
			return nil, suffixConflict("original suffix inputs differ")
		}
		snap.proofs = append(snap.proofs, doc{"executionId": xid, "commandId": command["id"], "eventId": proof["id"]})
	}
	if len(snap.proofs) == 0 {
		return nil, suffixConflict("no reconciled interrupted script")
	}
	snap.facts = doc{"definition": def, "pin": pin, "variant": variant, "cell": source["cell"], "example": source["example"], "exampleInputs": source["exampleInputs"], "inputs": snap.inputs, "artifacts": artifactFacts, "proofs": snap.proofs, "steps": source["steps"]}
	return snap, nil
}

func expRetrySuffix(q *Request, id, cell string, b doc) (doc, error) {
	for _, key := range []string{"experimentId", "cell", "sourceRunId", "requestId"} {
		v, ok := b[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return nil, fail(400, "Expected "+key)
		}
	}
	identity := suffixIdentity(id, cell, astr(b["sourceRunId"]), astr(b["requestId"]))
	var snap *suffixSnapshot
	var result string
	err := labReadTx(q, func(q *Request) error {
		if _, err := expRow(q, id); err != nil {
			return err
		}
		var err error
		result, err = suffixReplay(q, identity)
		if err != nil || result != "" {
			return err
		}
		snap, err = suffixRead(q, identity, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	if result != "" {
		return suffixReply(q, id, identity, result)
	}
	// Storage reads are deliberately outside every write transaction and run mutex.
	hashes := map[string]string{}
	pinID := astr(findPin(snap.exp, mapOf(snap.source["cell"])["example"])["artifact_id"])
	for aid, a := range snap.artifacts {
		bytes, err := artifactBytes(q.ctx(), q.app, aid, 64<<20)
		if err != nil || int64(len(bytes)) != a.Size || sha256hex(bytes) != a.ContentHash {
			return nil, suffixConflict("actual artifact bytes unavailable or corrupt")
		}
		hashes[aid] = sha256hex(bytes)
		if aid == pinID {
			var full doc
			if json.Unmarshal(bytes, &full) != nil {
				return nil, suffixConflict("invalid pinned example bytes")
			}
			runnable := doc{}
			for k, v := range full {
				if k != "references" {
					runnable[k] = v
				}
			}
			if !same(runnable, snap.source["example"]) {
				return nil, suffixConflict("frozen example content changed")
			}
		}
	}

	m := runLock(astr(identity["sourceRunId"]))
	m.Lock()
	var created bool
	var done doc
	err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		// A retried transaction must not retain side effects from a previous rolled-back attempt.
		created = false
		done = nil
		result = ""
		if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "experiment:"+id); err != nil {
			return err
		}
		if _, err := expLocked(q, id); err != nil {
			return err
		}
		var err error
		result, err = suffixReplay(q, identity)
		if err != nil || result != "" {
			return err
		}
		fresh, err := suffixRead(q, identity, true)
		if err != nil {
			return err
		}
		if !same(snap.facts, fresh.facts) {
			return suffixConflict("preflight facts changed before commit")
		}
		names := []any{}
		for _, step := range fresh.reuse {
			names = append(names, mapOf(step)["id"])
		}
		aids := []string{}
		for aid := range hashes {
			aids = append(aids, aid)
		}
		sort.Strings(aids)
		artifactAudit := []any{}
		for _, aid := range aids {
			artifactAudit = append(artifactAudit, doc{"artifactId": aid, "sha256": hashes[aid]})
		}
		recovery := doc{"identity": identity, "reusedStepIds": names, "audit": doc{"proofs": fresh.proofs, "artifacts": artifactAudit}}
		c := doc{}
		for k, v := range mapOf(fresh.source["cell"]) {
			c[k] = v
		}
		attempt, _ := intOK(c["attempt"])
		c["attempt"] = attempt + 1
		result, err = expInsertRunReuse(q, fresh.exp, suffixKey(identity), variantPipelineID(fresh.exp, fresh.variant)+".json", fresh.source["example"], fresh.variant, doc{"cell": c, "recovery": recovery, "exampleInputs": fresh.source["exampleInputs"]}, doc{"reuse": fresh.reuse, "rerunOf": fresh.source["id"]})
		if err != nil {
			return err
		}
		if result == "" {
			result, err = suffixReplay(q, identity)
			if err != nil {
				return err
			}
			if result == "" {
				return suffixConflict("request identity conflict")
			}
			return nil
		}
		_, done, err = pipelineCancelIn(q, astr(fresh.source["id"]))
		if err != nil {
			return err
		}
		if err := exec(q, "UPDATE experiment SET state='running' WHERE id=$1 AND state='done'", id); err != nil {
			return err
		}
		created = true
		return nil
	})
	m.Unlock()
	if err != nil {
		return nil, err
	}
	if created {
		if done != nil {
			notifyFinished(q.app, done)
		}
		pipelineAdvance(q.app, result)
		if err := labReconcile(q.ctx(), q.app, id); err != nil {
			return nil, err
		}
	}
	return suffixReply(q, id, identity, result)
}
func suffixReply(q *Request, id string, identity doc, runID string) (doc, error) {
	view, err := expGetTx(q, id)
	if err != nil {
		return nil, err
	}
	view["recovery"] = doc{"sourceRunId": identity["sourceRunId"], "runId": runID}
	return view, nil
}
