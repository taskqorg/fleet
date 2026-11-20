package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// labAssessmentCapture: one check's assessment file, captured before scratch cleanup (runChecks).
type labAssessmentCapture struct {
	CheckID  string `json:"checkId"`
	SchemaID string `json:"schemaId"`
	Bytes    []byte `json:"bytes"`
	Sha256   string `json:"sha256"`
}

// labAssessmentRef: a stored assessment linked to its trial; fields in canonical (sorted) key order.
type labAssessmentRef struct {
	ArtifactID      string `json:"artifactId"`
	CheckID         string `json:"checkId"`
	EvaluatorSha256 string `json:"evaluatorSha256"`
	InputSha256     string `json:"inputSha256"`
	SchemaID        string `json:"schemaId"`
	Sha256          string `json:"sha256"`
}

// labFileBinding: an evaluator input file by path and content; fields in canonical (sorted) key order.
type labFileBinding struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}

// labAssessmentSnap: what a trial's assessment was computed from (labAssessmentSnapshot).
type labAssessmentSnap struct {
	ExampleSha256   string `json:"exampleSha256"`
	PlanRevision    any    `json:"planRevision"`
	CanonicalResult any    `json:"canonicalResult"`
	Usage           any    `json:"usage"`
	DurationMs      any    `json:"durationMs"`
}

// labAssessTrial: the fields of a trial (a pipeline run of the experiment) that assessments read.
type labAssessTrial struct {
	ID                 string             `json:"id"`
	ExperimentID       string             `json:"experimentId"`
	State              string             `json:"state"`
	Observation        string             `json:"observation"`
	Cell               labCell            `json:"cell"`
	ProjectAssessments []labAssessmentRef `json:"projectAssessments"`
	Plan               struct {
		Revision any `json:"revision"`
	} `json:"plan"`
	CanonicalResult any `json:"canonicalResult"`
	Usage           any `json:"usage"`
	DurationMs      any `json:"durationMs"`
}

// labCanonicalInputs: the input artifacts of a trial's canonical result.
type labCanonicalInputs struct {
	CanonicalResult struct {
		Inputs []struct {
			ArtifactID string `json:"artifactId"`
			Sha256     string `json:"sha256"`
		} `json:"inputs"`
	} `json:"canonicalResult"`
}

// captureLabAssessment accepts one fixed, regular, contained JSON file. The
// bytes are retained before scratch cleanup; exit status is a separate fact.
func captureLabAssessment(dir string) (*labAssessmentCapture, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("evaluation")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("invalid assessment directory")
	}
	evaluation, err := root.OpenRoot("evaluation")
	if err != nil {
		return nil, err
	}
	defer evaluation.Close()
	directory, err := evaluation.Open(".")
	if err != nil {
		return nil, err
	}
	actual, err := directory.Stat()
	directory.Close()
	if err != nil || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("assessment directory changed during open")
	}
	f, err := regularFile(evaluation, "assessment.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(b) > 64<<20 {
		return nil, fmt.Errorf("assessment bytes unavailable or too large")
	}
	var value *struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal(b, &value) != nil || value == nil || strings.TrimSpace(value.Schema) == "" {
		return nil, fmt.Errorf("assessment requires a JSON object with schema")
	}
	return &labAssessmentCapture{SchemaID: value.Schema, Bytes: b, Sha256: sha256hex(b)}, nil
}

func labFileBindings(files []labFile) []labFileBinding {
	bindings := []labFileBinding{}
	for _, f := range files {
		bindings = append(bindings, labFileBinding{Path: f.path, Sha256: sha256hex(f.bytes)})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Path < bindings[j].Path })
	return bindings
}

func labEvaluatorIdentity(checks []labCheck, files []labFile) string {
	members := []labFileBinding{}
	for _, f := range files {
		if f.path == "tools/program.py" || f.path == "contracts.json" {
			members = append(members, labFileBinding{Path: f.path, Sha256: sha256hex(f.bytes)})
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	evaluator := []any{struct {
		Checks []labCheck `json:"checks"`
	}{checks}}
	for _, m := range members {
		evaluator = append(evaluator, m)
	}
	return sha256hex([]byte(canonical(evaluator)))
}

func labAssessmentInputs(trial doc, checks []labCheck, files []labFile) (string, string) {
	return labEvaluatorIdentity(checks, files), sha256hex([]byte(canonical(labFileBindings(files))))
}

// Upload before opening the linking transaction. A storage failure cannot make
// the script check score durable; an unlinked object is ordinary retained data.
func materializeLabAssessments(ctx context.Context, a *App, trial doc, r *labCheckRun, evaluator, input string) error {
	t, _ := asT[labAssessTrial](trial)
	refs := []labAssessmentRef{}
	for _, capture := range r.Assessments {
		name := filepath.ToSlash(filepath.Join("assessments", t.ID, capture.CheckID, evaluator, input+".json"))
		id, hash, err := saveObject(ctx, a, projectIDPtr(trial["projectId"]), "lab", t.ID, name, capture.Bytes, "application/json")
		if err != nil {
			return err
		}
		if hash != capture.Sha256 {
			return fail(409, "Assessment stored bytes differ")
		}
		refs = append(refs, labAssessmentRef{CheckID: capture.CheckID, SchemaID: capture.SchemaID, ArtifactID: id, Sha256: hash, EvaluatorSha256: evaluator, InputSha256: input})
	}
	sort.Slice(refs, func(i, j int) bool { return canonical(refs[i]) < canonical(refs[j]) })
	r.ProjectAssessments, r.Assessments = refs, nil
	return nil
}

func linkLabAssessments(q *Request, trial doc, r *labCheckRun) error {
	refs := r.ProjectAssessments
	if len(refs) == 0 {
		return nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return fail(409, "Assessment linking requires its transaction fence")
	}
	t, _ := asT[labAssessTrial](trial)
	if _, err := q.db().ExecutionLockState(q.ctx(), t.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	current, err := getRun(q, t.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return fail(409, "Assessment trial snapshot changed")
	}
	run, _ := asT[labAssessTrial](current)
	if run.State != "succeeded" || run.Observation == "unconfirmed" || !same(current["canonicalResult"], trial["canonicalResult"]) {
		return fail(409, "Assessment trial snapshot changed")
	}
	runs, err := expRuns(q, t.ExperimentID)
	if err != nil {
		return err
	}
	_, latest := expLatest(expTrials(runs))
	if l := latest[t.Cell.key()]; l == nil || l.ID != t.ID {
		return fail(409, "Assessment attempt was superseded")
	}
	existing := append([]labAssessmentRef{}, run.ProjectAssessments...)
	for _, ref := range refs {
		artifact, err := artifactRow(withTx(q), q.app, ref.ArtifactID)
		if err != nil || artifact.State != "stored" || !same(artifact.ProjectID, trial["projectId"]) || artifact.ExecutionID == nil || *artifact.ExecutionID != t.ID || artifact.ContentHash != ref.Sha256 {
			return fail(409, "Assessment artifact identity unavailable or changed")
		}
		found := false
		for _, old := range existing {
			if old.CheckID == ref.CheckID && old.EvaluatorSha256 == ref.EvaluatorSha256 && old.InputSha256 == ref.InputSha256 {
				if old.Sha256 != ref.Sha256 {
					return labConflict("Assessment identity conflict", "assessment_identity_conflict")
				}
				found = true
			}
		}
		if !found {
			existing = append(existing, ref)
		}
	}
	sort.Slice(existing, func(i, j int) bool {
		a, b := existing[i], existing[j]
		if a.CheckID != b.CheckID {
			return a.CheckID < b.CheckID
		}
		if a.EvaluatorSha256 != b.EvaluatorSha256 {
			return a.EvaluatorSha256 < b.EvaluatorSha256
		}
		return a.InputSha256 < b.InputSha256
	})
	return q.db().LabTrialAssessmentsSet(q.ctx(), db.LabTrialAssessmentsSetParams{ID: t.ID, Assessments: jsonBytes(existing)})
}

func labAssessmentSnapshot(trial doc, example labExample) labAssessmentSnap {
	t, _ := asT[labAssessTrial](trial)
	return labAssessmentSnap{ExampleSha256: sha256hex([]byte(canonical(rawValue(jsonBytes(example))))), PlanRevision: t.Plan.Revision, CanonicalResult: t.CanonicalResult, Usage: t.Usage, DurationMs: t.DurationMs}
}

func validateLabAssessmentSnapshot(q *Request, trial doc, example labExample, result *labCheckRun) error {
	if result.AssessmentSnapshot == nil || !same(*result.AssessmentSnapshot, labAssessmentSnapshot(trial, example)) {
		return fail(409, "Assessment evaluator or input snapshot changed")
	}
	// The output and every input artifact are immutable identities. Availability
	// and ownership are checked again at the linking boundary without object I/O.
	t, _ := asT[labCanonicalInputs](trial)
	for _, binding := range t.CanonicalResult.Inputs {
		a, err := artifactRow(withTx(q), q.app, binding.ArtifactID)
		if err != nil || a.State != "stored" || a.ContentHash != binding.Sha256 || !same(a.ProjectID, trial["projectId"]) {
			return fail(409, "Assessment input artifact changed")
		}
	}
	return nil
}

// Regular files only; hardlinks could otherwise disclose bytes outside the root.
func regularFile(root *os.Root, name string) (*os.File, error) {
	st, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("non-regular file rejected")
	}
	sys := reflect.ValueOf(st.Sys())
	if sys.Kind() == reflect.Pointer {
		sys = sys.Elem()
	}
	if sys.Kind() == reflect.Struct {
		n := sys.FieldByName("Nlink")
		if n.IsValid() && n.CanUint() && n.Uint() != 1 {
			return nil, fmt.Errorf("hardlink rejected")
		}
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, fmt.Errorf("file changed during open")
	}
	return f, nil
}
