package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: fleet.md Completion & Acceptance + T-FLT-37:
// 1) A task whose linked GitHub PR has merged and reached delivery (installed on live hosts/UI for taskqorg/fleet, or
//    merged/released for other repos) reconciles automatically: auto-accepts to completed when project and task rules
//    allow it (even if the worker process exited 0 without calling worker_done, as on T-FLT-24), or surfaces one
//    acceptance question naming the PR and release when human acceptance is required (integration.autoAccept=false,
//    integration.autoMerge=false, completionRequirements manual/human, or Landing: wait without post-delivery check).
// 2) An explicit person-called task_request_acceptance succeeds on an unblocked queued task with no primary execution
//    and transitions queued -> completed when answered accepted.
// Risk: tasks linger in Running after their linked PR merges and deploys, or human-acceptance / Landing: wait /
// non-installed releasing PRs close prematurely without review or deployment.
func TestDeliveryTaskReconcileAutoAcceptAndHumanAcceptance(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()

	now := time.Now().UTC().Truncate(time.Second)
	beforeMerge := now.Add(-2 * time.Minute).Format(time.RFC3339)
	mergedAt := now.Add(-60 * time.Second).Format(time.RFC3339)
	publishedAt := now.Add(-30 * time.Second).Format(time.RFC3339)
	afterDelivery := now.Add(-10 * time.Second).Format(time.RFC3339)

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','fleet','config/projects/fleet/project.json',1,'fleet'),
 ('project','manual-proj','config/projects/manual-proj/project.json',1,'manual-proj'),
 ('project','other-proj','config/projects/other-proj/project.json',1,'other-proj');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','fleet',1,'{"settings":{"id":"fleet","name":"Fleet","contextFiles":[],"repos":[{"id":"main","remote":"git@github.com:taskqorg/fleet.git","forge":"github","defaultBranch":"main"}],"integration":{"mode":"pr","mergeMethod":"squash","autoMerge":true}},"contextFiles":[]}','f1','editor','person','t'),
 ('project','manual-proj',1,'{"settings":{"id":"manual-proj","name":"Manual","contextFiles":[],"repos":[{"id":"main","remote":"git@github.com:taskqorg/manual.git","forge":"github","defaultBranch":"main"}],"integration":{"mode":"pr","mergeMethod":"squash","autoMerge":true,"autoAccept":false}},"contextFiles":[]}','m1','editor','person','t'),
 ('project','other-proj',1,'{"settings":{"id":"other-proj","name":"Other","contextFiles":[],"repos":[{"id":"main","remote":"git@github.com:taskqorg/specq.git","forge":"github","defaultBranch":"main"}],"integration":{"mode":"pr","mergeMethod":"squash","autoMerge":true}},"contextFiles":[]}','o1','editor','person','t');
INSERT INTO task(id,title,project_id,state,expected_outcome,completion_requirements,created_at) VALUES
 ('t-flt24','Reconcile worker without worker_done','fleet','in_progress',NULL,'{}',now()-interval '10 minutes'),
 ('t-human-req','Human acceptance required task','fleet','in_progress',NULL,'{"requireHumanAcceptance":true}',now()-interval '10 minutes'),
 ('t-wait-pre','Landing wait without live check','fleet','in_progress',E'Verify in prod\nLanding: wait — live check: curl /ops', '{}', now()-interval '10 minutes'),
 ('t-wait-post','Landing wait with post-delivery report','fleet','in_progress',E'Verify in prod\nLanding: wait — live check: curl /ops', '{}', now()-interval '10 minutes'),
 ('t-manual-proj','Project autoAccept false task','manual-proj','in_progress',NULL,'{}',now()-interval '10 minutes'),
 ('t-other-branch','Session branch task in release-less repo','other-proj','in_progress',NULL,'{}',now()-interval '10 minutes');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('s-flt24','fleet','t-flt24','h1','{}'),
 ('s-other','other-proj','t-other-branch','h1','{}');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,harness,model,started_at,ended_at,data) VALUES
 ('e-flt24','agent_run','succeeded','confirmed','fleet','t-flt24','s-flt24','h1','claude','sonnet',now()-interval '5 minutes',now()-interval '3 minutes','{}'),
 ('e-wait-pre','agent_run','succeeded','confirmed','fleet','t-wait-pre',NULL,'h1','claude','sonnet',now()-interval '5 minutes','`+beforeMerge+`','{"report":{"outcome":"succeeded","summary":"pre-merge only"}}'),
 ('e-wait-post','agent_run','succeeded','confirmed','fleet','t-wait-post',NULL,'h1','claude','sonnet',now()-interval '5 minutes','`+afterDelivery+`','{"report":{"outcome":"succeeded","summary":"verified live"}}');
`)

	fleetRepo := ghRepo{
		closed: []ghPull{
			{Number: 787, Title: "Fix task completion", URL: "https://github.com/taskqorg/fleet/pull/787", MergedAt: &mergedAt, MergeSHA: "sha-787", Head: ghPullHead{SHA: "h-787", Ref: "fleet/s-flt24"}},
			{Number: 901, Title: "Manual feature", Body: "Closes " + taskCode(t, e, owner, "t-human-req"), URL: "https://github.com/taskqorg/fleet/pull/901", MergedAt: &mergedAt, MergeSHA: "sha-901", Head: ghPullHead{SHA: "h-901", Ref: "fleet/901"}},
			{Number: 902, Title: "Wait pre-check (" + taskCode(t, e, owner, "t-wait-pre") + ")", URL: "https://github.com/taskqorg/fleet/pull/902", MergedAt: &mergedAt, MergeSHA: "sha-902", Head: ghPullHead{SHA: "h-902"}},
			{Number: 903, Title: "Wait post-check (" + taskCode(t, e, owner, "t-wait-post") + ")", URL: "https://github.com/taskqorg/fleet/pull/903", MergedAt: &mergedAt, MergeSHA: "sha-903", Head: ghPullHead{SHA: "h-903"}},
		},
	}

	// Stage 1: releasing (no release covering merged PRs) must NOT close or ask acceptance on taskqorg/fleet.
	_, _, releasing := deliveryStagesWithMerged("fleet", "taskqorg/fleet", fleetRepo, []string{"1.261123.1"}, "1.261123.1", now)
	if err := reconcileDeliveredTasks(e.ctx, e.app, filterDelivered(releasing)); err != nil {
		t.Fatalf("reconcile releasing: %v", err)
	}
	assertTaskState(t, e, owner, "t-flt24", "in_progress", 0)

	// Stage 2: release published (v1.261123.2), host still on 1.261123.1 (installing) -> STILL must NOT close!
	fleetRepo.releases = []ghRelease{{Tag: "v1.261123.2", Target: "sha-903", Published: publishedAt}}
	_, _, installing := deliveryStagesWithMerged("fleet", "taskqorg/fleet", fleetRepo, []string{"1.261123.1"}, "1.261123.1", now)
	if err := reconcileDeliveredTasks(e.ctx, e.app, filterDelivered(installing)); err != nil {
		t.Fatalf("reconcile installing: %v", err)
	}
	assertTaskState(t, e, owner, "t-flt24", "in_progress", 0)

	// Stage 3: host upgraded to 1.261123.2 (installed) + non-fleet repos merged!
	_, _, fleetInstalled := deliveryStagesWithMerged("fleet", "taskqorg/fleet", fleetRepo, []string{"1.261123.2"}, "1.261123.2", now)
	_, _, manualMerged := deliveryStagesWithMerged("manual-proj", "taskqorg/manual", ghRepo{
		closed: []ghPull{{Number: 12, Title: "Manual change", Body: "Closes " + taskCode(t, e, owner, "t-manual-proj"), URL: "https://github.com/taskqorg/manual/pull/12", MergedAt: &mergedAt, MergeSHA: "sha-m12", Head: ghPullHead{SHA: "hm12"}}},
	}, []string{"1.261123.2"}, "1.261123.2", now)
	_, _, specqMerged := deliveryStagesWithMerged("other-proj", "taskqorg/specq", ghRepo{
		closed: []ghPull{{Number: 44, Title: "Branch linked change", URL: "https://github.com/taskqorg/specq/pull/44", MergedAt: &mergedAt, MergeSHA: "sha-s44", Head: ghPullHead{SHA: "hs44", Ref: "fleet/s-other"}}},
	}, []string{"1.261123.2"}, "1.261123.2", now)

	var delivered []deliveryMergedPR
	delivered = append(delivered, filterDelivered(fleetInstalled)...)
	delivered = append(delivered, filterDelivered(manualMerged)...)
	delivered = append(delivered, filterDelivered(specqMerged)...)

	if err := reconcileDeliveredTasks(e.ctx, e.app, delivered); err != nil {
		t.Fatalf("reconcile delivered: %v", err)
	}

	// Auto-accepted tasks -> completed with acceptedBy == "fleet:delivery"
	flt24 := assertTaskState(t, e, owner, "t-flt24", "completed", 0)
	if flt24["acceptedBy"] != deliveryActor {
		t.Fatalf("t-flt24 acceptedBy = %v, want %s", flt24["acceptedBy"], deliveryActor)
	}
	assertTaskState(t, e, owner, "t-wait-post", "completed", 0)
	assertTaskState(t, e, owner, "t-other-branch", "completed", 0)

	// Human-acceptance required tasks stay in_progress with exactly 1 acceptance question mentioning PR/release.
	human := assertTaskState(t, e, owner, "t-human-req", "in_progress", 1)
	if human["completionReportMissing"] == true {
		t.Fatalf("t-human-req completionReportMissing should be false once delivery offer exists")
	}
	qText := textStr(anys(human["openQuestions"])[0].(map[string]any)["text"])
	if !strings.Contains(qText, "PR #901") || !strings.Contains(qText, "v1.261123.2") {
		t.Fatalf("human acceptance text = %q, expected PR #901 and v1.261123.2", qText)
	}
	assertTaskState(t, e, owner, "t-wait-pre", "in_progress", 1)
	man := assertTaskState(t, e, owner, "t-manual-proj", "in_progress", 1)

	// Re-running reconciliation must be idempotent (still 1 question, and rejecting t-human-req suppresses re-asking).
	if err := reconcileDeliveredTasks(e.ctx, e.app, delivered); err != nil {
		t.Fatalf("reconcile idempotent: %v", err)
	}
	assertTaskState(t, e, owner, "t-human-req", "in_progress", 1)

	humanQID := textStr(anys(human["openQuestions"])[0].(map[string]any)["id"])
	code, ans := e.callJSON(t, "POST", "/api/ops/questions_answer", contract.QuestionsAnswerRequest{QuestionId: humanQID, Answer: "rejected"}, owner)
	if code != 200 || ans["answer"] != "rejected" {
		t.Fatalf("reject t-human-req = %d %v", code, ans)
	}
	if err := reconcileDeliveredTasks(e.ctx, e.app, delivered); err != nil {
		t.Fatalf("reconcile after reject: %v", err)
	}
	assertTaskState(t, e, owner, "t-human-req", "in_progress", 0)

	// Accepting t-manual-proj completes it cleanly.
	manQID := textStr(anys(man["openQuestions"])[0].(map[string]any)["id"])
	code, ans = e.callJSON(t, "POST", "/api/ops/questions_answer", contract.QuestionsAnswerRequest{QuestionId: manQID, Answer: "accepted"}, owner)
	if code != 200 || ans["answer"] != "accepted" {
		t.Fatalf("accept t-manual-proj = %d %v", code, ans)
	}
	assertTaskState(t, e, owner, "t-manual-proj", "completed", 0)
}

// Requirement: T-FLT-25 premature-acceptance safeguards + queued externally-delivered task acceptance (T-FLT-34..38).
// Risk: exit-0 runs without worker_done and without a delivered PR silently accept or surface questions; failed reports,
// pending work, missing required artifacts, or earlier-merged PRs close tasks prematurely; or queued externally-delivered
// tasks cannot close via task_request_acceptance.
func TestDeliveryTaskReconcileSafeguardsAndQueuedAcceptance(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()

	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(-30 * time.Second).Format(time.RFC3339)
	earlierMerge := now.Add(-15 * time.Minute).Format(time.RFC3339)
	publishedAt := now.Add(-10 * time.Second).Format(time.RFC3339)

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','safeguard','config/projects/safeguard/project.json',1,'safeguard');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','safeguard',1,'{"settings":{"id":"safeguard","name":"Safeguard","contextFiles":[],"repos":[{"id":"main","remote":"git@github.com:taskqorg/safeguard.git","forge":"github","defaultBranch":"main"}],"integration":{"mode":"pr","mergeMethod":"squash","autoMerge":true}},"contextFiles":[]}','s1','editor','person','t');
INSERT INTO task(id,title,project_id,state,completion_requirements,created_at) VALUES
 ('t-no-pr','No PR and no worker_done','safeguard','in_progress','{}',now()-interval '10 minutes'),
 ('t-fail-rep','Failed outcome report','safeguard','in_progress','{}',now()-interval '10 minutes'),
 ('t-pending','Active work pending','safeguard','in_progress','{}',now()-interval '10 minutes'),
 ('t-miss-art','Missing required artifact','safeguard','in_progress','{"artifacts":["summary.md"]}',now()-interval '10 minutes'),
 ('t-old-pr','Task created after PR merged','safeguard','in_progress','{}',now()-interval '5 minutes'),
 ('t-followup','Follow-up mention must not match','safeguard','in_progress','{}',now()-interval '10 minutes'),
 ('t-dep-open','Blocking dep for queued task','safeguard','queued','{}',now()-interval '10 minutes'),
 ('t-queued-blocked','Queued with open dep','safeguard','queued','{}',now()-interval '10 minutes'),
 ('t-queued-ext','Externally delivered queued task','safeguard','queued','{}',now()-interval '10 minutes');
INSERT INTO task_dependency(task_id,depends_on_task_id,kind,project_id) VALUES
 ('t-queued-blocked','t-dep-open','blocks','safeguard');
INSERT INTO execution(id,kind,state,observation,project_id,task_id,host_id,harness,model,started_at,ended_at,data) VALUES
 ('e-no-pr','agent_run','succeeded','confirmed','safeguard','t-no-pr','h1','claude','sonnet',now()-interval '5 minutes',now()-interval '4 minutes','{}'),
 ('e-fail-rep','agent_run','succeeded','confirmed','safeguard','t-fail-rep','h1','claude','sonnet',now()-interval '5 minutes',now()-interval '4 minutes','{"report":{"outcome":"failed","summary":"incomplete"}}'),
 ('e-pending','agent_run','running','confirmed','safeguard','t-pending','h1','claude','sonnet',now()-interval '2 minutes',NULL,'{}'),
 ('e-miss-art','agent_run','succeeded','confirmed','safeguard','t-miss-art','h1','claude','sonnet',now()-interval '5 minutes',now()-interval '4 minutes','{"report":{"outcome":"succeeded"}}');
`)

	repo := ghRepo{
		closed: []ghPull{
			{Number: 1, Title: taskCode(t, e, owner, "t-fail-rep") + " work", URL: "https://github.com/taskqorg/safeguard/pull/1", MergedAt: &mergedAt, MergeSHA: "sha1"},
			{Number: 2, Title: taskCode(t, e, owner, "t-pending") + " work", URL: "https://github.com/taskqorg/safeguard/pull/2", MergedAt: &mergedAt, MergeSHA: "sha2"},
			{Number: 3, Title: taskCode(t, e, owner, "t-miss-art") + " work", URL: "https://github.com/taskqorg/safeguard/pull/3", MergedAt: &mergedAt, MergeSHA: "sha3"},
			{Number: 4, Title: taskCode(t, e, owner, "t-old-pr") + " earlier", URL: "https://github.com/taskqorg/safeguard/pull/4", MergedAt: &earlierMerge, MergeSHA: "sha4"},
			{Number: 5, Title: "Unrelated cleanup", Body: "Follow up to " + taskCode(t, e, owner, "t-followup") + " work", URL: "https://github.com/taskqorg/safeguard/pull/5", MergedAt: &mergedAt, MergeSHA: "sha5"},
		},
		releases: []ghRelease{{Tag: "v1.0.0", Target: "sha5", Published: publishedAt}},
	}
	_, _, merged := deliveryStagesWithMerged("safeguard", "taskqorg/safeguard", repo, []string{"1.0.0"}, "1.0.0", now)
	delivered := filterDelivered(merged)

	if err := reconcileDeliveredTasks(e.ctx, e.app, delivered); err != nil {
		t.Fatalf("reconcile safeguards: %v", err)
	}

	// T-FLT-25 control: no PR & no worker_done -> stays in_progress, 0 questions, completionReportMissing=true.
	noPR := assertTaskState(t, e, owner, "t-no-pr", "in_progress", 0)
	if noPR["completionReportMissing"] != true {
		t.Fatalf("t-no-pr expected completionReportMissing=true, got %v", noPR["completionReportMissing"])
	}
	// Safeguards keep failed report, pending execution, missing artifact, pre-task PR, and follow-up text from closing.
	assertTaskState(t, e, owner, "t-fail-rep", "in_progress", 0)
	assertTaskState(t, e, owner, "t-pending", "in_progress", 0)
	assertTaskState(t, e, owner, "t-miss-art", "in_progress", 0)
	assertTaskState(t, e, owner, "t-old-pr", "in_progress", 0)
	assertTaskState(t, e, owner, "t-followup", "in_progress", 0)

	// Once t-miss-art gets its required artifact stored, reconciliation completes it.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,required,role,state) VALUES
 ('art-sg4','safeguard','prod','hash-sg4',42,'text/markdown','summary.md','e-miss-art','t-miss-art',true,'original','stored');
`)
	if err := reconcileDeliveredTasks(e.ctx, e.app, delivered); err != nil {
		t.Fatalf("reconcile after artifact: %v", err)
	}
	assertTaskState(t, e, owner, "t-miss-art", "completed", 0)

	// Queued task with open dependency must reject task_request_acceptance with 409.
	code, errBody := e.callJSON(t, "POST", "/api/ops/task_request_acceptance", contract.TaskRequestAcceptanceRequest{TaskId: "t-queued-blocked"}, owner)
	if code != 409 {
		t.Fatalf("t-queued-blocked expected 409, got %d %v", code, errBody)
	}

	// Queued externally-delivered task (executions: []) succeeds via person-called task_request_acceptance -> accepted -> completed.
	code, qRow := e.callJSON(t, "POST", "/api/ops/task_request_acceptance", contract.TaskRequestAcceptanceRequest{TaskId: "t-queued-ext"}, owner)
	if code != 200 {
		t.Fatalf("task_request_acceptance on t-queued-ext = %d %v", code, qRow)
	}
	code, ans := e.callJSON(t, "POST", "/api/ops/questions_answer", contract.QuestionsAnswerRequest{QuestionId: textStr(qRow["id"]), Answer: "accepted"}, owner)
	if code != 200 || ans["answer"] != "accepted" {
		t.Fatalf("questions_answer on t-queued-ext = %d %v", code, ans)
	}
	ext := assertTaskState(t, e, owner, "t-queued-ext", "completed", 0)
	if ext["acceptedBy"] == nil || textStr(ext["acceptedBy"]) == "" {
		t.Fatalf("t-queued-ext missing acceptedBy: %v", ext)
	}
}

func taskCode(t *testing.T, e *testCoordEnv, hdr map[string]string, id string) string {
	t.Helper()
	code, body := e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: id}, hdr)
	if code != 200 {
		t.Fatalf("task_get %s = %d %v", id, code, body)
	}
	return textStr(body["code"])
}

func textStr(v any) string {
	s, _ := v.(string)
	return s
}

func filterDelivered(in []deliveryMergedPR) []deliveryMergedPR {
	var out []deliveryMergedPR
	for _, pr := range in {
		if pr.Delivered {
			out = append(out, pr)
		}
	}
	return out
}

func assertTaskState(t *testing.T, e *testCoordEnv, hdr map[string]string, id, wantState string, wantOpenQuestions int) map[string]any {
	t.Helper()
	code, body := e.callJSON(t, "POST", "/api/ops/task_get", contract.TaskGetRequest{TaskId: id}, hdr)
	if code != 200 {
		t.Fatalf("task_get %s = %d %v", id, code, body)
	}
	if textStr(body["state"]) != wantState {
		t.Fatalf("task %s state = %v, want %s (body=%v)", id, body["state"], wantState, body)
	}
	if got := len(anys(body["openQuestions"])); got != wantOpenQuestions {
		t.Fatalf("task %s openQuestions = %d, want %d (%v)", id, got, wantOpenQuestions, body["openQuestions"])
	}
	return body
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
