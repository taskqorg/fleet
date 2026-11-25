package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// Requirement: a pull request from a session branch (<prefix>/<session id>) links to the session's task with no step
// from the worker: publishing_status and delivery_status items name the session and task, publishing_status filters by
// taskId, and task rows carry the delivery stage of the task's first linked pull request.
// Risk: workers open pull requests with gh, so a link that waits for a publishing record leaves the task's PR row and
// stage chip empty, or a branch of another tool (claude/<name>) links to a task.
func TestDeliveryLinksBranchToTask(t *testing.T) {
	e := openTestCoordinator(t, false)
	const s1, s2 = "6623e6d4-0fc5-430b-94f7-35db808266ee", "ae39bb5c-032d-4175-8055-1567132a3f4a"
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO task(id,code,title,project_id,state) VALUES
 ('t-link','T-DSG-901','Linked task','desiign','in_progress'),
 ('t-none','T-DSG-902','Task without a pull request','desiign','in_progress');
INSERT INTO session(id,project_id,task_id,payload) VALUES
 ('`+s1+`','desiign','t-link','{}'),
 ('`+s2+`','desiign','t-link','{}');
INSERT INTO execution(id,kind,state,project_id,session_id,data) VALUES
 ('e-forge','agent_run','succeeded','desiign','`+s1+`','{}');
`)
	// A project-forge record from a renamed branch links through its source execution (its session's task).
	pubMu.Lock()
	savedRecs := pubRecords
	pubRecords = map[string]contract.PublishingRecord{"r": {ProjectId: "desiign", Repo: "taskqorg/fleet", Number: 13,
		Stage: string(contract.DeliveryStageMqWait), Branch: ptr("feat/renamed"), SourceExecutionId: ptr("e-forge")}}
	pubMu.Unlock()
	t.Cleanup(func() { pubMu.Lock(); pubRecords = savedRecs; pubMu.Unlock() })
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryLast = deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"taskqorg/fleet": {projects: []string{"desiign"}, items: []contract.DeliveryItem{
			{ProjectId: "desiign", Repo: "taskqorg/fleet", Number: 12, Stage: contract.DeliveryStageInstalled, Branch: ptr("fleet/" + s2)},
			{ProjectId: "desiign", Repo: "taskqorg/fleet", Number: 11, Stage: contract.DeliveryStagePrCi, Branch: ptr("fleet/" + s1)},
			{ProjectId: "desiign", Repo: "taskqorg/fleet", Number: 10, Stage: contract.DeliveryStagePrOpen, Branch: ptr("claude/" + s1 + "x")},
			{ProjectId: "desiign", Repo: "taskqorg/fleet", Number: 9, Stage: contract.DeliveryStagePrOpen, Branch: ptr("claude/board-gaps")},
		}},
	}}
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })
	owner := e.ownerHeaders()
	call := func(op string, body any, out any) {
		t.Helper()
		code, m := e.callJSON(t, "POST", "/api/ops/"+op, body, owner)
		if raw, _ := json.Marshal(m); code != 200 || json.Unmarshal(raw, out) != nil {
			t.Fatalf("%s: %d %s", op, code, raw)
		}
	}

	var pub contract.PublishingStatusReply
	call("publishing_status", doc{"projectId": "desiign", "taskId": "t-link"}, &pub)
	got := map[int64]string{}
	for _, it := range pub.Items {
		got[it.Number] = deref(it.SessionId)
		if deref(it.TaskId) != "t-link" {
			t.Fatalf("item %d names task %v", it.Number, it.TaskId)
		}
	}
	if len(got) != 3 || got[11] != s1 || got[12] != s2 || got[13] != s1 {
		t.Fatalf("publishing_status taskId t-link: %v, want #11 and #13 from %s, #12 from %s", got, s1, s2)
	}

	var del contract.DeliveryStatusReply
	call("delivery_status", doc{}, &del)
	for _, it := range del.Items {
		if linked := it.TaskId != nil; linked != (it.Number >= 11) {
			t.Fatalf("delivery_status #%d task %v", it.Number, it.TaskId)
		}
	}

	var page contract.TaskPageReply
	call("task_page", doc{"filter": doc{"projectId": []string{"desiign"}}}, &page)
	stages := map[string]*contract.DeliveryStage{}
	for _, r := range page.Tasks {
		stages[r.Id] = r.Stage
	}
	if s := stages["t-link"]; s == nil || *s != contract.DeliveryStagePrCi {
		t.Fatalf("t-link stage %v, want pr_ci (its first pull request, #11)", s)
	}
	if _, ok := stages["t-none"]; !ok || stages["t-none"] != nil {
		t.Fatalf("t-none: listed %v, stage %v; want listed with no stage", ok, stages["t-none"])
	}
}
