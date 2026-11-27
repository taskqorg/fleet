package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func runLifecycleWake(t *testing.T, e *testCoordEnv, id string, now time.Time) {
	t.Helper()
	k := newWorkKit(e.app, func() time.Time { return now }, 1)
	var found *kitClaim
	if err := k.read(e.ctx, func(q *Request) error {
		claims, err := taskWakesDue(q, now, 100)
		for _, claim := range claims {
			if claim.Key == id {
				found = claim
				break
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatalf("missing due wake %s", id)
	}
	if err := k.runOne(e.ctx, taskWakeQueue, found); err != nil {
		t.Fatal(err)
	}
}

func lifecycleSQL(t *testing.T, e *testCoordEnv, sql string, args ...any) {
	t.Helper()
	if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func lifecycleTask(t *testing.T, e *testCoordEnv, taskID, sid, run, state string) {
	t.Helper()
	lifecycleSQL(t, e, "INSERT INTO task(id,title,state,directed_by) VALUES($1,$1,'in_progress',$2)", taskID, testOwnerEmail)
	lifecycleSQL(t, e, "INSERT INTO session(id,task_id,directed_by) VALUES($1,$2,$3)", sid, taskID, testOwnerEmail)
	lifecycleSQL(t, e, `INSERT INTO execution(id,kind,state,observation,task_id,session_id,directed_by,started_at,ended_at)
		VALUES($1,'agent_run',$2::execution_state,'confirmed',$3,$4,$5,now()-interval '2400.530 seconds',
		CASE WHEN $2::execution_state='succeeded' THEN now() ELSE NULL END)`, run, state, taskID, sid, testOwnerEmail)
}

func TestTaskQuestionWakeReplay(t *testing.T) {
	e := openTestCoordinator(t, false)
	for _, tc := range []struct {
		kind, ingress string
		busy, read    bool
	}{
		{kind: "agent_question", ingress: "question_answer"},
		{kind: "agent_question", ingress: "reply", busy: true},
		{kind: "agent_question", ingress: "question_answer", busy: true, read: true},
		{kind: "operator_decision", ingress: "question_answer", busy: true},
	} {
		t.Run(fmt.Sprintf("%s/%s/busy=%t/read=%t", tc.kind, tc.ingress, tc.busy, tc.read), func(t *testing.T) {
			taskID, sid, run, qid := newID(), newID(), newID(), newID()
			state := "succeeded"
			if tc.busy {
				state = "running"
			}
			lifecycleTask(t, e, taskID, sid, run, state)
			if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
				_, err := askQuestionAs(q, questionAsk{ID: qid, Kind: tc.kind, Text: "Which target?", TaskID: taskID, SessionID: &sid, ExecutionID: &run, AskedBy: ptr("execution:" + run)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			answer := func() {
				e.opInto(t, tc.ingress, doc{"questionId": qid, "answer": "Use the existing target"}, e.ownerHeaders(), &map[string]any{})
			}
			check := func(receipts int, disposition *string) {
				t.Helper()
				var count int
				var got *string
				if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT disposition,(SELECT count(*) FROM message WHERE request_id=$1) FROM task_wake WHERE id=$1`, "reply:"+qid).Scan(&got, &count); err != nil {
					t.Fatal(err)
				}
				if count != receipts || (got == nil) != (disposition == nil) || deref(got) != deref(disposition) {
					t.Fatalf("receipt count %d, disposition %q, want %d, %q", count, deref(got), receipts, deref(disposition))
				}
			}
			answer()
			answer()
			check(0, nil)
			now := time.Now().Add(time.Second)
			if tc.busy {
				steer := newID()
				lifecycleSQL(t, e, `INSERT INTO queued_message(id,session_id,task_id,request_id,text,state,execution_id)
					VALUES($1,$2,$3,$1,'existing steer','steering',$4)`, steer, sid, taskID, run)
				runLifecycleWake(t, e, "reply:"+qid, now)
				check(0, nil)
				if tc.read {
					e.opInto(t, "ask", doc{"executionId": run, "resume": qid}, e.ownerHeaders(), &map[string]any{})
					check(0, ptr("read by asking session"))
					answer()
					check(0, ptr("read by asking session"))
					return
				}
				lifecycleSQL(t, e, "UPDATE execution SET state='succeeded',ended_at=now() WHERE id=$1", run)
				runLifecycleWake(t, e, "reply:"+qid, now.Add(6*time.Second))
				check(0, nil)
				lifecycleSQL(t, e, "UPDATE queued_message SET state='cancelled' WHERE id=$1", steer)
			}
			runLifecycleWake(t, e, "reply:"+qid, now.Add(12*time.Second))
			check(1, ptr("ordinary message admitted"))
			answer()
			check(1, ptr("ordinary message admitted"))
			if !tc.busy {
				// A legacy receipt remains proof even after the asking session is archived.
				lifecycleSQL(t, e, "UPDATE session SET archived_at=now() WHERE id=$1", sid)
				lifecycleSQL(t, e, "DELETE FROM task_wake WHERE id=$1", "reply:"+qid)
				answer()
				runLifecycleWake(t, e, "reply:"+qid, time.Now().Add(time.Second))
				check(1, ptr("ordinary message admitted"))
			}
		})
	}
}

func TestTaskMissingResultRecovery(t *testing.T) {
	e := openTestCoordinator(t, false)
	taskID, sid, run := newID(), newID(), newID()
	lifecycleTask(t, e, taskID, sid, run, "running")
	// The terminal fact and wake commit together. No in-process callback is needed to consume the wake.
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		ex, err := getExecution(q, run)
		if err != nil {
			return err
		}
		ex.State, ex.FinishedAt = "succeeded", jsonBytes(nowMs())
		return executionPut(q, &ex)
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	runLifecycleWake(t, e, "task:"+taskID, now)
	var recovery db.TaskRecovery
	if err := e.appStore.Read(e.ctx, nil, func(q *Request) (err error) {
		recovery, err = q.db().TaskRecoveryGet(q.ctx(), taskID)
		return err
	}); err != nil || recovery.Turns != 1 || deref(recovery.ExecutionID) == "" {
		var disposition, question string
		_ = e.ownerStore.pool.QueryRow(e.ctx, `SELECT coalesce(disposition,''),coalesce((SELECT text FROM question WHERE task_id=$1 AND state='open' LIMIT 1),'') FROM task_wake WHERE id='task:'||$1`, taskID).Scan(&disposition, &question)
		t.Fatalf("recovery reservation: %+v, %v; disposition=%q question=%q", recovery, err, disposition, question)
	}
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL, DISPATCH}, func(q *Request) error {
		allowed, deadline, err := taskAutomaticStartAllowed(q, *recovery.ExecutionID)
		if err != nil || !allowed || !deadline.Equal(recovery.Deadline) {
			return fmt.Errorf("automatic turn fence: allowed=%t deadline=%s error=%v", allowed, deadline, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An owned running recovery keeps custody of its stop deadline even while paused.
	lifecycleSQL(t, e, "UPDATE execution SET state='running' WHERE id=$1", *recovery.ExecutionID)
	e.opInto(t, "task_update", doc{"taskId": taskID, "action": "pause"}, e.ownerHeaders(), &contract.TaskDetail{})
	runLifecycleWake(t, e, "task:"+taskID, time.Now().Add(time.Second))
	var next time.Time
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT next_attempt_at FROM task_wake WHERE id=$1 AND disposition IS NULL", "task:"+taskID).Scan(&next); err != nil || !next.Equal(recovery.Deadline) {
		t.Fatalf("paused recovery deadline custody %s, %v", next, err)
	}
	e.opInto(t, "task_update", doc{"taskId": taskID, "action": "resume"}, e.ownerHeaders(), &contract.TaskDetail{})
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		ex, err := getExecution(q, *recovery.ExecutionID)
		if err != nil {
			return err
		}
		link, err := q.db().TaskExecutionLink(q.ctx(), ex.ID)
		if err != nil {
			return err
		}
		if deref(link.SessionID) != sid || deref(link.TaskID) != taskID {
			return fmt.Errorf("wrong continuation lineage: %s %s", deref(link.SessionID), deref(link.TaskID))
		}
		ex.State, ex.Observation, ex.FinishedAt = "succeeded", "confirmed", jsonBytes(nowMs())
		return executionPut(q, &ex)
	}); err != nil {
		t.Fatal(err)
	}
	runLifecycleWake(t, e, "task:"+taskID, time.Now().Add(time.Second))
	var decision contract.QuestionsListReply
	e.opInto(t, "questions_list", doc{"taskId": taskID, "state": "open", "kind": "operator_decision"}, e.ownerHeaders(), &decision)
	if len(decision.Questions) != 1 {
		t.Fatalf("want one visible no-progress decision, got %+v", decision.Questions)
	}

	// A missed historic answer is repaired by the independent source scan.
	taskID, sid, run = newID(), newID(), newID()
	lifecycleTask(t, e, taskID, sid, run, "succeeded")
	qid := newID()
	lifecycleSQL(t, e, `INSERT INTO question(id,kind,state,task_id,session_id,execution_id,asked_by,text,answer,answered_at)
		VALUES($1,'agent_question','answered',$2,$3,$4,'execution:'||$4,'Where?','Here',now())`, qid, taskID, sid, run)
	var audit contract.TaskLifecycleCheckReply
	e.opInto(t, "task_lifecycle_check", doc{}, e.ownerHeaders(), &audit)
	if audit.MissingReplyWakes != 1 || audit.SampleLimit != taskWakeCohort {
		t.Fatalf("auditor missed the orphan answer: %+v", audit)
	}
	if err := auditTaskLifecycle(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	runLifecycleWake(t, e, "reply:"+qid, time.Now().Add(time.Second))
	var count int
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM message WHERE request_id=$1", "reply:"+qid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("auditor repair delivered %d messages, err %v", count, err)
	}
}

func TestTaskRecoveryLimits(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		turns    int32
		progress []string
		age      time.Duration
		allowed  bool
	}{
		{name: "first", allowed: true},
		{name: "same evidence", turns: 1, progress: []string{"artifact:a"}},
		{name: "missing evidence", turns: 1},
		{name: "verified milestone", turns: 1, progress: []string{"artifact:a", "review:b"}, allowed: true},
		{name: "hard ceiling", turns: 2, progress: []string{"artifact:a", "review:b"}},
		{name: "deadline wins", turns: 1, progress: []string{"artifact:a", "review:b"}, age: recoveryWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := db.TaskRecovery{Turns: tc.turns, Deadline: now.Add(recoveryWindow), Progress: []string{"artifact:a"}}
			if got := recoveryStopReason(now.Add(tc.age), r, tc.progress); (got == "") != tc.allowed {
				t.Fatalf("disposition = %q, allowed=%t", got, tc.allowed)
			}
		})
	}
}

func TestTaskRetryWithDifferentSessionDirector(t *testing.T) {
	e := openTestCoordinator(t, false)
	taskID, sid, run, artifactID := newID(), newID(), newID(), newID()
	lifecycleTask(t, e, taskID, sid, run, "succeeded")
	lifecycleSQL(t, e, "UPDATE execution SET directed_by='invented-current-director' WHERE id=$1", run)
	lifecycleSQL(t, e, `INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,state)
		VALUES($1,'prod',$2,5,'text/plain','result.txt',$3,$4,'stored')`, artifactID, sha256hex([]byte("bytes")), run, taskID)

	// Reproduce a legacy retry card already awaiting the owner when the new reducer starts.
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		task, err := taskLoad(q, taskID)
		if err != nil {
			return err
		}
		if err := q.db().TaskRecoveryStart(q.ctx(), db.TaskRecoveryStartParams{TaskID: taskID, Deadline: time.Now().Add(recoveryWindow)}); err != nil {
			return err
		}
		return askTaskRecovery(q, task, run, sid, "The previous session is unavailable.", "missing_result")
	}); err != nil {
		t.Fatal(err)
	}
	h := e.ownerHeaders()
	openQuestion := func(kind string) contract.QuestionRow {
		t.Helper()
		var result contract.QuestionsListReply
		e.opInto(t, "questions_list", doc{"taskId": taskID, "state": "open", "kind": kind}, h, &result)
		if len(result.Questions) != 1 {
			t.Fatalf("want one open %s, got %+v", kind, result.Questions)
		}
		return result.Questions[0]
	}
	answer := func(id, option string) {
		t.Helper()
		e.opInto(t, "question_answer", doc{"questionId": id, "answer": option}, h, &contract.QuestionRow{})
	}
	reconcile := func() {
		t.Helper()
		if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return wakeTask(q, taskID) }); err != nil {
			t.Fatal(err)
		}
		runLifecycleWake(t, e, "task:"+taskID, time.Now().Add(time.Second))
	}
	retry := openQuestion("operator_decision")
	answer(retry.Id, idleRetryResult)
	reconcile()
	blocked := openQuestion("operator_decision")
	var options []string
	if err := json.Unmarshal(blocked.Options, &options); err != nil ||
		!slices.Equal(options, []string{idleFail, idleOfferRetainedResult}) ||
		!strings.Contains(blocked.Text, "different directing actors") {
		t.Fatalf("want an actionable authority prerequisite, got %+v, %v", blocked, err)
	}
	answer(retry.Id, idleRetryResult)
	reconcile()
	if got := openQuestion("operator_decision"); got.Id != blocked.Id {
		t.Fatalf("replay replaced the prerequisite: %s -> %s", blocked.Id, got.Id)
	}

	assertOfferBlocked := func() {
		t.Helper()
		code, got := e.callJSON(t, "POST", "/api/ops/question_answer", doc{"questionId": blocked.Id, "answer": idleOfferRetainedResult}, h)
		if code != 409 {
			t.Fatalf("want result offer refused, got %d %v", code, got)
		}
		if got := openQuestion("operator_decision"); got.Id != blocked.Id {
			t.Fatalf("refused offer replaced the prerequisite: %s -> %s", blocked.Id, got.Id)
		}
	}
	e.opInto(t, "task_update", doc{"taskId": taskID, "action": "pause"}, h, &contract.TaskDetail{})
	assertOfferBlocked()
	e.opInto(t, "task_update", doc{"taskId": taskID, "action": "resume"}, h, &contract.TaskDetail{})
	lifecycleSQL(t, e, "UPDATE task SET expected_outcome='Landing: wait — live check: verify production' WHERE id=$1", taskID)
	assertOfferBlocked()
	lifecycleSQL(t, e, "UPDATE task SET expected_outcome=NULL WHERE id=$1", taskID)

	answer(blocked.Id, idleOfferRetainedResult)
	answer(blocked.Id, idleOfferRetainedResult)
	reconcile()
	offer := openQuestion("acceptance")
	var artifacts []string
	if err := json.Unmarshal(offer.ArtifactIds, &artifacts); err != nil || deref(offer.ExecutionId) != run ||
		!slices.Equal(artifacts, []string{artifactID}) {
		t.Fatalf("offer must identify the exact retained result: %+v, %v", offer, err)
	}
	var generation, turns, runs, decisions int
	var acceptedAt *time.Time
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT generation,turns,
		(SELECT count(*) FROM execution WHERE task_id=$1 AND kind='agent_run'),
		(SELECT count(*) FROM question WHERE task_id=$1 AND kind='operator_decision'),
		(SELECT accepted_at FROM task WHERE id=$1)
		FROM task_recovery WHERE task_id=$1`, taskID).Scan(&generation, &turns, &runs, &decisions, &acceptedAt); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || turns != 0 || runs != 1 || decisions != 2 || acceptedAt != nil {
		t.Fatalf("want one renewed budget, no new work, one prerequisite, and an unaccepted offer, got generation=%d turns=%d runs=%d decisions=%d accepted=%v",
			generation, turns, runs, decisions, acceptedAt)
	}
}

func TestTaskDependencyWake(t *testing.T) {
	e := openTestCoordinator(t, false)
	const project = "dependency-wake-test"
	h := e.ownerHeaders()
	e.opInto(t, "project_create", doc{"id": project, "requestId": project, "content": doc{
		"settings":     doc{"id": project, "name": project, "contextFiles": []string{"rules.md"}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Dependency fixture."}},
	}}, h, &map[string]any{})
	var dependent, first, second contract.TaskDetail
	e.opInto(t, "task_create", doc{"title": "Waiting work", "projectId": project}, h, &dependent)
	lifecycleSQL(t, e, "UPDATE task SET state='in_progress' WHERE id=$1", dependent.Id)
	e.opInto(t, "task_create", doc{"title": "First blocker", "projectId": project, "blocksTaskId": dependent.Id}, h, &first)
	e.opInto(t, "task_create", doc{"title": "Second blocker", "projectId": project, "blocksTaskId": dependent.Id}, h, &second)
	runLifecycleWake(t, e, "task:"+dependent.Id, time.Now().Add(time.Second))
	complete := func(blocker string) {
		t.Helper()
		if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
			_, err := taskTransition(q, blocker, "accepted", "")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Second)
		runLifecycleWake(t, e, "blocker:"+blocker, at)
		runLifecycleWake(t, e, "task:"+dependent.Id, at)
	}
	check := func(want string) {
		t.Helper()
		if err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
			task, err := taskLoad(q, dependent.Id)
			if err != nil {
				return err
			}
			if deref(task.WaitReason) != want {
				return fmt.Errorf("wait reason %q, want %q", deref(task.WaitReason), want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("dependency")
	complete(first.Id)
	check("dependency")
	complete(second.Id)
	check("")
	if err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
		_, err := q.db().TaskRecoveryGet(q.ctx(), dependent.Id)
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("dependency completion created an unrelated recovery episode: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
