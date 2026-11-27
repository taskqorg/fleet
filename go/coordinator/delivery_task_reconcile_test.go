package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fleet/contract"
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
			"settings": map[string]any{"id": id, "name": id, "contextFiles": []string{"rules.md"}, "integration": map[string]any{
				"mode": "pr", "autoMerge": true, "autoAccept": auto,
			}},
			"contextFiles": []map[string]string{{"path": "rules.md", "text": "Delivery fixture."}},
		}}, headers, &created)
	}

	landedAt := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for i, tc := range []struct {
		name, requirements                                string
		projectManual, liveCheck, rejected, missingReport bool
	}{
		{name: "worker report", requirements: "{}"},
		{name: "post-delivery verification", requirements: "{}", liveCheck: true},
		{name: "project opt-out", requirements: "{}", projectManual: true},
		{name: "human required", requirements: `{"requireHumanAcceptance":true}`},
		{name: "human required with malformed completion metadata", requirements: `{"requireHumanAcceptance":true,"branch":[]}`},
		{name: "prior rejection", requirements: "{}", rejected: true},
		{name: "explicit offer without work report", requirements: "{}", missingReport: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := "delivery-auto"
			if tc.projectManual {
				project = "delivery-manual"
			}
			pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: int64(i + 1), Delivered: true, Stage: "installed",
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
			report := func(eid string, end time.Time, reportOutcome string) {
				t.Helper()
				exec(t, `INSERT INTO execution (id,kind,state,observation,project_id,task_id,session_id,ended_at,data)
					VALUES ($1,'agent_run','succeeded','confirmed',$2,$3,$4,$5,jsonb_build_object('finishedAt',$6::bigint))`, eid, project, id, sid, end, end.UnixMilli())
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
				e.opInto(t, "worker_done", map[string]any{"executionId": eid, "outcome": reportOutcome}, headers, &reply)
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
			reconcile := func(pr deliveryMergedPR) {
				t.Helper()
				if err := reconcileDeliveredTasks(e.ctx, e.app, []deliveryMergedPR{pr}); err != nil {
					t.Fatal(err)
				}
			}

			outcome := "succeeded"
			if tc.missingReport {
				outcome = ""
			}
			report("run-"+id, landedAt.Add(-time.Second), outcome)
			if tc.missingReport {
				offer()
			}
			original := open()
			if tc.rejected {
				if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
					_, err := answerQuestion(q, original.Id, "rejected", testOwnerEmail, nil)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				original = offer()
			}
			merged := pr
			merged.Delivered = false
			reconcile(merged)
			if got := open(); got.Id != original.Id {
				t.Fatalf("undelivered PR changed the offer from %s to %s", original.Id, got.Id)
			}

			reconcile(pr)
			if tc.liveCheck || tc.projectManual || tc.rejected || tc.missingReport || tc.requirements != "{}" {
				if got := open(); got.Id != original.Id {
					t.Fatalf("reconciliation changed the offer from %s to %s", original.Id, got.Id)
				}
				if !tc.liveCheck {
					return
				}
				report("verification-"+id, landedAt.Add(time.Second), "succeeded")
				original = open()
				reconcile(pr)
			}
			reconcile(pr) // Replaying the same delivery must leave one acceptance and one audit record.
			if task := get(); task.State != "completed" || deref(task.AcceptedBy) != deliveryActor {
				t.Fatalf("task state = %s, acceptedBy = %q", task.State, deref(task.AcceptedBy))
			}
			var state, answer, answeredBy string
			if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state,coalesce(answer,''),coalesce(answered_by,'')
				FROM question WHERE id=$1`, original.Id).Scan(&state, &answer, &answeredBy); err != nil {
				t.Fatal(err)
			}
			if state != "answered" || answer != "accepted" || answeredBy != deliveryActor {
				t.Fatalf("original question: state=%s answer=%q answeredBy=%q", state, answer, answeredBy)
			}
			var audits int
			if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_log WHERE target=$1 AND field='delivery_reconciled'
				AND payload->>'reason'='auto_accepted' AND value->>'url'=$2`, id, pr.URL).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("delivery audit count = %d, err = %v", audits, err)
			}
		})
	}
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
