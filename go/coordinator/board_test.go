package main

import (
	"slices"
	"testing"
)

// Requirement: Fleet Swarm Variant C (docs/designs/fleet-swarm.md): work_plan_set, board_post, board_read, and
// work_board_get share one durable work_plan + board_message core with monotonic seq, @mention/wakeTargets resolution,
// live session/execution/execution_totals/execution_spend roster join, summary counts, and project RLS isolation.
// Risk: concurrent or repeated board posts reuse seq or drop @mentions/wakeTargets, work_board_get returns stale plan
// state instead of live execution/totals telemetry, or an ungranted person reads/writes another project's board.
func TestBoardWorkPlanAndMessages(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	member := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	outsider := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "outsider@trial.test"}

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','board-priv','config/projects/board-priv/project.json',1,'board-priv');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','board-priv',1,'{"settings":{"id":"board-priv","name":"Board Priv","contextFiles":[]},"contextFiles":[]}','bp1','editor','person','t');
INSERT INTO host(id,host_id,project_id,state) VALUES ('h-board','h-board','board-priv','active');
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-board-priv','Swarm priv task','board-priv','in_progress'),
 ('t-board-pub','Swarm pub task','desiign','in_progress');
INSERT INTO session(id,project_id,task_id,host_id,payload) VALUES
 ('s-coord','board-priv','t-board-priv','h-board','{"agentConfig":{"id":"swarm-coordinator","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-w1','board-priv','t-board-priv','h-board','{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-v1','board-priv','t-board-priv','h-board','{"agentConfig":{"id":"claude-reviewer","harness":"claude","model":"claude-sonnet-4-6"}}');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,host_id,harness,model,started_at,ended_at,data) VALUES
 ('e-w1','agent_run','running','board-priv','t-board-priv','s-w1','h-board','codex','gpt-6.2-sol',now()-interval '30 seconds',NULL,'{"id":"e-w1","sessionId":"s-w1"}'),
 ('e-v1','agent_run','succeeded','board-priv','t-board-priv','s-v1','h-board','claude','claude-sonnet-4-6',now()-interval '60 seconds',now()-interval '10 seconds','{"id":"e-v1","sessionId":"s-v1"}');
INSERT INTO price_entry(id,subject,unit,currency,price,source,effective_from,observed_at) VALUES
 ('p-v1-in','claude-sonnet-4-6','input_tokens','USD','0.000003','owner',now()-interval '1 day',now()),
 ('p-v1-out','claude-sonnet-4-6','output_tokens','USD','0.000015','owner',now()-interval '1 day',now());
INSERT INTO execution_totals(execution_id,project_id,model,wall_ms,wall_ms_basis,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis) VALUES
 ('e-w1','board-priv','gpt-6.2-sol',NULL,'unavailable',1200,'measured',350,'measured'),
 ('e-v1','board-priv','claude-sonnet-4-6',50000,'measured',2000,'measured',500,'measured');
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,state) VALUES
 ('art-shot-1','board-priv','prod','h-shot-1',42,'image/png','board.png','e-w1','t-board-priv','stored');
`)

	call := func(op string, body any, headers map[string]string) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/"+op, body, headers)
	}

	// 1. work_plan_set: create v1, increment to v2 with optimistic concurrency, reject stale expectedVersion and invalid deps.
	planBody := doc{
		"projectId": "board-priv",
		"scopeKind": "task",
		"scopeId":   "t-board-priv",
		"title":     "FLT-19 Slice 1",
		"goal":      "Deliver durable board",
		"strategy":  "2-worker + 1-verifier",
		"threads": []any{
			doc{
				"id":     "th-impl",
				"title":  "Implementation",
				"goal":   "Build schema and handlers",
				"status": "active",
				"tasks": []any{
					doc{
						"id":            "tk-core",
						"title":         "Board core",
						"ownerMember":   "worker-1",
						"role":          "worker",
						"harness":       "codex",
						"model":         "gpt-6.2-sol",
						"workspaceMode": "share",
						"ownedPaths":    []string{"go/coordinator/board.go"},
						"sessionId":     "s-w1",
						"executionId":   "e-w1",
						"status":        "in_progress",
					},
				},
			},
			doc{
				"id":     "th-verify",
				"title":  "Verification",
				"status": "planned",
				"tasks": []any{
					doc{
						"id":            "tk-review",
						"title":         "Independent review",
						"ownerMember":   "verifier-1",
						"role":          "verifier",
						"harness":       "claude",
						"workspaceMode": "inherit",
						"sessionId":     "s-v1",
						"executionId":   "e-v1",
						"dependsOn":     []string{"tk-core"},
						"status":        "pending",
					},
				},
			},
		},
	}
	c, res := call("work_plan_set", planBody, owner)
	if c != 200 || num(mapOf(res["plan"])["version"]) != 1 {
		t.Fatalf("work_plan_set v1 = %d %v", c, res)
	}
	planBody["expectedVersion"] = 1
	planBody["goal"] = "Deliver durable board v2"
	c, res = call("work_plan_set", planBody, owner)
	if c != 200 || num(mapOf(res["plan"])["version"]) != 2 {
		t.Fatalf("work_plan_set v2 = %d %v", c, res)
	}
	if c, _ := call("work_plan_set", planBody, owner); c != 409 {
		t.Fatalf("stale expectedVersion = %d, want 409", c)
	}
	badPlan := doc{
		"projectId": "board-priv",
		"scopeKind": "task",
		"scopeId":   "t-board-priv",
		"title":     "Bad",
		"threads": []any{
			doc{"id": "t1", "title": "T1", "tasks": []any{doc{"id": "k1", "title": "K1", "dependsOn": []string{"missing"}}}},
		},
	}
	if c, _ := call("work_plan_set", badPlan, owner); c != 400 {
		t.Fatalf("invalid dependsOn = %d, want 400", c)
	}

	// 2. board_post: monotonic seq, @mention parsing (ignoring email), explicit mentions merge, wakeTargets resolution.
	c, p1 := call("board_post", doc{
		"projectId":       "board-priv",
		"scopeKind":       "task",
		"scopeId":         "t-board-priv",
		"threadId":        "th-impl",
		"authorMember":    "coordinator",
		"authorRole":      "coordinator",
		"authorSessionId": "s-coord",
		"kind":            "brief",
		"text":            "Start tk-core @worker-1 (contact ops@example.com for details).",
	}, owner)
	if c != 200 {
		t.Fatalf("board_post 1 = %d %v", c, p1)
	}
	if num(mapOf(p1["message"])["seq"]) != 1 {
		t.Fatalf("seq 1 = %v", p1["message"])
	}
	if mentions := strs(mapOf(p1["message"])["mentions"]); !slices.Equal(mentions, []string{"worker-1"}) {
		t.Fatalf("mentions = %v, want [worker-1] without email", mentions)
	}
	if targets := anys(p1["wakeTargets"]); len(targets) != 1 || astr(mapOf(targets[0])["sessionId"]) != "s-w1" {
		t.Fatalf("wakeTargets = %v", targets)
	}

	c, p2 := call("board_post", doc{
		"projectId":         "board-priv",
		"scopeKind":         "task",
		"scopeId":           "t-board-priv",
		"threadId":          "th-impl",
		"authorMember":      "worker-1",
		"authorRole":        "worker",
		"authorExecutionId": "e-w1",
		"kind":              "screenshot",
		"artifactId":        "art-shot-1",
		"text":              "Captured board UI screenshot; @all please note schema frozen.",
		"payload":           doc{"caption": "initial"},
	}, owner)
	if c != 200 || num(mapOf(p2["message"])["seq"]) != 2 {
		t.Fatalf("board_post 2 = %d %v", c, p2)
	}
	// @all wakes coordinator (s-coord from prior post) and verifier-1 (s-v1 from plan), excluding author worker-1 (s-w1).
	wakeSess := []string{}
	for _, wt := range anys(p2["wakeTargets"]) {
		wakeSess = append(wakeSess, astr(mapOf(wt)["sessionId"]))
	}
	if len(wakeSess) != 2 || !slices.Contains(wakeSess, "s-v1") || !slices.Contains(wakeSess, "s-coord") || slices.Contains(wakeSess, "s-w1") {
		t.Fatalf("@all wakeTargets = %v", p2["wakeTargets"])
	}

	c, p3 := call("board_post", doc{
		"projectId":    "board-priv",
		"scopeKind":    "task",
		"scopeId":      "t-board-priv",
		"threadId":     "th-verify",
		"authorMember": "verifier-1",
		"authorRole":   "verifier",
		"kind":         "blocker",
		"mentions":     []string{"@coordinator"},
		"text":         "Need fixture confirmation.",
	}, owner)
	if c != 200 || num(mapOf(p3["message"])["seq"]) != 3 {
		t.Fatalf("board_post 3 = %d %v", c, p3)
	}
	if targets := anys(p3["wakeTargets"]); len(targets) != 1 || astr(mapOf(targets[0])["sessionId"]) != "s-coord" {
		t.Fatalf("explicit mention wakeTargets = %v", targets)
	}

	// 3. board_read: since cursor, filters, limit/hasMore.
	c, rSince := call("board_read", doc{
		"scopeKind": "task",
		"scopeId":   "t-board-priv",
		"since":     1,
		"limit":     1,
	}, owner)
	if c != 200 || num(rSince["headSeq"]) != 3 || rSince["hasMore"] != true || len(anys(rSince["messages"])) != 1 || num(mapOf(anys(rSince["messages"])[0])["seq"]) != 2 {
		t.Fatalf("board_read since=1 limit=1 = %d %v", c, rSince)
	}
	c, rFilter := call("board_read", doc{
		"scopeKind": "task",
		"scopeId":   "t-board-priv",
		"kind":      "blocker",
		"mention":   "coordinator",
	}, owner)
	if c != 200 || len(anys(rFilter["messages"])) != 1 || astr(mapOf(anys(rFilter["messages"])[0])["authorMember"]) != "verifier-1" {
		t.Fatalf("board_read filtered = %d %v", c, rFilter)
	}

	// 4. work_board_get: live roster join (session + execution + execution_totals + execution_spend) + summary counts.
	c, wb := call("work_board_get", doc{
		"scopeKind": "task",
		"scopeId":   "t-board-priv",
	}, owner)
	if c != 200 {
		t.Fatalf("work_board_get = %d %v", c, wb)
	}
	if num(mapOf(wb["plan"])["version"]) != 2 || num(wb["headSeq"]) != 3 {
		t.Fatalf("work_board_get plan/headSeq = %v", wb)
	}
	rosterByMember := map[string]map[string]any{}
	for _, x := range anys(wb["roster"]) {
		m := mapOf(x)
		rosterByMember[astr(m["memberName"])] = m
	}
	w1 := rosterByMember["worker-1"]
	v1 := rosterByMember["verifier-1"]
	coord := rosterByMember["coordinator"]
	if w1 == nil || v1 == nil || coord == nil {
		t.Fatalf("roster missing members: %v", wb["roster"])
	}
	if astr(w1["state"]) != "running" || astr(w1["harness"]) != "codex" || astr(w1["hostId"]) != "h-board" || num(w1["tokensInput"]) != 1200 || num(w1["tokensOutput"]) != 350 || num(w1["lastPostSeq"]) != 2 {
		t.Fatalf("worker-1 roster mismatch: %v", w1)
	}
	if v1["durationMs"] == nil || num(v1["durationMs"]) != 50000 || astr(v1["state"]) != "succeeded" || astr(v1["harness"]) != "claude" || len(anys(v1["cost"])) == 0 {
		t.Fatalf("verifier-1 roster mismatch (duration/cost): %v", v1)
	}
	if astr(coord["role"]) != "coordinator" || num(coord["lastPostSeq"]) != 1 {
		t.Fatalf("coordinator roster mismatch: %v", coord)
	}
	summary := mapOf(wb["summary"])
	if num(summary["threadsTotal"]) != 2 || num(summary["tasksTotal"]) != 2 || num(summary["membersTotal"]) != 3 ||
		num(summary["messagesTotal"]) != 3 || num(summary["blockersCount"]) != 1 || num(summary["screenshotsCount"]) != 1 {
		t.Fatalf("summary counts mismatch: %v", summary)
	}
	if hc := mapOf(summary["harnessCounts"]); num(hc["codex"]) != 2 || num(hc["claude"]) != 1 {
		t.Fatalf("harnessCounts mismatch: %v", hc)
	}

	// 5. RLS isolation: member (granted only desiign) and outsider (no grants) cannot read or write board-priv,
	//    while member CAN write/read in desiign and change feed records kind='board'.
	for _, opName := range []string{"board_read", "work_board_get"} {
		if c, _ := call(opName, doc{"scopeKind": "task", "scopeId": "t-board-priv"}, member); c != 404 {
			t.Fatalf("member %s on board-priv = %d, want 404", opName, c)
		}
		if c, _ := call(opName, doc{"scopeKind": "task", "scopeId": "t-board-priv"}, outsider); c != 404 {
			t.Fatalf("outsider %s on board-priv = %d, want 404", opName, c)
		}
	}
	if c, _ := call("board_post", doc{"projectId": "board-priv", "scopeKind": "task", "scopeId": "t-board-priv", "authorMember": "x", "text": "hi"}, member); c != 404 {
		t.Fatalf("member board_post on board-priv = %d, want 404", c)
	}
	if c, _ := call("work_plan_set", doc{"projectId": "desiign", "scopeKind": "task", "scopeId": "t-board-pub", "title": "Desiign Swarm", "threads": []any{}}, member); c != 200 {
		t.Fatalf("member work_plan_set on granted desiign = %d", c)
	}
	if c, _ := call("work_board_get", doc{"scopeKind": "task", "scopeId": "t-board-pub"}, outsider); c != 404 {
		t.Fatalf("outsider work_board_get on desiign = %d, want 404", c)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM change WHERE kind='board' AND task_id='t-board-priv'"); len(n) == 0 || n[0] == "0" {
		t.Fatalf("expected fleet_change board rows for t-board-priv, got %v", n)
	}
}
