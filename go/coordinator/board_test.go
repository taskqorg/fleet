package main

import (
	"context"
	"slices"
	"testing"
	"time"
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

	// Suppress auto-wake on p2 so e-v1 remains verifier-1's latest execution for work_board_get telemetry assertions.
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
		"wake":              false,
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

// Requirement: FLT-19 Slice 4 Step 1 (docs/designs/fleet-swarm.md): board_post automatically wakes resolved @mention
// targets after committing board_message without holding board:* across session:* agentSubmit transactions, respects
// wake=false, and isolates wake failures (archived/invalid sessions) in BoardWakeTarget.wakeError.
// Risk: board_post drops wake submission on sleeping/busy workers, violates lockRank (session:* < board:*), ignores
// wake=false, or rolls back/fails the committed board message when one target session is archived or missing.
func TestBoardMentionAutoWake(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','board-wake','config/projects/board-wake/project.json',1,'board-wake');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','board-wake',1,'{"settings":{"id":"board-wake","name":"Board Wake","contextFiles":[]},"contextFiles":[]}','bw1','editor','person','t');
INSERT INTO host(id,host_id,project_id,state) VALUES ('h-wake','h-wake','board-wake','active');
INSERT INTO task(id,title,project_id,state) VALUES ('t-wake','Wake task','board-wake','in_progress');
INSERT INTO session(id,project_id,task_id,host_id,archived_at,payload) VALUES
 ('s-wake-idle','board-wake','t-wake','h-wake',NULL,'{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-wake-busy','board-wake','t-wake','h-wake',NULL,'{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-wake-arch','board-wake','t-wake','h-wake',now(),'{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,host_id,harness,model,started_at,data) VALUES
 ('e-wake-busy','agent_run','running','board-wake','t-wake','s-wake-busy','h-wake','codex','gpt-6.2-sol',now(),'{"id":"e-wake-busy","sessionId":"s-wake-busy"}');
`)

	call := func(op string, body any) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/"+op, body, owner)
	}

	c, wp := call("work_plan_set", doc{
		"projectId": "board-wake",
		"scopeKind": "task",
		"scopeId":   "t-wake",
		"title":     "Wake plan",
		"threads": []any{
			doc{
				"id":     "th-1",
				"title":  "Workers",
				"status": "active",
				"tasks": []any{
					doc{"id": "tk-idle", "title": "Idle worker", "ownerMember": "worker-idle", "role": "worker", "sessionId": "s-wake-idle", "status": "pending"},
					doc{"id": "tk-busy", "title": "Busy worker", "ownerMember": "worker-busy", "role": "worker", "sessionId": "s-wake-busy", "executionId": "e-wake-busy", "status": "in_progress"},
					doc{"id": "tk-arch", "title": "Archived worker", "ownerMember": "worker-arch", "role": "worker", "sessionId": "s-wake-arch", "status": "pending"},
					doc{"id": "tk-miss", "title": "Missing session worker", "ownerMember": "worker-miss", "role": "worker", "sessionId": "s-wake-missing", "status": "pending"},
				},
			},
		},
	})
	if c != 200 {
		t.Fatalf("work_plan_set = %d %v", c, wp)
	}

	// 1. Default wake=true on @worker-idle (creates execution+message) and @worker-busy (queues queued_message) with idempotent request_id.
	c, p1 := call("board_post", doc{
		"projectId":    "board-wake",
		"scopeKind":    "task",
		"scopeId":      "t-wake",
		"threadId":     "th-1",
		"authorMember": "coordinator",
		"authorRole":   "coordinator",
		"kind":         "brief",
		"text":         "Wake @worker-idle and @worker-busy now.",
	})
	if c != 200 {
		t.Fatalf("board_post wake=default = %d %v", c, p1)
	}
	targets1 := anys(p1["wakeTargets"])
	if len(targets1) != 2 {
		t.Fatalf("wakeTargets len = %d %v", len(targets1), targets1)
	}
	for _, raw := range targets1 {
		wt := mapOf(raw)
		if wt["woken"] != true || wt["wakeError"] != nil {
			t.Fatalf("expected woken=true without wakeError, got %v", wt)
		}
	}
	idleReqID := "board-wake:task:t-wake:1:s-wake-idle"
	busyReqID := "board-wake:task:t-wake:1:s-wake-busy"
	expectedWakeText := "[board #1] coordinator (brief): Wake @worker-idle and @worker-busy now.\nRead new board posts with board_read and reply via board_post."
	if n := psqlLines(t, e, "SELECT count(*) FROM message WHERE session_id='s-wake-idle' AND request_id='"+idleReqID+"' AND source='wake' AND text LIKE '[board #1] coordinator (brief): Wake @worker-idle and @worker-busy now.%Read new board posts with board_read and reply via board_post.'"); len(n) != 1 || n[0] != "1" {
		t.Fatalf("expected 1 wake message for s-wake-idle with request_id %s, got %v", idleReqID, n)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM queued_message WHERE session_id='s-wake-busy' AND request_id='"+busyReqID+"' AND state='queued' AND source='wake'"); len(n) != 1 || n[0] != "1" {
		t.Fatalf("expected 1 queued wake message for s-wake-busy with request_id %s, got %v", busyReqID, n)
	}
	// Replaying wake submission with the same idempotent request_id must not create duplicate executions or messages.
	if err := boardWakeTarget(backgroundRequest(t.Context(), e.app, nil), "task", "t-wake", 1, "s-wake-idle", expectedWakeText); err != nil {
		t.Fatalf("idempotent boardWakeTarget replay = %v", err)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM execution WHERE session_id='s-wake-idle'"); len(n) != 1 || n[0] != "1" {
		t.Fatalf("expected 1 execution after idempotent wake replay, got %v", n)
	}

	// 2. wake: false skips submission and reports woken: false.
	c, p2 := call("board_post", doc{
		"projectId":    "board-wake",
		"scopeKind":    "task",
		"scopeId":      "t-wake",
		"threadId":     "th-1",
		"authorMember": "coordinator",
		"authorRole":   "coordinator",
		"kind":         "message",
		"text":         "FYI only @worker-idle @worker-busy",
		"wake":         false,
	})
	if c != 200 {
		t.Fatalf("board_post wake=false = %d %v", c, p2)
	}
	for _, raw := range anys(p2["wakeTargets"]) {
		wt := mapOf(raw)
		if wt["woken"] != false || wt["wakeError"] != nil {
			t.Fatalf("expected woken=false without wakeError for wake=false, got %v", wt)
		}
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM message WHERE session_id='s-wake-idle' AND request_id='board-wake:task:t-wake:2:s-wake-idle'"); len(n) != 1 || n[0] != "0" {
		t.Fatalf("wake=false created message on s-wake-idle: %v", n)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM queued_message WHERE session_id='s-wake-busy' AND request_id='board-wake:task:t-wake:2:s-wake-busy'"); len(n) != 1 || n[0] != "0" {
		t.Fatalf("wake=false created queued_message on s-wake-busy: %v", n)
	}

	// 3. Archived and missing target sessions record woken=false + wakeError without failing board_post, while valid targets still wake.
	c, p3 := call("board_post", doc{
		"projectId":    "board-wake",
		"scopeKind":    "task",
		"scopeId":      "t-wake",
		"threadId":     "th-1",
		"authorMember": "coordinator",
		"authorRole":   "coordinator",
		"kind":         "message",
		"text":         "Ping @worker-arch @worker-miss @worker-busy",
	})
	if c != 200 || num(mapOf(p3["message"])["seq"]) != 3 {
		t.Fatalf("board_post with invalid/archived targets failed: %d %v", c, p3)
	}
	byMember := map[string]map[string]any{}
	for _, raw := range anys(p3["wakeTargets"]) {
		wt := mapOf(raw)
		byMember[astr(wt["member"])] = wt
	}
	if wt := byMember["worker-arch"]; wt == nil || wt["woken"] != false || astr(wt["wakeError"]) == "" {
		t.Fatalf("expected archived target wakeError, got %v", wt)
	}
	if wt := byMember["worker-miss"]; wt == nil || wt["woken"] != false || astr(wt["wakeError"]) == "" {
		t.Fatalf("expected missing target wakeError, got %v", wt)
	}
	if wt := byMember["worker-busy"]; wt == nil || wt["woken"] != true || wt["wakeError"] != nil {
		t.Fatalf("expected valid sibling target woken=true, got %v", wt)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM board_message WHERE scope_kind='task' AND scope_id='t-wake' AND seq=3"); len(n) != 1 || n[0] != "1" {
		t.Fatalf("expected committed board_message seq=3 despite wake errors, got %v", n)
	}
}

// Requirement: FLT-19 swarm-v2 (docs/designs/fleet-swarm.md): board_read supports server-side waitSeconds long-poll
// without holding a DB snapshot across the wait, work_board_get("pipeline_run", ...) falls back to the primary step
// session's board when the pipeline_run has no direct board rows, and board_post skips waking targets whose scope or
// parent pipeline_run/task is already terminal.
// Risk: board_read waitSeconds hangs or misses posts between read and wait, Lab pipeline_run trials hide coordinator
// session boards, or late board_post wakes spawn post-trial orphan turns after a run/task has already ended.
func TestBoardV2WaitScopeAndTerminalWake(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()

	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','board-v2','config/projects/board-v2/project.json',1,'board-v2');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','board-v2',1,'{"settings":{"id":"board-v2","name":"Board V2","contextFiles":[]},"contextFiles":[]}','bv2','editor','person','t');
INSERT INTO host(id,host_id,project_id,state) VALUES ('h-v2','h-v2','board-v2','active');
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-v2-live','V2 live task','board-v2','in_progress'),
 ('t-v2-done','V2 completed task','board-v2','completed');
INSERT INTO execution(id,kind,state,project_id,parent_execution_id,session_id,host_id,harness,model,started_at,data) VALUES
 ('run-v2','pipeline_run','running','board-v2',NULL,NULL,NULL,NULL,NULL,now()-interval '20 seconds','{"id":"run-v2","pipelineId":"swarm-plan-execute"}'),
 ('step-v2','pipeline_step','running','board-v2','run-v2','s-v2-coord','h-v2','codex','gpt-6.2-sol',now()-interval '15 seconds','{"id":"step-v2","parentExecutionId":"run-v2","sessionId":"s-v2-coord"}');
INSERT INTO session(id,project_id,task_id,host_id,pipeline_run_id,created_by_session_id,payload) VALUES
 ('s-v2-coord','board-v2',NULL,'h-v2','run-v2',NULL,'{"agentConfig":{"id":"swarm-coordinator","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-v2-w1','board-v2',NULL,'h-v2',NULL,'s-v2-coord','{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}'),
 ('s-v2-task-done','board-v2','t-v2-done','h-v2',NULL,NULL,'{"agentConfig":{"id":"coding-worker","harness":"codex","model":"gpt-6.2-sol"}}');
`)

	call := func(op string, body any) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/"+op, body, owner)
	}

	// 1. board_read waitSeconds: short timeout returns cleanly when stream is nil, and wake returns immediately when a post arrives.
	start := time.Now()
	c, rTimeout := call("board_read", doc{
		"scopeKind":   "task",
		"scopeId":     "t-v2-live",
		"since":       0,
		"waitSeconds": 1,
	})
	if c != 200 || len(anys(rTimeout["messages"])) != 0 {
		t.Fatalf("board_read waitSeconds=1 empty = %d %v", c, rTimeout)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("board_read waitSeconds=1 took too long: %v", elapsed)
	}

	bCtx, bCancel := context.WithCancel(e.ctx)
	b := &Broadcast{subs: map[chan struct{}]bool{}, ctx: bCtx, cancel: bCancel, done: make(chan struct{}), feedWake: make(chan struct{}, 1)}
	e.app.stream = b
	defer func() {
		b.close()
		close(b.done)
		e.app.stream = nil
	}()

	type readRes struct {
		code int
		body map[string]any
	}
	ch := make(chan readRes, 1)
	go func() {
		rc, rb := e.callJSON(t, "POST", "/api/ops/board_read", doc{
			"scopeKind":   "task",
			"scopeId":     "t-v2-live",
			"since":       0,
			"waitSeconds": 20,
		}, owner)
		ch <- readRes{rc, rb}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		nSubs := len(b.subs)
		b.mu.Unlock()
		if nSubs > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("board_read did not subscribe to stream")
		}
		time.Sleep(10 * time.Millisecond)
	}

	c, pWake := call("board_post", doc{
		"projectId":    "board-v2",
		"scopeKind":    "task",
		"scopeId":      "t-v2-live",
		"authorMember": "worker-1",
		"kind":         "progress",
		"text":         "Woken post",
		"wake":         false,
	})
	if c != 200 || num(mapOf(pWake["message"])["seq"]) != 1 {
		t.Fatalf("board_post for wake test = %d %v", c, pWake)
	}
	b.wake()

	select {
	case got := <-ch:
		if got.code != 200 || len(anys(got.body["messages"])) != 1 || num(mapOf(anys(got.body["messages"])[0])["seq"]) != 1 {
			t.Fatalf("woken board_read = %d %v", got.code, got.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("board_read waitSeconds did not return after stream wake")
	}

	// 2. work_board_get("pipeline_run", ...) falls back to the primary step's session board.
	c, wp := call("work_plan_set", doc{
		"projectId": "board-v2",
		"scopeKind": "session",
		"scopeId":   "s-v2-coord",
		"title":     "Step session swarm",
		"strategy":  "1-worker",
		"threads": []any{
			doc{
				"id":     "th-1",
				"title":  "Execute",
				"status": "active",
				"tasks": []any{
					doc{"id": "tk-1", "title": "Implement", "ownerMember": "worker-1", "role": "worker", "sessionId": "s-v2-w1", "status": "in_progress"},
				},
			},
		},
	})
	if c != 200 {
		t.Fatalf("work_plan_set on session = %d %v", c, wp)
	}
	c, ps := call("board_post", doc{
		"projectId":       "board-v2",
		"scopeKind":       "session",
		"scopeId":         "s-v2-coord",
		"threadId":        "th-1",
		"authorMember":    "coordinator",
		"authorRole":      "coordinator",
		"authorSessionId": "s-v2-coord",
		"kind":            "brief",
		"text":            "Start @worker-1",
	})
	if c != 200 || len(anys(ps["wakeTargets"])) != 1 || anys(ps["wakeTargets"])[0].(map[string]any)["woken"] != true {
		t.Fatalf("board_post on running session = %d %v", c, ps)
	}

	c, wb := call("work_board_get", doc{
		"scopeKind": "pipeline_run",
		"scopeId":   "run-v2",
	})
	if c != 200 {
		t.Fatalf("work_board_get pipeline_run fallback = %d %v", c, wb)
	}
	if astr(mapOf(wb["plan"])["title"]) != "Step session swarm" || num(wb["headSeq"]) != 1 || len(anys(wb["messages"])) != 1 {
		t.Fatalf("expected pipeline_run fallback to session board, got %v", wb)
	}
	if num(mapOf(wb["summary"])["messagesTotal"]) != 1 || len(anys(wb["roster"])) != 2 {
		t.Fatalf("expected session summary/roster via pipeline_run fallback, got %v", wb)
	}

	// 3. board_post skips waking targets when scope or parent pipeline_run/task is terminal.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
UPDATE execution SET state='succeeded', ended_at=now() WHERE id='run-v2';
`)

	// 3a. Session scope whose parent pipeline_run is terminal -> target wake skipped with "scope already terminal".
	c, pTermSess := call("board_post", doc{
		"projectId":       "board-v2",
		"scopeKind":       "session",
		"scopeId":         "s-v2-coord",
		"threadId":        "th-1",
		"authorMember":    "coordinator",
		"authorRole":      "coordinator",
		"authorSessionId": "s-v2-coord",
		"kind":            "done",
		"text":            "Late post @worker-1",
	})
	if c != 200 {
		t.Fatalf("board_post on terminal parent pipeline_run session = %d %v", c, pTermSess)
	}
	targetsSess := anys(pTermSess["wakeTargets"])
	if len(targetsSess) != 1 || mapOf(targetsSess[0])["woken"] != false || astr(mapOf(targetsSess[0])["wakeError"]) != "scope already terminal" {
		t.Fatalf("expected woken=false + wakeError='scope already terminal' on terminal parent run, got %v", targetsSess)
	}
	// Only the 1 pre-termination wake message should exist on s-v2-w1.
	if n := psqlLines(t, e, "SELECT count(*) FROM message WHERE session_id='s-v2-w1'"); len(n) != 1 || n[0] != "1" {
		t.Fatalf("expected no new wake message on s-v2-w1 after terminal run, got %v", n)
	}

	// 3b. Direct pipeline_run scope already terminal -> target wake skipped.
	c, _ = call("work_plan_set", doc{
		"projectId": "board-v2",
		"scopeKind": "pipeline_run",
		"scopeId":   "run-v2",
		"title":     "Run board",
		"threads": []any{
			doc{"id": "th-r", "title": "R", "tasks": []any{doc{"id": "tk-r", "title": "K", "ownerMember": "worker-1", "sessionId": "s-v2-w1"}}},
		},
	})
	if c != 200 {
		t.Fatalf("work_plan_set pipeline_run = %d", c)
	}
	c, pTermRun := call("board_post", doc{
		"projectId":    "board-v2",
		"scopeKind":    "pipeline_run",
		"scopeId":      "run-v2",
		"authorMember": "coordinator",
		"kind":         "message",
		"text":         "Orphan @worker-1",
	})
	if c != 200 {
		t.Fatalf("board_post pipeline_run terminal = %d %v", c, pTermRun)
	}
	targetsRun := anys(pTermRun["wakeTargets"])
	if len(targetsRun) != 1 || mapOf(targetsRun[0])["woken"] != false || astr(mapOf(targetsRun[0])["wakeError"]) != "scope already terminal" {
		t.Fatalf("expected pipeline_run terminal wake skip, got %v", targetsRun)
	}

	// 3c. Direct task scope already terminal -> target wake skipped.
	c, _ = call("work_plan_set", doc{
		"projectId": "board-v2",
		"scopeKind": "task",
		"scopeId":   "t-v2-done",
		"title":     "Done task board",
		"threads": []any{
			doc{"id": "th-d", "title": "D", "tasks": []any{doc{"id": "tk-d", "title": "K", "ownerMember": "worker-done", "sessionId": "s-v2-task-done"}}},
		},
	})
	if c != 200 {
		t.Fatalf("work_plan_set task = %d", c)
	}
	c, pTermTask := call("board_post", doc{
		"projectId":    "board-v2",
		"scopeKind":    "task",
		"scopeId":      "t-v2-done",
		"authorMember": "coordinator",
		"kind":         "message",
		"text":         "Orphan @worker-done",
	})
	if c != 200 {
		t.Fatalf("board_post task terminal = %d %v", c, pTermTask)
	}
	targetsTask := anys(pTermTask["wakeTargets"])
	if len(targetsTask) != 1 || mapOf(targetsTask[0])["woken"] != false || astr(mapOf(targetsTask[0])["wakeError"]) != "scope already terminal" {
		t.Fatalf("expected task terminal wake skip, got %v", targetsTask)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM message WHERE session_id='s-v2-task-done'"); len(n) != 1 || n[0] != "0" {
		t.Fatalf("expected 0 wake messages on terminal task session, got %v", n)
	}
}
