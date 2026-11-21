package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: a Lab script check observes cancellation (docs/designs/fleet-lab.md, script evaluators). Risk: a hung check
// keeps the experiment busy forever and graceful shutdown cannot stop it; a cancelled check must not be recorded as failed.
func TestRunChecksStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := runChecks(ctx, []labCheck{{ID: "hang", Run: "sleep 30"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatalf("runChecks = %v, %v; want no result and the context error", r, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the check ran %v after cancellation", d)
	}
}

func TestRegistryLoadRefusesBadEntries(t *testing.T) {
	t.Setenv("FLEET_LAB_DIRECTORY", "../../config/lab")
	if _, err := useCases(); err != nil {
		t.Fatalf("repo registry: %v", err)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "use-cases"), 0o755)
	t.Setenv("FLEET_LAB_DIRECTORY", dir)
	base := `{"id":"x:1","source":{"repo":"r","path":"p","anchor":"a","commit":"c","sha256":"s"},"source_status":"current","actor":"a","goal":"g","expected_outcome":"e","checks":[],"lab_fit":"none","tags":[],"maturity":"m"`
	for name, entry := range map[string]string{"maps_to": base + `,"maps_to":["fleet:UC99"]}`, "field": base + `,"extra":1}`, "status": base + `,"status":"gone"}`} {
		os.WriteFile(filepath.Join(dir, "use-cases", "x.json"), []byte("["+entry+"]"), 0o644)
		if _, err := useCases(); err == nil || !strings.Contains(err.Error(), "x:1") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestVariantArgumentsMergeOverStep(t *testing.T) {
	// Requirement: S4 variant arguments apply without base arguments; risk: silent omission.
	if got := mapOf(withArguments(doc{"id": "gen"}, doc{"arguments": doc{"gen": doc{"topology": "quad"}}})); got["topology"] != "quad" {
		t.Fatalf("override without base arguments: %v", got)
	}
	def := doc{"id": "gen", "arguments": doc{"image_url": "${steps.encode.output.data_uri}", "target_polycount": 3000}}
	got := mapOf(withArguments(def, doc{"arguments": doc{"gen": doc{"target_polycount": 8000, "topology": "quad"}, "other": doc{"x": 1}}}))
	if got["target_polycount"] != 8000 || got["topology"] != "quad" || got["image_url"] != "${steps.encode.output.data_uri}" || mapOf(def["arguments"])["target_polycount"] != 3000 {
		t.Fatalf("%v", got)
	}
}

func TestJudgeArtifactInputsReachSteps(t *testing.T) {
	// Requirement: S5 stages binary trial artifacts; risk: dropping inputs without text example files.
	run := doc{"example": doc{}, "exampleInputs": []any{doc{"artifactId": "render-artifact", "path": "example/trial/renders/front.png"}}, "steps": []any{}}
	got, err := inputsFor(nil, run, 0)
	if err != nil || len(got) != 1 || mapOf(got[0])["artifactId"] != "render-artifact" || mapOf(got[0])["path"] != "example/trial/renders/front.png" {
		t.Fatalf("binary artifact inputs: %v, %v", got, err)
	}
}

func TestJudgeRerunRetainsArtifactInputs(t *testing.T) {
	// Requirement: judge reruns receive original renders; risk: a rerun silently judges no files.
	original := doc{"exampleInputs": []any{doc{"artifactId": "immutable-png", "path": "example/trial/front.png"}}}
	rerun := rerunInputs(original, "all")
	rerun["example"], rerun["steps"] = doc{}, []any{}
	got, err := inputsFor(nil, rerun, 0)
	if err != nil || len(got) != 1 || mapOf(got[0])["artifactId"] != "immutable-png" {
		t.Fatalf("rerun inputs: %v, %v", got, err)
	}
}

func TestPairwiseOrderDependenceAndVersions(t *testing.T) {
	// Requirement: S5 never scores position bias and separates rubrics; risk: false agreement across versions.
	run := func(id, value string, order int) doc {
		return doc{"id": id, "state": "succeeded", "judge": doc{"evaluator": "judge:pair:pick_best", "groupId": "g", "trialIds": []any{"a", "b"}, "order": order}, "canonicalResult": doc{"stepId": "judge", "mode": "static"}, "plan": doc{"entries": []any{doc{"id": "judge"}}}, "steps": []any{doc{"id": "judge", "output": doc{"value": value}}}}
	}
	view := pairwiseView(labRunsOf([]doc{run("one", "a", 0), run("two", "b", 1)}))
	if len(view) != 1 || *view[0].Reason != "order-dependent" {
		t.Fatalf("%v", view)
	}
	scores := []doc{
		{"evaluator": "human:owner:pick_best", "form": "pick_best", "trialIds": []any{"b", "a"}, "value": "a"},
		{"evaluator": "judge:pair:pick_best", "evaluatorVersion": "v1", "form": "pick_best", "trialIds": []any{"a", "b"}, "value": "a"},
		{"evaluator": "judge:pair:pick_best", "evaluatorVersion": "v2", "form": "pick_best", "trialIds": []any{"a", "b"}, "value": "b"},
	}
	a := agreement(labScoresOf(scores))
	vs := a.ByVersion
	if len(vs) != 2 || vs[0].Matched != 1 || vs[1].Matched != 0 {
		t.Fatalf("%v", a)
	}
}

func TestPairwiseWaitsForAllLatestCandidates(t *testing.T) {
	// Requirement: compare complete groups of latest attempts; risk: scoring a candidate never shown to the judges.
	exp := doc{"variants": []any{doc{}, doc{}, doc{}}}
	trial := func(id, variant, state string, attempt int) doc {
		return doc{"id": id, "state": state, "cell": doc{"iteration": 0, "example": "e", "repeat": 0, "variant": variant, "attempt": attempt}}
	}
	trials := []doc{trial("a", "a", "succeeded", 0), trial("b", "b", "succeeded", 0), trial("c", "c", "running", 0)}
	if groups, complete := pairwiseGroups(labExpOf(exp), labRunsOf(trials)); complete || len(groups) != 0 {
		t.Fatalf("premature group: %v", groups)
	}
	trials[2]["state"] = "succeeded"
	trials = append(trials, trial("a-retry", "a", "running", 1))
	if groups, complete := pairwiseGroups(labExpOf(exp), labRunsOf(trials)); complete || len(groups) != 0 {
		t.Fatalf("stale retry group: %v", groups)
	}
	trials[3]["state"] = "succeeded"
	groups, complete := pairwiseGroups(labExpOf(exp), labRunsOf(trials))
	if !complete || len(groups) != 1 {
		t.Fatalf("missing complete group: %v", groups)
	}
	for _, g := range groups {
		if len(g) != 3 {
			t.Fatal(g)
		}
		for _, tr := range g {
			if tr.ID == "a" {
				t.Fatal("old attempt included")
			}
		}
	}
}

// Requirement: capture exact assessment bytes before scratch cleanup, independently
// of exit status. Risk: a later check inherits stale evidence or follows symlinks.
func TestRunChecksRetainsAssessmentEvidence(t *testing.T) {
	bytes := "{\"schema\":\"test.assessment.v1\",\"falseAccepts\":2}\n"
	checks := []labCheck{
		{ID: "contract", Run: "mkdir evaluation; printf '%s\\n' '{\"schema\":\"test.assessment.v1\",\"falseAccepts\":2}' > evaluation/assessment.json; exit 3"},
		{ID: "later", Run: "test ! -e evaluation/assessment.json"},
	}
	result, err := runChecks(context.Background(), checks, nil)
	if err != nil {
		t.Fatal(err)
	}
	captured := result.Assessments
	if len(captured) != 1 || len(result.Failed) != 1 || captured[0].CheckID != "contract" || captured[0].Sha256 != sha256hex([]byte(bytes)) || string(captured[0].Bytes) != bytes {
		t.Fatal(result)
	}
	// A structurally valid assessment with model errors is still a passing contract.
	result, err = runChecks(context.Background(), []labCheck{{ID: "valid", Run: "mkdir evaluation; printf '%s' '{\"schema\":\"test.assessment.v1\",\"falseAccepts\":9}' > evaluation/assessment.json"}}, nil)
	if err != nil || len(result.Failed) != 0 || len(result.Assessments) != 1 {
		t.Fatal(result, err)
	}
	for _, command := range []string{
		"mkdir evaluation; ln -s ../truth.json evaluation/assessment.json",
		"mkdir evaluation; printf '%s' '{}' > evaluation/assessment.json",
		"ln -s references evaluation",
	} {
		result, err = runChecks(context.Background(), []labCheck{{ID: "invalid", Run: command}}, []labFile{{"truth.json", []byte(bytes)}, {"references/truth.json", []byte(bytes)}})
		if err != nil || len(result.Failed) != 1 || len(result.Assessments) != 0 {
			t.Fatal(command, result, err)
		}
	}
	if _, err = runChecks(context.Background(), nil, []labFile{{"../escape", []byte("x")}}); err == nil {
		t.Fatal("escaped evaluator root")
	}
}

// Requirement: assessment evidence and its contract score link atomically under
// the current trial identity. Risk: rollback, changed bytes or a newer attempt
// leaves a passing score attached to stale or missing evidence.
func TestLabAssessmentLinkIdentityAndRollback(t *testing.T) {
	env := openTestCoordinator(t, false)
	trial := planFixture(t, "assessment-trial")
	definition := doc{"version": float64(2), "id": "assessment", "steps": []any{doc{"id": "route", "kind": "script", "command": "synthetic-not-executed"}}}
	trial["definition"], trial["definitionSha256"], trial["pipelineId"] = definition, sha256hex([]byte(canonical(definition))), "assessment"
	trial["steps"] = anys(trial["steps"])[:1]
	trial["experimentId"], trial["variant"], trial["cell"] = "assessment-exp", "v", doc{"example": "synthetic", "variant": "v", "repeat": 1, "attempt": 1}
	if err := initPipelinePlan(trial); err != nil {
		t.Fatal(err)
	}
	hash := sha256hex([]byte("synthetic assessment bytes"))
	ref := doc{"checkId": "contract", "schemaId": "synthetic.assessment.v1", "artifactId": "assessment-evidence", "sha256": hash, "evaluatorSha256": sha256hex([]byte("evaluator")), "inputSha256": sha256hex([]byte("inputs"))}
	refT, _ := asT[labAssessmentRef](ref)
	result := &labCheckRun{ProjectAssessments: []labAssessmentRef{refT}}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := exec(q, "INSERT INTO experiment(id,project_id,kind,pipeline_id) VALUES('assessment-exp','hello-world','single','assessment')"); err != nil {
			return err
		}
		if err := putRun(q, trial); err != nil {
			return err
		}
		if err := selectPipelineStep(q, trial, 0); err != nil {
			return err
		}
		output := doc{"synthetic": "result"}
		if err := exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES('assessment-result','hello-world','lab',$1,$2,'application/json','output.json',$3,'stored')`, sha256hex(jsonBytes(output)), len(jsonBytes(output)), stepRow(trial, 0)); err != nil {
			return err
		}
		entry := pipelineEntry(trial, 0)
		entry.Inputs = []contract.PipelineInputBinding{}
		entry.Dispatch.Phase = "handed_off"
		mapOf(anys(trial["steps"])[0])["outputArtifactId"] = "assessment-result"
		if err := completeStep(q, trial, 0, output, nil); err != nil {
			return err
		}
		if err := finishRun(q, trial, "succeeded", ""); err != nil {
			return err
		}
		return exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES('assessment-evidence','hello-world','lab',$1,26,'application/json','assessments/synthetic.json',$2,'stored')`, hash, trial["id"])
	}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("injected score/link rollback")
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := linkLabAssessments(q, mustRunOf(trial), result); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: "assessment-exp", ProjectID: "hello-world", TrialIDs: []string{astr(trial["id"])}, Evaluator: "script:example.checks:pass_fail", EvaluatorVersion: "synthetic", Form: "pass_fail", Value: true}); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		current, err := getRunDoc(q, astr(trial["id"]))
		if err != nil {
			return err
		}
		if current["projectAssessments"] != nil {
			t.Fatal("rollback retained an evidence link")
		}
		var n int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM score WHERE experiment_id='assessment-exp'").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("rollback retained a contract score")
		}
		if err := linkLabAssessments(q, mustRunOf(trial), result); err != nil {
			return err
		}
		if err := linkLabAssessments(q, mustRunOf(trial), result); err != nil {
			return err
		}
		current, err = getRunDoc(q, astr(trial["id"]))
		if err != nil {
			return err
		}
		if len(anys(current["projectAssessments"])) != 1 {
			t.Fatal("idempotent link duplicated evidence")
		}
		// A later plan write cannot erase the independently linked evidence.
		return putRun(q, trial)
	}); err != nil {
		t.Fatal(err)
	}
	changed := doc{}
	for k, v := range ref {
		changed[k] = v
	}
	changed["sha256"], changed["artifactId"] = sha256hex([]byte("different bytes")), "assessment-different"
	err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES('assessment-different','hello-world','lab',$1,15,'application/json','assessments/synthetic.json',$2,'stored')`, changed["sha256"], trial["id"]); err != nil {
			return err
		}
		changedT, _ := asT[labAssessmentRef](changed)
		return linkLabAssessments(q, mustRunOf(trial), &labCheckRun{ProjectAssessments: []labAssessmentRef{changedT}})
	})
	var conflict *HTTPError
	if !errors.As(err, &conflict) || conflict.Code != 409 || conflict.Extra != (reasonExtra{"assessment_identity_conflict"}) {
		t.Fatal("changed assessment bytes accepted", err)
	}
	failedStorage := &labCheckRun{Assessments: []labAssessmentCapture{{CheckID: "contract", SchemaID: "synthetic.assessment.v1", Bytes: []byte("{}"), Sha256: sha256hex([]byte("{}"))}}}
	if err := materializeLabAssessments(env.ctx, env.app, mustRunOf(trial), failedStorage, astr(ref["evaluatorSha256"]), astr(ref["inputSha256"])); err == nil || failedStorage.ProjectAssessments != nil {
		t.Fatal("missing storage fabricated durable evidence", err, failedStorage)
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		current, err := getRunDoc(q, astr(trial["id"]))
		if err != nil {
			return err
		}
		if len(anys(current["projectAssessments"])) != 1 {
			t.Fatal("plan write lost retained assessment")
		}
		full := labExample{Checks: []labCheck{}}
		snapshot := labAssessmentSnapshot(mustRunOf(current), full)
		checked := &labCheckRun{AssessmentSnapshot: &snapshot}
		if err := validateLabAssessmentSnapshot(q, mustRunOf(current), labExample{Checks: []labCheck{{ID: "changed", Run: "exit 0"}}}, checked); err == nil {
			t.Fatal("changed check snapshot accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		newer := planFixture(t, "assessment-new-attempt")
		newer["experimentId"], newer["variant"], newer["cell"] = "assessment-exp", "v", doc{"example": "synthetic", "variant": "v", "repeat": 1, "attempt": 2}
		if err := putRun(q, newer); err != nil {
			return err
		}
		return linkLabAssessments(q, mustRunOf(trial), result)
	})
	if !errors.As(err, &conflict) || conflict.Code != 409 {
		t.Fatal("superseded attempt accepted assessment", err)
	}
}

// Requirement: an unscored historical attempt cannot obstruct script grading
// after a newer attempt exists. Risk: the retained latest-attempt link fence
// causes every reconcile pass to roll back on the superseded trial.
func TestLabAssessmentSkipsSupersededScriptEvaluation(t *testing.T) {
	cell := func(attempt int) doc {
		return doc{"example": "synthetic", "variant": "v", "repeat": 1, "attempt": attempt}
	}
	old := doc{"id": "old", "state": "succeeded", "observation": "confirmed", "cell": cell(1)}
	latest := doc{"id": "latest", "state": "preparing", "observation": "confirmed", "cell": cell(2)}
	exp := doc{"state": "running", "evaluators": []any{doc{"kind": "script", "ref": "example.checks", "form": "pass_fail"}}}
	launched := []string{}
	complete, err := expEvaluate(nil, labExpOf(exp), labRunsOf([]doc{old, latest}), nil, nil, &launched, nil)
	if err != nil || !complete || len(launched) != 0 {
		t.Fatal("superseded unscored trial obstructed grading", complete, err, launched)
	}
}
