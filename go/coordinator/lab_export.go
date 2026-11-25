// Lab notes, amendments, registry/examples, draft imports, and trial artifact/evidence helpers.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var (
	labSha   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	labMedia = regexp.MustCompile(`^[a-z]+/[A-Za-z0-9.+-]+$`)
)

// labExampleFile: an example input or reference file in its canonical shape: text, or a binary file by its content hash
// (its bytes sit next to the seed and import stores them). Field order is the canonical key order.
type labExampleFile struct {
	Path      string  `json:"path"`
	Text      *string `json:"text,omitempty"`
	Sha256    string  `json:"sha256,omitempty"`
	MediaType string  `json:"media_type,omitempty"`
}

// labFixture: an example's fixture repository.
type labFixture struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
}

// labCheck: one script check of an example.
type labCheck struct {
	ID  string `json:"id"`
	Run string `json:"run"`
}

// labExample: an example in its canonical shape (parseExample); field order is the stored key order, so equal content
// gives an equal hash.
type labExample struct {
	ID         string           `json:"id"`
	UseCaseID  string           `json:"use_case_id"`
	Version    int64            `json:"version"`
	Origin     string           `json:"origin"`
	Fixture    *labFixture      `json:"fixture"`
	Prompt     string           `json:"prompt"`
	Inputs     []labExampleFile `json:"inputs"`
	References []labExampleFile `json:"references"`
	Checks     []labCheck       `json:"checks"`
	Split      string           `json:"split"`
}

// labRunExample: the example a trial runs: the example without its references (the known-good answers).
type labRunExample struct {
	ID        string           `json:"id"`
	UseCaseID string           `json:"use_case_id"`
	Version   int64            `json:"version"`
	Origin    string           `json:"origin"`
	Fixture   *labFixture      `json:"fixture"`
	Prompt    string           `json:"prompt"`
	Inputs    []labExampleFile `json:"inputs"`
	Checks    []labCheck       `json:"checks"`
	Split     string           `json:"split"`
}

// runnable: the example as a trial runs it.
func (e labExample) runnable() labRunExample {
	return labRunExample{ID: e.ID, UseCaseID: e.UseCaseID, Version: e.Version, Origin: e.Origin, Fixture: e.Fixture, Prompt: e.Prompt, Inputs: e.Inputs, Checks: e.Checks, Split: e.Split}
}

// labFileBytes: a text file's text, or a binary file's stored bytes, found by its content hash among the example
// files of the run's project or of no project.
func labFileBytes(q *Request, projectID *string, f labExampleFile) ([]byte, error) {
	if f.Sha256 == "" {
		return []byte(deref(f.Text)), nil
	}
	id, err := q.db().LabExampleFileFind(q.ctx(), db.LabExampleFileFindParams{ContentHash: f.Sha256, Project: deref(projectID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(409, "Example file "+f.Path+" is not stored; import the example again")
	}
	if err != nil {
		return nil, err
	}
	return artifactBytes(withTx(q), q.app, id, 64<<20)
}

// storeExampleFiles checks each binary file of an example against the seed directory and, with write, stores it.
func storeExampleFiles(q *Request, projectID *string, ex labExample, write bool) error {
	for _, f := range append(append([]labExampleFile{}, ex.Inputs...), ex.References...) {
		if f.Sha256 == "" {
			continue
		}
		path, sha, media := f.Path, f.Sha256, f.MediaType
		b, err := os.ReadFile(filepath.Join(labDir(), "examples", ex.ID, path))
		if err != nil {
			return fail(400, "Example file "+path+" is missing next to the seed")
		}
		if sha256hex(b) != sha {
			return fail(400, "Example file "+path+" does not match its sha256")
		}
		if len(b) > 64<<20 {
			return fail(400, "Example file "+path+" is over 64 MiB")
		}
		if !write {
			continue
		}
		n, err := q.db().LabExampleFileCount(q.ctx(), db.LabExampleFileCountParams{ContentHash: sha, Project: deref(projectID)})
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		h, storeID, err := artifactWriteObject(q, optText(projectID), "lab", b, media)
		if err != nil {
			return err
		}
		aid := newID()
		if err := q.db().LabExampleFileInsert(q.ctx(), db.LabExampleFileInsertParams{ID: aid, ProjectID: projectID, ContentHash: h, Size: int64(len(b)), MediaType: media,
			Name: "examples/" + ex.ID + "/" + path}); err != nil {
			return err
		}
		if err := artifactCopyInsertVerified(q.ctx(), q.db(), aid, storeID, projectID); err != nil {
			return err
		}
	}
	return nil
}

// labFileIn: an example file as a request gives it; each field is checked for its type.
type labFileIn struct {
	Path      json.RawMessage `json:"path"`
	Text      json.RawMessage `json:"text"`
	Sha256    json.RawMessage `json:"sha256"`
	MediaType json.RawMessage `json:"media_type"`
}

func labFiles(v json.RawMessage, what string) ([]labExampleFile, error) {
	if rawValue(v) == nil {
		return []labExampleFile{}, nil
	}
	var l []json.RawMessage
	if json.Unmarshal(v, &l) != nil {
		return nil, fail(400, "Invalid "+what)
	}
	seen, out := map[string]bool{}, []labExampleFile{}
	for _, x := range l {
		var f labFileIn
		json.Unmarshal(x, &f)
		p, _ := rawValue(f.Path).(string)
		bad := p == "" || len(p) > 300 || strings.HasPrefix(p, "/")
		for _, part := range strings.Split(p, "/") {
			bad = bad || part == "" || part == "." || part == ".."
		}
		if bad {
			return nil, fail(400, "Invalid file path")
		}
		if seen[p] {
			return nil, fail(400, "Duplicate "+what+" path "+p)
		}
		seen[p] = true
		sha, _ := rawValue(f.Sha256).(string)
		media, _ := rawValue(f.MediaType).(string)
		text := rawValue(f.Text)
		if text == nil && labSha.MatchString(sha) && labMedia.MatchString(media) {
			// Binary form: the bytes sit next to the seed (config/lab/examples/<example id>/<path>) and import stores them.
			out = append(out, labExampleFile{Path: p, Sha256: sha, MediaType: media})
			continue
		}
		t, ok := text.(string)
		if !ok {
			return nil, fail(400, "Invalid "+what+" text")
		}
		out = append(out, labExampleFile{Path: p, Text: &t})
	}
	return out, nil
}

// labUseCaseID: a registry entry's id, read when the entry itself does not decode.
type labUseCaseID struct {
	ID any `json:"id"`
}

func useCases() ([]contract.UseCase, error) {
	dir := filepath.Join(labDir(), "use-cases")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := []contract.UseCase{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		var l []json.RawMessage
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		for _, e := range l {
			u, err := validUseCase(e)
			if err != nil {
				var id labUseCaseID
				json.Unmarshal(e, &id)
				return nil, fmt.Errorf("%s: %v: %w", n, id.ID, err)
			}
			out = append(out, u)
		}
	}
	ids := map[string]bool{}
	for _, e := range out {
		ids[e.Id] = true
	}
	for _, e := range out {
		for _, t := range e.MapsTo {
			if !fleetUC.MatchString(t) || !ids[t] {
				return nil, fmt.Errorf("use case %v: maps_to %v is not an existing fleet:UCn entry", e.Id, t)
			}
		}
	}
	return out, nil
}

var fleetUC = regexp.MustCompile(`^fleet:UC[0-9]+$`)

// validUseCase checks one registry entry against contracts/lab-registry.schema.json (UseCase).
func validUseCase(e json.RawMessage) (contract.UseCase, error) {
	var u contract.UseCase
	dec := json.NewDecoder(bytes.NewReader(e))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return u, err
	}
	if u.Id == "" {
		return u, contract.Missing("id")
	}
	return u, u.Validate()
}

// labExampleIn: an example as a request gives it; each field is checked for its type.
type labExampleIn struct {
	ID         json.RawMessage `json:"id"`
	UseCaseID  json.RawMessage `json:"use_case_id"`
	Version    json.RawMessage `json:"version"`
	Origin     json.RawMessage `json:"origin"`
	Fixture    json.RawMessage `json:"fixture"`
	Checks     json.RawMessage `json:"checks"`
	Split      json.RawMessage `json:"split"`
	Prompt     json.RawMessage `json:"prompt"`
	Inputs     json.RawMessage `json:"inputs"`
	References json.RawMessage `json:"references"`
}

// parseExample validates one example into its canonical shape.
func parseExample(v json.RawMessage, ids map[string]bool) (labExample, error) {
	var e labExampleIn
	if _, ok := labRawObject(v); !ok {
		return labExample{}, fail(400, "Expected an example object")
	}
	json.Unmarshal(v, &e)
	id, err := labID(rawValue(e.ID), "example id")
	if err != nil {
		return labExample{}, err
	}
	uc, err := labStr(rawValue(e.UseCaseID), "use_case_id", 200)
	if err != nil {
		return labExample{}, err
	}
	if !ids[uc] {
		return labExample{}, fail(400, "Unknown use case "+uc)
	}
	version, ok := intOK(rawValue(e.Version))
	if !ok || version < 1 {
		return labExample{}, fail(400, "Invalid version")
	}
	origin, _ := rawValue(e.Origin).(string)
	if origin != "synthetic" && !labOrigin.MatchString(origin) {
		return labExample{}, fail(400, "Invalid origin")
	}
	var fixture *labFixture
	if _, ok := labRawObject(e.Fixture); ok {
		var f struct {
			Repo   json.RawMessage `json:"repo"`
			Commit json.RawMessage `json:"commit"`
		}
		json.Unmarshal(e.Fixture, &f)
		repo, err := labID(rawValue(f.Repo), "fixture repo")
		if err != nil {
			return labExample{}, err
		}
		c, _ := rawValue(f.Commit).(string)
		if !labCommit.MatchString(c) {
			return labExample{}, fail(400, "Invalid fixture commit")
		}
		fixture = &labFixture{Repo: repo, Commit: c}
	} else if rawValue(e.Fixture) != nil {
		return labExample{}, fail(400, "Invalid fixture repo")
	}
	var cl []json.RawMessage
	if !rawArray(e.Checks) || json.Unmarshal(e.Checks, &cl) != nil {
		return labExample{}, fail(400, "Invalid checks")
	}
	checks, seen := []labCheck{}, map[string]bool{}
	for _, x := range cl {
		var c struct {
			ID  json.RawMessage `json:"id"`
			Run json.RawMessage `json:"run"`
		}
		json.Unmarshal(x, &c)
		cid, err := labID(rawValue(c.ID), "check id")
		if err != nil {
			return labExample{}, err
		}
		run, err := labStr(rawValue(c.Run), "check command", 10000)
		if err != nil {
			return labExample{}, err
		}
		if seen[cid] {
			return labExample{}, fail(400, "Duplicate check id")
		}
		seen[cid] = true
		checks = append(checks, labCheck{ID: cid, Run: run})
	}
	split, _ := rawValue(e.Split).(string)
	if split != "tune" && split != "held_out" {
		return labExample{}, fail(400, "Invalid split")
	}
	prompt, err := labStr(rawValue(e.Prompt), "prompt", 100000)
	if err != nil {
		return labExample{}, err
	}
	inputs, err := labFiles(e.Inputs, "input")
	if err != nil {
		return labExample{}, err
	}
	refs, err := labFiles(e.References, "reference")
	if err != nil {
		return labExample{}, err
	}
	return labExample{ID: id, UseCaseID: uc, Version: version, Origin: origin, Fixture: fixture, Prompt: prompt, Inputs: inputs, References: refs, Checks: checks, Split: split}, nil
}

// labExampleAdded: one imported example (lab_examples_add).
type labExampleAdded struct {
	ArtifactID string `json:"artifactId"`
	ID         string `json:"id"`
	Version    int64  `json:"version"`
	Hash       string `json:"hash"`
	Created    bool   `json:"created"`
}

// Import: an identical example (same id, version and content) returns the stored artifact; other content under the same
// id and version is refused. A refused batch stores nothing.
func labExamplesAdd(q *Request, r *contract.LabExamplesAddRequest) (contract.LabExamplesAddReply, error) {
	var out contract.LabExamplesAddReply
	var list []json.RawMessage
	json.Unmarshal(r.Examples, &list)
	if len(list) == 0 {
		return out, fail(400, "Expected examples")
	}
	var projectID *string
	if r.ProjectId != nil {
		p, err := labID(*r.ProjectId, "projectId")
		if err != nil {
			return out, err
		}
		projectID = &p
	}
	ucs, err := useCases()
	if err != nil {
		return out, err
	}
	ids := map[string]bool{}
	for _, u := range ucs {
		ids[u.Id] = true
	}
	type plan struct {
		ex    labExample
		bytes []byte
		name  string
		hash  string
		old   *db.LabExampleByNameRow
	}
	existing := func(name string) (*db.LabExampleByNameRow, error) {
		r, err := q.db().LabExampleByName(q.ctx(), db.LabExampleByNameParams{Name: name, Project: deref(projectID)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &r, nil
	}
	planned := []plan{}
	for _, x := range list {
		ex, err := parseExample(x, ids)
		if err != nil {
			return out, err
		}
		bytes, _ := json.MarshalIndent(ex, "", " ")
		bytes = append(bytes, '\n')
		name := "examples/" + exampleRef(ex.ID, ex.Version) + ".json"
		hash := sha256hex(bytes)
		pid := deref(projectID)
		if pid == "" {
			pid = "_"
		}
		n, err := q.db().LabPurgedCount(q.ctx(), db.LabPurgedCountParams{ProjectID: pid, ContentHash: hash})
		if err != nil {
			return out, err
		}
		if n > 0 {
			return out, fail(409, name+": content was purged")
		}
		old, err := existing(name)
		if err != nil {
			return out, err
		}
		if old != nil && old.ContentHash != hash {
			return out, fail(409, name+" exists with other content; raise version")
		}
		if err := storeExampleFiles(q, projectID, ex, false); err != nil {
			return out, err
		}
		planned = append(planned, plan{ex, bytes, name, hash, old})
	}
	approved := r.Approved != nil && *r.Approved
	// Fix: approval lets the coordinator run the example's check commands, so only the owner approves at import.
	if approved && q.person != nil && !fullAccess(q.person) {
		return out, fail(403, "Only the owner approves examples")
	}
	added := []labExampleAdded{}
	for _, p := range planned {
		// Binary files are written only once the whole batch passed, so a refused batch stores nothing.
		if err := storeExampleFiles(q, projectID, p.ex, true); err != nil {
			return out, err
		}
		old := p.old
		if old == nil {
			h, storeID, err := artifactWriteObject(q, optText(projectID), "lab", p.bytes, "application/json")
			if err != nil {
				return out, err
			}
			id := newID()
			var at *time.Time
			if approved {
				now := time.Now()
				at = &now
			}
			rs, err := q.db().LabExampleInsert(q.ctx(), db.LabExampleInsertParams{ID: id, ProjectID: projectID, ContentHash: h, Size: int64(len(p.bytes)), Name: p.name, ApprovedAt: at, UseCaseID: p.ex.UseCaseID})
			if err != nil {
				return out, err
			}
			if len(rs) > 0 {
				if err := artifactCopyInsertVerified(q.ctx(), q.db(), id, storeID, projectID); err != nil {
					return out, err
				}
				added = append(added, labExampleAdded{ArtifactID: id, ID: p.ex.ID, Version: p.ex.Version, Hash: p.hash, Created: true})
				continue
			}
			if old, err = existing(p.name); err != nil || old == nil {
				return out, fail(500, "Example row missing after conflict")
			}
			if old.ContentHash != p.hash {
				return out, fail(409, p.name+" exists with other content; raise version")
			}
		}
		if approved && old.ApprovedAt == nil {
			if err := q.db().LabExampleApprove(q.ctx(), old.ID); err != nil {
				return out, err
			}
		}
		added = append(added, labExampleAdded{ArtifactID: old.ID, ID: p.ex.ID, Version: p.ex.Version, Hash: p.hash, Created: false})
	}
	out.Examples = []map[string]json.RawMessage{}
	for _, a := range added {
		out.Examples = append(out.Examples, rawObjectOr(jsonBytes(a)))
	}
	return out, nil
}

// labExampleState: an example row's state.
func labExampleState(approvedAt *time.Time) string {
	if approvedAt != nil {
		return "approved"
	}
	return "proposed"
}

func labExamplesList(q *Request, state *contract.LabExampleState) (contract.LabExamplesListReply, error) {
	out := contract.LabExamplesListReply{Examples: []contract.LabExampleEntry{}}
	var approved *bool
	if state != nil {
		switch *state {
		case "approved", "proposed":
			a := *state == "approved"
			approved = &a
		default:
			return out, fail(400, "Invalid state")
		}
	}
	rs, err := q.db().LabExamplesList(q.ctx(), approved)
	if err != nil {
		return out, err
	}
	for _, r := range rs {
		hash := r.ContentHash
		uc := deref(r.UseCaseID) // recorded on the row (migration 102); read from the content until the backfill did
		if r.UseCaseID == nil {
			uc = cachedExampleUseCaseID(q, r.ID)
		}
		var useCaseID *string
		if uc != "" {
			useCaseID = &uc
		}
		out.Examples = append(out.Examples, contract.LabExampleEntry{ArtifactId: r.ID, ProjectId: r.ProjectID, Name: r.Name, Hash: &hash, State: labExampleState(r.ApprovedAt),
			UseCaseId: useCaseID, ApprovedAt: jsonBytes(r.ApprovedAt), CreatedAt: jsonBytes(r.CreatedAt)})
	}
	return out, nil
}

// labStoredExample: a stored example artifact (labExampleRead): its row, its bytes and its content.
type labStoredExample struct {
	reply   contract.LabExampleGetReply
	state   string
	example labExample
}

// labExampleRead: the example artifact id.
func labExampleRead(q *Request, id string) (labStoredExample, error) {
	r, err := q.db().LabExample(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return labStoredExample{}, fail(404, "Unknown example")
	}
	if err != nil {
		return labStoredExample{}, err
	}
	b, err := artifactBytes(withTx(q), q.app, id, 8<<20)
	if err != nil {
		return labStoredExample{}, err
	}
	var ex labExample
	if _, ok := labRawObject(b); !ok || json.Unmarshal(b, &ex) != nil {
		return labStoredExample{}, fail(500, "Invalid example artifact")
	}
	st := labExampleState(r.ApprovedAt)
	return labStoredExample{reply: contract.LabExampleGetReply{ArtifactId: id, State: jsonBytes(st), Example: b, Hash: r.ContentHash, ProjectId: r.ProjectID}, state: st, example: ex}, nil
}

// expNote: an append-only note, or an amendment that changes one record field and keeps its old and new value.
func expNote(q *Request, r *contract.LabExperimentNoteRequest) (labNote, error) {
	e, err := expLockedRow(q, r.ExperimentId)
	if err != nil {
		return labNote{}, err
	}
	text := r.Text
	if strings.TrimSpace(text) == "" {
		return labNote{}, fail(400, "A note needs text")
	}
	kind, field := string(r.Kind), deref(r.Field)
	var old, value any
	switch kind {
	case "note":
		if r.Field != nil || r.Value != nil {
			return labNote{}, fail(400, "A note has no field or value")
		}
	case "amendment":
		col, ok := recordColumns[field]
		if !ok {
			return labNote{}, fail(400, "An amendment changes one of "+strings.Join(recordFields, ", "))
		}
		v := deref(r.Value)
		if (field == "goal" || field == "hypothesis") && strings.TrimSpace(v) == "" {
			return labNote{}, fail(400, field+" cannot be cleared")
		}
		var evaluators []labEvaluator
		json.Unmarshal(e.Evaluators, &evaluators)
		if field == "primaryEvaluator" && v != "" && !slices.ContainsFunc(evaluators, func(x labEvaluator) bool { return x.name() == v }) {
			return labNote{}, fail(400, "primaryEvaluator names none of the evaluators")
		}
		old, value = labOpt(expRecordField(e, col)), labOpt(r.Value)
		if err := q.db().LabExperimentRecordSet(q.ctx(), db.LabExperimentRecordSetParams{ID: e.ID, Field: col, Value: strPtr(v)}); err != nil {
			return labNote{}, err
		}
	default:
		return labNote{}, fail(400, "kind is amendment or note")
	}
	n := labNote{ID: newID(), ExperimentID: e.ID, Kind: kind, Field: strPtr(field), Old: old, New: value, Text: text, Author: actor(q, "operator")}
	if err := q.db().LabNoteInsert(q.ctx(), db.LabNoteInsertParams{ID: n.ID, ExperimentID: e.ID, ProjectID: e.ProjectID, Kind: kind, Field: n.Field,
		Old: jsonBytes(old), New: jsonBytes(value), Text: text, Author: n.Author}); err != nil {
		return labNote{}, err
	}
	// A note changes the experiment as its readers see it: touch the row so its change reaches the feed.
	return n, q.db().LabExperimentTouch(q.ctx(), e.ID)
}

// labNote: an experiment note or amendment as recorded.
type labNote struct {
	ID           string  `json:"id"`
	ExperimentID string  `json:"experimentId"`
	Kind         string  `json:"kind"`
	Field        *string `json:"field"`
	Old          any     `json:"old"`
	New          any     `json:"new"`
	Text         string  `json:"text"`
	Author       string  `json:"author"`
}

// expRecordField: one record column of an experiment row.
func expRecordField(e db.Experiment, col string) *string {
	switch col {
	case "goal":
		return e.Goal
	case "hypothesis":
		return e.Hypothesis
	case "decision_rule":
		return e.DecisionRule
	case "lab_group":
		return e.LabGroup
	case "primary_evaluator":
		return e.PrimaryEvaluator
	}
	return nil
}

// expDecide: a person confirms or overrides the decision of a stopped or done experiment; an override needs a reason and
// is an amendment.
func expDecide(q *Request, r *contract.LabExperimentDecideRequest) (labExperimentView, error) {
	e, err := expLockedRow(q, r.ExperimentId)
	if err != nil {
		return labExperimentView{}, err
	}
	if e.State != "stopped" && e.State != "done" {
		return labExperimentView{}, fail(409, "An experiment is decided once it is stopped or done")
	}
	d, reason := r.Decision, strings.TrimSpace(deref(r.Reason))
	label, adopt := strings.CutPrefix(d, "adopt ")
	var variants []labVariant
	json.Unmarshal(e.Variants, &variants)
	if adopt && !slices.ContainsFunc(variants, func(v labVariant) bool { return v.Label == label }) {
		return labExperimentView{}, fail(400, "Unknown variant "+label)
	}
	if !adopt && d != "keep baseline" && d != "inconclusive" {
		return labExperimentView{}, fail(400, "decision is adopt <variant>, keep baseline or inconclusive")
	}
	prev := deref(e.Decision)
	if prev == d {
		return expGet(q, e.ID) // already decided so: nothing changes, no second record
	}
	if prev != "" && prev != "pending" && prev != d {
		if reason == "" {
			return labExperimentView{}, fail(400, "Overriding a decision needs a reason")
		}
		field := "decision"
		if err := q.db().LabNoteInsert(q.ctx(), db.LabNoteInsertParams{ID: newID(), ExperimentID: e.ID, ProjectID: e.ProjectID, Kind: "amendment", Field: &field,
			Old: jsonBytes(prev), New: jsonBytes(d), Text: reason, Author: actor(q, "operator")}); err != nil {
			return labExperimentView{}, err
		}
	}
	if err := q.db().LabExperimentDecide(q.ctx(), db.LabExperimentDecideParams{ID: e.ID, Decision: &d, DecisionReason: strPtr(reason), DecidedBy: ptr(actor(q, "operator"))}); err != nil {
		return labExperimentView{}, err
	}
	return expGet(q, e.ID)
}

type labFile struct {
	path  string
	bytes []byte
}

// labCanonicalArtifact: the artifact binding of a trial's canonical result.
type labCanonicalArtifact struct {
	Artifact *struct {
		ArtifactID string `json:"artifactId"`
		Sha256     string `json:"sha256"`
	} `json:"artifact"`
}

// trialFiles: example inputs, then each step's files at the top level (later steps win).
func trialFiles(q *Request, trial *labRun, example labExample) ([]labFile, error) {
	order, m := []string{}, map[string][]byte{}
	set := func(p string, b []byte) {
		if _, ok := m[p]; !ok {
			order = append(order, p)
		}
		m[p] = b
	}
	for _, f := range example.Inputs {
		b, err := labFileBytes(q, trial.ProjectID, f)
		if err != nil {
			return nil, err
		}
		set(f.Path, b)
	}
	outs, err := trial.run.outputFiles(q, -1)
	if err != nil {
		return nil, err
	}
	for _, o := range outs {
		_, rel, _ := strings.Cut(o.Path, "/")
		if rel == "" || rel == "output.json" || pipelineReservedInput(rel) {
			continue
		}
		b, err := artifactBytes(withTx(q), q.app, o.ArtifactID, 64<<20)
		if err != nil {
			return nil, err
		}
		set(rel, b)
	}
	if _, ok := labRawObject(trial.CanonicalResult); ok {
		var result labCanonicalArtifact
		json.Unmarshal(trial.CanonicalResult, &result)
		var aid, sha string
		if result.Artifact != nil {
			aid, sha = result.Artifact.ArtifactID, result.Artifact.Sha256
		}
		b, err := artifactBytes(withTx(q), q.app, aid, 64<<20)
		if err != nil || sha256hex(b) != sha {
			return nil, fail(409, "Canonical trial result bytes unavailable")
		}
		set("output.json", b)
	}
	out := []labFile{}
	for _, p := range order {
		out = append(out, labFile{p, m[p]})
	}
	return out, nil
}

// labEvidence: the evaluator's view of a trial's plan and evidence files (labSelectedEvidence); fields in canonical
// (sorted) key order, as trial.json holds them.
type labEvidence struct {
	ConsumerStepID string             `json:"consumerStepId"`
	Entries        []labEvidenceEntry `json:"entries"`
	Inputs         []labEvidenceInput `json:"inputs"`
	Schema         string             `json:"schema"`
}

// labEvidenceEntry: one plan entry of the trial; fields in canonical key order.
type labEvidenceEntry struct {
	Activation     contract.PipelineActivation    `json:"activation"`
	API            *labEvidenceAPI                `json:"api,omitempty"`
	DefinitionPins json.RawMessage                `json:"definitionPins,omitempty"`
	ExecutionRef   *contract.PipelineExecutionRef `json:"executionRef"`
	ExecutionState string                         `json:"executionState"`
	ID             string                         `json:"id"`
	Observation    string                         `json:"observation"`
}

// labEvidenceAPI: an api step's call; fields in canonical key order.
type labEvidenceAPI struct {
	Arguments  json.RawMessage `json:"arguments"`
	Connection json.RawMessage `json:"connection"`
	Mode       string          `json:"mode"`
	Output     json.RawMessage `json:"output"`
	Tool       json.RawMessage `json:"tool"`
}

// labEvidenceInput: one evidence file and its producer; fields in canonical key order.
type labEvidenceInput struct {
	ArtifactID          string  `json:"artifactId"`
	EvidencePath        string  `json:"evidencePath"`
	Path                string  `json:"path"`
	ProducerExecutionID *string `json:"producerExecutionId"`
	ProducerRunID       *string `json:"producerRunId"`
	ProducerStepID      string  `json:"producerStepId"`
	Sha256              string  `json:"sha256"`
}

// labDefinitionSteps: the steps of a run's frozen definition as the Lab reads them.
type labDefinitionSteps struct {
	ID    string `json:"id"`
	Steps []struct {
		Kind       string          `json:"kind"`
		Connection json.RawMessage `json:"connection"`
		Tool       json.RawMessage `json:"tool"`
		Mode       string          `json:"mode"`
		Output     json.RawMessage `json:"output"`
		Command    json.RawMessage `json:"command"`
		TimeoutMs  json.RawMessage `json:"timeout_ms"`
	} `json:"steps"`
}

// labSelectedEvidence is evaluator-only; never stage this snapshot for producers/judges.
func labSelectedEvidence(q *Request, trial *labRun) (labEvidence, []labFile, error) {
	out := labEvidence{Schema: "fleet.pipeline-control.v1", ConsumerStepID: "final", Entries: []labEvidenceEntry{}, Inputs: []labEvidenceInput{}}
	files := []labFile{}
	var def labDefinitionSteps
	json.Unmarshal(trial.Definition, &def)
	entries := trial.run.entries()
	for i := range entries {
		entry := trial.run.entry(i)
		var step labRunStep
		if i < len(trial.Steps) {
			step = trial.Steps[i]
		}
		item := labEvidenceEntry{ID: entry.Id, Activation: entry.Activation, ExecutionRef: entry.ExecutionRef, ExecutionState: step.State, Observation: firstString(step.Observation, "confirmed")}
		if i < len(def.Steps) && def.Steps[i].Kind == "api" {
			d := def.Steps[i]
			item.API = &labEvidenceAPI{Connection: d.Connection, Tool: d.Tool, Arguments: step.Arguments, Mode: firstString(d.Mode, "sync"), Output: d.Output}
		}
		if native := step.ExecutionID; native != "" {
			pins, err := q.db().LabExecutionDefinitions(q.ctx(), db.LabExecutionDefinitionsParams{ID: native, ProjectID: trial.ProjectID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, nil, err
			}
			if err == nil {
				item.DefinitionPins = jsonBytes(labJSON(pins))
			}
		}
		out.Entries = append(out.Entries, item)
	}
	outputs, err := trial.run.outputFiles(q, -1)
	if err != nil {
		return out, nil, err
	}
	for _, output := range outputs {
		if !strings.HasSuffix(output.Path, ".json") {
			continue
		}
		artifact, err := artifactRow(withTx(q), q.app, output.ArtifactID)
		if err != nil {
			return out, nil, err
		}
		if artifact.State != "stored" || !same(artifact.ProjectID, trial.ProjectID) {
			return out, nil, fail(409, "Unavailable evaluator evidence")
		}
		bytes, err := artifactBytes(withTx(q), q.app, output.ArtifactID, 64<<20)
		if err != nil {
			return out, nil, err
		}
		if sha256hex(bytes) != artifact.ContentHash {
			return out, nil, fail(409, "Evaluator evidence hash differs")
		}
		producer, _, _ := strings.Cut(output.Path, "/")
		var entry *contract.PipelinePlanEntry
		for i := range entries {
			if e := trial.run.entry(i); e.Id == producer {
				entry = e
				break
			}
		}
		if entry == nil {
			return out, nil, fail(409, "Evaluator producer unavailable")
		}
		path := "__fleet_evaluation/evidence/" + output.Path
		in := labEvidenceInput{Path: output.Path, EvidencePath: path, ArtifactID: output.ArtifactID, Sha256: artifact.ContentHash, ProducerStepID: producer}
		if ref := entry.ExecutionRef; ref != nil {
			in.ProducerRunID, in.ProducerExecutionID = &ref.SourceRunId, &ref.ExecutionId
		}
		out.Inputs = append(out.Inputs, in)
		files = append(files, labFile{path, bytes})
	}
	return out, files, nil
}

// labExampleAsk: a drafted example's rating question data.
type labExampleAsk struct {
	ExampleArtifactID string `json:"exampleArtifactId"`
}

// importDraft: one examples/*.json file of a completed draft-examples run becomes a proposed example with one pass_fail
// rating question; the owner's pass approves it (pipelineImportDrafts, after the run's end).
func importDraft(q *Request, run *runRecord, ex any) error {
	r := labRunFacts(run)
	approved := false
	res, err := labExamplesAdd(q, &contract.LabExamplesAddRequest{Examples: jsonBytes([]any{ex}), ProjectId: r.ProjectID, Approved: &approved})
	if err != nil {
		return err
	}
	var st labExampleAdded
	json.Unmarshal(jsonBytes(res.Examples[0]), &st)
	aid := st.ArtifactID
	if n, err := q.db().LabExampleRatingCount(q.ctx(), aid); err != nil || n > 0 {
		return err
	}
	got, err := labExampleRead(q, aid)
	if err != nil {
		return err
	}
	lines := []string{}
	for _, c := range got.example.Checks {
		lines = append(lines, fmt.Sprintf("%v: %v", c.ID, c.Run))
	}
	_, err = askRow(q, "rating", fmt.Sprintf("Approve drafted example %s? Approving lets the coordinator run its checks:\n%s", exampleRef(st.ID, st.Version), strings.Join(lines, "\n")),
		optText(r.ProjectID), labRatingAsk{Form: "pass_fail", ExampleArtifactID: aid, Seed: aid}, "lab", labExampleAsk{ExampleArtifactID: aid})
	return err
}
func labRegistry() (contract.LabRegistryReply, error) {
	out := contract.LabRegistryReply{UseCases: []contract.LabUseCaseEntry{}}
	ucs, err := useCases()
	if err != nil {
		return out, err
	}
	for _, e := range ucs {
		status := "current"
		if e.Status != nil {
			status = string(*e.Status)
		}
		checks := []json.RawMessage{}
		for _, c := range e.Checks {
			checks = append(checks, jsonBytes(c))
		}
		out.UseCases = append(out.UseCases, contract.LabUseCaseEntry{Id: e.Id, Goal: e.Goal, Source: e.Source, SourceStatus: string(e.SourceStatus), LabFit: string(e.LabFit),
			Maturity: e.Maturity, Checks: checks, Status: status, MapsTo: listOrEmpty(e.MapsTo)})
	}
	return out, nil
}
