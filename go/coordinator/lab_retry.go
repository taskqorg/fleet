package main

// Local-suffix retry is one Lab action, not outcome reconciliation. The original unknown exit stays unknown.
import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// labSuffixIdentity: what names one suffix retry request (a retried run's recovery identity).
type labSuffixIdentity struct {
	ExperimentID string `json:"experimentId"`
	Cell         string `json:"cell"`
	SourceRunID  string `json:"sourceRunId"`
	RequestID    string `json:"requestId"`
}

// labRecovery: how a retried run recovered its source (the run's recovery field).
type labRecovery struct {
	Identity      labSuffixIdentity `json:"identity"`
	ReusedStepIDs []string          `json:"reusedStepIds"`
	Audit         *labRecoveryAudit `json:"audit,omitempty"`
}

// labRecoveryAudit: the proofs and artifact contents a suffix retry relied on.
type labRecoveryAudit struct {
	Proofs    []labSuffixProof   `json:"proofs"`
	Artifacts []labArtifactAudit `json:"artifacts"`
}

// labSuffixProof: the command-bound absence proof of one interrupted script.
type labSuffixProof struct {
	ExecutionID string `json:"executionId"`
	CommandID   string `json:"commandId"`
	EventID     string `json:"eventId"`
}

// labArtifactAudit: one artifact's content as the retry read it.
type labArtifactAudit struct {
	ArtifactID string `json:"artifactId"`
	Sha256     string `json:"sha256"`
}

// labProofExecution: the fields of a suffix step's execution a retry reads.
type labProofExecution struct {
	ID             string `json:"id"`
	SessionID      string `json:"sessionId"`
	HostID         string `json:"hostId"`
	PipelineStepID string `json:"pipelineStepId"`
	PipelineRunID  string `json:"pipelineRunId"`
	Actor          string `json:"actor"`
	State          string `json:"state"`
	Observation    string `json:"observation"`
}

// labSuffixCommand: the fields of an execution's command a retry reads.
type labSuffixCommand struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	ExecutionID string          `json:"executionId"`
	SessionID   string          `json:"sessionId"`
	HostID      string          `json:"hostId"`
	Payload     json.RawMessage `json:"payload"`
}

// labProofEvent: the fields of a process.reconciled event a retry reads.
type labProofEvent struct {
	ID          string `json:"id"`
	Seq         *int64 `json:"seq"`
	ExecutionID string `json:"executionId"`
	SessionID   string `json:"sessionId"`
	Type        string `json:"type"`
	Data        struct {
		OwnedProcesses         *int64          `json:"ownedProcesses"`
		Resolved               *bool           `json:"resolved"`
		LaunchWindowUnresolved *bool           `json:"launchWindowUnresolved"`
		Survived               *bool           `json:"survived"`
		FleetNormalized        json.RawMessage `json:"fleetNormalized"`
	} `json:"data"`
}

// labRecordOf: a record (get, all, eventsWhere) as T; nil when it is none or does not decode as one.
func labRecordOf[T any](v any) *T {
	if string(jsonBytes(v)) == "null" {
		return nil
	}
	t, err := asT[T](v)
	if err != nil {
		return nil
	}
	return &t
}

// labSuffixFacts: what a retry verified, compared again under the write lock.
type labSuffixFacts struct {
	Definition    json.RawMessage            `json:"definition"`
	Pin           labPin                     `json:"pin"`
	Variant       labVariant                 `json:"variant"`
	Cell          *labCell                   `json:"cell"`
	Example       json.RawMessage            `json:"example"`
	ExampleInputs json.RawMessage            `json:"exampleInputs"`
	Inputs        []labExampleInput          `json:"inputs"`
	Artifacts     map[string]json.RawMessage `json:"artifacts"`
	Proofs        []labSuffixProof           `json:"proofs"`
	Steps         []labRunStep               `json:"steps"`
}

type suffixSnapshot struct {
	exp       *labExperiment
	source    *labRun
	variant   *labVariant
	pin       *labPin
	reuse     []runStep
	reused    []labRunStep
	inputs    []labExampleInput
	facts     labSuffixFacts
	artifacts map[string]*Artifact
	expected  map[string]string
	proofs    []labSuffixProof
}

func suffixConflict(reason string) error { return fail(409, "Local suffix retry: "+reason) }
func suffixKey(identity labSuffixIdentity) string {
	return "lab-suffix:" + sha256hex([]byte(canonical([]any{identity.ExperimentID, identity.RequestID})))
}

// labSuffixRun: the fields of a run record a replay reads.
type labSuffixRun struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ExperimentID string `json:"experimentId"`
	Recovery     struct {
		Identity map[string]json.RawMessage `json:"identity"`
	} `json:"recovery"`
}

// Called only after expRow succeeds in the caller's ordinary project scope. No cached result bypasses authorization.
func suffixReplay(q *Request, identity labSuffixIdentity) (string, error) {
	key := suffixKey(identity)
	records, err := q.db().LabSuffixRuns(q.ctx(), &key)
	if err != nil || len(records) == 0 {
		return "", err
	}
	var r labSuffixRun
	if err := json.Unmarshal(records[0], &r); err != nil {
		return "", err
	}
	got := r.Recovery.Identity
	if r.Kind != "pipeline_run" || len(got) != 4 || !same(got, identity) || r.ExperimentID != identity.ExperimentID || r.ID == "" {
		return "", suffixConflict("request identity conflict")
	}
	return r.ID, nil
}

// Exact command/event identity is required in addition to the trusted host's resolved recorded ownership.
func suffixProven(execution *labProofExecution, command *labSuffixCommand, event *labProofEvent, stepID string) bool {
	if execution == nil || command == nil || event == nil {
		return false
	}
	if execution.PipelineStepID != stepID || execution.Actor != "program-worker" || command.Kind != "script" || command.Status != "unknown" {
		return false
	}
	xid, cid, sid, hid := execution.ID, command.ID, execution.SessionID, execution.HostID
	if xid == "" || cid == "" || sid == "" || hid == "" || command.ExecutionID != xid || command.SessionID != sid || command.HostID != hid {
		return false
	}
	if event.Seq == nil || *event.Seq < 0 || event.ID != fmt.Sprintf("event:%s:%s-%d", xid, cid, *event.Seq) || event.ExecutionID != xid || event.SessionID != sid || event.Type != "process.reconciled" {
		return false
	}
	d := event.Data
	return d.OwnedProcesses != nil && *d.OwnedProcesses > 0 && d.Resolved != nil && *d.Resolved && d.LaunchWindowUnresolved != nil && !*d.LaunchWindowUnresolved &&
		d.Survived != nil && !*d.Survived && rawValue(d.FleetNormalized) == nil
}

// A stop command neither launches work nor supplies absence proof. Any other second command is ambiguous.
func suffixScript(commands []labSuffixCommand) (int, error) {
	script := -1
	for i, command := range commands {
		switch command.Kind {
		case "stop":
			continue
		case "script":
			if script >= 0 {
				return -1, suffixConflict("additional launch command")
			}
			script = i
		default:
			return -1, suffixConflict("unexpected execution command")
		}
	}
	if script < 0 {
		return -1, suffixConflict("missing script command")
	}
	return script, nil
}

// Frozen example inputs may retain their original owner through an earlier suffix recovery.
func suffixExampleOwner(q *Request, source *labRun, a *Artifact) error {
	current := source
	seen := map[string]bool{}
	for current != nil {
		id := current.ID
		if id == "" || seen[id] || !same(current.ProjectID, source.ProjectID) || !same(current.Example, source.Example) {
			break
		}
		seen[id] = true
		found := false
		for _, ref := range current.inputs() {
			if ref.ArtifactID == a.ID && ref.Path == a.Name {
				found = true
			}
		}
		if !found {
			break
		}
		if a.ExecutionID != nil && *a.ExecutionID == id {
			return nil
		}
		if current.RerunOf == "" {
			break
		}
		var err error
		current, err = labRunRead(q, current.RerunOf)
		if err != nil {
			return err
		}
	}
	return suffixConflict("example input owner changed")
}

// labProjectRevision: a project's revision as a run froze it (projectSnapshot) and projectContext gives it.
type labProjectRevision struct {
	ContentSha string  `json:"contentSha"`
	Revision   float64 `json:"revision"`
}

// labCommandPayload: the fields of a script command's payload a retry reads.
type labCommandPayload struct {
	Command   json.RawMessage   `json:"command"`
	TimeoutMs json.RawMessage   `json:"timeout_ms"`
	Inputs    []json.RawMessage `json:"inputs"`
}

// labOrderedInputs: input bindings sorted by path, as their canonical JSON (preparation sorts binding facts, while
// commands retain staging order).
func labOrderedInputs(values []json.RawMessage) string {
	type entry struct {
		path  string
		value any
	}
	list := []entry{}
	for _, raw := range values {
		var p struct {
			Path any `json:"path"`
		}
		json.Unmarshal(raw, &p)
		path, _ := p.Path.(string)
		list = append(list, entry{path, rawValue(raw)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].path < list[j].path })
	out := []any{}
	for _, e := range list {
		out = append(out, e.value)
	}
	return canonical(out)
}

// No artifact content is read here. The same function rederives all immutable facts under the write lock.
func suffixRead(q *Request, identity labSuffixIdentity, lock bool) (*suffixSnapshot, error) {
	id, cell, sourceID := identity.ExperimentID, identity.Cell, identity.SourceRunID
	exp, err := expRow(q, id)
	if err != nil {
		return nil, err
	}
	if exp.State == "draft" || exp.State == "stopped" {
		return nil, suffixConflict("experiment is not eligible")
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return nil, err
	}
	_, latest := expLatest(expTrials(runs))
	source := latest[cell]
	if source == nil || source.ID != sourceID || comparable(source) {
		return nil, suffixConflict("source is not the latest eligible trial")
	}
	scores, err := scoresCurrent(q, id)
	if err != nil {
		return nil, err
	}
	for _, score := range scores {
		for _, trial := range score.TrialIDs {
			if trial == sourceID {
				return nil, suffixConflict("source is scored")
			}
		}
	}
	c := source.cell()
	variant := exp.variant(c.Variant)
	pin := exp.pin(c.Example)
	if variant == nil || pin == nil {
		return nil, suffixConflict("variant or example changed")
	}
	overrides := variant.Overrides
	if overrides == nil {
		overrides = rawValue(json.RawMessage("{}"))
	}
	if !same(source.Overrides, overrides) {
		return nil, suffixConflict("variant or example changed")
	}
	def, err := loadPipelineDef(q, variantPipelineID(exp, variant)+".json")
	if err != nil {
		return nil, err
	}
	if !same(def.value(), source.Definition) || sha256hex([]byte(canonical(def.value()))) != source.DefinitionSha256 {
		return nil, suffixConflict("frozen definition changed")
	}
	var steps labDefinitionSteps
	json.Unmarshal(jsonBytes(def), &steps)
	ref, err := definitionRef(q, "pipeline", def.ID, def.value())
	if err != nil {
		return nil, err
	}
	refs := []any{ref}
	if pid := deref(source.ProjectID); pid != "" {
		current, err := projectContext(q, pid)
		if err != nil {
			return nil, err
		}
		var frozen, now labProjectRevision
		json.Unmarshal(source.ProjectSnapshot, &frozen)
		now, _ = asT[labProjectRevision](current)
		if rawValue(source.ProjectSnapshot) == nil || frozen.ContentSha != now.ContentSha || frozen.Revision != now.Revision {
			return nil, suffixConflict("frozen project revision changed or unavailable")
		}
		refs = append(refs, contract.DefinitionRef{Kind: contract.DefinitionKindProject, Id: pid, Revision: int64(frozen.Revision), ContentSha: frozen.ContentSha})
	}
	if !same(source.Definitions, refs) {
		return nil, suffixConflict("frozen pipeline revision changed")
	}
	reuse, err := runPrefix(q, source.run, def)
	if err != nil {
		return nil, err
	}
	reused, _ := asT[[]labRunStep](reuse)
	run := source.Steps
	definitions := steps.Steps
	if len(reused) == 0 || len(reused) >= len(run) || len(run) != len(definitions) {
		return nil, suffixConflict("requires a nonempty confirmed prefix and local suffix")
	}
	for _, s := range reused {
		if s.Observation == "unconfirmed" {
			return nil, suffixConflict("prefix outcome is unconfirmed")
		}
	}
	snap := &suffixSnapshot{exp: exp, source: source, variant: variant, pin: pin, reuse: reuse, reused: reused, artifacts: map[string]*Artifact{}, expected: map[string]string{},
		proofs: []labSuffixProof{}}
	// Include every original prefix coordinator output, even when a child file overrides its staging path.
	for i, s := range reused {
		if e := source.run.entry(i); e != nil && e.Activation == contract.PipelineActivationNotSelected {
			continue
		}
		aid := s.OutputArtifactID
		if aid == "" {
			return nil, suffixConflict("prefix output artifact missing")
		}
		if err := outputOwner(q, source.run, i, aid); err != nil {
			return nil, err
		}
		snap.expected[aid] = sha256hex(jsonNorm(s.Output))
	}
	fs, err := source.run.outputFiles(q, len(reused))
	if err != nil {
		return nil, err
	}
	sourceInputs := source.inputs()
	snap.inputs = append([]labExampleInput{}, sourceInputs...)
	for _, f := range fs {
		snap.inputs = append(snap.inputs, labExampleInput{ArtifactID: f.ArtifactID, Path: f.Path})
	}
	// Example input artifacts must still represent the frozen runnable example's file content.
	var example struct {
		Inputs []labExampleFile `json:"inputs"`
	}
	json.Unmarshal(source.Example, &example)
	if len(example.Inputs) != len(sourceInputs) {
		return nil, suffixConflict("frozen example input references missing")
	}
	for i, f := range example.Inputs {
		ref := sourceInputs[i]
		aid := ref.ArtifactID
		if aid == "" || ref.Path != "example/"+f.Path {
			return nil, suffixConflict("example input path changed")
		}
		hash := f.Sha256
		if hash == "" {
			hash = sha256hex([]byte(deref(f.Text)))
		}
		snap.expected[aid] = hash
	}
	pinID := pin.ArtifactID
	if pinID == "" {
		return nil, suffixConflict("missing pinned example")
	}
	snap.expected[pinID] = pin.Hash
	for _, x := range snap.inputs {
		aid := x.ArtifactID
		if aid == "" {
			return nil, suffixConflict("missing input artifact")
		}
		if _, ok := snap.expected[aid]; !ok {
			snap.expected[aid] = ""
		}
	}
	// Every selected suffix freezes its own inputs, including the server control
	// artifact. Verify those exact command bindings rather than guessing inputs
	// from a flattened prefix projection.
	for i := len(reused); i < len(run); i++ {
		entry := source.run.entry(i)
		if entry == nil || entry.Activation != contract.PipelineActivationSelected {
			continue
		}
		bindings := entry.Inputs
		if bindings == nil || entry.ExecutionRef == nil || sha256hex([]byte(canonical(bindings))) != entry.ExecutionRef.SourceInputSha256 {
			return nil, suffixConflict("suffix input binding unavailable")
		}
		for _, binding := range bindings {
			snap.expected[binding.ArtifactId] = binding.Sha256
		}
	}
	artifactFacts := map[string]json.RawMessage{}
	for aid, expected := range snap.expected {
		a, err := artifactRow(withTx(q), q.app, aid)
		if err != nil {
			return nil, suffixConflict("artifact unavailable")
		}
		if a.State != "stored" || !labSha.MatchString(a.ContentHash) || (expected != "" && a.ContentHash != expected) {
			return nil, suffixConflict("artifact content identity changed")
		}
		// The pinned example can be shared lab data. All run inputs/outputs belong to this source project.
		if aid != pinID && !same(a.ProjectID, source.ProjectID) {
			return nil, suffixConflict("foreign artifact")
		}
		if strings.HasPrefix(a.Name, "example/") {
			if err := suffixExampleOwner(q, source, a); err != nil {
				return nil, err
			}
		}
		snap.artifacts[aid] = a
		artifactFacts[aid] = jsonBytes(a.view())
	}
	if lock {
		var keys []string
		for i := len(reused); i < len(run); i++ {
			if xid := run[i].ExecutionID; xid != "" {
				keys = append(keys, "execution:"+xid)
			}
		}
		if err := acquireTxLock(q, keys...); err != nil {
			return nil, err
		}
	}
	for i := len(reused); i < len(run); i++ {
		s, d := run[i], definitions[i]
		if d.Kind != "script" {
			return nil, suffixConflict("suffix is not script-only")
		}
		entry := source.run.entry(i)
		if entry == nil {
			return nil, suffixConflict("missing suffix plan entry")
		}
		xid := s.ExecutionID
		if xid == "" {
			if entry.ExecutionRef == nil && (entry.Activation == contract.PipelineActivationPending || entry.Activation == contract.PipelineActivationBlocked || entry.Activation == contract.PipelineActivationNotSelected) {
				continue
			}
			if s.State != "preparing" {
				return nil, suffixConflict("missing suffix execution")
			}
			continue
		}
		record, err := recGet[rawObject](q, recExecution, xid)
		if err != nil {
			return nil, err
		}
		e := labRecordOf[labProofExecution](record)
		if e == nil {
			return nil, suffixConflict("required execution, command, event or receipt link is missing")
		}
		owned, err := q.db().LabHostJobInProject(q.ctx(), db.LabHostJobInProjectParams{ID: xid, ProjectID: source.ProjectID})
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, suffixConflict("wrong execution project")
		}
		cs, err := commandsList(q, db.CommandsParams{ExecutionID: &xid})
		if err != nil {
			return nil, err
		}
		commands := make([]labSuffixCommand, 0, len(cs))
		for _, c := range cs {
			commands = append(commands, labSuffixCommand{ID: c.ID, Kind: c.Kind, Status: c.Status, ExecutionID: c.ExecutionID, SessionID: c.SessionID, HostID: c.HostID, Payload: c.Payload})
		}
		ci, err := suffixScript(commands)
		if err != nil {
			return nil, err
		}
		command := &commands[ci]
		x := entry.Dispatch
		if entry.Activation != contract.PipelineActivationSelected || entryRefMode(entry) != contract.PipelineRefModeFresh || x == nil || x.Phase != contract.PipelineDispatchPhaseHandedOff ||
			x.CommandId == nil || *x.CommandId != command.ID || x.NativeExecutionId == nil || *x.NativeExecutionId != xid {
			return nil, suffixConflict("selected launch plan binding changed")
		}
		session, err := sessionByID(q, e.SessionID)
		if err != nil {
			return nil, err
		}
		if e.PipelineRunID != sourceID || session.PipelineRunID != sourceID || !same(strPtr(session.ProjectID), source.ProjectID) {
			return nil, suffixConflict("execution/session source binding changed")
		}
		outputs := map[string]json.RawMessage{}
		for _, prev := range run[:i] {
			outputs[prev.ID] = labNull(prev.Output)
		}
		resolved, err := resolveRefsRaw(d.Command, source.Example, outputs, true)
		if err != nil {
			return nil, suffixConflict("frozen script cannot be resolved")
		}
		var payload labCommandPayload
		json.Unmarshal(command.Payload, &payload)
		if !same(rawValue(payload.Command), rawValue(resolved)) || !same(rawValue(payload.TimeoutMs), rawValue(d.TimeoutMs)) {
			return nil, suffixConflict("original script command changed")
		}

		events, err := eventsWhere(q, xid, ptr("process.reconciled"))
		if err != nil {
			return nil, err
		}
		var proof *labProofEvent
		for _, event := range events {
			v := labRecordOf[labProofEvent](event)
			if suffixProven(e, command, v, source.run.row(i)) {
				if proof != nil {
					return nil, suffixConflict("ambiguous absence proof")
				}
				proof = v
			}
		}
		if proof == nil {
			return nil, suffixConflict("required execution, command, event or receipt link is missing")
		}
		if e.State == "succeeded" && e.Observation != "unconfirmed" {
			return nil, suffixConflict("suffix is already confirmed successful")
		}
		// Only the first pending suffix command has inputs; later steps were never launched.
		frozen := []json.RawMessage{}
		for _, binding := range entry.Inputs {
			frozen = append(frozen, jsonBytes(labExampleInput{Path: binding.Path, ArtifactID: binding.ArtifactId}))
		}
		if labOrderedInputs(payload.Inputs) != labOrderedInputs(frozen) {
			return nil, suffixConflict("original suffix inputs differ")
		}
		snap.proofs = append(snap.proofs, labSuffixProof{ExecutionID: xid, CommandID: command.ID, EventID: proof.ID})
	}
	if len(snap.proofs) == 0 {
		return nil, suffixConflict("no reconciled interrupted script")
	}
	snap.facts = labSuffixFacts{Definition: jsonBytes(def), Pin: *pin, Variant: *variant, Cell: source.Cell, Example: source.Example, ExampleInputs: source.ExampleInputs, Inputs: snap.inputs,
		Artifacts: artifactFacts, Proofs: snap.proofs, Steps: source.Steps}
	return snap, nil
}

func expRetrySuffix(q *Request, id, cell, sourceRunID, requestID string) (labExperimentView, error) {
	var none labExperimentView
	for i, v := range []string{id, cell, sourceRunID, requestID} {
		if strings.TrimSpace(v) == "" {
			return none, fail(400, "Expected "+[]string{"experimentId", "cell", "sourceRunId", "requestId"}[i])
		}
	}
	identity := labSuffixIdentity{ExperimentID: id, Cell: cell, SourceRunID: sourceRunID, RequestID: requestID}
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
		return none, err
	}
	if result != "" {
		return suffixReply(q, id, identity, result)
	}
	// Storage reads are deliberately outside every write transaction and run mutex.
	hashes := map[string]string{}
	pinID := snap.pin.ArtifactID
	for aid, a := range snap.artifacts {
		bytes, err := artifactBytes(q.ctx(), q.app, aid, 64<<20)
		if err != nil || int64(len(bytes)) != a.Size || sha256hex(bytes) != a.ContentHash {
			return none, suffixConflict("actual artifact bytes unavailable or corrupt")
		}
		hashes[aid] = sha256hex(bytes)
		if aid == pinID {
			var full labExample
			if _, ok := labRawObject(bytes); !ok || json.Unmarshal(bytes, &full) != nil {
				return none, suffixConflict("invalid pinned example bytes")
			}
			if !same(full.runnable(), snap.source.Example) {
				return none, suffixConflict("frozen example content changed")
			}
		}
	}

	var created bool
	err = q.app.store.Write(q.ctx(), q, []string{GLOBAL, "experiment:" + id}, func(q *Request) error {
		// A retried transaction must not retain side effects from a previous rolled-back attempt.
		created = false
		result = ""
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
		names := []string{}
		for _, step := range fresh.reused {
			names = append(names, step.ID)
		}
		aids := []string{}
		for aid := range hashes {
			aids = append(aids, aid)
		}
		sort.Strings(aids)
		artifactAudit := []labArtifactAudit{}
		for _, aid := range aids {
			artifactAudit = append(artifactAudit, labArtifactAudit{ArtifactID: aid, Sha256: hashes[aid]})
		}
		recovery := labRecovery{Identity: identity, ReusedStepIDs: names, Audit: &labRecoveryAudit{Proofs: fresh.proofs, Artifacts: artifactAudit}}
		c := fresh.source.cell()
		c.Attempt++
		result, err = labStartRun(q, fresh.exp, labRunStart{RequestID: suffixKey(identity), File: variantPipelineID(fresh.exp, fresh.variant) + ".json", Example: rawValue(fresh.source.Example),
			Variant: fresh.variant, Extra: labRunExtra{Cell: &c, Recovery: &recovery, ExampleInputs: labNull(fresh.source.ExampleInputs)}, Reuse: fresh.reuse, RerunOf: fresh.source.ID})
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
		_, _, err = pipelineCancelIn(q, fresh.source.ID)
		if err != nil {
			return err
		}
		if _, err := experimentTransition(q, id, experimentEvent{To: "running", From: []string{"done"}}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return none, err
	}
	if created {
		if err := labRunNow(q.ctx(), q.app, id); err != nil {
			return none, err
		}
	}
	return suffixReply(q, id, identity, result)
}

// labSuffixResult: a suffix retry's source and new run (the reply's recovery).
type labSuffixResult struct {
	SourceRunID string `json:"sourceRunId"`
	RunID       string `json:"runId"`
}

func suffixReply(q *Request, id string, identity labSuffixIdentity, runID string) (labExperimentView, error) {
	view, err := expGetTx(q, id)
	if err != nil {
		return view, err
	}
	if view.Rest == nil {
		view.Rest = map[string]json.RawMessage{}
	}
	view.Rest["recovery"] = jsonBytes(labSuffixResult{SourceRunID: identity.SourceRunID, RunID: runID})
	return view, nil
}
