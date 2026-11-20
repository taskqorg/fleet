// Lab (docs/designs/fleet-lab.md): examples, experiments, scores and ratings.
// An example is a lab-scope artifact with role 'example'. An experiment is pinned approved examples x variants x repeats of
// one v2 pipeline; a trial is a pipeline_run with experiment_id, variant and request_id; a judge run has variant null.
// labWorkStep (the work kit's lab queue, pipeline_work.go) is the one function that moves an experiment, under the
// experiment's scope lock.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var (
	labIdent     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
	labIdent200  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	labOrigin    = regexp.MustCompile(`^(task|export):[A-Za-z0-9._-]{1,80}$`) // export: a project export row (contracts/lab-export.schema.json)
	labCommit    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	labLabel     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,59}$`)
	draftExample = regexp.MustCompile(`/examples/[^/]+\.json$`)
	labOverrides = []string{"profile", "model", "skills", "prompt", "web_route", "account", "host", "arguments"}
	// labVariantKeys: a Lab variant's keys; bands steer its typed evaluator, never a run, so runs take labOverrides only.
	labVariantKeys = append(append([]string{}, labOverrides...), "bands")
)

func labStr(v any, what string, max int) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > max {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func labID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func expID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent200.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

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
		if err := q.db().LabArtifactCopyVerified(q.ctx(), db.LabArtifactCopyVerifiedParams{ArtifactID: aid, StoreID: storeID, ProjectID: projectID}); err != nil {
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
			rs, err := q.db().LabExampleInsert(q.ctx(), db.LabExampleInsertParams{ID: id, ProjectID: projectID, ContentHash: h, Size: int64(len(p.bytes)), Name: p.name, ApprovedAt: at})
			if err != nil {
				return out, err
			}
			if len(rs) > 0 {
				if err := q.db().LabArtifactCopyVerified(q.ctx(), db.LabArtifactCopyVerifiedParams{ArtifactID: id, StoreID: storeID, ProjectID: projectID}); err != nil {
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
		out.Examples = append(out.Examples, contract.LabExampleEntry{ArtifactId: r.ID, ProjectId: r.ProjectID, Name: r.Name, Hash: &hash, State: labExampleState(r.ApprovedAt),
			ApprovedAt: jsonBytes(r.ApprovedAt), CreatedAt: jsonBytes(r.CreatedAt)})
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

// labOpt: an optional column as a map value: nil when absent.
func labOpt[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// ---- scores ----

// labShuffle: seeded shuffle (mulberry32 seeded from sha256(seed)), the same order as the TypeScript one.
func labShuffle(items []any, seed string) []any {
	h := sha256.Sum256([]byte(seed))
	a := binary.LittleEndian.Uint32(h[:4])
	rnd := func() float64 {
		a += 0x6D2B79F5
		t := (a ^ a>>15) * (1 | a)
		t = (t + (t^t>>7)*(61|t)) ^ t
		return float64(t^t>>14) / 4294967296
	}
	out := append([]any{}, items...)
	for i := len(out) - 1; i > 0; i-- {
		j := int(math.Floor(rnd() * float64(i+1)))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func checkValue(form string, v any, trials []any) error {
	ok := false
	switch form {
	case "pass_fail":
		_, ok = v.(bool)
	case "scale":
		n, isInt := intOK(v)
		ok = isInt && n >= 1 && n <= 5
	case "pick_best":
		for _, t := range trials {
			ok = ok || t == v
		}
	case "metric":
		f, isNum := numOK(v)
		ok = isNum && !math.IsInf(f, 0) && !math.IsNaN(f)
	case "typed":
		m, isObject := labRawObject(rawOf(v)) // exact keys, as the value's own object
		_, isP := numOK(rawValue(m["p"]))
		answer, label := rawValue(m["answer"]), rawValue(m["label"])
		ok = isObject && (isP && (answer == "yes" || answer == "no" || answer == "abstain") && (label == "yes" || label == "no") || answer == "unscored" && rawValue(m["reason"]) != nil)
	}
	if !ok {
		return fail(400, "Invalid "+form+" value")
	}
	return nil
}

func parseRating(form, answer string, trials []any) (any, any, error) {
	head, rest, _ := strings.Cut(answer, ":")
	var note any
	if n := strings.TrimSpace(rest); n != "" {
		note = n
	}
	h := strings.TrimSpace(head)
	switch form {
	case "pass_fail":
		if h != "pass" && h != "fail" {
			return nil, nil, fail(400, "A pass_fail answer is pass or fail")
		}
		return h == "pass", note, nil
	case "scale":
		n, err := strconv.ParseFloat(h, 64)
		if err != nil {
			return nil, nil, fail(400, "Invalid scale value")
		}
		return n, note, checkValue("scale", n, nil)
	case "pick_best":
		v := strings.TrimSpace(answer)
		return v, nil, checkValue("pick_best", v, trials)
	}
	return nil, nil, fail(400, "Owner ratings are pass_fail, pick_best or scale")
}

// labCheckRun: a script check run of one trial (runChecks), the assessments it captured, then stored and linked
// (materializeLabAssessments), and what they were computed from (labWorkStep).
type labCheckRun struct {
	Passed             int                    `json:"passed"`
	Total              int                    `json:"total"`
	Failed             []string               `json:"failed"`
	Assessments        []labAssessmentCapture `json:"assessments,omitempty"`
	ProjectAssessments []labAssessmentRef     `json:"projectAssessments,omitempty"`
	AssessmentSnapshot *labAssessmentSnap     `json:"assessmentSnapshot,omitempty"`
}

// runChecks: each check (a shell command, exit 0 = pass) in a fresh directory holding the files, FLEET_OUTPUT_DIR set to it.
// Cancelling ctx kills the running check and returns ctx's error: the trial stays unscored, never failed.
func runChecks(ctx context.Context, checks []labCheck, files []labFile) (*labCheckRun, error) {
	dir, err := os.MkdirTemp("", "fleet-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	seenFiles := map[string]bool{}
	for _, f := range files {
		if !pipelineInputPath(f.path) || seenFiles[f.path] {
			return nil, fail(400, "Invalid evaluator input path")
		}
		seenFiles[f.path] = true
		p := filepath.Join(dir, f.path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, f.bytes, 0o644); err != nil {
			return nil, err
		}
	}
	out := &labCheckRun{Total: len(checks), Failed: []string{}, Assessments: []labAssessmentCapture{}}
	for _, c := range checks {
		// Never attribute a previous check's file to the current check.
		if err := os.RemoveAll(filepath.Join(dir, "evaluation")); err != nil {
			return nil, err
		}
		cmd := osexec.CommandContext(ctx, "sh", "-c", c.Run)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "FLEET_OUTPUT_DIR=" + dir}
		// On Unix the check runs in its own process group; cancelling, and finishing, kills the whole group, so a child the shell
		// left behind (`sleep 1000 &`) never outlives the check or its deleted directory.
		ownGroup(cmd)
		err := cmd.Run()
		killGroup(cmd)
		assessment, captureErr := captureLabAssessment(dir)
		if assessment != nil {
			assessment.CheckID = c.ID
			out.Assessments = append(out.Assessments, *assessment)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // capture precedes cleanup; cancelled work remains unscored
		}
		if err != nil || captureErr != nil {
			out.Failed = append(out.Failed, c.ID)
		}
	}
	out.Passed = len(checks) - len(out.Failed)
	return out, nil
}

// labJSON: a JSON column's value decoded; SQL NULL is nil.
func labJSON(b []byte) any {
	if b == nil {
		return nil
	}
	var v any
	json.Unmarshal(b, &v)
	return v
}

// labIDStrings: trial IDs as strings (a non-string is "").
func labIDStrings(ids []any) []string {
	out := []string{}
	for _, id := range ids {
		s, _ := id.(string)
		out = append(out, s)
	}
	return out
}

// scoreView: a score row as the Lab reads and shows it.
func scoreView(r db.Score) labScore {
	trials := append([]string{}, r.TrialIds...)
	return labScore{ID: r.ID, ExperimentID: r.ExperimentID, TrialIDs: trials, Evaluator: r.Evaluator, EvaluatorVersion: r.EvaluatorVersion, Form: r.Form,
		Value: r.Value, SourceExecutionID: r.SourceExecutionID, QuestionID: r.QuestionID, Rationale: r.Rationale, ReplacesScoreID: r.ReplacesScoreID, CreatedAt: r.CreatedAt}
}

func scoresCurrent(q *Request, expID string) ([]labScore, error) {
	rs, err := q.db().LabScoresCurrent(q.ctx(), expID)
	out := []labScore{}
	for _, r := range rs {
		out = append(out, scoreView(r))
	}
	return out, err
}

// labAgreement: how often a human rating matches the judges' (agreementBase), overall and per judge version.
type labAgreement struct {
	Compared  int               `json:"compared"`
	Matched   int               `json:"matched"`
	Percent   *int              `json:"percent"`
	ByVersion []labAgreementRow `json:"byVersion,omitempty"`
}

// labAgreementRow: the agreement with one judge version.
type labAgreementRow struct {
	Compared int    `json:"compared"`
	Matched  int    `json:"matched"`
	Percent  *int   `json:"percent"`
	Version  string `json:"version"`
}

func agreementBase(scores []labScore) labAgreement {
	compared, matched := 0, 0
	var judges []labScore
	for _, s := range scores {
		if strings.HasPrefix(s.Evaluator, "judge:") {
			judges = append(judges, s)
		}
	}
	judgeOf := func(form string, trial string) *labScore {
		for i, j := range judges {
			if j.Form == form && j.first() == trial {
				return &judges[i]
			}
		}
		return nil
	}
	for _, o := range scores {
		if !strings.HasPrefix(o.Evaluator, "human:") {
			continue
		}
		switch o.Form {
		case "pass_fail":
			if j := judgeOf("pass_fail", o.first()); j != nil {
				compared++
				if j.value() == o.value() {
					matched++
				}
			}
		case "pick_best":
			ts := o.TrialIDs
			paired := false
			for _, j := range judges {
				if j.Form == "pick_best" && sameTrialSet(ts, j.TrialIDs) {
					compared++
					if j.value() == o.value() {
						matched++
					}
					paired = true
					break
				}
			}
			if paired {
				continue
			}
			vals, ok := []float64{}, true
			for _, t := range ts {
				j := judgeOf("scale", t)
				if j == nil {
					ok = false
					break
				}
				v, _ := numOK(j.value())
				vals = append(vals, v)
			}
			if !ok {
				continue
			}
			best := math.Inf(-1)
			for _, v := range vals {
				best = math.Max(best, v)
			}
			top := []string{}
			for i, v := range vals {
				if v == best {
					top = append(top, ts[i])
				}
			}
			if len(top) != 1 {
				continue
			}
			compared++
			if o.value() == any(top[0]) {
				matched++
			}
		}
	}
	var pct *int
	if compared > 0 {
		p := int(math.Round(100 * float64(matched) / float64(compared)))
		pct = &p
	}
	return labAgreement{Compared: compared, Matched: matched, Percent: pct}
}

// scoreNumber: a one-trial score as a number (pass is 1, fail 0; metric and scale as given).
func scoreNumber(sc labScore) float64 {
	if sc.Form == "typed" { // correct confident answer 1; a wrong one or an abstention 0
		if v := sc.typed(); (v.Answer == "yes" || v.Answer == "no") && v.Answer == v.Label {
			return 1
		}
		return 0
	}
	if sc.Form == "pass_fail" {
		if rawValue(sc.Value) == true {
			return 1
		}
		return 0
	}
	v, _ := numOK(rawValue(sc.Value))
	return v
}

func summarize(scores []labScore, trials []string) map[string]labSummary {
	in := map[string]bool{}
	for _, t := range trials {
		in[t] = true
	}
	type acc struct {
		form   string
		n      int
		values []float64
	}
	m, order := map[string]*acc{}, []string{}
	for _, sc := range scores {
		mine := false
		for _, t := range sc.TrialIDs {
			mine = mine || in[t]
		}
		if !mine || sc.unscored() {
			continue
		}
		k := sc.Evaluator
		e := m[k]
		if e == nil {
			e = &acc{form: sc.Form}
			m[k] = e
			order = append(order, k)
		}
		e.n++
		if sc.Form == "pick_best" {
			if in[sc.text()] {
				e.values = append(e.values, 1)
			}
		} else {
			e.values = append(e.values, scoreNumber(sc))
		}
	}
	out := map[string]labSummary{}
	for _, k := range order {
		e := m[k]
		r := labSummary{Form: e.form, N: e.n}
		sum := 0.0
		for _, v := range e.values {
			sum += v
		}
		switch e.form {
		case "pick_best":
			wins := len(e.values)
			r.Wins = &wins
		case "pass_fail":
			r.Passed = &sum
		default:
			if len(e.values) > 0 {
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, v := range e.values {
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
				mean := sum / float64(len(e.values))
				r.Mean, r.Min, r.Max = &mean, &lo, &hi
			}
		}
		out[k] = r
	}
	return out
}

// labSummary: one evaluator's scores of a set of trials (summarize).
type labSummary struct {
	Form   string   `json:"form"`
	N      int      `json:"n"`
	Wins   *int     `json:"wins,omitempty"`
	Passed *float64 `json:"passed,omitempty"`
	Mean   *float64 `json:"mean,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// ---- experiments ----

func cellKey(ex, variant string, repeat int64) string {
	return fmt.Sprintf("%v|%v|%d", ex, variant, repeat)
}

func expRow(q *Request, id string) (*labExperiment, error) {
	e, err := q.db().LabExperiment(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	return expOf(e)
}

// labTimeText: a timestamp as its JSON text (RFC 3339), as a reply shows it.
func labTimeText(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

// expView: the experiment's record as the Lab shows it.
func expView(e *labExperiment) (contract.LabExperimentRecord, error) {
	r := contract.LabExperimentRecord{Id: e.ID, ProjectId: e.ProjectID, Kind: e.Kind, PipelineId: e.PipelineID, Repeats: float64(e.Repeats), PrimaryEvaluator: e.PrimaryEvaluator,
		Strategy: e.Strategy, State: e.State, StopReason: e.StopReason, CreatedAt: *labTimeText(&e.CreatedAt), Goal: e.Goal, Hypothesis: e.Hypothesis, DecisionRule: e.DecisionRule,
		LabGroup: e.LabGroup, Decision: e.Decision, DecisionReason: e.DecisionReason, DecidedBy: e.DecidedBy, DecidedAt: labTimeText(e.DecidedAt), LaunchedAt: labTimeText(e.LaunchedAt),
		RequestedBy: e.RequestedBy, RequesterRemovedAt: labTimeText(e.RequesterRemovedAt), Evaluators: []json.RawMessage{}}
	if r.Strategy == nil {
		r.Strategy = json.RawMessage("null")
	}
	for _, ev := range e.Evaluators {
		r.Evaluators = append(r.Evaluators, jsonBytes(ev))
	}
	var err error
	if r.Examples, err = asT[[]contract.LabExample](listOrEmpty(e.Examples)); err != nil {
		return r, err
	}
	if r.Variants, err = asT[[]contract.LabVariant](listOrEmpty(e.Variants)); err != nil {
		return r, err
	}
	r.Budgets, err = asT[[]contract.LabBudget](listOrEmpty(e.Budgets))
	return r, err
}

// listOrEmpty: a list, empty when it is nil (a reply shows [] for none).
func listOrEmpty[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// recordFields: the experiment record fields of a create request, frozen at launch (fleet-lab.md, Experiment record).
var recordFields = []string{"goal", "hypothesis", "decisionRule", "labGroup", "primaryEvaluator"}

var recordColumns = map[string]string{"goal": "goal", "hypothesis": "hypothesis", "decisionRule": "decision_rule", "labGroup": "lab_group", "primaryEvaluator": "primary_evaluator"}

// expLocked: the experiment row, locked for this transaction (a record change reads and writes it once).
func expLocked(q *Request, id string) (*labExperiment, error) {
	e, err := q.db().LabExperimentForUpdate(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	return expOf(e)
}

// expLaunch: draft to running, refused without a goal and a hypothesis; the record is frozen from here on.
func expLaunch(q *Request, id string) error {
	e, err := q.db().LabExperiment(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "Unknown experiment")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(deref(e.Goal)) == "" || strings.TrimSpace(deref(e.Hypothesis)) == "" {
		return fail(400, "Launch needs the experiment's goal and hypothesis")
	}
	return expSetState(q, id, []string{"draft"}, "running", nil) // the experiment_record trigger stamps launched_at
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

// expLockedRow: the experiment row, locked for this transaction.
func expLockedRow(q *Request, id string) (db.Experiment, error) {
	e, err := q.db().LabExperimentForUpdate(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, fail(404, "Unknown experiment")
	}
	return e, err
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

// labExperimentView: an experiment as lab_experiment_get shows it (expGet).
type labExperimentView = contract.LabExperimentGetReply

// labDecode: a request value decoded; exact keeps numbers as their text (json.Number), as a stored definition keeps them.
func labDecode(raw json.RawMessage, exact bool) any {
	if exact {
		return decodeNumbers(string(raw))
	}
	return rawValue(raw)
}

// labOwnerOverride: overrides naming a host, account or profile, which only the owner sets.
func labOwnerOverride(q *Request, o map[string]json.RawMessage) bool {
	return q.person != nil && !fullAccess(q.person) && (rawSet(o, "host") || rawSet(o, "account") || rawSet(o, "profile"))
}

// labVariantIn: a variant as a create request gives it.
type labVariantIn struct {
	Label      json.RawMessage `json:"label"`
	PipelineID json.RawMessage `json:"pipelineId"`
	Overrides  json.RawMessage `json:"overrides"`
}

// labEvaluatorIn: an evaluator as a create request gives it.
type labEvaluatorIn struct {
	Kind      json.RawMessage `json:"kind"`
	Ref       json.RawMessage `json:"ref"`
	Form      json.RawMessage `json:"form"`
	Overrides json.RawMessage `json:"overrides"`
}

// labBudgetIn: a budget as a create request gives it.
type labBudgetIn struct {
	Unit   json.RawMessage `json:"unit"`
	Limit  json.RawMessage `json:"limit"`
	Action json.RawMessage `json:"action"`
}

// labList: a request list, empty when it is absent or not a list.
func labList(raw json.RawMessage) []json.RawMessage {
	var l []json.RawMessage
	json.Unmarshal(raw, &l)
	return l
}

// labText: a request value when it is text, else "".
func labText(raw json.RawMessage) string {
	s, _ := rawValue(raw).(string)
	return s
}

// expCreate: a draft experiment from a create request; exact keeps the request's numbers as their text (the variants'
// overrides of a catalogue method, lab_compare_methods).
func expCreate(q *Request, r *contract.LabExperimentCreateRequest, exact bool) (labExperimentView, error) {
	var none labExperimentView
	// Serialize creator validation with the existing people projection/removal transaction.
	if err := acquireTxLock(q, GLOBAL); err != nil {
		return none, err
	}
	if q.person == nil {
		return none, fail(403, "Lab creation needs an authenticated requester")
	}
	if err := refreshRequestAccess(q); err != nil {
		return none, err
	}
	// The declared auth again, under the lock: a grant removed meanwhile refuses the creation.
	if err := declaredAuth(q, "lab_experiment_create"); err != nil {
		return none, err
	}

	record := map[string]json.RawMessage{"goal": jsonBytes(r.Goal), "hypothesis": jsonBytes(r.Hypothesis), "decisionRule": r.DecisionRule, "labGroup": r.LabGroup,
		"primaryEvaluator": r.PrimaryEvaluator}
	for _, k := range recordFields {
		if v := rawValue(record[k]); v != nil {
			if _, ok := v.(string); !ok {
				return none, fail(400, k+" must be text")
			}
		}
	}
	kind := deref(r.Kind)
	if kind != "single" && kind != "ab" && kind != "loop" {
		return none, fail(400, "Invalid kind")
	}
	if r.Strategy != nil && kind != "loop" {
		return none, fail(400, "strategy is for loop experiments")
	}
	projectID, err := expID(optText(r.ProjectId), "projectId")
	if err != nil {
		return none, err
	}
	pipelineID, err := expID(optText(r.PipelineId), "pipelineId")
	if err != nil {
		return none, err
	}
	if _, err := loadPipelineDef(q, pipelineID+".json"); err != nil {
		return none, err
	}
	if !projectKnown(q, projectID) {
		return none, fail(400, "Unknown project "+projectID)
	}
	exl := labList(r.Examples)
	if len(exl) == 0 {
		return none, fail(400, "Expected examples (approved example artifact IDs)")
	}
	examples, seen := []labPin{}, map[string]bool{}
	for _, x := range exl {
		id, err := expID(rawValue(x), "example artifact")
		if err != nil {
			return none, err
		}
		ex, err := labExampleRead(q, id)
		if he, ok := err.(*HTTPError); ok && he.Code == 404 {
			return none, fail(400, "Unknown example "+id)
		} else if err != nil {
			return none, err
		}
		if ex.state != "approved" {
			return none, fail(409, "Example "+id+" is not approved")
		}
		hash, err := q.db().LabArtifactHash(q.ctx(), id)
		if err != nil {
			return none, err
		}
		pin := exampleRef(ex.example.ID, ex.example.Version)
		if seen[pin] {
			return none, fail(400, "Duplicate example")
		}
		seen[pin] = true
		examples = append(examples, labPin{ID: pin, ArtifactID: id, Hash: hash, Split: ex.example.Split})
	}
	vl := labList(r.Variants)
	if len(vl) == 0 {
		return none, fail(400, "Expected variants")
	}
	variants, labels := []labVariant{}, map[string]bool{}
	for _, x := range vl {
		var v labVariantIn
		json.Unmarshal(x, &v)
		label := labText(v.Label)
		if !labLabel.MatchString(label) {
			return none, fail(400, "Invalid variant label")
		}
		raw := v.Overrides
		if rawValue(raw) == nil {
			raw = json.RawMessage("{}")
		}
		o, ok := labRawObject(raw)
		if !ok || !rawKeysOnly(o, labVariantKeys...) {
			return none, fail(400, "Variant overrides are "+strings.Join(labVariantKeys, ", "))
		}
		if labOwnerOverride(q, o) {
			return none, fail(403, "Only the owner overrides host, account or profile")
		}
		if w, has := o["web_route"]; has && rawValue(w) != "connections" && rawValue(w) != "builtin" {
			return none, fail(400, "web_route must be connections or builtin")
		}
		if args, has := o["arguments"]; has {
			m, ok := labRawObject(args)
			for _, step := range m {
				if _, isObject := labRawObject(step); !isObject {
					ok = false
				}
			}
			if !ok {
				return none, fail(400, "arguments must be {<step id>: {<argument>: value}}")
			}
		}
		if bands, has := o["bands"]; has {
			if _, _, err := bandsOf(rawValue(bands)); err != nil {
				return none, err
			}
		}
		if labels[label] {
			return none, fail(400, "Duplicate variant label")
		}
		labels[label] = true
		variantPipeline := pipelineID
		if rawValue(v.PipelineID) != nil {
			variantPipeline, err = expID(rawValue(v.PipelineID), "variant pipelineId")
			if err != nil {
				return none, err
			}
			if _, err := loadPipelineDef(q, variantPipeline+".json"); err != nil {
				return none, err
			}
		}
		overrides := labDecode(raw, exact)
		variants = append(variants, labVariant{Label: label, PipelineID: variantPipeline, Overrides: overrides, ConfigRef: labConfigDigestOf(variantPipeline, overrides)})
	}
	if kind == "single" && len(variants) != 1 {
		return none, fail(400, "A single experiment has one variant")
	}
	if kind == "ab" && len(variants) < 2 {
		return none, fail(400, "An ab experiment has two or more variants")
	}
	repeats := int64(1)
	if rawValue(r.Repeats) != nil {
		n, ok := intOK(rawValue(r.Repeats))
		if !ok || n < 1 {
			return none, fail(400, "Invalid repeats")
		}
		repeats = n
	}
	evaluators := []labEvaluator{}
	names := map[string]bool{}
	for _, x := range labList(r.Evaluators) {
		var e labEvaluatorIn
		json.Unmarshal(x, &e)
		k, f, ref := labText(e.Kind), labText(e.Form), rawValue(e.Ref)
		switch k {
		case "script":
			if ref != "example.checks" || f != "pass_fail" && f != "metric" {
				return none, fail(400, "A script evaluator is {kind: script, ref: example.checks, form: pass_fail|metric}")
			}
		case "judge":
			r, err := expID(ref, "judge pipeline")
			if err != nil {
				return none, err
			}
			if _, err := loadPipelineDef(q, r+".json"); err != nil {
				return none, err
			}
			if f != "pass_fail" && f != "scale" && f != "pick_best" {
				return none, fail(400, "A judge form is pass_fail, scale or pick_best")
			}
		case "typed":
			if ref != "labels" || f != "typed" {
				return none, fail(400, "A typed evaluator is {kind: typed, ref: labels, form: typed}: the trial's p through the variant's bands against the example's label")
			}
		case "human":
			if ref != "owner" || f != "pass_fail" && f != "pick_best" && f != "scale" {
				return none, fail(400, "A human evaluator is {kind: human, ref: owner, form: pass_fail|pick_best|scale}")
			}
		default:
			return none, fail(400, "Evaluator kind is script, typed, judge or human")
		}
		ev := labEvaluator{Kind: k, Ref: labText(e.Ref), Form: f}
		if len(e.Overrides) > 0 {
			ov, ok := labRawObject(e.Overrides)
			if k != "judge" || !ok || !rawKeysOnly(ov, labOverrides...) {
				return none, fail(400, "Judge overrides use pipeline override keys")
			}
			if labOwnerOverride(q, ov) {
				return none, fail(403, "Only the owner overrides host, account or profile")
			}
			ev.Overrides = labDecode(e.Overrides, exact)
		}
		names[ev.name()] = true
		evaluators = append(evaluators, ev)
	}
	primary := labText(r.PrimaryEvaluator)
	if rawValue(r.PrimaryEvaluator) != nil && !names[primary] {
		return none, fail(400, "primaryEvaluator names none of the evaluators")
	}
	var strategy any
	if kind == "loop" {
		if primary == "" || strings.HasSuffix(primary, ":pick_best") {
			return none, fail(400, "Loop primaryEvaluator must name a scalar evaluator")
		}
		st, err := loopStrategy(q, r.Strategy)
		if err != nil {
			return none, err
		}
		strategy = st
	}
	budgets := []labBudget{}
	for _, x := range labList(r.Budgets) {
		var bu labBudgetIn
		json.Unmarshal(x, &bu)
		u := labText(bu.Unit)
		if u != "trials" && u != "wall_ms" && u != "calls" && u != "credits" {
			return none, fail(400, "Budget unit is trials, wall_ms, calls, credits")
		}
		l, ok := numOK(rawValue(bu.Limit))
		if !ok || !(l > 0) || math.IsInf(l, 0) {
			return none, fail(400, "Invalid budget limit")
		}
		act := labText(bu.Action)
		if rawValue(bu.Action) == nil {
			act = "pause"
		}
		if act != "warn" && act != "pause" && act != "stop" {
			return none, fail(400, "Budget action is warn, pause or stop")
		}
		budgets = append(budgets, labBudget{Unit: u, Limit: l, Action: act})
	}
	if repeats > math.MaxInt32 {
		return none, fail(400, "Invalid repeats")
	}
	text := func(k string) *string {
		if s, ok := rawValue(record[k]).(string); ok {
			return &s
		}
		return nil
	}
	id := newID()
	if err := q.db().LabExperimentInsert(q.ctx(), db.LabExperimentInsertParams{ID: id, ProjectID: projectID, Kind: kind, PipelineID: pipelineID, Examples: jsonBytes(examples),
		Variants: jsonBytes(variants), Repeats: int32(repeats), Evaluators: jsonBytes(evaluators), PrimaryEvaluator: text("primaryEvaluator"), Budgets: jsonBytes(budgets),
		Goal: text("goal"), Hypothesis: text("hypothesis"), DecisionRule: text("decisionRule"), LabGroup: text("labGroup"), Strategy: jsonBytes(strategy),
		RequestedBy: &q.person.Email}); err != nil {
		return none, err
	}
	return expGet(q, id)
}

func expSpend(q *Request, id string) (contract.LabSpend, error) {
	run, err := q.db().LabTrialSpend(q.ctx(), &id)
	if err != nil {
		return contract.LabSpend{}, err
	}
	tool, err := q.db().LabToolSpend(q.ctx(), &id)
	if err != nil {
		return contract.LabSpend{}, err
	}
	basis := "measured"
	if tool.Unmeasured > 0 {
		basis = "partial"
	}
	unmeasured := float64(tool.Unmeasured)
	return contract.LabSpend{Trials: contract.LabMeasure{Value: float64(run.Trials), Basis: "measured"}, WallMs: contract.LabMeasure{Value: math.Round(run.WallMs), Basis: "measured"},
		Calls: contract.LabMeasure{Value: float64(tool.Calls), Basis: "measured"}, Credits: contract.LabMeasure{Value: tool.Credits, Basis: basis, Unmeasured: &unmeasured}}, nil
}

// spendOf: the spend in a budget's unit.
func spendOf(s contract.LabSpend, unit string) float64 {
	switch unit {
	case "trials":
		return s.Trials.Value
	case "wall_ms":
		return s.WallMs.Value
	case "calls":
		return s.Calls.Value
	case "credits":
		return s.Credits.Value
	}
	return 0
}

// labRun: a Lab run (a trial, a judge run or a loop strategy run) as the Lab reads it, decoded once from its record
// (labRunFacts). run is the typed run record the pipelines.go functions take.
type labRun struct {
	run                *runRecord
	ID                 string          `json:"id"`
	Kind               string          `json:"kind"`
	ExperimentID       string          `json:"experimentId"`
	ProjectID          *string         `json:"projectId"`
	State              string          `json:"state"`
	Observation        string          `json:"observation"`
	Cell               *labCell        `json:"cell"`
	Judge              *labJudge       `json:"judge"`
	LoopStrategy       *labLoopMark    `json:"loopStrategy"`
	Variant            *string         `json:"variant"`
	Problem            json.RawMessage `json:"problem"`
	StartedAt          json.RawMessage `json:"startedAt"`
	FinishedAt         json.RawMessage `json:"finishedAt"`
	DurationMs         *float64        `json:"durationMs"`
	CanonicalResult    json.RawMessage `json:"canonicalResult"`
	ProjectAssessments json.RawMessage `json:"projectAssessments"`
	Recovery           *labRecovery    `json:"recovery"`
	Overrides          json.RawMessage `json:"overrides"`
	DefinitionSha256   string          `json:"definitionSha256"`
	Usage              json.RawMessage `json:"usage"`
	Steps              []labRunStep    `json:"steps"`
	Definition         json.RawMessage `json:"definition"`
	Definitions        json.RawMessage `json:"definitions"`
	Example            json.RawMessage `json:"example"`
	ExampleInputs      json.RawMessage `json:"exampleInputs"`
	RerunOf            string          `json:"rerunOf"`
	ProjectSnapshot    json.RawMessage `json:"projectSnapshot"`
}

// labRunStep: a run step as the Lab reads it.
type labRunStep struct {
	ID               string          `json:"id"`
	ExecutionID      string          `json:"executionId"`
	State            string          `json:"state"`
	Observation      string          `json:"observation"`
	Arguments        json.RawMessage `json:"arguments"`
	Output           json.RawMessage `json:"output"`
	OutputArtifactID string          `json:"outputArtifactId"`
}

// labRunFacts: a run record (getRun) as the Lab reads it; nil stays nil.
func labRunFacts(run *runRecord) *labRun {
	if run == nil {
		return nil
	}
	r, _ := asT[labRun](docOf(run)) // values as decoded JSON writes them (object keys sorted), as the trial evidence hashes them
	r.run = run
	return &r
}

// labRunRead: run id (getRun); nil when there is none.
func labRunRead(q *Request, id string) (*labRun, error) {
	run, err := getRun(q, id)
	return labRunFacts(run), err
}

// inputs: the run's example input artifacts.
func (t *labRun) inputs() []labExampleInput {
	var l []labExampleInput
	json.Unmarshal(t.ExampleInputs, &l)
	return l
}

// output: the run's result value (pipelineResultValue), its JSON.
func (t *labRun) output() json.RawMessage { return labNull(t.run.resultValue()) }

// outputObject: the run's result when it is an object, else nil.
func (t *labRun) outputObject() json.RawMessage {
	if _, ok := labRawObject(t.output()); ok {
		return t.output()
	}
	return nil
}

func (t *labRun) terminal() bool    { return executionEnds(t.State) }
func (t *labRun) unconfirmed() bool { return t.Observation == "unconfirmed" }

func expRuns(q *Request, id string) ([]*labRun, error) {
	rs, err := q.db().LabRunIDs(q.ctx(), &id)
	if err != nil {
		return nil, err
	}
	out := []*labRun{}
	for _, r := range rs {
		run, err := getRun(q, r.ID)
		if err != nil {
			return nil, err
		}
		if run != nil {
			t := labRunFacts(run)
			t.Variant, t.Problem = r.Variant, jsonBytes(r.Problem)
			out = append(out, t)
		}
	}
	return out, nil
}

func expTrials(runs []*labRun) []*labRun {
	out := []*labRun{}
	for _, r := range runs {
		if r.Cell != nil {
			out = append(out, r)
		}
	}
	return out
}

// latest attempt per cell, in first-seen order.
func expLatest(trials []*labRun) ([]string, map[string]*labRun) {
	order, m, attempts := []string{}, map[string]*labRun{}, map[string]int64{}
	for _, t := range trials {
		c := t.cell()
		k := c.key()
		oa, ok := attempts[k]
		if !ok {
			order = append(order, k)
		}
		if !ok || c.Attempt > oa {
			m[k], attempts[k] = t, c.Attempt
		}
	}
	return order, m
}

// cell: the run's cell; a run without one (a judge or strategy run) has the zero cell.
func (t *labRun) cell() labCell {
	if t.Cell == nil {
		return labCell{}
	}
	return *t.Cell
}

func comparable(t *labRun) bool { return t.State == "succeeded" && !t.unconfirmed() }

// labRunStart: a Lab run to start (labStartRun): its request, pipeline, example, variant and what the run records
// besides; a suffix retry also reuses the source run's confirmed prefix.
type labRunStart struct {
	RequestID string
	File      string
	Example   any
	Variant   *labVariant
	Extra     labRunExtra
	Reuse     []runStep
	RerunOf   string
}

// labExecutionRow: the execution row of a Lab run, reserved before the run starts (insertExecution).
type labExecutionRow struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	State        string  `json:"state"`
	ProjectID    string  `json:"projectId"`
	ExperimentID string  `json:"experimentId"`
	Variant      *string `json:"variant"`
	RequestID    string  `json:"requestId"`
	ConfigRef    *struct {
		ConfigRef string `json:"config_ref"`
	} `json:"configRef"`
	Data struct {
		Recovery *labRecovery `json:"recovery"`
	} `json:"data"`
}

// labStartRun reserves exactly once; an existing request never reinitializes a run. It is the Lab's one start of a
// pipeline run: the execution row (insertExecution) and the run (pipelineStart) from its options.
func labStartRun(q *Request, exp *labExperiment, s labRunStart) (string, error) {
	requester, err := labRequester(q, exp.ID)
	if err != nil {
		return "", err
	}
	id := newID()
	row := labExecutionRow{ID: id, Kind: "pipeline_run", State: "preparing", ProjectID: exp.ProjectID, ExperimentID: exp.ID, RequestID: s.RequestID}
	row.Data.Recovery = s.Extra.Recovery
	var overrides json.RawMessage
	if v := s.Variant; v != nil {
		if v.Label != "" {
			row.Variant = &v.Label
		}
		row.ConfigRef = &struct {
			ConfigRef string `json:"config_ref"`
		}{v.ConfigRef}
		if v.Overrides != nil {
			overrides = jsonBytes(v.Overrides)
		}
	}
	ok, err := insertExecution(q, docOf(row))
	if err != nil || !ok {
		return "", err
	}
	s.Extra.Priority = "background"
	extra, err := recordOf(s.Extra)
	if err != nil {
		return "", err
	}
	reuse := []json.RawMessage{}
	for _, step := range s.Reuse {
		reuse = append(reuse, jsonBytes(step))
	}
	project := exp.ProjectID
	opts := pipelineStartOptions{ID: id, File: s.File, ProjectID: &project, RequestID: s.RequestID, Example: jsonBytes(s.Example), Overrides: overrides, ExperimentID: exp.ID,
		Variant: deref(row.Variant), RequestedBy: &pipelineRequester{Kind: "lab", ExperimentID: exp.ID, Email: requester.Email}, Extra: extra, Reuse: reuse, RerunOf: s.RerunOf}
	_, err = startRun(q, opts)
	return id, err
}

// labJudgeExample: a judge run's example: the prompt, and the candidate trials of a pairwise judge or the form of a
// one-trial judge.
type labJudgeExample struct {
	Prompt     string   `json:"prompt"`
	Candidates []string `json:"candidates,omitempty"`
	Form       string   `json:"form,omitempty"`
}

// labStrategyExample: a loop strategy run's example: its input files.
type labStrategyExample struct {
	Inputs []labStrategyInput `json:"inputs"`
}

// exampleOf: the pinned example, refused when its content changed.
func exampleOf(q *Request, pin *labPin) (labExample, error) {
	if pin == nil {
		return labExample{}, fail(404, "Unknown example")
	}
	ex, err := labExampleRead(q, pin.ArtifactID)
	if err != nil {
		return labExample{}, err
	}
	hash, _ := q.db().LabArtifactHash(q.ctx(), pin.ArtifactID)
	if hash != pin.Hash {
		return labExample{}, fail(409, fmt.Sprintf("Example %v changed since it was pinned", pin.ID))
	}
	return ex.example, nil
}

func expLaunchTrial(q *Request, exp *labExperiment, pin *labPin, variant *labVariant, repeat, attempt int64) (string, error) {
	iteration := variant.Iteration
	cell := labCell{Iteration: &iteration, Example: pin.ID, Variant: variant.Label, Repeat: repeat, Attempt: attempt}
	full, err := exampleOf(q, pin)
	if err != nil {
		return "", err
	}
	rid := "lab:" + sha256hex(jsonBytes([]any{exp.ID, iteration, pin.ID, variant.Label, repeat, attempt}))
	return labStartRun(q, exp, labRunStart{RequestID: rid, File: variantPipelineID(exp, variant) + ".json", Example: full.runnable(), Variant: variant, Extra: labRunExtra{Cell: &cell}})
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

// expEvaluate: automatic evaluators over completed trials; true when every completed trial is evaluated by each.
func expEvaluate(q *Request, exp *labExperiment, trials, runs []*labRun, current []labScore, launched *[]string, checked map[string]*labCheckRun) (bool, error) {
	_, latest := expLatest(trials)
	complete, typedExamples := true, map[string]labExample{} // each pinned example read once per pass
	for _, e := range exp.Evaluators {
		if e.Kind == "judge" && e.Form == "pick_best" {
			done, err := pairwiseEvaluate(q, exp, e, trials, runs, current, launched)
			if err != nil {
				return false, err
			}
			complete = complete && done
		}
	}
	for _, t := range trials {
		if !comparable(t) {
			continue
		}
		for _, e := range exp.Evaluators {
			if e.Kind == "human" || e.Kind == "judge" && e.Form == "pick_best" {
				continue
			}
			name := e.name()
			has := false
			for _, s := range current {
				has = has || s.Evaluator == name && s.first() == t.ID
			}
			if has {
				continue
			}
			pin := exp.pin(t.cell().Example)
			if e.Kind == "typed" {
				if err := scoreTyped(q, exp, t, pin, name, typedExamples); err != nil {
					return false, err
				}
				continue
			}
			if e.Kind == "script" {
				if l := latest[t.cell().key()]; l == nil || l.ID != t.ID {
					continue // superseded evidence cannot link or obstruct the current attempt
				}
				r := checked[t.ID]
				if r == nil {
					complete = false
					continue
				}
				full, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				if len(r.ProjectAssessments) > 0 {
					if e.Form != "pass_fail" {
						return false, fail(409, "Project assessment contract checks require pass_fail")
					}
					if err := validateLabAssessmentSnapshot(q, t.run, full, r); err != nil {
						return false, err
					}
					if err := linkLabAssessments(q, t.run, r); err != nil {
						return false, err
					}
				}
				var value any = len(r.Failed) == 0
				if e.Form == "metric" {
					value = 0.0
					if r.Total > 0 {
						value = float64(r.Passed) / float64(r.Total)
					}
				}
				rat := fmt.Sprintf("%v of %v checks pass", r.Passed, r.Total)
				if len(r.ProjectAssessments) > 0 {
					rat += "; project assessment contract only, see retained artifacts for quality"
				}
				if len(r.Failed) > 0 {
					rat = "failed: " + strings.Join(r.Failed, ", ")
				}
				if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name,
					EvaluatorVersion: sha256hex(jsonBytes(full.Checks))[:16], Form: e.Form, Value: value, Rationale: &rat}); err != nil {
					return false, err
				}
				continue
			}
			var run *labRun
			for _, r := range runs {
				if j := r.Judge; j != nil && j.Evaluator == name && j.TrialID == t.ID {
					run = r
				}
			}
			if run == nil && exp.State != "running" {
				complete = false
				continue
			}
			if run == nil {
				full, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				files, err := trialFiles(q, t, full)
				if err != nil {
					return false, err
				}
				inputs := []labExampleInput{}
				for _, f := range files {
					// Judges inspect renders and measurements, never the source GLB.
					if strings.HasSuffix(strings.ToLower(f.path), ".glb") {
						continue
					}
					id, err := t.run.saveArtifact(q, t.ID, "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, labExampleInput{ArtifactID: id, Path: "example/trial/" + f.path})
				}
				jid, err := labStartRun(q, exp, labRunStart{RequestID: "lab:" + sha256hex(jsonBytes([]any{exp.ID, "judge", name, t.ID})), File: e.Ref + ".json",
					Example: labJudgeExample{Prompt: full.Prompt, Form: e.Form}, Variant: &labVariant{Overrides: e.Overrides, ConfigRef: sha256hex(jsonBytes(e.Overrides))},
					Extra: labRunExtra{ExampleInputs: jsonBytes(inputs), Judge: &labJudge{Evaluator: name, TrialID: t.ID}}})
				if err != nil {
					return false, err
				}
				if jid != "" {
					*launched = append(*launched, jid)
				}
				complete = false
				continue
			}
			if !run.terminal() {
				complete = false
				continue
			}
			if run.State != "succeeded" {
				continue // a failed judge run counts as evaluated; it has no score
			}
			var last struct {
				Value     json.RawMessage `json:"value"`
				Rationale *string         `json:"rationale"`
			}
			json.Unmarshal(run.outputObject(), &last)
			if checkValue(e.Form, rawValue(last.Value), nil) != nil {
				continue
			}
			ver := "unknown"
			if d := run.DefinitionSha256; len(d) >= 16 {
				ver = sha256hex(jsonBytes([]any{d, e.Overrides}))[:16]
			}
			if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name, EvaluatorVersion: ver,
				Form: e.Form, Value: rawValue(last.Value), SourceExecutionID: &run.ID, Rationale: last.Rationale}); err != nil {
				return false, err
			}
		}
	}
	return complete, nil
}

// labPending: a script check of one trial, read in the step's transaction and run outside it.
type labPending struct {
	checks           []labCheck
	files            []labFile
	trial            *labRun
	evaluator, input string
	snapshot         labAssessmentSnap
}

// labChecked: the script checks' results by trial, or why they could not run.
type labChecked struct {
	checked map[string]*labCheckRun
	err     error
}

// labLaterEvery: how often a running experiment looks again besides its trials' ends (budgets, the requester's access):
// FLEET_PIPELINE_TICK_MS, 0 = only on wakes.
var labLaterEvery = envMs("FLEET_PIPELINE_TICK_MS", 5000)

// labWorkStep moves experiment c.Key (the lab queue's step, in the kit's transaction under GLOBAL and the experiment's
// lock): trials whose script checks are due leave the checks to run outside the transaction (the next step records
// their scores); otherwise the experiment's budgets, evaluations, launches and end are written. A running experiment
// looks again later; any other leaves the queue until a producer enqueues it again.
func labWorkStep(q *Request, now time.Time, c *kitClaim) error {
	id := c.Key
	if err := acquireTxLock(q, GLOBAL, "experiment:"+id); err != nil {
		return err
	}
	checked := map[string]*labCheckRun{}
	if r, ok := c.Result.(labChecked); ok {
		if r.err != nil {
			return r.err
		}
		checked = r.checked
	} else {
		todo, err := labPendingChecks(q, id)
		if err != nil {
			return err
		}
		if len(todo) > 0 {
			a := q.app
			c.Call = func(ctx context.Context) any {
				checked := map[string]*labCheckRun{}
				for id, p := range todo {
					r, err := runChecks(ctx, p.checks, p.files)
					if err == nil {
						err = materializeLabAssessments(ctx, a, p.trial.run, r, p.evaluator, p.input)
					}
					if err != nil {
						return labChecked{err: err}
					}
					snapshot := p.snapshot
					r.AssessmentSnapshot = &snapshot
					checked[id] = r
				}
				return labChecked{checked: checked}
			}
			return nil
		}
	}
	if err := labRecord(q, id, checked); err != nil {
		return err
	}
	state, err := q.db().LabExperimentState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && state != "running" {
		return q.db().LabWorkDelete(q.ctx(), id)
	}
	if err != nil {
		return err
	}
	if _, err := q.db().LabWorkEnsure(q.ctx(), id); err != nil {
		return err
	}
	at := now.Add(100 * 365 * 24 * time.Hour) // only on wakes
	if labLaterEvery > 0 {
		at = time.Now().Add(labLaterEvery)
	}
	return kitSchedule(q, "lab", id, at)
}

// labTrialMeta: what a script check reads of its trial (__fleet_evaluation/trial.json); fields in canonical (sorted)
// key order.
type labTrialMeta struct {
	CanonicalResult json.RawMessage              `json:"canonicalResult"`
	EvaluatorSha256 string                       `json:"evaluatorSha256"`
	Evidence        labEvidence                  `json:"evidence"`
	ExecutionState  string                       `json:"executionState"`
	ExperimentID    string                       `json:"experimentId"`
	Inputs          []labFileBinding             `json:"inputs"`
	Observation     string                       `json:"observation"`
	PlanSummary     contract.PipelinePlanSummary `json:"planSummary"`
	Resources       labTrialResources            `json:"resources"`
	RunID           string                       `json:"runId"`
	Schema          string                       `json:"schema"`
	TrialID         string                       `json:"trialId"`
	VariantID       string                       `json:"variantId"`
}

// labTrialResources: a trial's usage and duration; fields in canonical key order.
type labTrialResources struct {
	DurationMs *float64        `json:"durationMs"`
	Usage      json.RawMessage `json:"usage"`
}

// labPendingChecks: the latest comparable trials of experiment id whose script checks have not scored them, with what
// the checks read.
func labPendingChecks(q *Request, id string) (map[string]labPending, error) {
	todo := map[string]labPending{}
	err := func() error {
		exp, err := expRow(q, id)
		if err != nil || exp.State == "draft" {
			return err
		}
		hasScript := false
		for _, e := range exp.Evaluators {
			hasScript = hasScript || e.Kind == "script"
		}
		if !hasScript {
			return nil
		}
		cur, err := scoresCurrent(q, id)
		if err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		trials := expTrials(runs)
		_, latest := expLatest(trials)
		for _, t := range trials {
			if l := latest[t.cell().key()]; l == nil || l.ID != t.ID {
				continue
			}
			if !comparable(t) {
				continue
			}
			done := false
			for _, s := range cur {
				done = done || strings.HasPrefix(s.Evaluator, "script:") && s.first() == t.ID
			}
			if done {
				continue
			}
			full, err := exampleOf(q, exp.pin(t.cell().Example))
			if err != nil {
				return err
			}
			files, err := trialFiles(q, t, full)
			if err != nil {
				return err
			}
			// Script checks only (never a judge or an agent): the known-good result under references/, for comparison.
			for _, f := range full.References {
				b, err := labFileBytes(q, t.ProjectID, f)
				if err != nil {
					return err
				}
				files = append(files, labFile{"references/" + f.Path, b})
			}
			for _, f := range files {
				first, _, _ := strings.Cut(f.path, "/")
				if strings.EqualFold(first, "__fleet_evaluation") || strings.EqualFold(first, "evaluation") {
					return fail(409, "Evaluator input collides with reserved namespace")
				}
			}
			evidence, evidenceFiles, err := labSelectedEvidence(q, t)
			if err != nil {
				return err
			}
			files = append(files, evidenceFiles...)
			result := t.CanonicalResult
			if result == nil {
				result = json.RawMessage("null")
			}
			metadata := labTrialMeta{Schema: "fleet.lab-trial.v1", EvaluatorSha256: labEvaluatorIdentity(full.Checks, files), TrialID: t.ID, RunID: t.ID, ExperimentID: id,
				VariantID: t.cell().Variant, PlanSummary: planSummary(t.run.entries()), CanonicalResult: result, Evidence: evidence, ExecutionState: t.State,
				Observation: firstString(t.Observation, "confirmed"), Resources: labTrialResources{Usage: t.Usage, DurationMs: t.DurationMs}, Inputs: labFileBindings(files)}
			files = append(files, labFile{"__fleet_evaluation/trial.json", jsonBytes(metadata)})
			evaluator, input := labAssessmentInputs(t.run, full.Checks, files)
			todo[t.ID] = labPending{full.Checks, files, t, evaluator, input, labAssessmentSnapshot(t.run, full)}
		}
		return nil
	}()
	return todo, err
}

// labRecord writes one look at experiment id: its requester, budgets, the evaluations (checked: the script checks just
// run), the trials to launch and its end. A launched trial's run is on the kit from its first write.
func labRecord(q *Request, id string, checked map[string]*labCheckRun) error {
	var launched []string
	exp, err := expRow(q, id)
	if err != nil || exp.State == "draft" {
		return err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return err
	}
	trials := expTrials(runs)
	cur, err := scoresCurrent(q, id)
	if err != nil {
		return err
	}
	if exp.State != "running" {
		_, err := expEvaluate(q, exp, trials, runs, cur, &launched, checked)
		return err
	}
	if _, err := labRequester(q, id); err != nil {
		var reason *codedError
		if errors.As(err, &reason) && strings.HasPrefix(reason.code, "requester:") {
			state := "stopped"
			if reason.code == "requester:access-revoked" {
				state = "paused"
			}
			_, err := experimentTransition(q, id, experimentEvent{To: state, StopReason: &reason.code})
			return err
		}
		return err
	}
	if strings.HasPrefix(deref(exp.StopReason), "requester:") {
		if err := q.db().LabExperimentStopReasonClear(q.ctx(), id); err != nil {
			return err
		}
	}
	order, last := expLatest(trials)
	repeats := exp.Repeats
	total := len(exp.Examples) * len(exp.Variants) * int(repeats)
	missing := total - len(order)
	s, err := expSpend(q, id)
	if err != nil {
		return err
	}
	budgets := []labBudget{}
	if missing > 0 || exp.Kind == "loop" {
		budgets = listOrEmpty(exp.Budgets)
	}
	state, changed := "running", false
	var reason *string
	reached := func(b labBudget) bool {
		return b.FiredAt == nil && spendOf(s, b.Unit) >= b.Limit
	}
	for i := range budgets {
		b := &budgets[i]
		if b.Action == "stop" && reached(*b) {
			at := nowMs()
			r := "budget:" + b.Unit
			b.FiredAt, changed, state, reason = &at, true, "stopped", &r
			break
		}
	}
	if state == "running" {
		for i := range budgets {
			b := &budgets[i]
			if b.Action == "stop" || !reached(*b) {
				continue
			}
			at := nowMs()
			b.FiredAt, changed = &at, true
			if b.Action == "pause" {
				state = "paused"
				if _, err := askRow(q, "operator_decision", fmt.Sprintf("Lab budget reached: %v %v of %v. Running trials finish; resume launches or stop the experiment?", b.Unit, spendOf(s, b.Unit), b.Limit),
					exp.ProjectID, []any{"resume", "stop"}, "lab", docOf(labBudgetAsk{ExperimentID: id, Budget: b.Unit})); err != nil {
					return err
				}
				break
			}
		}
	}
	if changed || state != "running" {
		if err := q.db().LabExperimentBudgetsSet(q.ctx(), db.LabExperimentBudgetsSetParams{ID: id, Budgets: jsonBytes(budgets)}); err != nil {
			return err
		}
		if _, err := experimentTransition(q, id, experimentEvent{To: state, StopReason: reason}); err != nil {
			return err
		}
	}
	evalExp := *exp
	evalExp.State = state
	evaluated, err := expEvaluate(q, &evalExp, trials, runs, cur, &launched, checked)
	if err != nil {
		return err
	}
	if state != "running" {
		return nil
	}
	// A trials budget that has not fired caps this pass's launches, so it is reached exactly.
	room := math.Inf(1)
	for _, b := range budgets {
		if b.Unit == "trials" && b.Action != "warn" && b.FiredAt == nil {
			room = math.Min(room, b.Limit-s.Trials.Value)
		}
	}
launch:
	for i := range exp.Examples {
		for j := range exp.Variants {
			p, v := &exp.Examples[i], &exp.Variants[j]
			for r := int64(0); r < repeats; r++ {
				if _, ok := last[cellKey(p.ID, v.Label, r)]; ok {
					continue
				}
				if room <= 0 {
					break launch
				}
				rid, err := expLaunchTrial(q, exp, p, v, r, 0)
				if err != nil {
					return err
				}
				if rid != "" {
					launched = append(launched, rid)
					room--
				}
			}
		}
	}
	allFinal := true
	for _, k := range order {
		allFinal = allFinal && last[k].terminal()
	}
	if len(launched) == 0 && len(order) == total && allFinal && evaluated {
		if exp.Kind == "loop" {
			// Evaluators may have just inserted the final scores in this transaction.
			fresh, err := scoresCurrent(q, id)
			if err != nil {
				return err
			}
			return loopAdvance(q, exp, trials, runs, fresh, s, &launched)
		}
		complete := "complete"
		_, err = experimentTransition(q, id, experimentEvent{To: "done", From: []string{"running"}, StopReason: &complete})
		return err
	}
	return nil
}

// labBudgetAsk: a budget question's data (labAnswerAfter reads it back as labQuestionData).
type labBudgetAsk struct {
	ExperimentID string `json:"experimentId"`
	Budget       string `json:"budget,omitempty"`
}

// labConflict: a 409 whose body names its reason.
func labConflict(msg, reason string) error {
	return &HTTPError{Code: 409, Msg: msg, Extra: docOf(struct {
		Reason string `json:"reason"`
	}{reason})}
}

func expSetState(q *Request, id string, from []string, to string, reason any) error {
	_, err := experimentTransition(q, id, experimentEvent{To: to, From: from, Strict: true, StopReason: strPtr(reason)})
	return err
}

func labReadTx(q *Request, fn func(q *Request) error) error {
	return q.app.store.Tx(q.ctx(), scopeOf(q.person), false, "", func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		return fn(q)
	})
}

func expGetTx(q *Request, id string) (out labExperimentView, err error) {
	err = labReadTx(q, func(q *Request) error { out, err = expGet(q, id); return err })
	return
}

func expAction(q *Request, id, action string) (labExperimentView, error) {
	var err error
	switch action {
	case "launch":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expLaunch(q, id) })
	case "stop":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expSetState(q, id, []string{"running", "paused"}, "stopped", "owner") })
	case "resume":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expSetState(q, id, []string{"paused"}, "running", nil) })
	default:
		return labExperimentView{}, fail(400, "action is launch, stop or resume")
	}
	if err != nil {
		return labExperimentView{}, err
	}
	if err := labRunNow(q.ctx(), q.app, id); err != nil {
		return labExperimentView{}, err
	}
	out, err := expGetTx(q, id)
	if err != nil || action != "launch" {
		return out, err
	}
	// Drift warns on launch, never refuses (registry source_status of the pinned examples' use cases).
	ucs := map[string]bool{}
	if err := labReadTx(q, func(q *Request) error {
		for _, p := range out.Examples {
			ex, err := labExampleRead(q, deref(p.ArtifactId))
			if err != nil {
				return err
			}
			ucs[ex.example.UseCaseID] = true
		}
		return nil
	}); err != nil {
		return labExperimentView{}, err
	}
	warnings := []string{}
	reg, _ := useCases()
	for _, u := range reg {
		if ucs[u.Id] && u.SourceStatus != "current" {
			warnings = append(warnings, fmt.Sprintf("%v: source %v", u.Id, u.SourceStatus))
		}
	}
	if out.Rest == nil {
		out.Rest = map[string]json.RawMessage{}
	}
	out.Rest["warnings"] = jsonBytes(warnings)
	return out, nil
}

func expCellAction(q *Request, id, cell, action string) (labExperimentView, error) {
	var exp *labExperiment
	var t *labRun
	if err := labReadTx(q, func(q *Request) error {
		var err error
		if exp, err = expRow(q, id); err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		_, last := expLatest(expTrials(runs))
		t = last[cell]
		return nil
	}); err != nil {
		return labExperimentView{}, err
	}
	if t == nil {
		return labExperimentView{}, fail(404, "Unknown cell")
	}
	if action == "cancel" {
		if t.terminal() {
			return labExperimentView{}, fail(409, "Trial already finished")
		}
		own, err := pipelineCancel(q.ctx(), q.app, t.ID)
		if err != nil {
			return labExperimentView{}, err
		}
		if own != "" {
			if err := stopExecution(q.ctx(), q.app, own); err != nil && !isNotFound(err) { // the run is cancelled already
				return labExperimentView{}, err
			}
		}
	} else {
		if !t.terminal() {
			return labExperimentView{}, fail(409, "Trial still running; cancel it first")
		}
		if exp.State == "draft" || exp.State == "stopped" {
			return labExperimentView{}, fail(409, fmt.Sprintf("Experiment is %v", exp.State))
		}
		c := t.cell()
		if err := runTxLocks(q.ctx(), q.app, q, true, []string{GLOBAL, "experiment:" + id}, func(q *Request) error {
			runs, err := expRuns(q, id)
			if err != nil {
				return err
			}
			_, latest := expLatest(expTrials(runs))
			if latest[cell] == nil || latest[cell].ID != t.ID {
				return fail(409, "Cell source changed; read it again")
			}
			if err := recoveryFence(q, latest[cell].run); err != nil {
				return err
			}
			if _, err := experimentTransition(q, id, experimentEvent{To: "running", From: []string{"done"}}); err != nil {
				return err
			}
			pin, variant := exp.pin(c.Example), exp.variant(c.Variant)
			if pin == nil || variant == nil {
				return fail(404, "Unknown example")
			}
			_, err = expLaunchTrial(q, exp, pin, variant, c.Repeat, c.Attempt+1)
			return err
		}); err != nil {
			return labExperimentView{}, err
		}
	}
	if err := labRunNow(q.ctx(), q.app, id); err != nil {
		return labExperimentView{}, err
	}
	return expGetTx(q, id)
}

var methodSlug = regexp.MustCompile(`[^a-z0-9]+`)

// labMethod: a catalogue method as Promote writes it and reads it back (the catalogue's methods).
type labMethod struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Scope            string          `json:"scope"`
	Pipeline         string          `json:"pipeline"`
	Overrides        json.RawMessage `json:"overrides"`
	ConfigRef        string          `json:"configRef,omitempty"`
	SourceExperiment string          `json:"sourceExperiment,omitempty"`
}

// overridesValue: the method's overrides decoded ({} when it has none), as its config digest reads them.
func (m labMethod) overridesValue() any {
	if _, ok := labRawObject(m.Overrides); ok {
		return decodeNumbers(string(m.Overrides))
	}
	return decodeNumbers("{}")
}

// Promote: the variant as a catalogue method, saved as a catalogue revision (source lab) unless a method with its
// config_ref exists; then the project recommendation is set to it (recommend: the recommendation_set operation).
func expPromote(q *Request, id, label string) (contract.LabPromoteReply, error) {
	var out contract.LabPromoteReply
	exp, err := expRow(q, id)
	if err != nil {
		return out, err
	}
	v := exp.variant(label)
	if v == nil {
		return out, fail(400, "Unknown variant")
	}
	identity := firstString(v.CatalogueConfigRef, v.ConfigRef)
	slug := strings.Trim(methodSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	// Bands steer the Lab's typed evaluator; a method run applies no overrides of that kind, so they are left out and named.
	overrides := map[string]json.RawMessage{}
	var note *string
	vo, _ := labRawObject(jsonBytes(v.Overrides))
	for k, x := range vo {
		if k == "bands" {
			n := fmt.Sprintf("bands %s are a Lab scoring setting and do not travel with the method", jsonBytes(rawValue(x)))
			note = &n
			continue
		}
		overrides[k] = jsonBytes(rawValue(x))
	}
	pipeline := variantPipelineID(exp, v)
	method := labMethod{ID: fmt.Sprintf("%v-%s", pipeline, slug), Name: fmt.Sprintf("%v %s", pipeline, label), Scope: exp.ProjectID, Pipeline: pipeline + ".json",
		Overrides: jsonBytes(overrides), ConfigRef: identity, SourceExperiment: id}
	digest := labConfigDigestOf(pipeline, rawValue(jsonBytes(overrides)))
	// A method's config ref is its configRef, else the hash Lab gives a variant (pipeline id and overrides).
	refOf := func(m labMethod) string {
		if m.ConfigRef != "" {
			return m.ConfigRef
		}
		return sha256hex([]byte(canonical(labConfigIdentity{Overrides: m.overridesValue(), Pipeline: strings.TrimSuffix(m.Pipeline, ".json")})))
	}
	var existing json.RawMessage
	base, err := catalogueBase(q, exp.ProjectID)
	if err != nil {
		return out, err
	}
	c, _ := asT[struct {
		Methods []json.RawMessage `json:"methods"`
	}](base)
	for _, raw := range c.Methods {
		var m labMethod
		json.Unmarshal(raw, &m)
		if refOf(m) == identity && labConfigDigestOf(strings.TrimSuffix(m.Pipeline, ".json"), m.overridesValue()) == digest {
			existing = raw
		}
	}
	// A new method is saved as a catalogue revision (source lab) in this transaction, so the recommendation below sees it
	// (fleet-definition-editors.md, Promote); the repository file stays the seed.
	if existing == nil {
		head, err := headOf(q, "catalogue", "catalogue", true)
		if err != nil {
			return out, err
		}
		if head == nil {
			return out, fail(404, "No catalogue definition")
		}
		if st := head.state(); st == contract.DefinitionStateRepositoryChanged || st == contract.DefinitionStateRepositoryDeleted {
			return out, &HTTPError{Code: 409, Msg: "The catalogue's repository file changed: resolve it in the Catalogue editor, then promote", Extra: conflictExtra(contract.ConflictReasonRepositoryChanged, head.Head, head.RepoSha)}
		}
		var content map[string]json.RawMessage // numbers keep their text
		if _, ok := labRawObject(head.Content); !ok || json.Unmarshal(head.Content, &content) != nil {
			return out, fail(500, "Invalid catalogue definition")
		}
		var methods []json.RawMessage
		json.Unmarshal(content["methods"], &methods)
		ids := map[string]bool{}
		for _, m := range methods {
			var x struct {
				ID any `json:"id"`
			}
			json.Unmarshal(m, &x)
			if s, ok := x.ID.(string); ok {
				ids[s] = true
			}
		}
		for base, n := method.ID, 2; ids[method.ID]; n++ { // an id already taken gets a number
			method.ID = fmt.Sprintf("%s-%d", base, n)
		}
		content["methods"] = jsonBytes(append(methods, jsonBytes(method)))
		text, err := canonicalText(jsonBytes(content))
		if err != nil {
			return out, err
		}
		if ps := problems("catalogue", "catalogue", text); len(ps) > 0 {
			return out, fail(400, "The promoted catalogue is invalid: "+ps[0].Message)
		}
		rev, err := appendRevision(q, "catalogue", "catalogue", head.Head, &text, head.RepoSha, "lab", fmt.Sprintf("Promote %s from %s", label, id))
		if err != nil {
			return out, err
		}
		out.Catalogue = &contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: rev.Revision, ContentSha: rev.ContentSha}
		existing = jsonBytes(method)
	}
	var chosen labMethod
	json.Unmarshal(existing, &chosen)
	rec, err := recommend(q, exp.ProjectID, chosen.ID)
	if err != nil {
		return out, err
	}
	m, err := asT[contract.LabPromotedMethod](existing)
	if err != nil {
		return out, err
	}
	out.CataloguePatch, out.Method, out.Recommendation, out.Note = json.RawMessage("null"), m, jsonBytes(rec), note
	return out, nil
}

func expRate(q *Request, r *contract.LabRatingCreateRequest) (labRatingView, error) {
	eid, err := expID(r.ExperimentId, "experimentId")
	if err != nil {
		return labRatingView{}, err
	}
	exp, err := expRow(q, eid)
	if err != nil {
		return labRatingView{}, err
	}
	form := string(r.Form)
	if form != "pass_fail" && form != "pick_best" && form != "scale" {
		return labRatingView{}, fail(400, "form is pass_fail, pick_best or scale")
	}
	if r.TrialIds == nil {
		return labRatingView{}, fail(400, "Expected trialIds")
	}
	ids := []string{}
	for _, x := range r.TrialIds {
		s, err := expID(x, "trial")
		if err != nil {
			return labRatingView{}, err
		}
		ids = append(ids, s)
	}
	if form == "pick_best" && len(ids) < 2 {
		return labRatingView{}, fail(400, "pick_best rates two or more trials")
	}
	if form != "pick_best" && len(ids) != 1 {
		return labRatingView{}, fail(400, form+" rates one trial")
	}
	runs, err := expRuns(q, eid)
	if err != nil {
		return labRatingView{}, err
	}
	trials := expTrials(runs)
	for _, t := range ids {
		var x *labRun
		for _, y := range trials {
			if y.ID == t {
				x = y
			}
		}
		if x == nil {
			return labRatingView{}, fail(400, fmt.Sprint("Unknown trial ", t))
		}
		if !comparable(x) {
			return labRatingView{}, fail(409, fmt.Sprint("Trial ", t, " has no comparable result"))
		}
	}
	text := "Rate this output from 1 to 5"
	if form == "pick_best" {
		text = fmt.Sprintf("Pick the best of %d outputs", len(ids))
	} else if form == "pass_fail" {
		text = "Pass or fail this output"
	}
	qid, err := askRow(q, "rating", text, exp.ProjectID, labRatingAsk{Form: form, ExperimentID: eid, TrialIDs: ids, Seed: newID()}, "operator", docOf(labBudgetAsk{ExperimentID: eid}))
	if err != nil {
		return labRatingView{}, err
	}
	return labRating(q, qid)
}

// labRatingAsk: a rating question's options as expRate writes them (labRatingOptions reads them back).
type labRatingAsk struct {
	Form              string   `json:"form"`
	ExperimentID      string   `json:"experiment_id,omitempty"`
	ExampleArtifactID string   `json:"example_artifact_id,omitempty"`
	TrialIDs          []string `json:"trial_ids,omitempty"`
	Seed              string   `json:"seed"`
}

// labRating: outputs in the seeded order, labelled A, B, ...; never the variant.
func labRating(q *Request, qid string) (labRatingView, error) {
	row, err := q.db().LabRating(q.ctx(), qid)
	if errors.Is(err, pgx.ErrNoRows) {
		return labRatingView{}, fail(404, "Unknown question")
	}
	if err != nil {
		return labRatingView{}, err
	}
	var o labRatingOptions
	json.Unmarshal(row.Options, &o)
	var d labQuestionData
	json.Unmarshal(row.Data, &d)
	var runs []*labRun
	if eid := deref(o.ExperimentID); eid != "" {
		if runs, err = expRuns(q, eid); err != nil {
			return labRatingView{}, err
		}
	}
	items := []labRatingItem{}
	for i, tid := range labShuffle(o.TrialIDs, o.Seed) {
		files := []labExampleInput{}
		for _, t := range runs {
			if t.ID == tid {
				fs, err := t.run.outputFiles(q, -1)
				if err != nil {
					return labRatingView{}, err
				}
				for _, f := range fs {
					files = append(files, labExampleInput{ArtifactID: f.ArtifactID, Path: f.Path})
				}
			}
		}
		items = append(items, labRatingItem{Label: string(rune('A' + i)), TrialID: tid, Files: files})
	}
	return labRatingView{ID: row.ID, Kind: row.Kind, Form: o.Form, ExperimentID: o.ExperimentID, ExampleArtifactID: o.ExampleArtifactID, State: row.State, Answer: row.Answer,
		Text: row.Text, Items: items, Withdrawal: d.Withdrawal}, nil
}

// labRatingOptions: a rating question's options (expRate, importDraft).
type labRatingOptions struct {
	ExperimentID      *string `json:"experiment_id"`
	ExampleArtifactID *string `json:"example_artifact_id"`
	Form              *string `json:"form"`
	TrialIDs          []any   `json:"trial_ids"`
	Seed              string  `json:"seed"`
}

// labQuestionData: the Lab fields of a question's data (a budget ask, a withdrawal).
type labQuestionData struct {
	ExperimentID *string         `json:"experimentId"`
	Budget       json.RawMessage `json:"budget"`
	Withdrawal   json.RawMessage `json:"withdrawal"`
}

// labQuestion: an answered or answering question as pipelines.go hands it over.
type labQuestion struct {
	ID      string           `json:"id"`
	Kind    string           `json:"kind"`
	Answer  string           `json:"answer"`
	Options labRatingOptions `json:"options"`
	Data    labQuestionData  `json:"data"`
}

func (q labQuestion) budget() bool {
	return q.Data.ExperimentID != nil && rawValue(q.Data.Budget) != nil
}

// labRatingView: a rating with its outputs in the seeded order (labRating).
type labRatingView struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	Form              *string         `json:"form"`
	ExperimentID      *string         `json:"experimentId"`
	ExampleArtifactID *string         `json:"exampleArtifactId"`
	State             string          `json:"state"`
	Answer            *string         `json:"answer"`
	Text              string          `json:"text"`
	Items             []labRatingItem `json:"items"`
	Withdrawal        json.RawMessage `json:"withdrawal"`
}

type labRatingItem struct {
	Label   string            `json:"label"`
	TrialID any               `json:"trialId"`
	Files   []labExampleInput `json:"files"`
}

func labRatings(q *Request, experimentID, state *string) (contract.LabRatingsListReply, error) {
	out := contract.LabRatingsListReply{Ratings: []contract.LabRating{}}
	rs, err := q.db().LabRatingIDs(q.ctx(), db.LabRatingIDsParams{ExperimentID: experimentID, State: state})
	if err != nil {
		return out, err
	}
	for _, id := range rs {
		x, err := labRating(q, id)
		if err != nil {
			return out, err
		}
		r, err := asT[contract.LabRating](x)
		if err != nil {
			return out, err
		}
		out.Ratings = append(out.Ratings, r)
	}
	return out, nil
}

// labAnswerCheck and labAnswerAfter: the answer hook for questions_answer (questions_answer calls both inside its transaction:
// check before recording the answer, after once it is recorded). question: the question row (snake_case columns).
func labAnswerCheck(q *Request, question doc, answer string) error {
	x, _ := asT[labQuestion](question)
	// Lab ratings are the owner's (scored as human:owner; a pass on a drafted example lets the coordinator run its checks)
	// and budget asks move the owner's spend: owner only.
	lab := x.Kind == "rating" && (x.Options.ExampleArtifactID != nil || x.Options.ExperimentID != nil) || x.budget()
	if lab && q.person != nil && !fullAccess(q.person) {
		return fail(403, "Only the owner answers Lab ratings and budget questions")
	}
	if x.Kind == "rating" {
		_, _, err := parseRating(deref(x.Options.Form), answer, x.Options.TrialIDs)
		return err
	}
	if x.budget() && answer != "resume" && answer != "stop" {
		return fail(400, "Answer resume or stop")
	}
	return nil
}

// labNoteText: a rating's note (parseRating), when it has one.
func labNoteText(note any) *string {
	if s, ok := note.(string); ok {
		return &s
	}
	return nil
}

func labAnswerAfter(q *Request, question doc) error {
	x, _ := asT[labQuestion](question)
	o := x.Options
	eid := ""
	switch {
	case x.Kind == "rating" && o.ExperimentID != nil:
		v, rat, err := parseRating(deref(o.Form), x.Answer, o.TrialIDs)
		if err != nil {
			return err
		}
		exp, err := q.db().LabExperiment(q.ctx(), *o.ExperimentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Unknown experiment")
		}
		if err != nil {
			return err
		}
		form := deref(o.Form)
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: labIDStrings(o.TrialIDs), Evaluator: "human:owner:" + form,
			EvaluatorVersion: "1", Form: form, Value: v, QuestionID: strPtr(x.ID), Rationale: labNoteText(rat)}); err != nil {
			return err
		}
		eid = exp.ID
	case x.Kind == "rating" && o.ExampleArtifactID != nil:
		if v, _, err := parseRating("pass_fail", x.Answer, nil); err == nil && v == true {
			return q.db().LabExampleApproveOpen(q.ctx(), *o.ExampleArtifactID)
		}
		return nil
	case x.budget():
		to := "stopped"
		if x.Answer == "resume" {
			to = "running"
		}
		var owner *string
		if to == "stopped" {
			owner = ptr("owner")
		}
		if _, err := experimentTransition(q, *x.Data.ExperimentID, experimentEvent{To: to, From: []string{"paused"}, StopReason: owner}); err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		eid = *x.Data.ExperimentID
	default:
		return nil
	}
	return labEnqueue(q, eid)
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
		optText(r.ProjectID), labRatingAsk{Form: "pass_fail", ExampleArtifactID: aid, Seed: aid}, "lab", docOf(labExampleAsk{ExampleArtifactID: aid}))
	return err
}

// labJudgeView: a judge run as the experiment shows it.
type labJudgeView struct {
	ID        string          `json:"id"`
	State     string          `json:"state"`
	Evaluator string          `json:"evaluator"`
	TrialID   *string         `json:"trialId"`
	TrialIDs  []string        `json:"trialIds"`
	GroupID   *string         `json:"groupId"`
	Order     *int64          `json:"order"`
	Output    json.RawMessage `json:"output"`
	Problem   json.RawMessage `json:"problem"`
}

// labNoteView: an experiment note as the experiment shows it.
type labNoteView struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Field  *string         `json:"field"`
	Old    json.RawMessage `json:"old"`
	New    json.RawMessage `json:"new"`
	Text   string          `json:"text"`
	Author string          `json:"author"`
	At     time.Time       `json:"at"`
}

// labOptText: text, nil when empty.
func labOptText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// labNull: raw, null when absent.
func labNull(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return json.RawMessage("null")
	}
	return raw
}

func expGet(q *Request, id string) (labExperimentView, error) {
	var out labExperimentView
	exp, err := expRow(q, id)
	if err != nil {
		return out, err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return out, err
	}
	trials := expTrials(runs)
	cur, err := scoresCurrent(q, id)
	if err != nil {
		return out, err
	}
	s, err := expSpend(q, id)
	if err != nil {
		return out, err
	}
	stepRows, err := q.db().LabStepAgents(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	credits, err := q.db().LabRunCredits(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	// Each trial's provider usage, priced per call (fleet-analytics.md: usage x the latest entry in force at the call).
	usageRows, err := q.db().LabRunUsage(q.ctx(), &id)
	if err != nil {
		return out, err
	}
	costs := map[string][]usage{}
	for _, u := range usageRows {
		var amount *float64
		if u.HasAmount {
			a := u.Amount
			amount = &a
		}
		costs[u.Run] = append(costs[u.Run], usage{u.Subject, u.Unit, amount, u.At.Time})
	}
	prices, err := priceEntries(q)
	if err != nil {
		return out, err
	}
	trialCost := map[string][]contract.CostFigure{}
	for run, us := range costs {
		trialCost[run] = estimateCost(prices, us)
	}
	uniq := func(l []string) []string {
		seen, out := map[string]bool{}, []string{}
		for _, s := range l {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	trialView := func(t *labRun) (contract.LabTrialRecord, error) {
		hosts, harnesses, models := []string{}, []string{}, []string{}
		for _, r := range stepRows {
			if r.Run == "" || r.Run != t.ID {
				continue
			}
			hosts, models = append(hosts, deref(r.HostID)), append(models, deref(r.Model))
			harnesses = append(harnesses, strings.TrimSpace(strings.Join([]string{deref(r.Harness), deref(r.HarnessVersion)}, " ")))
		}
		calls, credit, basis := float64(0), float64(0), "measured"
		for _, c := range credits {
			if c.Run != "" && c.Run == t.ID {
				calls, credit = float64(c.Calls), c.Credits
				if c.Unmeasured > 0 {
					basis = "partial"
				}
			}
		}
		var o struct {
			WebRoute json.RawMessage `json:"web_route"`
		}
		json.Unmarshal(t.Overrides, &o)
		web := o.WebRoute
		if rawValue(web) == nil {
			web = jsonBytes("connections")
		}
		mine := []contract.LabScore{}
		for _, sc := range cur {
			if slices.Contains(sc.TrialIDs, t.ID) {
				v, err := asT[contract.LabScore](sc)
				if err != nil {
					return contract.LabTrialRecord{}, err
				}
				mine = append(mine, v)
			}
		}
		cell, err := asT[contract.LabTrialCell](t.Cell)
		if err != nil {
			return contract.LabTrialRecord{}, err
		}
		v := contract.LabTrialRecord{Id: t.ID, Cell: cell, State: contract.ExecutionState(t.State), Observation: contract.PipelineObservation(t.Observation), Problem: labNull(t.Problem),
			Comparable: comparable(t), StartedAt: labNull(t.StartedAt), FinishedAt: labNull(t.FinishedAt), DurationMs: t.DurationMs,
			Spend: contract.LabTrialSpend{Calls: &calls, Credits: &credit, Basis: &basis}, Cost: append([]contract.CostFigure{}, trialCost[t.ID]...),
			Confounders: contract.LabConfounders{Hosts: uniq(hosts), Harnesses: uniq(harnesses), Models: uniq(models), WebRoute: web}, Scores: mine, PlanSummary: planSummary(t.run.entries())}
		if rawValue(t.CanonicalResult) != nil {
			r, err := asT[contract.PipelineCanonicalResult](t.CanonicalResult)
			if err != nil {
				return v, err
			}
			v.CanonicalResult = &r
		}
		if rawValue(t.ProjectAssessments) != nil {
			if v.ProjectAssessments, err = asT[[]contract.ProjectAssessmentRef](t.ProjectAssessments); err != nil {
				return v, err
			}
		}
		if r := t.Recovery; r != nil {
			source := r.Identity.SourceRunID
			v.Recovery = &contract.LabRecovery{SourceRunId: &source, ReusedStepIds: r.ReusedStepIDs}
		}
		return v, nil
	}
	order, last := expLatest(trials)
	idsOf := func(variant string) []string {
		out := []string{}
		for _, k := range order {
			if t := last[k]; t.cell().Variant == variant && comparable(t) {
				out = append(out, t.ID)
			}
		}
		return out
	}
	summaryOf := func(trials []string) (map[string]contract.LabScoreSummary, error) {
		return asT[map[string]contract.LabScoreSummary](summarize(cur, trials))
	}
	rec, err := expView(exp)
	if err != nil {
		return out, err
	}
	if out, err = asT[labExperimentView](rec); err != nil {
		return out, err
	}
	out.Cells = []contract.LabCell{}
	for _, p := range exp.Examples {
		for _, v := range exp.Variants {
			ts := []*labRun{}
			for _, k := range order {
				if c := last[k].cell(); c.Example == p.ID && c.Variant == v.Label {
					ts = append(ts, last[k])
				}
			}
			sort.SliceStable(ts, func(i, j int) bool { return ts[i].cell().Repeat < ts[j].cell().Repeat })
			keys, comp := []string{}, []string{}
			for _, t := range ts {
				keys = append(keys, cellKey(p.ID, v.Label, t.cell().Repeat))
				if comparable(t) {
					comp = append(comp, t.ID)
				}
			}
			summary, err := summaryOf(comp)
			if err != nil {
				return out, err
			}
			example, variant := p.ID, v.Label
			out.Cells = append(out.Cells, contract.LabCell{Example: &example, Variant: &variant, Trials: keys, Summary: summary})
		}
	}
	out.Variants = []contract.LabVariant{}
	for _, v := range exp.Variants {
		view, err := asT[contract.LabVariant](v)
		if err != nil {
			return out, err
		}
		if view.Summary, err = summaryOf(idsOf(v.Label)); err != nil {
			return out, err
		}
		out.Variants = append(out.Variants, view)
	}
	out.Trials = []contract.LabTrialRecord{}
	for _, t := range trials {
		view, err := trialView(t)
		if err != nil {
			return out, err
		}
		out.Trials = append(out.Trials, view)
	}
	out.Judges = []json.RawMessage{}
	for _, r := range runs {
		if j := r.Judge; j != nil {
			out.Judges = append(out.Judges, jsonBytes(labJudgeView{ID: r.ID, State: r.State, Evaluator: j.Evaluator, TrialID: labOptText(j.TrialID), TrialIDs: j.TrialIDs,
				GroupID: labOptText(j.GroupID), Order: j.Order, Output: labNull(r.outputObject()), Problem: labNull(r.Problem)}))
		}
	}
	if out.Scores, err = asT[[]contract.LabScore](cur); err != nil {
		return out, err
	}
	noteRows, err := q.db().LabNotes(q.ctx(), id)
	if err != nil {
		return out, err
	}
	out.Notes = []json.RawMessage{}
	for _, n := range noteRows {
		out.Notes = append(out.Notes, jsonBytes(labNoteView{ID: n.ID, Kind: n.Kind, Field: n.Field, Old: labNull(n.Old), New: labNull(n.New), Text: n.Text, Author: n.Author, At: n.At}))
	}
	out.Spend = s
	if out.Agreement, err = asT[contract.LabAgreement](agreement(cur)); err != nil {
		return out, err
	}
	out.Pairwise = pairwiseView(runs)
	out.Strategies = []contract.LabStrategyRun{}
	for _, r := range runs {
		if st := r.LoopStrategy; st != nil {
			view := contract.LabStrategyRun{Id: &r.ID, Iteration: jsonBytes(st.Iteration), State: &r.State, Problem: labNull(r.Problem)}
			if o := r.outputObject(); o != nil {
				output, err := asT[contract.LabJudgeOutput](o)
				if err != nil {
					return out, err
				}
				view.Output = &output
			}
			out.Strategies = append(out.Strategies, view)
		}
	}
	if out.Comparison, err = asT[[]contract.LabComparison](expComparison(exp, trials, cur)); err != nil {
		return out, err
	}
	if out.Typed, err = asT[[]contract.LabTypedBlock](expTyped(exp, trials, cur)); err != nil {
		return out, err
	}
	value, note := "background", "No effect until the scheduler exists"
	out.Priority = contract.LabPriority{Value: &value, Note: &note}
	return out, nil
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

// optText: an optional request string as a query argument (nil when absent).
func optText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func labIDArg(s, what string) error {
	if s == "" || len(s) > 300 {
		return fail(400, "Expected "+what)
	}
	return nil
}

// scoreCorrect: a correction keeps the trials, evaluator and form; only a current score can be corrected.
func scoreCorrect(q *Request, r *contract.LabScoreCorrectRequest) (labScore, error) {
	id := r.ScoreId
	old, err := q.db().LabScore(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return labScore{}, fail(404, "Unknown score")
	}
	if err != nil {
		return labScore{}, err
	}
	if old.Form == "typed" {
		return labScore{}, fail(400, "A typed score follows from the trial's p, its bands and the label; correct the example or the bands instead")
	}
	if err := acquireTxLock(q, "experiment:"+old.ExperimentID); err != nil {
		return labScore{}, err
	}
	n, err := q.db().LabScoreReplacedCount(q.ctx(), &id)
	if err != nil {
		return labScore{}, err
	}
	if n > 0 {
		return labScore{}, fail(409, "Score already corrected")
	}
	nid, err := scoreInsert(q, labNewScore{ExperimentID: old.ExperimentID, ProjectID: old.ProjectID, TrialIDs: append([]string{}, old.TrialIds...), Evaluator: old.Evaluator,
		EvaluatorVersion: old.EvaluatorVersion, Form: old.Form, Value: rawValue(r.Value), QuestionID: old.QuestionID, SourceExecutionID: old.SourceExecutionID,
		Rationale: r.Rationale, Replaces: &id})
	if err != nil {
		return labScore{}, err
	}
	s, err := q.db().LabScore(q.ctx(), nid)
	if err != nil {
		return labScore{}, err
	}
	return scoreView(s), nil
}

// labMapReply: a typed reply as an operation's open (map) reply.
func labMapReply(v any, err error) (map[string]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return rawObjectOr(jsonBytes(v)), nil
}

func init() {
	// Lab operations (contracts/ops-lab.schema.json): lab_experiment_action and lab_cell_action are long and open their own
	// transactions; the rest run in runOp's.
	op("lab_compare_methods", func(q *Request, r *contract.LabCompareMethodsRequest) (contract.LabCompareMethodsReply, error) {
		return labCompareMethods(q, r)
	})
	op("lab_examples_add", func(q *Request, r *contract.LabExamplesAddRequest) (contract.LabExamplesAddReply, error) {
		return labExamplesAdd(q, r)
	})
	op("lab_examples_list", func(q *Request, r *contract.LabExamplesListRequest) (contract.LabExamplesListReply, error) {
		return labExamplesList(q, r.State)
	})
	op("lab_example_get", func(q *Request, r *contract.LabExampleGetRequest) (contract.LabExampleGetReply, error) {
		if err := labIDArg(r.ArtifactId, "artifactId"); err != nil {
			return contract.LabExampleGetReply{}, err
		}
		ex, err := labExampleRead(q, r.ArtifactId)
		return ex.reply, err
	})
	op("lab_experiment_create", func(q *Request, r *contract.LabExperimentCreateRequest) (contract.LabExperimentCreateReply, error) {
		return labMapReply(expCreate(q, r, false))
	})
	op("lab_experiment_note", func(q *Request, r *contract.LabExperimentNoteRequest) (contract.LabExperimentNoteReply, error) {
		n, err := expNote(q, r)
		if err != nil {
			return contract.LabExperimentNoteReply{}, err
		}
		return asT[contract.LabExperimentNoteReply](n)
	})
	op("lab_experiment_decide", func(q *Request, r *contract.LabExperimentDecideRequest) (contract.LabExperimentDecideReply, error) {
		return labMapReply(expDecide(q, r))
	})
	op("lab_experiments_list", func(q *Request, _ *contract.LabExperimentsListRequest) (contract.LabExperimentsListReply, error) {
		out := contract.LabExperimentsListReply{Experiments: []contract.LabExperimentRecord{}}
		rs, err := q.db().LabExperiments(q.ctx())
		if err != nil {
			return out, err
		}
		for _, r := range rs {
			e, err := expOf(r)
			if err != nil {
				return out, err
			}
			v, err := expView(e)
			if err != nil {
				return out, err
			}
			out.Experiments = append(out.Experiments, v)
		}
		return out, nil
	})
	op("lab_experiment_get", func(q *Request, r *contract.LabExperimentGetRequest) (contract.LabExperimentGetReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabExperimentGetReply{}, err
		}
		return expGet(q, r.ExperimentId)
	})
	op("lab_experiment_action", func(q *Request, r *contract.LabExperimentActionRequest) (contract.LabExperimentActionReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabExperimentActionReply{}, err
		}
		return labMapReply(expAction(q, r.ExperimentId, string(r.Action)))
	})
	op("lab_cell_action", func(q *Request, r *contract.LabCellActionRequest) (contract.LabCellActionReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return nil, err
		}
		if err := labIDArg(r.Cell, "cell"); err != nil {
			return nil, err
		}
		if r.Action == contract.LabCellActionRetrySuffix {
			return labMapReply(expRetrySuffix(q, r.ExperimentId, r.Cell, deref(r.SourceRunId), deref(r.RequestId)))
		}
		return labMapReply(expCellAction(q, r.ExperimentId, r.Cell, string(r.Action)))
	})
	op("lab_promote", func(q *Request, r *contract.LabPromoteRequest) (contract.LabPromoteReply, error) {
		if err := labIDArg(r.ExperimentId, "experimentId"); err != nil {
			return contract.LabPromoteReply{}, err
		}
		if err := labIDArg(r.Variant, "variant"); err != nil {
			return contract.LabPromoteReply{}, err
		}
		return expPromote(q, r.ExperimentId, r.Variant)
	})
	op("lab_rating_create", func(q *Request, r *contract.LabRatingCreateRequest) (contract.LabRatingCreateReply, error) {
		v, err := expRate(q, r)
		if err != nil {
			return contract.LabRatingCreateReply{}, err
		}
		return asT[contract.LabRatingCreateReply](v)
	})
	op("lab_rating_withdraw", func(q *Request, r *contract.LabRatingWithdrawRequest) (contract.LabRatingWithdrawReply, error) {
		return labMapReply(labRatingWithdraw(q, r.QuestionId, r.Reason))
	})
	op("lab_ratings_list", func(q *Request, r *contract.LabRatingsListRequest) (contract.LabRatingsListReply, error) {
		return labRatings(q, r.ExperimentId, r.State)
	})
	op("lab_rating_get", func(q *Request, r *contract.LabRatingGetRequest) (contract.LabRatingGetReply, error) {
		if err := labIDArg(r.QuestionId, "questionId"); err != nil {
			return contract.LabRatingGetReply{}, err
		}
		return labMapReply(labRating(q, r.QuestionId))
	})
	op("lab_score_correct", func(q *Request, r *contract.LabScoreCorrectRequest) (contract.LabScoreCorrectReply, error) {
		if err := labIDArg(r.ScoreId, "scoreId"); err != nil {
			return contract.LabScoreCorrectReply{}, err
		}
		return labMapReply(scoreCorrect(q, r))
	})
	op("lab_registry", func(q *Request, _ *contract.LabRegistryRequest) (contract.LabRegistryReply, error) {
		return labRegistry()
	})

}

// expComparison: each non-baseline variant against the baseline (the first variant) on the primary evaluator, through
// the one statistics function, over held_out examples when the suite has any (bands are tuned on tune), else over all.
// Only the latest attempt of a cell counts, with its latest primary score, and only one evaluator version (the newest):
// rows of different versions are never averaged. An unscored typed trial is left out.
func expComparison(e *labExperiment, trials []*labRun, cur []labScore) []labComparisonRow {
	primary, vs := e.primary(), e.Variants
	out := []labComparisonRow{}
	if primary == "" || len(vs) < 2 {
		return out
	}
	baseline := vs[0].Label
	scope := "all"
	if hasHeldOut(e) {
		scope = "held_out"
	}
	reason := ""
	if strings.HasSuffix(primary, ":pick_best") {
		reason = "pick_best ranks trials together; it gives no per-trial value to pair"
	}
	version := ""
	for _, sc := range cur {
		if sc.Evaluator == primary && len(sc.TrialIDs) == 1 {
			version = sc.EvaluatorVersion
		}
	}
	values, versions := map[string]map[string][]float64{}, map[string]bool{}
	for _, st := range scoredLatest(e, trials, cur, func(sc labScore) bool { return sc.Evaluator == primary }) {
		sc := st.score
		if scope == "held_out" && st.split != "held_out" || sc.typed().Answer == "unscored" {
			continue
		}
		versions[sc.EvaluatorVersion] = true
		if sc.EvaluatorVersion != version {
			continue
		}
		if values[st.variant] == nil {
			values[st.variant] = map[string][]float64{}
		}
		values[st.variant][st.example] = append(values[st.variant][st.example], scoreNumber(st.score))
	}
	if len(versions) > 1 && reason == "" {
		reason = "scores of older evaluator versions are left out"
	}
	if len(values[baseline]) == 0 && reason == "" {
		reason = "the baseline has no scored trial"
	}
	for _, v := range vs[1:] {
		out = append(out, labComparisonRow{Variant: v.Label, Baseline: baseline, Evaluator: primary, EvaluatorVersion: version, Split: scope, Reason: strPtr(reason),
			Result: compare(values[baseline], values[v.Label])})
	}
	return out
}

// labComparisonRow: one non-baseline variant against the baseline (expComparison).
type labComparisonRow struct {
	Variant          string     `json:"variant"`
	Baseline         string     `json:"baseline"`
	Evaluator        string     `json:"evaluator"`
	EvaluatorVersion string     `json:"evaluatorVersion"`
	Split            string     `json:"split"`
	Reason           *string    `json:"reason"`
	Result           comparison `json:"result"`
}

// bandsOf: a variant's bands {yes, no}: p >= yes answers yes, p <= no answers no, between abstains. Without bands every
// answer is confident at 0.5 (no middle band).
func bandsOf(v any) (float64, float64, error) {
	if v == nil {
		return 0.5, 0.5, nil
	}
	m, isObject := labRawObject(jsonBytes(v))
	yes, okY := numOK(rawValue(m["yes"]))
	no, okN := numOK(rawValue(m["no"]))
	if !isObject || len(m) != 2 || !okY || !okN || no < 0 || yes > 1 || no > yes {
		return 0, 0, fail(400, "bands are {yes, no} with 0 <= no <= yes <= 1")
	}
	return yes, no, nil
}

// scoreTyped: the typed evaluator for one trial: its output's p (the last step's output in the run record) through the
// variant's bands, against the example's label (references/expected.json {expected, tag?}). A trial it cannot score is
// recorded as {answer: unscored, reason} so the reason is visible and it is not tried again; it counts in no statistic.
func scoreTyped(q *Request, exp *labExperiment, t *labRun, pin *labPin, name string, examples map[string]labExample) error {
	key := ""
	if pin != nil {
		key = pin.ArtifactID
	}
	full, ok := examples[key]
	if !ok {
		var err error
		if full, err = exampleOf(q, pin); err != nil {
			return err
		}
		examples[key] = full
	}
	var label, tag, reason string
	for _, f := range full.References {
		if f.Path == "expected.json" && label == "" && reason == "" { // paths are unique; the first counts
			var e map[string]json.RawMessage // exact keys
			if err := json.Unmarshal([]byte(deref(f.Text)), &e); err != nil {
				reason = "references/expected.json is not a JSON object"
			}
			json.Unmarshal(e["expected"], &label)
			json.Unmarshal(e["tag"], &tag)
		}
	}
	var out struct {
		P json.RawMessage `json:"p"`
	}
	json.Unmarshal(t.output(), &out)
	pv := rawValue(out.P)
	p, isP := numOK(pv)
	switch {
	case reason != "":
	case label != "yes" && label != "no":
		reason = "the example has no yes/no label in references/expected.json"
	case !isP:
		reason = "the trial output has no number p"
	case p < 0 || p > 1:
		reason = fmt.Sprintf("the trial output p %v is outside 0..1", pv)
	}
	var value labTypedValue
	rationale := ""
	if reason != "" {
		value, rationale = labTypedValue{Answer: "unscored", Reason: reason, Label: strPtr(label), Tag: strPtr(tag)}, "unscored: "+reason
	} else {
		var bands any
		if v := exp.variant(t.cell().Variant); v != nil {
			bands = v.bands()
		}
		yes, no, err := bandsOf(bands)
		if err != nil {
			return err
		}
		answer := bandAnswer(p, yes, no)
		value = labTypedValue{Answer: answer, Label: &label, P: &p, Tag: strPtr(tag)}
		rationale = fmt.Sprintf("%s at p %.3g (bands %.3g/%.3g), label %s", answer, p, yes, no, label)
	}
	_, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name, EvaluatorVersion: "labels-v1", Form: "typed",
		Value: value, Rationale: &rationale})
	return err
}

// labTypedValue: a typed score's value: the banded answer against the example's label, or unscored with its reason.
type labTypedValue struct {
	Answer string   `json:"answer"`
	Label  *string  `json:"label"`
	P      *float64 `json:"p,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Tag    *string  `json:"tag"`
}

// scoredTrial: the latest attempt of one cell with its latest score of one evaluator.
type scoredTrial struct {
	trial          *labRun
	score          labScore
	example, split string
	variant        string
}

// scoredLatest: the latest comparable attempt of every cell with its latest score that match accepts, and the split of
// its example. Comparisons and typed statistics read trials only through it.
func scoredLatest(e *labExperiment, trials []*labRun, cur []labScore, match func(labScore) bool) []scoredTrial {
	split := map[string]string{}
	for _, p := range e.Examples {
		split[p.ID] = p.Split
	}
	byTrial := map[string]labScore{}
	for _, sc := range cur { // in order: a later score of a trial replaces the earlier
		if match(sc) && len(sc.TrialIDs) == 1 {
			byTrial[sc.TrialIDs[0]] = sc
		}
	}
	out := []scoredTrial{}
	order, latest := expLatest(trials)
	for _, k := range order {
		t := latest[k]
		c := t.cell()
		if sc, ok := byTrial[t.ID]; ok && comparable(t) {
			out = append(out, scoredTrial{t, sc, c.Example, split[c.Example], c.Variant})
		}
	}
	return out
}

func hasHeldOut(e *labExperiment) bool {
	for _, p := range e.Examples {
		if p.Split == "held_out" {
			return true
		}
	}
	return false
}

// expTyped: per variant and split, the typed evaluator's coverage, selective accuracy and escalation share per example
// tag (the language). Bands are tuned on tune and reported on held_out, so both splits are shown, never mixed.
func expTyped(e *labExperiment, trials []*labRun, cur []labScore) []labTypedRow {
	out := []labTypedRow{}
	answers, perExample := map[[2]string][]typedAnswer{}, map[[3]string][]typedAnswer{}
	for _, st := range scoredLatest(e, trials, cur, func(sc labScore) bool { return sc.Form == "typed" }) {
		v := st.score.typed()
		if v.Answer == "unscored" {
			continue
		}
		perExample[[3]string{st.variant, st.split, st.example}] = append(perExample[[3]string{st.variant, st.split, st.example}], typedAnswer{v.Tag, v.Answer, v.Label})
	}
	// Identical repeats of one example count as one answer (a near-deterministic variant); differing ones count each.
	for k, as := range perExample {
		same := true
		for _, a := range as[1:] {
			same = same && a.Answer == as[0].Answer
		}
		if same {
			as = as[:1]
		}
		answers[[2]string{k[0], k[1]}] = append(answers[[2]string{k[0], k[1]}], as...)
	}
	for _, v := range e.Variants {
		for _, sp := range []string{"tune", "held_out"} {
			if as := answers[[2]string{v.Label, sp}]; len(as) > 0 {
				out = append(out, labTypedRow{Variant: v.Label, Split: sp, Stats: typedStats(as)})
			}
		}
	}
	return out
}

// labTypedRow: a variant's typed statistics on one split (expTyped).
type labTypedRow struct {
	Variant string      `json:"variant"`
	Split   string      `json:"split"`
	Stats   []typedStat `json:"stats"`
}

// exampleRef: <id>@<version>.
func exampleRef(id string, version int64) string {
	return fmt.Sprintf("%s@%d", id, version)
}

func labRatingWithdraw(q *Request, id, reason string) (labRatingView, error) {
	if err := labIDArg(id, "questionId"); err != nil {
		return labRatingView{}, err
	}
	if _, err := labStr(reason, "withdrawal reason", 4000); err != nil {
		return labRatingView{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return labRatingView{}, fail(400, "Withdrawal reason is empty")
	}

	state, err := q.db().LabRatingLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return labRatingView{}, fail(404, "Unknown Lab rating")
	}
	if err != nil {
		return labRatingView{}, err
	}
	if state == "answered" {
		return labRatingView{}, fail(409, "Rating already answered")
	}
	if state == "open" {
		withdrawal := labWithdrawal{Reason: reason, Actor: actor(q, "operator"), At: nowMs()}
		if _, err := questionTransition(q, id, questionEvent{To: "withdrawn", Withdrawal: jsonBytes(withdrawal)}); err != nil {
			return labRatingView{}, err
		}
	}
	return labRating(q, id)
}

// labWithdrawal: why and by whom a rating was withdrawn.
type labWithdrawal struct {
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
	At     int64  `json:"at"`
}

// Lock the experiment row FOR SHARE before person/person_grant FOR SHARE, matching setPeople's table lock order
// (experiment -> person) without acquiring GLOBAL after experiment:<id>.
func labRequester(q *Request, id string) (*Person, error) {
	exp, err := q.db().LabExperimentForShare(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown experiment")
	}
	if err != nil {
		return nil, err
	}
	email := deref(exp.RequestedBy)
	if email == "" {
		return nil, failCode(409, "Lab requester is unknown; retained history cannot authorize new work", "requester:unknown")
	}
	if exp.RequesterRemovedAt != nil {
		return nil, failCode(409, "Lab requester was removed; this experiment cannot transfer identity", "requester:removed")
	}
	p, err := unscoped(q, func() (*Person, error) { return resolveAccessShareTx(q.ctx(), q.tx, email) })
	if err != nil {
		var denied *HTTPError
		if errors.As(err, &denied) && denied.Code == 403 {
			return nil, failCode(409, "Lab requester is no longer present", "requester:removed")
		}
		return nil, err
	}
	if (!fullAccess(p) && !hasRole(p, "architect")) || !mayAccessProjectID(p, exp.ProjectID) {
		return nil, failCode(403, "Lab requester lacks current role or project access", "requester:access-revoked")
	}
	return p, nil
}
