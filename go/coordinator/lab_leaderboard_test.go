package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: docs/designs/fleet-ia-and-journeys.md R2/R7 shared lab_leaderboard operation and honest
// subscription token/list-price accounting on LabTrialRecord and leaderboard rollups.
// Risk: leaderboard drops failed trials from evaluator groups, omits subscription child-step tokens or
// list-price USD costs, mixes unconfirmed/cancelled runs into averages, or fails to roll up multiple
// experiments on the same use case.
func TestLabLeaderboard(t *testing.T) {
	env := openTestCoordinator(t, false)
	var mu sync.Mutex
	objects := map[string][]byte{}
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			mu.Lock()
			objects[r.URL.Path] = b
			mu.Unlock()
			w.WriteHeader(200)
		case "GET", "HEAD":
			mu.Lock()
			b, ok := objects[r.URL.Path]
			mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(200)
			if r.Method == "GET" {
				_, _ = w.Write(b)
			}
		default:
			w.WriteHeader(204)
		}
	}))
	defer s3.Close()
	prevSecrets := storeSecretsDir
	storeSecretsDir = t.TempDir()
	defer func() { storeSecretsDir = prevSecrets }()
	if err := os.WriteFile(filepath.Join(storeSecretsDir, "st-lb.key"), []byte(`{"accessKeyId":"AKIAINVENTED","secretAccessKey":"invented"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env.registerHost(t, "pds18")
	runTestPSQL(t, env.binDir, env.sockDir, env.pgPort, env.dbName, "postgres",
		"INSERT INTO store(id,host_id,bucket,endpoint,state) VALUES ('st-lb','pds18','b','"+s3.URL+"','reachable');")

	// Add approved examples for two distinct use cases (LLM Code Benchmarks + Real Work).
	addExample := func(id, ucID string) string {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/ops/lab_examples_add", doc{
			"projectId": "hello-world",
			"approved":  true,
			"examples": []any{doc{
				"id":          id,
				"use_case_id": ucID,
				"version":     1,
				"origin":      "synthetic",
				"fixture":     nil,
				"prompt":      "Solve " + id,
				"inputs":      []any{},
				"references":  []any{},
				"checks":      []any{doc{"id": "c1", "run": "true"}, doc{"id": "c2", "run": "false"}},
				"split":       "held_out",
			}},
		}, env.ownerHeaders())
		if code != 200 {
			t.Fatalf("lab_examples_add(%s) = %d %v", id, code, body)
		}
		return astr(mapOf(anys(body["examples"])[0])["artifactId"])
	}
	artBench := addExample("lb-bench-ex", "bench:bug-finding")
	artReal := addExample("lb-real-ex", "real:prod-alert-triage")

	createExp := func(pipelineID string, artID string, variants []any, evals []any, primary string) string {
		t.Helper()
		kind := "ab"
		if len(variants) == 1 {
			kind = "single"
		}
		code, body := env.callJSON(t, "POST", "/api/ops/lab_experiment_create", doc{
			"kind":             kind,
			"projectId":        "hello-world",
			"pipelineId":       pipelineID,
			"examples":         []any{artID},
			"variants":         variants,
			"evaluators":       evals,
			"primaryEvaluator": primary,
		}, env.ownerHeaders())
		if code != 200 {
			t.Fatalf("lab_experiment_create = %d %v", code, body)
		}
		return astr(body["id"])
	}
	variantsBench := []any{
		doc{"label": "codex", "pipelineId": "coding-task", "overrides": doc{"profile": "coding-worker", "model": "gpt-5.3-codex"}},
		doc{"label": "claude", "pipelineId": "coding-task-reviewed", "overrides": doc{"profile": "claude-reviewer", "model": "claude-sonnet-4-6"}},
	}
	evalMetric := []any{doc{"kind": "script", "ref": "example.checks", "form": "metric"}}
	exp1ID := createExp("coding-task", artBench, variantsBench, evalMetric, "script:example.checks:metric")
	exp2ID := createExp("coding-task", artBench, variantsBench[:1], evalMetric, "script:example.checks:metric")

	// Also one Real Work experiment (single variant via ab? Use 2 variants or single).
	codeReal, bodyReal := env.callJSON(t, "POST", "/api/ops/lab_experiment_create", doc{
		"kind":             "single",
		"projectId":        "hello-world",
		"pipelineId":       "coding-task",
		"examples":         []any{artReal},
		"variants":         []any{doc{"label": "triage-v1", "overrides": doc{"profile": "coding-worker", "model": "gpt-5.3-codex"}}},
		"evaluators":       []any{doc{"kind": "script", "ref": "example.checks", "form": "pass_fail"}},
		"primaryEvaluator": "script:example.checks:pass_fail",
	}, env.ownerHeaders())
	if codeReal != 200 {
		t.Fatalf("create real exp = %d %v", codeReal, bodyReal)
	}
	exp3ID := astr(bodyReal["id"])

	now := time.Now().UTC().Truncate(time.Second)
	err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		// Price entries for gpt-5.3-codex and claude-sonnet-4-6.
		for _, p := range []struct {
			id, subj, unit string
			price          float64
		}{
			{"pr-codex-in", "gpt-5.3-codex", "input_tokens", 0.000002},
			{"pr-codex-out", "gpt-5.3-codex", "output_tokens", 0.000010},
			{"pr-codex-cr", "gpt-5.3-codex", "cache_read_tokens", 0.0000005},
			{"pr-claude-in", "claude-sonnet-4-6", "input_tokens", 0.000003},
			{"pr-claude-out", "claude-sonnet-4-6", "output_tokens", 0.000015},
		} {
			if err := exec(q, `INSERT INTO price_entry(id,subject,unit,price,currency,source,observed_at,effective_from) VALUES($1,$2,$3,$4,'USD','owner',$5,$6)`,
				p.id, p.subj, p.unit, p.price, now.Add(-time.Hour), now.Add(-200*24*time.Hour)); err != nil {
				return err
			}
		}

		// Read variant config_refs from created experiments.
		e1, err := expRow(q, exp1ID)
		if err != nil {
			return err
		}
		e2, err := expRow(q, exp2ID)
		if err != nil {
			return err
		}
		e3, err := expRow(q, exp3ID)
		if err != nil {
			return err
		}
		codexRef := e1.variant("codex").ConfigRef
		claudeRef := e1.variant("claude").ConfigRef
		triageRef := e3.variant("triage-v1").ConfigRef

		insertTrial := func(id, expID, pipelineID, variant, state, obs, exPin, configRef string, startAt, endAt time.Time, durMs float64, repeat, attempt int) error {
			tr := planFixture(t, id)
			def := doc{"version": float64(2), "id": pipelineID, "steps": []any{doc{"id": "implement", "kind": "agent", "profile": "coding-worker", "prompt": "Solve"}}}
			tr["definition"] = def
			tr["definitionSha256"] = sha256hex([]byte(canonical(def)))
			tr["pipelineId"] = pipelineID
			tr["steps"] = []any{doc{"id": "implement", "kind": "agent", "state": state}}
			tr["experimentId"] = expID
			tr["variant"] = variant
			tr["cell"] = doc{"iteration": 0, "example": exPin, "variant": variant, "repeat": repeat, "attempt": attempt}
			tr["durationMs"] = durMs
			if err := initPipelinePlan(tr); err != nil {
				return err
			}
			if err := putRun(q, tr); err != nil {
				return err
			}
			cfg := jsonBytes(doc{"config_ref": configRef})
			return exec(q, `UPDATE execution SET state=$2,observation=$3,pipeline_id=$4,config_ref=$5::jsonb,started_at=$6,ended_at=$7,data=jsonb_set(data,'{durationMs}',to_jsonb($8::float8)) WHERE id=$1`,
				id, state, obs, pipelineID, cfg, startAt, endAt, durMs)
		}
		insertAgentStep := func(trialID, stepExecID, expID, variant, pipelineID, profileID, model, accountID, billingBasis string, startAt, endAt time.Time, tokIn, tokOut, tokCR int64) error {
			data := jsonBytes(doc{"id": stepExecID, "pipelineRunId": trialID, "pipelineStepId": trialID + ":step:0"})
			if err := exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,experiment_id,variant,parent_execution_id,pipeline_id,profile_id,model,account_id,started_at,ended_at,data)
				VALUES($1,'agent_run','succeeded','confirmed','hello-world',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`,
				stepExecID, expID, variant, trialID+":step:0", pipelineID, profileID, model, strPtr(accountID), startAt, endAt, data); err != nil {
				return err
			}
			inBasis, outBasis, crBasis := billingBasis, billingBasis, billingBasis
			if billingBasis == "estimated" {
				inBasis, outBasis, crBasis = "measured", "measured", "measured"
			}
			return exec(q, `INSERT INTO execution_totals(execution_id,project_id,model,billing_basis,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,tokens_cache_read,tokens_cache_read_basis)
				VALUES($1,'hello-world',$2,$3,$4,$5,$6,$7,$8,$9)`,
				stepExecID, model, billingBasis, tokIn, inBasis, tokOut, outBasis, tokCR, crBasis)
		}

		// Exp1: codex trial 1a (succeeded, subscription: 1000 in + 500 out + 200 cr = 1700 tokens, 2000ms, metric 0.5)
		t1 := now.Add(-4 * time.Hour)
		if err := insertTrial("tr-1a", exp1ID, "coding-task", "codex", "succeeded", "confirmed", e1.Examples[0].ID, codexRef, t1, t1.Add(2*time.Second), 2000, 0, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-1a", "step-1a", exp1ID, "codex", "coding-task", "coding-worker", "gpt-5.3-codex", "acc-sub-1", "subscription", t1, t1.Add(2*time.Second), 1000, 500, 200); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp1ID, ProjectID: "hello-world", TrialIDs: []string{"tr-1a"}, Evaluator: "script:example.checks:metric", EvaluatorVersion: "v1", Form: "metric", Value: 0.5}); err != nil {
			return err
		}

		// Exp1: codex trial 1b (failed, confirmed, subscription: 400 in + 100 out = 500 tokens, 4000ms, unscored)
		t2 := now.Add(-3 * time.Hour)
		if err := insertTrial("tr-1b", exp1ID, "coding-task", "codex", "failed", "confirmed", e1.Examples[0].ID, codexRef, t2, t2.Add(4*time.Second), 4000, 1, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-1b", "step-1b", exp1ID, "codex", "coding-task", "coding-worker", "gpt-5.3-codex", "acc-sub-1", "subscription", t2, t2.Add(4*time.Second), 400, 100, 0); err != nil {
			return err
		}

		// Exp1: claude trial 1c (succeeded, confirmed, metered estimated: 2000 in + 1000 out = 3000 tokens, 6000ms, metric 1.0)
		t3 := now.Add(-2 * time.Hour)
		if err := insertTrial("tr-1c", exp1ID, "coding-task-reviewed", "claude", "succeeded", "confirmed", e1.Examples[0].ID, claudeRef, t3, t3.Add(6*time.Second), 6000, 0, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-1c", "step-1c", exp1ID, "claude", "coding-task-reviewed", "claude-reviewer", "claude-sonnet-4-6", "", "estimated", t3, t3.Add(6*time.Second), 2000, 1000, 0); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp1ID, ProjectID: "hello-world", TrialIDs: []string{"tr-1c"}, Evaluator: "script:example.checks:metric", EvaluatorVersion: "v1", Form: "metric", Value: 1.0}); err != nil {
			return err
		}

		// Exp1: claude unconfirmed & cancelled trials (must be excluded from leaderboard)
		if err := insertTrial("tr-1d-unconf", exp1ID, "coding-task-reviewed", "claude", "succeeded", "unconfirmed", e1.Examples[0].ID, claudeRef, t3, t3.Add(1*time.Second), 1000, 1, 0); err != nil {
			return err
		}
		if err := insertTrial("tr-1e-cancel", exp1ID, "coding-task-reviewed", "claude", "cancelled", "confirmed", e1.Examples[0].ID, claudeRef, t3, t3.Add(1*time.Second), 1000, 2, 0); err != nil {
			return err
		}

		// Exp2: same codex variant on same bench:bug-finding use case (newer: -1h), 800 in + 200 out = 1000 tokens, 3000ms, metric 1.0
		t4 := now.Add(-1 * time.Hour)
		if err := insertTrial("tr-2a", exp2ID, "coding-task", "codex", "succeeded", "confirmed", e2.Examples[0].ID, codexRef, t4, t4.Add(3*time.Second), 3000, 0, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-2a", "step-2a", exp2ID, "codex", "coding-task", "coding-worker", "gpt-5.3-codex", "acc-sub-1", "subscription", t4, t4.Add(3*time.Second), 800, 200, 0); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp2ID, ProjectID: "hello-world", TrialIDs: []string{"tr-2a"}, Evaluator: "script:example.checks:metric", EvaluatorVersion: "v1", Form: "metric", Value: 1.0}); err != nil {
			return err
		}

		// Exp2: old trial 120 days ago (excluded at default days=90, included at days=180)
		tOld := now.Add(-120 * 24 * time.Hour)
		if err := insertTrial("tr-2-old", exp2ID, "coding-task", "codex", "succeeded", "confirmed", e2.Examples[0].ID, codexRef, tOld, tOld.Add(1*time.Second), 1000, 1, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-2-old", "step-2-old", exp2ID, "codex", "coding-task", "coding-worker", "gpt-5.3-codex", "acc-sub-1", "subscription", tOld, tOld.Add(1*time.Second), 100, 50, 0); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp2ID, ProjectID: "hello-world", TrialIDs: []string{"tr-2-old"}, Evaluator: "script:example.checks:metric", EvaluatorVersion: "v1", Form: "metric", Value: 0.0}); err != nil {
			return err
		}

		// Exp3: Real Work use case (real:prod-alert-triage)
		t5 := now.Add(-30 * time.Minute)
		if err := insertTrial("tr-3a", exp3ID, "coding-task", "triage-v1", "succeeded", "confirmed", e3.Examples[0].ID, triageRef, t5, t5.Add(5*time.Second), 5000, 0, 0); err != nil {
			return err
		}
		if err := insertAgentStep("tr-3a", "step-3a", exp3ID, "triage-v1", "coding-task", "coding-worker", "gpt-5.3-codex", "acc-sub-1", "subscription", t5, t5.Add(5*time.Second), 500, 100, 0); err != nil {
			return err
		}
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp3ID, ProjectID: "hello-world", TrialIDs: []string{"tr-3a"}, Evaluator: "script:example.checks:pass_fail", EvaluatorVersion: "v1", Form: "pass_fail", Value: true}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Deliverable 2 check: lab_experiment_get enriches subscription LabTrialRecord spend.tokens and subscription CostFigure.
	codeGet, rawGet := env.callJSON(t, "POST", "/api/ops/lab_experiment_get", doc{"experimentId": exp1ID}, env.ownerHeaders())
	if codeGet != 200 {
		t.Fatalf("lab_experiment_get = %d %v", codeGet, rawGet)
	}
	var gotExp contract.LabExperimentGetReply
	if err := json.Unmarshal(jsonBytes(rawGet), &gotExp); err != nil {
		t.Fatal(err)
	}
	var found1a bool
	for _, tr := range gotExp.Trials {
		if tr.Id != "tr-1a" {
			continue
		}
		found1a = true
		if tr.Spend.Tokens == nil || *tr.Spend.Tokens != 1700 {
			t.Fatalf("tr-1a spend.tokens = %v, want 1700", tr.Spend.Tokens)
		}
		if len(tr.Cost) != 1 || tr.Cost[0].Basis != contract.ResourceBasisSubscription || deref(tr.Cost[0].Currency) != "USD" || tr.Cost[0].Value == nil {
			t.Fatalf("tr-1a cost = %+v, want 1 subscription USD figure", tr.Cost)
		}
		want1aUsd := 1000*0.000002 + 500*0.000010 + 200*0.0000005 // 0.0071
		if math.Abs(*tr.Cost[0].Value-want1aUsd) > 1e-9 {
			t.Fatalf("tr-1a costUsd = %v, want %v", *tr.Cost[0].Value, want1aUsd)
		}
	}
	if !found1a {
		t.Fatal("tr-1a missing from lab_experiment_get")
	}

	// 2. Deliverable 1 check: lab_leaderboard validation + aggregation across exp1 & exp2.
	if badCode, _ := env.callJSON(t, "POST", "/api/ops/lab_leaderboard", doc{"days": 0}, env.ownerHeaders()); badCode != 400 {
		t.Fatalf("days=0 status = %d, want 400", badCode)
	}
	callLB := func(req doc) contract.LabLeaderboardReply {
		t.Helper()
		c, raw := env.callJSON(t, "POST", "/api/ops/lab_leaderboard", req, env.ownerHeaders())
		if c != 200 {
			t.Fatalf("lab_leaderboard(%v) = %d %v", req, c, raw)
		}
		var rep contract.LabLeaderboardReply
		if err := json.Unmarshal(jsonBytes(raw), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}

	lb := callLB(doc{"projectId": "hello-world"})
	if len(lb.Collections) < 2 {
		t.Fatalf("collections = %v", lb.Collections)
	}
	if len(lb.Entries) != 3 {
		t.Fatalf("entries count = %d, want 3: %+v", len(lb.Entries), lb.Entries)
	}
	var codexEntry, claudeEntry *contract.LabLeaderboardEntry
	for i := range lb.Entries {
		e := &lb.Entries[i]
		if e.UseCaseId == "bench:bug-finding" && e.VariantLabel == "codex" {
			codexEntry = e
		}
		if e.UseCaseId == "bench:bug-finding" && e.VariantLabel == "claude" {
			claudeEntry = e
		}
	}
	if codexEntry == nil || claudeEntry == nil {
		t.Fatalf("missing expected benchmark entries: %+v", lb.Entries)
	}
	if codexEntry.Collection != "LLM Code Benchmarks" || deref(codexEntry.Model) != "gpt-5.3-codex" || deref(codexEntry.Profile) != "coding-worker" {
		t.Fatalf("codex metadata = %+v", codexEntry)
	}
	if codexEntry.Trials != 3 || codexEntry.SucceededTrials != 2 || codexEntry.FailedTrials != 1 {
		t.Fatalf("codex counts = (%d, %d, %d), want (3, 2, 1)", codexEntry.Trials, codexEntry.SucceededTrials, codexEntry.FailedTrials)
	}
	if codexEntry.ScoreForm != "metric" || codexEntry.ScoreMean == nil || math.Abs(*codexEntry.ScoreMean-0.75) > 1e-9 || *codexEntry.ScoreMin != 0.5 || *codexEntry.ScoreMax != 1.0 {
		t.Fatalf("codex scores = mean:%v min:%v max:%v", codexEntry.ScoreMean, codexEntry.ScoreMin, codexEntry.ScoreMax)
	}
	if codexEntry.WallMsMedian == nil || *codexEntry.WallMsMedian != 3000 {
		t.Fatalf("codex wallMsMedian = %v, want 3000", codexEntry.WallMsMedian)
	}
	if codexEntry.TokensTotal != 3200 || codexEntry.CostBasis != "subscription" || codexEntry.CostUsd == nil {
		t.Fatalf("codex tokens/cost = tokens:%d basis:%s usd:%v", codexEntry.TokensTotal, codexEntry.CostBasis, codexEntry.CostUsd)
	}
	wantCodexUsd := (1000+400+800)*0.000002 + (500+100+200)*0.000010 + 200*0.0000005 // 0.0125
	if math.Abs(*codexEntry.CostUsd-wantCodexUsd) > 1e-9 {
		t.Fatalf("codex costUsd = %v, want %v", *codexEntry.CostUsd, wantCodexUsd)
	}
	if codexEntry.LatestExperimentId != exp2ID {
		t.Fatalf("codex latestExperimentId = %s, want %s", codexEntry.LatestExperimentId, exp2ID)
	}

	if claudeEntry.Trials != 1 || claudeEntry.SucceededTrials != 1 || claudeEntry.FailedTrials != 0 || claudeEntry.TokensTotal != 3000 || claudeEntry.CostBasis != "estimated" {
		t.Fatalf("claude entry = %+v", claudeEntry)
	}
	wantClaudeUsd := 2000*0.000003 + 1000*0.000015 // 0.021
	if claudeEntry.CostUsd == nil || math.Abs(*claudeEntry.CostUsd-wantClaudeUsd) > 1e-9 {
		t.Fatalf("claude costUsd = %v, want %v", claudeEntry.CostUsd, wantClaudeUsd)
	}

	// 3. Collection, useCaseId, and days window filters.
	realOnly := callLB(doc{"collection": "Real Work"})
	if len(realOnly.Entries) != 1 || realOnly.Entries[0].UseCaseId != "real:prod-alert-triage" || realOnly.Entries[0].ScoreForm != "pass_fail" {
		t.Fatalf("Real Work filter = %+v", realOnly.Entries)
	}
	benchKeyOnly := callLB(doc{"collection": "benchmarks", "useCaseId": "bench:bug-finding"})
	if len(benchKeyOnly.Entries) != 2 {
		t.Fatalf("benchmarks key filter = %+v", benchKeyOnly.Entries)
	}
	wide := callLB(doc{"useCaseId": "bench:bug-finding", "days": 180})
	var codexWide *contract.LabLeaderboardEntry
	for i := range wide.Entries {
		if wide.Entries[i].VariantLabel == "codex" {
			codexWide = &wide.Entries[i]
		}
	}
	if codexWide == nil || codexWide.Trials != 4 || codexWide.TokensTotal != 3350 {
		t.Fatalf("180d codex entry = %+v, want 4 trials & 3350 tokens", codexWide)
	}
}
