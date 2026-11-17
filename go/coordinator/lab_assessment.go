package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// captureLabAssessment accepts one fixed, regular, contained JSON file. The
// bytes are retained before scratch cleanup; exit status is a separate fact.
func captureLabAssessment(dir string) (doc, error) {
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
	var value doc
	if json.Unmarshal(b, &value) != nil || strings.TrimSpace(astr(value["schema"])) == "" {
		return nil, fmt.Errorf("assessment requires a JSON object with schema")
	}
	return doc{"schemaId": value["schema"], "bytes": b, "sha256": sha256hex(b)}, nil
}

func labFileBindings(files []labFile) []any {
	bindings := []any{}
	for _, f := range files {
		bindings = append(bindings, doc{"path": f.path, "sha256": sha256hex(f.bytes)})
	}
	sort.Slice(bindings, func(i, j int) bool { return astr(mapOf(bindings[i])["path"]) < astr(mapOf(bindings[j])["path"]) })
	return bindings
}

func labEvaluatorIdentity(checks []any, files []labFile) string {
	members := []any{}
	for _, f := range files {
		if f.path == "tools/program.py" || f.path == "contracts.json" {
			members = append(members, doc{"path": f.path, "sha256": sha256hex(f.bytes)})
		}
	}
	sort.Slice(members, func(i, j int) bool { return astr(mapOf(members[i])["path"]) < astr(mapOf(members[j])["path"]) })
	evaluator := append([]any{doc{"checks": checks}}, members...)
	return sha256hex([]byte(canonical(evaluator)))
}

func labAssessmentInputs(trial doc, checks []any, files []labFile) (string, string) {
	return labEvaluatorIdentity(checks, files), sha256hex([]byte(canonical(labFileBindings(files))))
}

// Upload before opening the linking transaction. A storage failure cannot make
// the script check score durable; an unlinked object is ordinary retained data.
func materializeLabAssessments(ctx context.Context, a *App, trial doc, result doc, evaluator, input string) error {
	refs := []any{}
	for _, v := range anys(result["assessments"]) {
		capture := mapOf(v)
		name := filepath.ToSlash(filepath.Join("assessments", astr(trial["id"]), astr(capture["checkId"]), evaluator, input+".json"))
		id, hash, err := saveObject(ctx, a, projectIDPtr(trial["projectId"]), "lab", astr(trial["id"]), name, capture["bytes"].([]byte), "application/json")
		if err != nil {
			return err
		}
		if hash != capture["sha256"] {
			return fail(409, "Assessment stored bytes differ")
		}
		refs = append(refs, doc{"checkId": capture["checkId"], "schemaId": capture["schemaId"], "artifactId": id, "sha256": hash, "evaluatorSha256": evaluator, "inputSha256": input})
	}
	sort.Slice(refs, func(i, j int) bool { return canonical(refs[i]) < canonical(refs[j]) })
	result["projectAssessments"] = refs
	delete(result, "assessments")
	return nil
}

func linkLabAssessments(q *Request, trial doc, result doc) error {
	refs := anys(result["projectAssessments"])
	if len(refs) == 0 {
		return nil
	}
	if !txHasLock(q.tx, GLOBAL) {
		return fail(409, "Assessment linking requires its transaction fence")
	}
	if _, err := first(q, "SELECT id FROM execution WHERE id=$1 FOR UPDATE", trial["id"]); err != nil {
		return err
	}
	current, err := getRun(q, astr(trial["id"]))
	if err != nil {
		return err
	}
	if current == nil || current["state"] != "succeeded" || current["observation"] == "unconfirmed" || !same(current["canonicalResult"], trial["canonicalResult"]) {
		return fail(409, "Assessment trial snapshot changed")
	}
	runs, err := expRuns(q, astr(trial["experimentId"]))
	if err != nil {
		return err
	}
	_, latest := expLatest(expTrials(runs))
	cell := mapOf(trial["cell"])
	if latest[cellKey(cell["example"], cell["variant"], cell["repeat"])]["id"] != trial["id"] {
		return fail(409, "Assessment attempt was superseded")
	}
	existing := append([]any{}, anys(current["projectAssessments"])...)
	for _, value := range refs {
		ref := mapOf(value)
		artifact, err := artifactRow(withTx(q), q.app, astr(ref["artifactId"]))
		if err != nil || artifact.State != "stored" || !same(artifact.ProjectID, trial["projectId"]) || artifact.ExecutionID == nil || *artifact.ExecutionID != astr(trial["id"]) || artifact.ContentHash != ref["sha256"] {
			return fail(409, "Assessment artifact identity unavailable or changed")
		}
		found := false
		for _, v := range existing {
			old := mapOf(v)
			if old["checkId"] == ref["checkId"] && old["evaluatorSha256"] == ref["evaluatorSha256"] && old["inputSha256"] == ref["inputSha256"] {
				if old["sha256"] != ref["sha256"] {
					return &HTTPError{Code: 409, Msg: "Assessment identity conflict", Extra: doc{"reason": "assessment_identity_conflict"}}
				}
				found = true
			}
		}
		if !found {
			existing = append(existing, ref)
		}
	}
	sort.Slice(existing, func(i, j int) bool {
		a, b := mapOf(existing[i]), mapOf(existing[j])
		for _, key := range []string{"checkId", "evaluatorSha256", "inputSha256"} {
			if a[key] != b[key] {
				return astr(a[key]) < astr(b[key])
			}
		}
		return false
	})
	return exec(q, "UPDATE execution SET data=jsonb_set(data,'{projectAssessments}',$2::jsonb) WHERE id=$1", trial["id"], jsonBytes(existing))
}

func labAssessmentSnapshot(trial, example doc) doc {
	return doc{"exampleSha256": sha256hex([]byte(canonical(example))), "planRevision": mapOf(trial["plan"])["revision"], "canonicalResult": trial["canonicalResult"], "usage": trial["usage"], "durationMs": trial["durationMs"]}
}

func validateLabAssessmentSnapshot(q *Request, trial, example, result doc) error {
	if !same(result["assessmentSnapshot"], labAssessmentSnapshot(trial, example)) {
		return fail(409, "Assessment evaluator or input snapshot changed")
	}
	// The output and every input artifact are immutable identities. Availability
	// and ownership are checked again at the linking boundary without object I/O.
	for _, v := range anys(mapOf(trial["canonicalResult"])["inputs"]) {
		binding := mapOf(v)
		a, err := artifactRow(withTx(q), q.app, astr(binding["artifactId"]))
		if err != nil || a.State != "stored" || a.ContentHash != binding["sha256"] || !same(a.ProjectID, trial["projectId"]) {
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
