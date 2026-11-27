package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Requirement: delivered results reach acceptance from the same wait and question
// created by worker_done or an explicit acceptance request. Policy, live evidence,
// and a rejection of the result still control whether Fleet can answer.
func TestDeliveredTaskAcceptance(t *testing.T) {
	e := openTestCoordinator(t, false)
	headers := e.ownerHeaders()
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for id, auto := range map[string]any{"delivery-auto": nil, "delivery-manual": false} {
		var created map[string]any
		e.opInto(t, "project_create", map[string]any{"id": id, "requestId": id, "content": map[string]any{
			"settings": map[string]any{"id": id, "name": id, "contextFiles": []string{"rules.md"}, "repos": []map[string]string{{"remote": "https://github.com/taskqorg/fleet.git"}}, "integration": map[string]any{
				"mode": "pr", "autoMerge": true, "autoAccept": auto,
			}},
			"contextFiles": []map[string]string{{"path": "rules.md", "text": "Delivery fixture."}},
		}}, headers, &created)
	}

	landedAt := time.Now().UTC().Truncate(time.Second)
	for i, tc := range []struct {
		name, requirements                                                               string
		projectManual, liveCheck, rejected, missingReport, existingOffer, queuedRecovery bool
	}{
		{name: "worker report", requirements: "{}"},
		{name: "post-delivery verification", requirements: "{}", liveCheck: true},
		{name: "project opt-out", requirements: "{}", projectManual: true},
		{name: "human required", requirements: `{"requireHumanAcceptance":true}`},
		{name: "human required with malformed unrelated metadata", requirements: `{"requireHumanAcceptance":true,"branch":[]}`},

		{name: "prior rejection", requirements: "{}", rejected: true},
		{name: "verified delivery without work report", requirements: "{}", missingReport: true},
		{name: "delivery supersedes unhanded recovery", requirements: "{}", missingReport: true, queuedRecovery: true},
		{name: "existing offer without work report", requirements: "{}", missingReport: true, existingOffer: true},
		{name: "manual delivery without work report", requirements: `{"autoAccept":false}`, missingReport: true},
		{name: "task requires live check before manual offer", requirements: `{"autoAccept":false}`, missingReport: true, liveCheck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := "delivery-auto"
			if tc.projectManual {
				project = "delivery-manual"
			}
			pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: int64(i + 1), Delivered: true, Stage: "installed",
				HeadSHA: strings.Repeat("a", 40), MergeSHA: strings.Repeat("b", 40),
				ReleaseCovered: true, ReleaseTarget: strings.Repeat("b", 40), InstalledOn: []taskDeliveryHostProof{{Target: "coordinator", Version: "1.261127.50"}},
				MergedAt: landedAt.Add(-time.Minute).Format(time.RFC3339), DeliveredAt: landedAt.Format(time.RFC3339)}
			pr.URL = fmt.Sprintf("https://github.com/%s/pull/%d", pr.Repo, pr.Number)
			id, expected := fmt.Sprintf("delivery-task-%d", i), "Deliver the task."
			if tc.liveCheck {
				expected += "\nLanding: wait — live check: verify on production."
			}
			exec(t, `INSERT INTO task (id,project_id,title,state,expected_outcome,completion_requirements,result_destination)
				VALUES ($1,$2,$3,'in_progress',$4,$5,$6)`, id, project, tc.name, expected, tc.requirements, pr.URL)
			sid := "session-" + id
			exec(t, `INSERT INTO session (id,project_id,task_id,directed_by) VALUES ($1,$2,$3,$4)`, sid, project, id, testOwnerEmail)
			report := func(eid string, end time.Time, reportOutcome string, liveKey string) {
				t.Helper()
				exec(t, `INSERT INTO execution (id,kind,state,observation,project_id,task_id,session_id,ended_at,data,directed_by)
					VALUES ($1,'agent_run','succeeded','confirmed',$2,$3,$4,$5,jsonb_build_object('finishedAt',$6::bigint),$7)`, eid, project, id, sid, end, end.UnixMilli(), testOwnerEmail)
				if reportOutcome == "" {
					if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
						_, err := settleIdleTask(q, id)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					return
				}
				var reply contract.WorkerDoneReply
				body := map[string]any{"executionId": eid, "outcome": reportOutcome, "summary": "Confirmed task result"}
				if liveKey != "" {
					body["liveCheck"] = liveKey
				}
				e.opInto(t, "worker_done", body, headers, &reply)
				if reply.Action != "accepted" {
					t.Fatalf("worker_done: %+v", reply)
				}
			}
			get := func() contract.TaskDetail {
				t.Helper()
				var task contract.TaskDetail
				e.opInto(t, "task_get", map[string]any{"taskId": id}, headers, &task)
				return task
			}
			offer := func() contract.QuestionRow {
				t.Helper()
				var question contract.QuestionRow
				e.opInto(t, "task_request_acceptance", map[string]any{"taskId": id, "text": "Review my result"}, headers, &question)
				return question
			}
			open := func() contract.QuestionRow {
				t.Helper()
				task := get()
				if task.State != "waiting" || deref(task.WaitReason) != awaitingResult {
					t.Fatalf("task state = %s (%s), want waiting (%s)", task.State, deref(task.WaitReason), awaitingResult)
				}
				var result contract.QuestionsListReply
				e.opInto(t, "questions_list", map[string]any{"taskId": id, "state": "open", "kind": "acceptance"}, headers, &result)
				if len(result.Questions) != 1 {
					t.Fatalf("want one open acceptance question, got %+v", result.Questions)
				}
				return result.Questions[0]
			}
			retain := func(pr deliveryMergedPR) {
				t.Helper()
				if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
					return deliverySave(q, deliveryState{repos: map[string]*deliveryRepo{
						pr.Repo: {projects: []string{project}, delivered: []deliveryMergedPR{pr}},
					}})
				}); err != nil {
					t.Fatal(err)
				}
			}
			questionCount := func() int {
				t.Helper()
				var count int
				if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM question WHERE task_id=$1 AND kind='acceptance' AND state='open'", id).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			outcome := "succeeded"
			if tc.missingReport {
				outcome = ""
			}
			report("run-"+id, landedAt.Add(-time.Second), outcome, "")
			var original contract.QuestionRow
			if tc.existingOffer {
				original = offer()
			} else if !tc.missingReport && !tc.liveCheck {
				original = open()
			}
			if tc.rejected {
				if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
					_, err := answerQuestion(q, original.Id, "rejected", testOwnerEmail, nil)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.liveCheck {
				// Generic success after the delivery timestamp cannot satisfy a named live check.
				report("generic-"+id, landedAt.Add(time.Second), "succeeded", "")
				if questionCount() != 0 {
					t.Fatal("generic work report opened an acceptance offer before live verification")
				}
			}
			var automaticExecution string
			if tc.queuedRecovery {
				if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return wakeTask(q, id) }); err != nil {
					t.Fatal(err)
				}
				runLifecycleWake(t, e, "task:"+id, time.Now().Add(time.Second))
				if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT execution_id FROM task_recovery WHERE task_id=$1", id).Scan(&automaticExecution); err != nil {
					t.Fatal(err)
				}
			}
			retain(pr)
			unknown := pr
			unknown.Delivered, unknown.ReleaseCovered, unknown.InstalledOn = false, false, nil
			retain(unknown) // An observer gap cannot revoke the prior exact installation.
			if tc.liveCheck {
				var proof db.TaskDeliveryResult
				if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) {
					proof, err = q.db().TaskDeliveryProof(q.ctx(), id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				// Use the exact retained target. A later watch turn may end without a report.
				report("verified-"+id, landedAt.Add(2*time.Second), "succeeded", proof.VerificationKey)
				report("watch-"+id, landedAt.Add(3*time.Second), "", "")
			}
			runLifecycleWake(t, e, "task:"+id, time.Now().Add(time.Second))
			if automaticExecution != "" {
				var state, reason string
				if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT state,data->>'automaticStop' FROM execution WHERE id=$1", automaticExecution).Scan(&state, &reason); err != nil {
					t.Fatal(err)
				}
				if state != "cancelled" || reason != "superseded" {
					t.Fatalf("unhanded recovery: %s %s", state, reason)
				}
			}
			retain(pr) // Identical provider evidence is idempotent.
			if tc.rejected {
				if task := get(); task.State != "waiting" || deref(task.WaitReason) != "question" || questionCount() != 0 {
					t.Fatalf("rejected result was reoffered or lost its recovery decision: %+v", task)
				}
				return
			}
			if tc.projectManual || tc.requirements != "{}" {
				if got := open(); original.Id != "" && got.Id != original.Id {
					t.Fatalf("reconciliation replaced the same result offer: %s, want %s", got.Id, original.Id)
				}
				return
			}
			if task := get(); task.State != "completed" || deref(task.AcceptedBy) != deliveryActor {
				t.Fatalf("task state = %s, acceptedBy = %q", task.State, deref(task.AcceptedBy))
			}
			if original.Id != "" {
				var state, answer, answeredBy string
				if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state,coalesce(answer,''),coalesce(answered_by,'')
					FROM question WHERE id=$1`, original.Id).Scan(&state, &answer, &answeredBy); err != nil {
					t.Fatal(err)
				}
				if state != "answered" || answer != "accepted" || answeredBy != deliveryActor {
					t.Fatalf("original question: state=%s answer=%q answeredBy=%q", state, answer, answeredBy)
				}
			}
			var audits int
			if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_log WHERE target=$1 AND field='delivery_reconciled'
				AND payload->>'reason'='auto_accepted' AND value->>'url'=$2`, id, pr.URL).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("delivery audit count = %d, err = %v", audits, err)
			}
		})
	}
	t.Run("explicit earlier result after a failed auxiliary pipeline", func(t *testing.T) {
		for _, tc := range []struct {
			name, wantRecovery string
			selectEarlier      bool
		}{
			{name: "keeps the exact offer", selectEarlier: true},
			{name: "latest failure without an offer", wantRecovery: "Latest work has no confirmed successful result"},
			{name: "offered artifact lost", selectEarlier: true, wantRecovery: "The offer no longer matches the eligible result"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				const project = "delivery-auto"
				id, sid, run, artifact := newID(), newID(), newID(), newID()
				lifecycleTask(t, e, id, sid, run, "succeeded")
				exec(t, "UPDATE task SET project_id=$2 WHERE id=$1", id, project)
				exec(t, "UPDATE session SET project_id=$2 WHERE id=$1", sid, project)
				exec(t, `UPDATE execution SET project_id=$2,data='{"report":{"outcome":"succeeded"}}' WHERE id=$1`, run, project)
				exec(t, `INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,project_id,state)
					VALUES($1,'prod',$2,5,'text/plain','result.txt',$3,$4,$5,'stored')`, artifact, sha256hex([]byte("bytes")), run, id, project)
				exec(t, `INSERT INTO execution (id,kind,state,observation,project_id,task_id,started_at,ended_at,problem)
					VALUES($1,'pipeline_run','failed','confirmed',$2,$3,now()-interval '1 minute',now(),'authority_denied')`, newID(), project, id)
				write := func(fn func(*Request) error) {
					t.Helper()
					if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, fn); err != nil {
						t.Fatal(err)
					}
				}
				get := func() contract.TaskDetail {
					t.Helper()
					var task contract.TaskDetail
					e.opInto(t, "task_get", doc{"taskId": id}, headers, &task)
					return task
				}
				answer := func(question, option string) {
					t.Helper()
					e.opInto(t, "question_answer", doc{"questionId": question, "answer": option}, headers, &contract.QuestionRow{})
				}
				var offer string
				if tc.selectEarlier {
					write(func(q *Request) error { _, err := settleIdleTask(q, id); return err })
					decision := get().NextAction
					if decision == nil || decision.Kind != contract.TaskNextActionKindQuestion {
						t.Fatalf("expected initial failure decision, got %+v", decision)
					}
					answer(decision.Reference, idleAccept)
					action := get().NextAction
					if action == nil || action.Kind != contract.TaskNextActionKindAcceptance {
						t.Fatalf("expected earlier acceptance offer, got %+v", action)
					}
					offer = action.Reference
					if tc.wantRecovery != "" {
						exec(t, "UPDATE artifact SET role='diagnostic' WHERE id=$1", artifact)
					}
				}
				pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: 90001, URL: "https://github.com/taskqorg/fleet/pull/90001",
					Delivered: true, Stage: "installed", Release: "v1.261127.1",
					HeadSHA: strings.Repeat("a", 40), MergeSHA: strings.Repeat("b", 40),
					ReleaseCovered: true, ReleaseTarget: strings.Repeat("b", 40),
					InstalledOn: []taskDeliveryHostProof{{Target: "coordinator", Version: "1.261127.1"}},
					DeliveredAt: landedAt.Format(time.RFC3339)}
				write(func(q *Request) error {
					task, err := taskLoad(q, id)
					if err != nil {
						return err
					}
					return retainTaskDelivery(q, task, pr)
				})
				for attempt := range 2 {
					if attempt == 1 && tc.wantRecovery == "" {
						// An older coordinator can already have created the conflicting decision.
						write(func(q *Request) error {
							task, err := taskLoad(q, id)
							if err != nil {
								return err
							}
							return deliveredTaskDecision(q, task, nil, "Latest work has no confirmed successful result")
						})
						write(func(q *Request) error { _, err := settleIdleTask(q, id); return err })
					}
					write(func(q *Request) error { return wakeTask(q, id) })
					runLifecycleWake(t, e, "task:"+id, time.Now().Add(time.Second))
					task := get()
					if tc.wantRecovery != "" {
						if task.NextAction == nil || task.NextAction.Kind != contract.TaskNextActionKindQuestion {
							t.Fatalf("expected a recovery decision, got %+v", task.NextAction)
						}
						var decision contract.QuestionRow
						for _, question := range task.Questions {
							if question.Id == task.NextAction.Reference {
								decision = question
								break
							}
						}
						if decision.State != "open" || !strings.Contains(decision.Text, tc.wantRecovery) ||
							string(decision.Options) != `["fail task"]` {
							t.Fatalf("wrong recovery decision: %s (%s)", decision.Text, decision.Options)
						}
					} else if task.AcceptedAt != nil || task.NextAction == nil || task.NextAction.Kind != contract.TaskNextActionKindAcceptance ||
						task.NextAction.Reference != offer {
						t.Fatalf("original offer must stay actionable for an explicit answer: %+v", task.NextAction)
					}
				}
				if tc.wantRecovery == "" {
					answer(offer, "accepted")
					task := get()
					if task.State != "completed" || deref(task.AcceptedBy) != testOwnerEmail ||
						len(task.AcceptedArtifactIds) != 1 || task.AcceptedArtifactIds[0] != artifact {
						t.Fatalf("explicit acceptance lost the selected result: state=%s artifacts=%v", task.State, task.AcceptedArtifactIds)
					}
				}
			})
		}
	})
}

// Requirement: deliveryMergedPR JSON payload stored on acceptance question data and audit_log omits internal-only flags.
// Risk: internal fields like Delivered leak into public question data or audit records.
func TestDeliveryTaskReconcileAuditEntry(t *testing.T) {
	// Sanity check JSON round-trip of deliveryMergedPR stored inside questionData & audit_log.
	pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: 787, Title: "Fix", URL: "https://github.com/taskqorg/fleet/pull/787", MergedAt: "2026-11-23T14:53:16Z", Stage: "installed", Release: "v1.261123.1", DeliveredAt: "2026-11-23T15:10:00Z", Delivered: true}
	raw, err := json.Marshal(pr)
	if err != nil || strings.Contains(string(raw), `"Delivered"`) {
		t.Fatalf("unexpected deliveryMergedPR marshal: %s err=%v", raw, err)
	}
}

func TestDeliveryTeardownNeedsMerge(t *testing.T) {
	closed := "45b43cf3-dbe5-4334-b9a4-c4e11e674ca4"
	merged := "08a24aa8-535b-4aa3-acaf-f7f0433132bf"
	deliveryMu.Lock()
	before := deliveryLast
	deliveryLast = deliveryState{repos: map[string]*deliveryRepo{
		"example/repo": {delivered: []deliveryMergedPR{
			{Branch: "fleet/" + closed, Stage: "closed"},
			{Branch: "fleet/" + merged, Stage: "releasing", MergedAt: "2026-11-27T12:00:00Z"},
		}},
	}}
	deliveryMu.Unlock()
	t.Cleanup(func() {
		deliveryMu.Lock()
		deliveryLast = before
		deliveryMu.Unlock()
	})
	if sessions := mergedSessionIDs(); sessions[closed] || !sessions[merged] {
		t.Fatalf("teardown merge evidence = %v", sessions)
	}
}

func TestDeliveryRollbackRequiresProvenTarget(t *testing.T) {
	proof := deliveryMergedPR{Release: "v1.261127.50", InstalledOn: []taskDeliveryHostProof{{Target: "coordinator", Version: "1.261127.50"}}}
	if confirmedRolloutRollback(proof, []taskDeliveryHostProof{{Target: "new-host", Version: "1.261127.49"}}) {
		t.Fatal("a new host must not revoke an existing proof")
	}
	if !confirmedRolloutRollback(proof, []taskDeliveryHostProof{{Target: "coordinator", Version: "1.261127.49"}}) {
		t.Fatal("a fresh observation on a proven target must revoke a rolled-back version")
	}
}
