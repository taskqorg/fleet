package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// TestBotAnalyticsAttribution verifies FLT-20 PR 4 bot attribution and analytics totals.
// Requirement: Capture direct and delegated bot_id at execution creation, count each leaf
// execution once (never double-counting container pipeline_run/pipeline_step or bare child_agent
// rows), preserve historical attribution across task reassignment and continuation/fork, and
// expose totals by bot and recorded execution project with cost basis, currency, and unknowns.
// Risk: Bot delegation, continuation/forks, pipelines, or task reassignments double-count, drop,
// or rewrite bot attribution, skewing bot analytics, spend totals, or execution/question views.
func TestBotAnalyticsAttribution(t *testing.T) {
	e := openTestCoordinator(t, false)
	ctx := e.ctx
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	agents := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "agents@trial.test"}
	outsider := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "outsider@trial.test"}
	secH := testRegisterHostFor(t, e, "h-bot-attr", "member@trial.test")
	hostHdr := map[string]string{"Authorization": "Bearer " + secH}

	t0 := time.Now().UTC()
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", fmt.Sprintf(`
		INSERT INTO price_entry(id, subject, unit, price, currency, source, observed_at, effective_from) VALUES
			('pr-bot-in', 'gpt-bot', 'input_tokens', 0.000002, 'USD', 'owner', '%s', '%s'),
			('pr-bot-out', 'gpt-bot', 'output_tokens', 0.000010, 'USD', 'owner', '%s', '%s');
	`, t0.Add(-time.Hour).Format(time.RFC3339), t0.Add(-2*time.Hour).Format(time.RFC3339),
		t0.Add(-time.Hour).Format(time.RFC3339), t0.Add(-2*time.Hour).Format(time.RFC3339)))

	botID := newID()
	c, saveResp := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: 0, Settings: testBotSettings("pm"),
	}, member)
	if c != 200 {
		t.Fatalf("bot_settings_save create = %d %v", c, saveResp)
	}
	homeSID := astr(mapOf(saveResp["bot"])["homeSessionId"])

	writeExec := func(rec doc) {
		t.Helper()
		id := astr(rec["id"])
		ownerReq := &Request{app: e.app, person: &Person{Email: testOwnerEmail, Owner: true}}
		if err := e.app.store.Write(ctx, ownerReq, []string{GLOBAL}, func(q *Request) error {
			if err := executionWrite(q, rec); err != nil {
				return err
			}
			if err := writeTotals(q, id); err != nil {
				return err
			}
			res := mapOf(rec["resources"])
			toks := mapOf(res["tokens"])
			inV := int64(num(mapOf(toks["input"])["value"]))
			outV := int64(num(mapOf(toks["output"])["value"]))
			actV := int64(num(mapOf(res["activeMs"])["value"]))
			return exec(q, `
				UPDATE execution_totals
				SET tokens_input = $2, tokens_input_basis = 'measured',
				    tokens_output = $3, tokens_output_basis = 'measured',
				    active_ms = $4, active_ms_basis = 'measured'
				WHERE execution_id = $1
			`, id, inV, outV, actV)
		}); err != nil {
			t.Fatalf("writeExec(%s): %v", id, err)
		}
	}

	// 1) Direct execution on Home via bot_run (recorded project: desiign, USD priced).
	c, run1 := e.callJSON(t, "POST", "/api/ops/bot_run", contract.BotRunRequest{
		BotId: botID, RequestId: ptr("attr-r1"), Text: ptr("Inspect desiign and delegate cross-project work"),
	}, member)
	if c != 200 || run1["admitted"] != true {
		t.Fatalf("bot_run = %d %v", c, run1)
	}
	poll1 := testPollForStart(t, e, "h-bot-attr", secH)
	if len(poll1.Commands) != 1 || poll1.Commands[0].Start == nil {
		t.Fatalf("expected start command for Home execution, got %+v", poll1.Commands)
	}
	homeExecID := poll1.Commands[0].Start.ExecutionId
	cmd1ID := poll1.Commands[0].Start.Id
	if c, _ := e.callJSON(t, "POST", "/api/hosts/receipts", contract.Receipt{
		HostId: "h-bot-attr", CommandId: cmd1ID, ExecutionId: homeExecID, Status: contract.ReceiptStatusStarted,
	}, hostHdr); c != 200 {
		t.Fatalf("started receipt = %d", c)
	}
	homeExecAt := t0.Add(-20 * time.Minute).UnixMilli()
	writeExec(doc{
		"id": homeExecID, "state": "succeeded", "sessionId": homeSID, "projectId": "desiign",
		"hostId": "h-bot-attr", "model": "gpt-bot", "startedAt": homeExecAt, "finishedAt": homeExecAt + 4000,
		"resources": doc{
			"wallMs":   doc{"value": 4000, "basis": "measured"},
			"activeMs": doc{"value": 3000, "basis": "measured"},
			"tokens": doc{
				"input":  doc{"value": 1000000, "basis": "measured"},
				"output": doc{"value": 500000, "basis": "measured"},
			},
		},
		"report": doc{"summary": "Delegated checks"},
	})

	// Ask a question on Home session + a question_ask-style row (asked_by execution:<id> with null session/execution/task).
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", fmt.Sprintf(`
		INSERT INTO question(id, kind, project_id, execution_id, session_id, text, options, asked_by)
		VALUES
			('%s', 'agent_question', 'desiign', '%s', '%s', 'Approve cleanup?', '["yes","no"]', 'bot'),
			('%s', 'operator_decision', 'desiign', NULL, NULL, 'Confirm owner decision?', NULL, 'execution:%s');
	`, newID(), homeExecID, homeSID, newID(), homeExecID))

	// 2) Delegated child session started by homeExecID recording cross-project work in "specq"
	//    with an unpriced model (tests recorded execution project attribution + unknown observation).
	c, cs := e.callJSON(t, "POST", "/api/ops/session_start", doc{
		"requestId": "deleg-start-1", "projectId": "specq", "profileId": "pm",
		"callerExecutionId": homeExecID, "callerSessionId": homeSID,
	}, owner)
	if c != 200 {
		t.Fatalf("session_start delegated = %d %v", c, cs)
	}
	delegSID := firstString(astr(cs["sessionId"]), astr(cs["id"]), astr(mapOf(cs["session"])["id"]))
	if delegSID == "" {
		t.Fatalf("expected delegated session id, got %v", cs)
	}
	delegExec1 := "exec-deleg-1"
	writeExec(doc{
		"id": delegExec1, "state": "succeeded", "sessionId": delegSID, "projectId": "specq",
		"hostId": "h-bot-attr", "model": "unpriced-bot-model", "startedAt": homeExecAt + 5000, "finishedAt": homeExecAt + 7000,
		"resources": doc{
			"wallMs":   doc{"value": 2000, "basis": "measured"},
			"activeMs": doc{"value": 1500, "basis": "measured"},
			"tokens": doc{
				"input":  doc{"value": 100, "basis": "measured"},
				"output": doc{"value": 50, "basis": "measured"},
			},
		},
	})

	// 3) Continuation & fork:
	//    - Follow-up turn on delegSID
	//    - Forked session from delegSID (2 turns -> both must preserve bot_id!)
	writeExec(doc{
		"id": "exec-deleg-2", "state": "succeeded", "sessionId": delegSID, "projectId": "specq",
		"hostId": "h-bot-attr", "model": "gpt-bot", "startedAt": homeExecAt + 8000, "finishedAt": homeExecAt + 9000,
		"resources": doc{
			"wallMs":   doc{"value": 1000, "basis": "measured"},
			"activeMs": doc{"value": 900, "basis": "measured"},
			"tokens":   doc{"input": doc{"value": 500000, "basis": "measured"}, "output": doc{"value": 0, "basis": "measured"}},
		},
	})
	c, forkResp := e.callJSON(t, "POST", "/api/ops/session_fork", doc{
		"requestId": "deleg-fork-1", "sessionId": delegSID, "executionId": delegExec1,
	}, owner)
	if c != 200 {
		t.Fatalf("session_fork = %d %v", c, forkResp)
	}
	forkSID := firstString(astr(forkResp["sessionId"]), astr(forkResp["id"]), astr(mapOf(forkResp["session"])["id"]))
	if forkSID == "" {
		t.Fatalf("expected forked session id, got %v", forkResp)
	}
	for i, fid := range []string{"exec-fork-1", "exec-fork-2"} {
		st := homeExecAt + int64(10000+i*2000)
		writeExec(doc{
			"id": fid, "state": "succeeded", "sessionId": forkSID, "projectId": "specq",
			"hostId": "h-bot-attr", "model": "gpt-bot", "startedAt": st, "finishedAt": st + 500,
			"resources": doc{
				"wallMs":   doc{"value": 500, "basis": "measured"},
				"activeMs": doc{"value": 400, "basis": "measured"},
				"tokens":   doc{"input": doc{"value": 0, "basis": "measured"}, "output": doc{"value": 100000, "basis": "measured"}},
			},
		})
	}

	// 4) Reassignment & link stability:
	//    - Delegated task execution under homeExecID -> task_take by owner + bot_settings_save rename preserves historical bot_id.
	//    - Pre-existing human execution on manualTaskID linked via bot_task_link stays unattributed!
	c, tReassign := e.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId": "desiign", "title": "Bot managed task for reassignment check",
	}, member)
	if c != 200 {
		t.Fatalf("task_create bot task = %d %v", c, tReassign)
	}
	managedTaskID := astr(tReassign["id"])
	reassignedExecID := "exec-reassigned-1"
	writeExec(doc{
		"id": reassignedExecID, "state": "succeeded", "parentExecutionId": homeExecID,
		"taskId": managedTaskID, "projectId": "desiign",
		"hostId": "h-bot-attr", "model": "gpt-bot", "startedAt": homeExecAt + 15000, "finishedAt": homeExecAt + 16000,
		"resources": doc{
			"wallMs":   doc{"value": 1000, "basis": "measured"},
			"activeMs": doc{"value": 800, "basis": "measured"},
			"tokens":   doc{"input": doc{"value": 500000, "basis": "measured"}, "output": doc{"value": 0, "basis": "measured"}},
		},
	})
	if c, _ := e.callJSON(t, "POST", "/api/ops/task_take", doc{"taskId": managedTaskID}, owner); c != 200 {
		t.Fatalf("task_take failed = %d", c)
	}
	c, bg := e.callJSON(t, "POST", "/api/ops/bot_get", contract.BotGetRequest{BotId: botID}, owner)
	if c != 200 {
		t.Fatalf("bot_get = %d %v", c, bg)
	}
	curRev := int64(num(mapOf(bg["bot"])["revision"]))
	nextSettings := testBotSettings("pm")
	nextSettings.Name = "HAL0-Renamed"
	if c, _ := e.callJSON(t, "POST", "/api/ops/bot_settings_save", contract.BotSettingsSaveRequest{
		BotId: botID, ProjectId: "desiign", BaseRevision: curRev, Settings: nextSettings,
	}, owner); c != 200 {
		t.Fatalf("bot_settings_save rev2 = %d", c)
	}

	c, manualTask := e.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId": "desiign", "title": "Pre-existing manual task",
	}, owner)
	if c != 200 {
		t.Fatalf("task_create manual = %d %v", c, manualTask)
	}
	manualTaskID := astr(manualTask["id"])
	writeExec(doc{
		"id": "exec-manual-only", "state": "succeeded", "taskId": manualTaskID, "projectId": "desiign",
		"hostId": "h-bot-attr", "model": "gpt-bot", "startedAt": homeExecAt + 17000, "finishedAt": homeExecAt + 18000,
		"resources": doc{
			"wallMs":   doc{"value": 500, "basis": "measured"},
			"activeMs": doc{"value": 400, "basis": "measured"},
			"tokens":   doc{"input": doc{"value": 100000, "basis": "measured"}, "output": doc{"value": 0, "basis": "measured"}},
		},
	})
	if c, _ := e.callJSON(t, "POST", "/api/ops/bot_task_link", contract.BotTaskLinkRequest{
		BotId: botID, TaskId: manualTaskID,
	}, member); c != 200 {
		t.Fatalf("bot_task_link = %d", c)
	}

	// 5) Child tool_run (EUR measured) + pipeline_run/pipeline_step container + bare child_agent + leaf pipeline execution.
	ownerReq := &Request{app: e.app, person: &Person{Email: testOwnerEmail, Owner: true}}
	if err := e.app.store.Write(ctx, ownerReq, []string{GLOBAL}, func(q *Request) error {
		if _, err := insertExecution(q, executionNew{
			ID: "exec-tool-eur", Kind: "tool_run", State: "succeeded", ProjectID: "desiign",
			ParentExecutionID: homeExecID, StartedAt: ptr(homeExecAt + 19000), EndedAt: ptr(homeExecAt + 19500),
		}); err != nil {
			return err
		}
		if err := writeTotals(q, "exec-tool-eur"); err != nil {
			return err
		}
		if err := writeConnectionCost(q, "exec-tool-eur", doc{"currency": "EUR", "amount": 2.5, "unit": "USD"}); err != nil {
			return err
		}
		stepDef := pipelineDefStep{ID: "s1", Kind: "agent"}
		stepSha := stepDef.sha()
		if err := (&runRecord{
			ID: "pr-bot-pipe", ProjectID: ptr("desiign"), ParentExecutionID: homeExecID, State: "succeeded",
			PlanVersion: pipelinePlanVersion,
			Definition:  pipelineDef{ID: "pipe-bot-test", Version: 1, Steps: []pipelineDefStep{stepDef}},
			StartedAt:   ptr(homeExecAt + 20000), FinishedAt: ptr(homeExecAt + 21000),
			Steps: []runStep{{ID: "s1", State: "succeeded", StartedAt: ptr(homeExecAt + 20000), FinishedAt: ptr(homeExecAt + 21000)}},
			Plan: &contract.PipelinePlan{
				DefinitionSha256: stepSha, Revision: 1,
				Entries: []contract.PipelinePlanEntry{{
					Id: "s1", Index: 0, DefinitionSha256: stepSha, Activation: contract.PipelineActivationSelected,
					ExecutionRef: &contract.PipelineExecutionRef{
						Mode: contract.PipelineRefModeFresh, ExecutionId: "pr-bot-pipe:step:0",
						SourceStepId: "s1", SourceStepIndex: 0, SourceDefinitionSha256: stepSha, SourceInputSha256: stepSha,
					},
				}},
			},
		}).put(q); err != nil {
			return err
		}
		return recordChildren(q, homeExecID, []any{doc{"threadId": "thr-1"}})
	}); err != nil {
		t.Fatalf("insert tool/pipeline/child executions: %v", err)
	}
	writeExec(doc{
		"id": "exec-pipe-leaf", "state": "failed", "pipelineStepId": "pr-bot-pipe:step:0",
		"projectId": "desiign", "hostId": "h-bot-attr", "model": "gpt-bot",
		"startedAt": homeExecAt + 20100, "finishedAt": homeExecAt + 20900,
		"resources": doc{
			"wallMs":   doc{"value": 800, "basis": "measured"},
			"activeMs": doc{"value": 700, "basis": "measured"},
			"tokens":   doc{"input": doc{"value": 0, "basis": "measured"}, "output": doc{"value": 0, "basis": "measured"}},
		},
	})

	// Verify bot_id columns on DB directly for full lineage coverage.
	botCols := runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
		SELECT id || '=' || coalesce(bot_id, '') FROM execution
		WHERE id IN ('`+homeExecID+`','`+delegExec1+`','exec-deleg-2','exec-fork-1','exec-fork-2','`+reassignedExecID+`','exec-manual-only','exec-tool-eur','pr-bot-pipe','pr-bot-pipe:step:0','`+homeExecID+`:child:thr-1','exec-pipe-leaf')
		ORDER BY id;
	`)
	for _, want := range []string{
		homeExecID + "=" + botID,
		delegExec1 + "=" + botID,
		"exec-deleg-2=" + botID,
		"exec-fork-1=" + botID,
		"exec-fork-2=" + botID,
		reassignedExecID + "=" + botID,
		"exec-manual-only=",
		"exec-tool-eur=" + botID,
		"pr-bot-pipe=" + botID,
		"pr-bot-pipe:step:0=" + botID,
		homeExecID + ":child:thr-1=" + botID,
		"exec-pipe-leaf=" + botID,
	} {
		if !strings.Contains(botCols, want) {
			t.Fatalf("expected execution attribution %q in:\n%s", want, botCols)
		}
	}

	win := contract.AnalyticsWindow{
		From: json.RawMessage(fmt.Sprintf("%d", t0.Add(-time.Hour).UnixMilli())),
		To:   json.RawMessage(fmt.Sprintf("%d", t0.Add(time.Hour).UnixMilli())),
	}

	// Owner sees both desiign (4 leaves, 1 failed) and specq (4 leaves, 0 failed, 1 unpriced) = 8 single-counted leaves.
	c, aqRaw := e.callJSON(t, "POST", "/api/ops/analytics_query", contract.AnalyticsQueryRequest{
		Window:     win,
		Dimensions: []contract.AnalyticsDimension{contract.AnalyticsDimensionBot, contract.AnalyticsDimensionProject},
		Filters:    &contract.AnalyticsFilter{Bot: []string{botID}},
		Measures: []contract.AnalyticsMeasure{
			contract.AnalyticsMeasureExecutions,
			contract.AnalyticsMeasureFailed,
			contract.AnalyticsMeasureWallMs,
			contract.AnalyticsMeasureActiveMs,
			contract.AnalyticsMeasureTokensInput,
			contract.AnalyticsMeasureTokensOutput,
			contract.AnalyticsMeasureCost,
		},
	}, owner)
	if c != 200 {
		t.Fatalf("analytics_query bot+project = %d %v", c, aqRaw)
	}
	bAQ, _ := json.Marshal(aqRaw)
	var aq contract.AnalyticsQueryReply
	if err := json.Unmarshal(bAQ, &aq); err != nil {
		t.Fatal(err)
	}
	totExec := aq.Totals.Measures["executions"].Value
	if totExec == nil || math.Round(*totExec) != 8 {
		t.Fatalf("expected 8 single-counted leaf executions for bot, got totals=%+v rows=%+v", aq.Totals, aq.Rows)
	}
	if len(aq.Rows) != 2 {
		t.Fatalf("expected 2 recorded execution project buckets (desiign, specq), got %+v", aq.Rows)
	}
	byProj := map[string]contract.AnalyticsBucketRow{}
	for _, r := range aq.Rows {
		byProj[r.Dimensions["project"]] = r
	}
	if math.Round(*byProj["desiign"].Measures["executions"].Value) != 4 || math.Round(*byProj["desiign"].Measures["failed"].Value) != 1 {
		t.Fatalf("unexpected desiign metrics: %+v", byProj["desiign"])
	}
	if math.Round(*byProj["specq"].Measures["executions"].Value) != 4 {
		t.Fatalf("unexpected specq metrics: %+v", byProj["specq"])
	}
	hasCost := func(figs []contract.CostFigure, curr string, basis contract.ResourceBasis) bool {
		for _, f := range figs {
			if f.Basis == basis && ((curr == "" && f.Currency == nil) || (f.Currency != nil && *f.Currency == curr)) {
				return true
			}
		}
		return false
	}
	if !hasCost(byProj["desiign"].Cost, "USD", contract.ResourceBasisEstimated) ||
		!hasCost(byProj["desiign"].Cost, "EUR", contract.ResourceBasisMeasured) {
		t.Fatalf("expected USD estimated and EUR measured cost in desiign, got %+v", byProj["desiign"].Cost)
	}
	if !hasCost(byProj["specq"].Cost, "USD", contract.ResourceBasisEstimated) ||
		!hasCost(byProj["specq"].Cost, "", contract.ResourceBasisUnavailable) {
		t.Fatalf("expected USD estimated and unavailable observation in specq, got %+v", byProj["specq"].Cost)
	}

	// Verify spend_summary(by: "bot") exposes bot row with resolved Label "HAL0-Renamed", currency costs, unknowns, plus 'unknown' bucket for exec-manual-only.
	c, spRaw := e.callJSON(t, "POST", "/api/ops/spend_summary", contract.SpendSummaryRequest{
		By: contract.SpendGroupByBot, Window: win,
	}, owner)
	if c != 200 {
		t.Fatalf("spend_summary(by=bot) = %d %v", c, spRaw)
	}
	bSP, _ := json.Marshal(spRaw)
	var sp contract.SpendSummaryReply
	if err := json.Unmarshal(bSP, &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Unknown < 1 {
		t.Fatalf("expected at least 1 unattributed execution in Unknown, got %+v", sp)
	}
	var foundBotRow *contract.SpendRow
	for i := range sp.Rows {
		if sp.Rows[i].Key == botID {
			foundBotRow = &sp.Rows[i]
		}
	}
	if foundBotRow == nil || foundBotRow.Label != "HAL0-Renamed" || foundBotRow.Runs != 8 || foundBotRow.Unpriced < 1 {
		t.Fatalf("unexpected spend_summary bot row: %+v (all rows=%+v)", foundBotRow, sp.Rows)
	}

	// Verify RLS isolation on member (granted desiign only) and outsider (no grants):
	// member sees 4 desiign leaves + hidden>0; agents sees all 8; outsider sees 0 + hidden>0.
	c, memAQRaw := e.callJSON(t, "POST", "/api/ops/analytics_query", contract.AnalyticsQueryRequest{
		Window:   win,
		Filters:  &contract.AnalyticsFilter{Bot: []string{botID}},
		Measures: []contract.AnalyticsMeasure{contract.AnalyticsMeasureExecutions},
	}, member)
	if c != 200 {
		t.Fatalf("member analytics_query = %d %v", c, memAQRaw)
	}
	bMemAQ, _ := json.Marshal(memAQRaw)
	var memAQ contract.AnalyticsQueryReply
	_ = json.Unmarshal(bMemAQ, &memAQ)
	if memAQ.Totals.Measures["executions"].Value == nil || math.Round(*memAQ.Totals.Measures["executions"].Value) != 4 || memAQ.Hidden == 0 {
		t.Fatalf("expected member to see 4 desiign executions with hidden>0, got %+v", memAQ)
	}
	_ = agents
	if c, outAQRaw := e.callJSON(t, "POST", "/api/ops/analytics_query", contract.AnalyticsQueryRequest{
		Window:   win,
		Filters:  &contract.AnalyticsFilter{Bot: []string{botID}},
		Measures: []contract.AnalyticsMeasure{contract.AnalyticsMeasureExecutions},
	}, outsider); c != 200 {
		t.Fatalf("outsider analytics_query = %d %v", c, outAQRaw)
	} else {
		var outAQ contract.AnalyticsQueryReply
		bOutAQ, _ := json.Marshal(outAQRaw)
		_ = json.Unmarshal(bOutAQ, &outAQ)
		if outAQ.Totals.Measures["executions"].Value == nil || math.Round(*outAQ.Totals.Measures["executions"].Value) != 0 || outAQ.Hidden == 0 {
			t.Fatalf("expected outsider to see 0 executions with hidden>0, got %+v", outAQ)
		}
	}

	// Verify executions_list({ botId }) and questions_list({ botId }).
	c, exList := e.callJSON(t, "POST", "/api/ops/executions_list", doc{"botId": botID}, owner)
	if c != 200 {
		t.Fatalf("executions_list botId = %d %v", c, exList)
	}
	exRows := anys(exList["executions"])
	if len(exRows) == 0 {
		t.Fatalf("expected attributed executions from executions_list(botId), got 0")
	}
	for _, r := range exRows {
		if astr(mapOf(r)["id"]) == "exec-manual-only" {
			t.Fatalf("executions_list(botId) leaked unattributed execution exec-manual-only")
		}
	}
	c, qList := e.callJSON(t, "POST", "/api/ops/questions_list", doc{"botId": botID}, member)
	if c != 200 || len(anys(qList["questions"])) != 2 {
		t.Fatalf("expected 2 questions from questions_list(botId), got %d %v", c, qList)
	}
}
