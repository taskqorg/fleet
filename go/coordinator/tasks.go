// Tasks and questions (docs/designs/fleet-data-foundation.md §2): state only through transition(), one typed question table with one
// answer path, the operator-app task list (page, counts, groups, membership) and the task operations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"

	"fleet/contract"
)

var taskStates = []string{"queued", "in_progress", "waiting", "paused", "completed", "failed", "cancelled"}

// listStates: the states of lists and counts; "held" is a paused task with pause_reason 'held' (fleet-pm-agent.md §4).
var listStates = append([]string{"held"}, taskStates...)

const stateSQL = "(CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)"

var openTask = map[string]bool{"queued": true, "in_progress": true, "waiting": true, "paused": true}

type taskRule struct {
	from []string
	to   string
}

var taskTransitions = map[string]taskRule{
	"first_execution": {[]string{"queued"}, "in_progress"},
	"blocked":         {[]string{"queued", "in_progress"}, "waiting"},
	"unblocked":       {[]string{"waiting"}, "in_progress"},
	"accepted":        {[]string{"in_progress"}, "completed"},
	"rejected":        {[]string{"in_progress"}, "in_progress"},
	"operator_fail":   {[]string{"in_progress", "waiting"}, "failed"},
	"pause":           {[]string{"queued", "in_progress", "waiting"}, "paused"},
	"hold":            {[]string{"queued"}, "paused"},
	"release":         {[]string{"paused"}, "previous"},
	"resume":          {[]string{"paused"}, "previous"},
	"cancel":          {[]string{"queued", "in_progress", "waiting", "paused"}, "cancelled"},
}

var priorities = map[string]string{"urgent": "urgent", "normal": "normal", "background": "background", "p0": "urgent", "p1": "normal", "p2": "background"}

func priorityClass(v any) (string, error) {
	if s, ok := v.(string); ok && priorities[s] != "" {
		return priorities[s], nil
	}
	return "", fail(400, "Invalid priority: use urgent, normal or background")
}

func taskRow(q *Request, id string) (doc, error) {
	t, err := first(q, "SELECT * FROM task WHERE id=$1", id)
	if err == nil && t == nil {
		return nil, errNotFound
	}
	return t, err
}

func waitReason(q *Request, id string) (string, error) {
	if ok, err := has(q, "SELECT 1 FROM question WHERE task_id=$1 AND state='open' AND kind IN ('operator_decision','agent_question','secret')", id); err != nil || ok {
		return "question", err
	}
	if ok, err := openDependency(q, id); err != nil || ok {
		return "dependency", err
	}
	return "", nil
}

func openDependency(q *Request, id string) (bool, error) {
	return has(q, "SELECT 1 FROM task_dependency d JOIN task t ON t.id=d.depends_on_task_id WHERE d.task_id=$1 AND d.kind='blocks' AND t.state<>'completed'", id)
}

func taskTransition(q *Request, id, trigger, reason string) (string, error) {
	t, err := taskRow(q, id)
	if err != nil {
		return "", err
	}
	rule := taskTransitions[trigger]
	state := astr(t["state"])
	if !slices.Contains(rule.from, state) {
		return "", fail(409, fmt.Sprintf("Task %s cannot %s", state, strings.Replace(trigger, "_", " ", 1)))
	}
	if trigger == "first_execution" {
		started, err := has(q, "SELECT 1 FROM execution WHERE task_id=$1 AND state<>'preparing'", id)
		if err != nil {
			return "", err
		}
		dep, err := openDependency(q, id)
		if err != nil {
			return "", err
		}
		if !started || dep {
			return "", fail(409, "Task has no started execution or a blocking dependency is open")
		}
	}
	if trigger == "unblocked" {
		if r, err := waitReason(q, id); err != nil || r != "" {
			if err == nil {
				err = fail(409, "Task is still blocked")
			}
			return "", err
		}
	}
	if trigger == "release" && astr(t["pause_reason"]) != "held" {
		return "", fail(409, "Paused by a person: resume it")
	}
	to := rule.to
	if to == "previous" {
		to = astr(t["state_before_pause"])
	}
	var wr, before any
	if to == "waiting" {
		if reason == "" {
			if reason, err = waitReason(q, id); err != nil {
				return "", err
			}
		}
		wr = nilIf(reason)
	}
	var pauseReason any
	if trigger == "pause" || trigger == "hold" {
		before = state
	}
	if trigger == "hold" {
		pauseReason = "held"
	}
	return to, exec(q, "UPDATE task SET state=$2,wait_reason=$3,state_before_pause=$4,pause_reason=$5,updated_at=now() WHERE id=$1", id, to, wr, before, pauseReason)
}

// settleTask moves an open task toward target through the transitions whose guards hold now.
func settleTask(q *Request, id, target string) (string, error) {
	t, err := taskRow(q, id)
	if err != nil {
		return "", err
	}
	state := astr(t["state"])
	if !openTask[state] || state == "paused" {
		return state, nil
	}
	reason, err := waitReason(q, id)
	if err != nil {
		return "", err
	}
	step := func(trigger, r string) error {
		to, err := taskTransition(q, id, trigger, r)
		var he *HTTPError
		if errors.As(err, &he) && he.Code == 409 {
			return nil
		}
		if err == nil {
			state = to
		}
		return err
	}
	if target == "failed" {
		if state == "queued" {
			if err := step("first_execution", ""); err != nil {
				return "", err
			}
		}
		if state == "in_progress" || state == "waiting" {
			err = step("operator_fail", "")
		}
		return state, err
	}
	if reason != "" || target == "waiting" {
		if state != "waiting" {
			if reason == "" {
				reason = "question"
			}
			err = step("blocked", reason)
		}
		return state, err
	}
	if state == "waiting" {
		if err := step("unblocked", ""); err != nil {
			return "", err
		}
	}
	if state == "queued" && target != "queued" {
		err = step("first_execution", "")
	}
	return state, err
}

func takeTask(q *Request, id, taker, from string) error {
	t, err := taskRow(q, id)
	if err != nil {
		return err
	}
	if astr(t["directed_by"]) == taker {
		return nil
	}
	prev := firstString(t["directed_by"], from)
	if err := exec(q, "UPDATE task SET directed_by=$2,taken_from=$3,updated_at=now() WHERE id=$1", id, taker, nilIf(prev)); err != nil {
		return err
	}
	return exec(q, "UPDATE execution SET directed_by=$2 WHERE task_id=$1 AND state IN ('preparing','running','waiting')", id, taker)
}

// askQuestion: v has kind, text and optional id, projectId, taskId, executionId, sessionId, options, askedBy, requestId, data, askedAt.
func askQuestion(q *Request, v doc) (string, error) {
	taskID, projectID := astr(v["taskId"]), astr(v["projectId"])
	if taskID == "" && astr(v["executionId"]) != "" {
		e, err := first(q, "SELECT task_id,project_id FROM execution WHERE id=$1", v["executionId"])
		if err != nil {
			return "", err
		}
		if e != nil {
			taskID = astr(e["task_id"])
			if projectID == "" {
				projectID = astr(e["project_id"])
			}
		}
	}
	id := firstString(v["id"], newID())
	var options any
	if v["options"] != nil {
		options = js(v["options"])
	}
	data := v["data"]
	if data == nil {
		data = doc{}
	}
	if err := exec(q, "INSERT INTO question(id,kind,project_id,task_id,execution_id,session_id,text,options,asked_by,asked_at,request_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,coalesce($10::timestamptz,now()),$11,$12)",
		id, v["kind"], nilIf(projectID), nilIf(taskID), v["executionId"], v["sessionId"], v["text"], options, v["askedBy"], msTime(v["askedAt"]), v["requestId"], js(data)); err != nil {
		return "", err
	}
	if taskID != "" && v["kind"] != "acceptance" {
		if _, err := settleTask(q, taskID, ""); err != nil {
			return "", err
		}
	}
	return id, nil
}

// idleTaskSQL: tasks in progress with nothing that can move them: no open execution (child agent rows do not count: no
// writer ends them), no open question, and no rejected acceptance since the run that ended last (a rejection waits for a
// new run, data foundation §3). l is the run that ended last; null when the task has no run at all.
const idleTaskSQL = `FROM task t LEFT JOIN LATERAL (SELECT x.id, x.state, x.session_id, x.ended_at FROM execution x WHERE x.task_id=t.id AND x.kind<>'child_agent'
	  ORDER BY x.ended_at DESC NULLS LAST, x.seq DESC LIMIT 1) l ON true
	WHERE t.state='in_progress'
	AND NOT EXISTS (SELECT 1 FROM execution x WHERE x.task_id=t.id AND x.kind<>'child_agent' AND x.state NOT IN ('succeeded','failed','cancelled'))
	AND NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.state='open')
	AND NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.kind='acceptance' AND qq.answer='rejected' AND qq.answered_at >= coalesce(l.ended_at, '-infinity'))`

// settleIdleTask: no task stays in progress with nothing that can move it. When it is idle (idleTaskSQL): if the run that
// ended last succeeded, its result goes to the operator for acceptance; otherwise the operator gets a decision that
// blocks the task, so it shows in Needs you: fail the task, or (when an earlier run succeeded) accept that result. A
// retry is a new run, which withdraws the decision (withdrawIdleDecision). Task 2273393d: its only run failed before it
// started and the task sat in progress with nothing to show it. Data foundation §3 task states. Legacy delivery tasks
// are included: nothing writes or reads delivery_method any more. Runs on every execution end, after an answer, on
// resume, and for every task at coordinator start (settleIdleTasks). Reports whether it asked.
func settleIdleTask(q *Request, id string) (bool, error) {
	t, err := first(q, `SELECT t.*, l.id AS latest_id, l.state::text AS latest, l.session_id AS latest_session,
		EXISTS (SELECT 1 FROM execution x WHERE x.task_id=t.id AND x.state='succeeded') AS succeeded `+idleTaskSQL+` AND t.id=$1`, id)
	if err != nil || t == nil {
		return false, err
	}
	if astr(t["latest"]) == "succeeded" {
		_, err = requestAcceptance(q, t, nil, nil, "fleet")
		return err == nil, err
	}
	options := []string{idleFail}
	if t["succeeded"] == true {
		options = append(options, idleAccept)
	}
	what := "The last run of " + astr(t["title"]) + " " + astr(t["latest"])
	if t["latest"] == nil {
		what = astr(t["title"]) + " is in progress with no run"
	}
	_, err = askQuestion(q, doc{"kind": "operator_decision", "taskId": id, "projectId": t["project_id"], "executionId": t["latest_id"], "sessionId": t["latest_session"],
		"askedBy": "fleet", "options": options, "data": doc{"idle": true},
		"text": what + ". Retry it with a new message in its session, or answer."})
	return err == nil, err
}

func init() {
	// At start: every idle task, one transaction each, so one bad task neither rolls back nor blocks the others.
	onStart(func(ctx context.Context, a *App) {
		var ids []doc
		if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { ids, err = rowsOf(q, "SELECT t.id "+idleTaskSQL+" ORDER BY t.created_at"); return }); err != nil {
			log.Printf("settle idle tasks: %v", err)
			return
		}
		n := 0
		for _, r := range ids {
			asked := false
			if err := runTx(ctx, a, nil, true, "", func(q *Request) (err error) { asked, err = settleIdleTask(q, astr(r["id"])); return }); err != nil {
				log.Printf("settle idle task %s: %v", astr(r["id"]), err)
			} else if asked {
				n++
			}
		}
		if n > 0 {
			log.Printf("settle idle tasks: %d tasks settled", n)
		}
	})
}

const idleFail, idleAccept = "fail task", "accept earlier result"

// withdrawIdleDecision: a new run of the task answers settleIdleTask's decision by itself.
func withdrawIdleDecision(q *Request, id string) error {
	return exec(q, "UPDATE question SET state='withdrawn' WHERE task_id=$1 AND state='open' AND data->>'idle'='true'", id)
}

// requestAcceptance asks the task's acceptance question with the given artifacts (default: its stored originals). An open
// acceptance question is reused and updated, so an explicit request after the automatic one never leaves a second open.
func requestAcceptance(q *Request, t doc, artifactIDs []string, text *string, askedBy string) (string, error) {
	id := astr(t["id"])
	ids := []any{}
	if artifactIDs != nil {
		for _, x := range artifactIDs {
			ids = append(ids, x)
		}
	} else {
		rs, err := rowsOf(q, "SELECT coalesce(legacy_id,id) AS id FROM artifact WHERE task_id=$1 AND state='stored' AND role='original' ORDER BY seq", id)
		if err != nil {
			return "", err
		}
		for _, x := range rs {
			ids = append(ids, x["id"])
		}
	}
	body := "Accept the result of: " + astr(t["title"])
	if text != nil {
		body = string([]rune(*text)[:min(len([]rune(*text)), 2000)])
	}
	open, err := first(q, "SELECT id FROM question WHERE task_id=$1 AND kind='acceptance' AND state='open' ORDER BY asked_at DESC LIMIT 1", id)
	if err != nil {
		return "", err
	}
	if open != nil {
		qid := astr(open["id"])
		return qid, exec(q, "UPDATE question SET text=$2,asked_by=$4,asked_at=now(),data=coalesce(data,'{}'::jsonb)||jsonb_build_object('artifactIds',$3::jsonb) WHERE id=$1", qid, body, js(ids), askedBy)
	}
	return askQuestion(q, doc{"kind": "acceptance", "taskId": id, "projectId": t["project_id"], "text": body, "options": []string{"accepted", "rejected"}, "askedBy": askedBy, "data": doc{"artifactIds": ids}})
}

// answerQuestion: one answer wins; an acceptance completes (or keeps in progress) its task, other answers unblock it.
// runAnswerChecks and runAnswerAfter apply the Lab and budget rules of the question.
func answerQuestion(q *Request, id, answer, by string, artifactIDs []any) (doc, error) {
	qu, err := first(q, "SELECT * FROM question WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	if qu == nil {
		return nil, errNotFound
	}
	if astr(qu["state"]) == "answered" {
		if astr(qu["answer"]) != answer {
			return nil, fail(409, "Question already answered differently")
		}
		// A replay (a client retry) keeps the owner-only rule of a key request; the answer itself was checked when given.
		if qu["kind"] == "secret" && (q.agent != "" || q.person == nil || !fullAccess(q.person)) {
			return nil, fail(403, "Only the owner answers a key request")
		}
		return qu, nil
	}
	if astr(qu["state"]) != "open" {
		return nil, fail(409, "Question withdrawn")
	}
	if astr(qu["kind"]) == "acceptance" && answer != "accepted" && answer != "rejected" {
		return nil, fail(400, "Acceptance answer is accepted or rejected")
	}
	if mapOf(qu["data"])["idle"] == true && answer != idleFail && answer != idleAccept {
		return nil, fail(400, "Answer "+idleFail+", or "+idleAccept+" when offered; retry with a new message in the session")
	}
	if err := runAnswerChecks(q, qu, answer); err != nil { // Lab and budget answers
		return nil, err
	}
	tag, err := q.tx.Exec(q.ctx(), "UPDATE question SET answer=$2,answered_by=$3,answered_at=now(),state='answered' WHERE id=$1 AND state='open'", id, answer, by)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		now, _ := first(q, "SELECT * FROM question WHERE id=$1", id)
		if astr(now["state"]) == "answered" && astr(now["answer"]) == answer {
			return now, nil
		}
		if astr(now["state"]) == "answered" {
			return nil, fail(409, "Question already answered differently")
		}
		return nil, fail(409, "Question withdrawn")
	}
	if err := runAnswerAfter(q, id); err != nil {
		return nil, err
	}
	if t := astr(qu["task_id"]); t != "" {
		if astr(qu["kind"]) == "acceptance" {
			if answer == "accepted" {
				if _, err := taskTransition(q, t, "accepted", ""); err != nil {
					return nil, err
				}
				ids := []string{}
				src := artifactIDs
				if src == nil {
					src, _ = mapOf(qu["data"])["artifactIds"].([]any)
				}
				for _, x := range src {
					if s, ok := x.(string); ok {
						ids = append(ids, s)
					}
				}
				if err := exec(q, "UPDATE task SET accepted_by=$2,accepted_at=now(),accepted_artifact_ids=$3 WHERE id=$1", t, by, ids); err != nil {
					return nil, err
				}
				if err := deliveredTeardown(q, t); err != nil {
					return nil, err
				}
			} else if _, err := taskTransition(q, t, "rejected", ""); err != nil {
				return nil, err
			}
		} else {
			idle := mapOf(qu["data"])["idle"] == true
			target := ""
			if idle && answer == idleFail {
				target = "failed"
			}
			if _, err := settleTask(q, t, target); err != nil {
				return nil, err
			}
			if idle && answer == idleAccept {
				task, err := taskRow(q, t)
				if err != nil {
					return nil, err
				}
				if _, err := requestAcceptance(q, task, nil, nil, by); err != nil {
					return nil, err
				}
			}
			if _, err := settleIdleTask(q, t); err != nil { // an answered decision may leave the task with nothing to move it
				return nil, err
			}
		}
	}
	return first(q, "SELECT * FROM question WHERE id=$1", id)
}

const questionCols = `id,code,kind,project_id AS "projectId",task_id AS "taskId",execution_id AS "executionId",session_id AS "sessionId",text,options,asked_by AS "askedBy",asked_at AS "askedAt",
 answer,answered_by AS "answeredBy",answered_at AS "answeredAt",state,data->'artifactIds' AS "artifactIds",data->'cause' AS cause`

func questionsList(q *Request, taskID, state, kind string, projects ...string) ([]doc, error) {
	c, p := projectIn(nil, nil, projects)
	for _, f := range [][2]string{{taskID, "task_id"}, {state, "state::text"}, {kind, "kind::text"}} {
		if f[0] != "" {
			p = append(p, f[0])
			c = append(c, fmt.Sprintf("%s=$%d", f[1], len(p)))
		}
	}
	rs, err := rowsOf(q, "SELECT "+questionCols+" FROM question"+where(c)+" ORDER BY seq", p...)
	for _, r := range rs { // absent, not null, as in the TypeScript view
		for _, k := range []string{"artifactIds", "cause"} {
			if r[k] == nil {
				delete(r, k)
			}
		}
	}
	return rs, err
}

// projectIn: the condition "project in ids" ('-' is no project) appended to c and p; none when ids is empty. The bare
// column keeps the project_id indexes usable.
func projectIn(c []string, p []any, ids []string) ([]string, []any) {
	if c == nil {
		c, p = []string{}, []any{}
	}
	if len(ids) == 0 {
		return c, p
	}
	named := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
	p = append(p, named)
	cond := fmt.Sprintf("project_id=ANY($%d)", len(p))
	if len(named) < len(ids) {
		cond = "(" + cond + " OR project_id IS NULL)"
	}
	return append(c, cond), p
}

func where(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c, " AND ")
}

func createTask(q *Request, v doc) (string, error) {
	title, _ := v["title"].(string)
	if strings.TrimSpace(title) == "" || len([]rune(title)) > 500 {
		return "", fail(400, "Expected task title")
	}
	id := firstString(v["id"], newID())
	pr := v["priority"]
	if pr == nil {
		pr = "normal"
	}
	priority, err := priorityClass(pr)
	if err != nil {
		return "", err
	}
	cr := v["completionRequirements"]
	if cr == nil {
		cr = doc{}
	}
	if pid := astr(v["projectId"]); pid != "" {
		if retired, err := projectRetired(q, pid); err != nil || retired {
			if err == nil {
				err = fail(404, "Unknown project")
			}
			return "", err
		}
	}
	held := v["held"] == true // a Held task: paused before its first queue (fleet-pm-agent.md §4)
	if err := exec(q, "INSERT INTO task(id,project_id,title,expected_outcome,completion_requirements,result_destination,created_by,data,state,state_before_pause,pause_reason) VALUES($1,$2,$3,$4,$5,$6,$7,'{}',CASE WHEN $8 THEN 'paused' ELSE 'queued' END::task_state,CASE WHEN $8 THEN 'queued' END::task_state,CASE WHEN $8 THEN 'held' END)",
		id, v["projectId"], title, v["expectedOutcome"], js(cr), v["resultDestination"], v["createdBy"], held); err != nil {
		return "", err
	}
	if d := astr(v["directedBy"]); d != "" {
		if err := exec(q, "UPDATE task SET directed_by=$2,requested_by=$2 WHERE id=$1", id, d); err != nil {
			return "", err
		}
	}
	if priority != "normal" {
		if err := exec(q, "UPDATE task SET priority=$2 WHERE id=$1", id, priority); err != nil {
			return "", err
		}
	}
	deps, _ := v["dependsOn"].([]string)
	for _, d := range deps {
		if err := exec(q, "INSERT INTO task_dependency(task_id,depends_on_task_id,kind,project_id) VALUES($1,$2,'blocks',$3) ON CONFLICT DO NOTHING", id, d, v["projectId"]); err != nil {
			return "", err
		}
	}
	return id, nil
}

const taskViewCols = `id,code,project_id AS "projectId",title,expected_outcome AS "expectedOutcome",state,wait_reason AS "waitReason",priority,completion_requirements AS "completionRequirements",
 result_destination AS "resultDestination",delivery_method AS "deliveryMethod",accepted_by AS "acceptedBy",accepted_at AS "acceptedAt",accepted_artifact_ids AS "acceptedArtifactIds",
 created_by AS "createdBy",directed_by AS "directedBy",requested_by AS "requestedBy",taken_from AS "takenFrom",created_at AS "createdAt",updated_at AS "updatedAt",pause_reason AS "pauseReason"`

const executionCols = `id,kind,state,observation,last_seen_at AS "lastSeenAt",project_id AS "projectId",task_id AS "taskId",experiment_id AS "experimentId",variant,parent_execution_id AS "parentExecutionId",
 session_id AS "sessionId",account_id AS "accountId",host_id AS "hostId",environment_id AS "environmentId",harness,harness_version AS "harnessVersion",model,config_ref AS "configRef",problem,
 started_at AS "startedAt",ended_at AS "endedAt",definitions`

func executionsList(q *Request, f contract.ExecutionsListRequest) ([]contract.ExecutionRow, error) {
	c, p := []string{}, []any{}
	for _, x := range []struct {
		v   *string
		col string
	}{{f.TaskId, "task_id"}, {f.ProjectId, "project_id"}, {f.ParentExecutionId, "parent_execution_id"}, {f.SessionId, "session_id"}} {
		if v := deref(x.v); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf("%s=$%d", x.col, len(p)))
		}
	}
	if f.Problem != nil && *f.Problem {
		c = append(c, "problem IS NOT NULL")
	}
	if f.NeedsYou != nil && *f.NeedsYou {
		// Tool calls are per-call gateway records (a dead key asks its own question). A step shows through its run
		// when the run carries a problem. A binding problem holds only while the execution is open.
		c = append(c, `problem IS NOT NULL AND problem NOT IN ('unknown:abandoned','cancelled') AND kind<>'tool_run'
		AND NOT EXISTS (SELECT 1 FROM document x WHERE x.kind='problem-dismissal' AND x.id=execution.id)
		AND NOT (state IN ('succeeded','failed','cancelled') AND (problem LIKE 'unregistered host %' OR problem LIKE 'unregistered account %' OR problem LIKE 'no registered environment of project %'))
		AND NOT (kind='pipeline_step' AND EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.problem IS NOT NULL AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1))))
		AND (project_id IS NULL OR EXISTS (SELECT 1 FROM definition d WHERE d.kind='project' AND d.id=execution.project_id))`)
	}
	c, p = projectIn(c, p, f.ProjectIds)
	rs, err := rowsOf(q, "SELECT "+executionCols+" FROM execution"+where(c)+" ORDER BY seq", p...)
	if err != nil {
		return nil, err
	}
	return asT[[]contract.ExecutionRow](rs)
}

func taskGet(q *Request, id string) (doc, error) {
	t, err := first(q, "SELECT "+taskViewCols+" FROM task WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errNotFound
	}
	d, err := first(q, "SELECT * FROM task_view WHERE task_id=$1", id)
	if err != nil {
		return nil, err
	}
	t["progress"] = doc{"executions": d["execution_count"], "byState": d["executions_by_state"]}
	t["cost"], t["latestResult"], t["openQuestions"] = d["totals"], d["latest_result"], d["open_questions"]
	if t["executions"], err = executionsList(q, contract.ExecutionsListRequest{TaskId: &id}); err != nil {
		return nil, err
	}
	if t["dependencies"], err = rowsOf(q, "SELECT d.depends_on_task_id AS id,d.kind,t.code FROM task_dependency d LEFT JOIN task t ON t.id=d.depends_on_task_id WHERE d.task_id=$1", id); err != nil {
		return nil, err
	}
	t["questions"], err = questionsList(q, id, "", "")
	return t, err
}

type taskGroup struct {
	sql     string
	derived bool
}

var taskGroups = map[string]taskGroup{
	"none":     {"left(t.id,0)", false},
	"priority": {"(CASE t.priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END)", false},
	"project":  {"coalesce(t.project_id,'~')", false},
	"agent":    {"coalesce(d.agent,'~')", true},
	"host":     {"coalesce(d.host,'~')", true},
	"state":    {"(CASE " + stateSQL + " WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'in_progress' THEN '3' WHEN 'queued' THEN '4' WHEN 'held' THEN '5' WHEN 'paused' THEN '6' WHEN 'completed' THEN '7' ELSE '8' END)", false},
}

var taskLabels = map[string]string{"priority": "t.priority", "state": stateSQL, "project": "t.project_id", "agent": "d.agent", "host": "d.host"}

func groupOf(g any) (string, taskGroup, error) {
	if g == nil {
		return "none", taskGroups["none"], nil
	}
	name, ok := g.(string)
	G, found := taskGroups[name]
	if !ok || !found {
		return "", G, fail(400, "Invalid group")
	}
	return name, G, nil
}

func likePattern(s string) string {
	r := strings.NewReplacer(`%`, `\%`, `_`, `\_`, `\`, `\\`)
	return "%" + r.Replace(strings.ToLower(strings.TrimSpace(s))) + "%"
}

// filterFields: a list filter field the list does not implement is an error, never a silent "all rows".
// Each known field has one type; a wrong type is a 400 too, so no condition silently drops out.
func filterFields(f doc, known ...string) error {
	for k, v := range f {
		if !slices.Contains(known, k) {
			return fail(400, "Unknown filter field "+k)
		}
		ok := false
		switch k {
		case "state":
			_, ok = v.([]any)
		case "projectId":
			l, isList := v.([]any)
			ok = isList && len(l) > 0
		case "search":
			_, ok = v.(string)
		case "archived":
			_, ok = v.(bool)
		}
		if !ok {
			return fail(400, "Invalid filter field "+k)
		}
	}
	return nil
}

func taskWhere(f doc, p *[]any, withState bool) ([]string, error) {
	if err := filterFields(f, "projectId", "state", "search"); err != nil {
		return nil, err
	}
	c, err := projectWhere(f, p, "t.project_id", []string{})
	if err != nil {
		return nil, err
	}
	if st, ok := f["state"].([]any); withState && ok && len(st) > 0 {
		ss := []string{}
		for _, x := range st {
			v, ok := x.(string)
			if !ok || !slices.Contains(listStates, v) {
				return nil, fail(400, "Invalid state")
			}
			ss = append(ss, v)
		}
		*p = append(*p, ss)
		c = append(c, fmt.Sprintf(stateSQL+"=ANY($%d)", len(*p)))
	}
	if s, ok := f["search"].(string); ok {
		c = append(c, searchWhere(s, p, taskSearchText, taskSearchFields, withState)...)
	}
	return c, nil
}

// projectWhere: the projectId filter (a list; null is work outside projects) as a condition on col.
func projectWhere(f doc, p *[]any, col string, c []string) ([]string, error) {
	if pv, ok := f["projectId"]; ok {
		ps, _ := pv.([]any)
		ids, o, null := []string{}, []string{}, false
		for _, x := range ps {
			switch v := x.(type) {
			case string:
				ids = append(ids, v)
			case nil:
				null = true
			default:
				return nil, fail(400, "Invalid projectId")
			}
		}
		if len(ids) > 0 {
			*p = append(*p, ids)
			o = append(o, fmt.Sprintf("%s=ANY($%d)", col, len(*p)))
		}
		if null {
			o = append(o, col+" IS NULL")
		}
		if len(o) > 0 {
			c = append(c, "("+strings.Join(o, " OR ")+")")
		}
	}
	return c, nil
}

const taskListCols = "t.id,t.code,t.project_id,t.title,t.state,t.pause_reason,t.wait_reason,t.priority,t.created_at,t.updated_at,t.seq,d.agent,d.host,d.started_at,coalesce(d.open_questions,0) AS open_questions"

func taskListRow(x doc) doc {
	return doc{"id": x["id"], "code": x["code"], "projectId": x["project_id"], "title": x["title"], "state": x["state"], "pauseReason": x["pause_reason"], "waitReason": x["wait_reason"], "priority": x["priority"], "agent": x["agent"], "host": x["host"],
		"startedAt": x["started_at"], "createdAt": x["created_at"], "updatedAt": x["updated_at"], "openQuestions": x["open_questions"], "sortKey": []any{x["g"], -num(x["seq"]), x["id"]}}
}

func pageLimit(v any, def int) int {
	if v == nil {
		return def
	}
	return int(math.Max(1, math.Min(500, math.Floor(num(v)))))
}

func zeroCounts(states []string) doc {
	c := doc{}
	for _, s := range states {
		c[s] = 0
	}
	return c
}

func taskSummary(q *Request, f doc, group any, groupCounts bool) (doc, []doc, error) {
	name, G, err := groupOf(group)
	if err != nil {
		return nil, nil, err
	}
	cp := []any{}
	cc, err := taskWhere(f, &cp, false)
	if err != nil {
		return nil, nil, err
	}
	byState := zeroCounts(listStates)
	rs, err := rowsOf(q, "SELECT "+stateSQL+" AS state,count(*) AS n FROM task t"+where(cc)+" GROUP BY 1", cp...)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rs {
		byState[astr(r["state"])] = r["n"]
	}
	groups := []doc{}
	if name == "none" {
		return byState, groups, nil
	}
	from := "FROM task t"
	if G.derived {
		from = "FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"
	}
	gp := []any{}
	gc, _ := taskWhere(f, &gp, true)
	gr, err := rowsOf(q, "SELECT "+G.sql+" AS g,min("+taskLabels[name]+") AS label,count(*) AS n "+from+where(gc)+" GROUP BY 1 ORDER BY 1", gp...)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range gr {
		groups = append(groups, doc{"key": r["g"], "label": r["label"], "count": r["n"]})
	}
	if !groupCounts || len(groups) == 0 {
		return byState, groups, nil
	}
	by := map[string]doc{}
	for _, g := range groups {
		by[astr(g["key"])] = zeroCounts(listStates)
	}
	rs, err = rowsOf(q, "SELECT "+G.sql+" AS g,"+stateSQL+" AS state,count(*) AS n "+from+where(cc)+" GROUP BY 1,2", cp...)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rs {
		if c := by[astr(r["g"])]; c != nil {
			c[astr(r["state"])] = r["n"]
		}
	}
	for _, g := range groups {
		g["counts"] = by[astr(g["key"])]
	}
	return byState, groups, nil
}

func total(c doc) float64 {
	t := 0.0
	for _, v := range c {
		t += num(v)
	}
	return t
}

func taskPage(q *Request, b doc) (any, error) {
	f := mapOf(b["filter"])
	if f == nil {
		f = doc{}
	}
	_, G, err := groupOf(b["group"])
	if err != nil {
		return nil, err
	}
	limit := pageLimit(b["limit"], 100)
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return nil, err
	}
	if b["after"] != nil {
		a, ok := b["after"].([]any)
		if !ok || len(a) != 3 {
			return nil, fail(400, "Invalid after")
		}
		p = append(p, fmt.Sprint(a[0]), num(a[1]), fmt.Sprint(a[2]))
		c = append(c, fmt.Sprintf("(%s,-t.seq,t.id) > ($%d,$%d::float8,$%d)", G.sql, len(p)-2, len(p)-1, len(p)))
	}
	g, lim := G.sql, strconv.Itoa(limit+1)
	var sql string
	if G.derived {
		sql = "SELECT " + taskListCols + "," + g + " AS g FROM task t LEFT JOIN task_derived d ON d.task_id=t.id" + where(c) + " ORDER BY " + g + ",t.seq DESC,t.id LIMIT " + lim
	} else {
		sql = "SELECT " + taskListCols + ",t.g FROM (SELECT t.*," + g + " AS g FROM task t" + where(c) + " ORDER BY " + g + ",t.seq DESC,t.id LIMIT " + lim + ") t LEFT JOIN task_derived d ON d.task_id=t.id ORDER BY t.g,t.seq DESC,t.id"
	}
	rs, err := rowsOf(q, sql, p...)
	if err != nil {
		return nil, err
	}
	counts, groups, err := taskSummary(q, f, b["group"], b["groupCounts"] == true)
	if err != nil {
		return nil, err
	}
	page := []doc{}
	for i, r := range rs {
		if i < limit {
			page = append(page, taskListRow(r))
		}
	}
	var next any
	if len(rs) > limit {
		next = page[len(page)-1]["sortKey"]
	}
	return doc{"tasks": page, "nextAfter": next, "counts": counts, "total": total(counts), "groups": groups}, nil
}

func taskMembership(q *Request, f doc, group any, ids []string) ([]doc, error) {
	_, G, err := groupOf(group)
	if err != nil {
		return nil, err
	}
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return nil, err
	}
	p = append(p, ids)
	c = append(c, fmt.Sprintf("t.id=ANY($%d)", len(p)))
	rs, err := rowsOf(q, "SELECT "+taskListCols+","+G.sql+" AS g FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"+where(c), p...)
	out := []doc{}
	for _, r := range rs {
		out = append(out, taskListRow(r))
	}
	return out, err
}

func init() {
	gatedObjOp("task_create", false, typedOp(func(q *Request, r *contract.TaskCreateRequest) (contract.TaskDetail, error) {
		if r.Title == "" || len([]rune(r.Title)) > 500 {
			return contract.TaskDetail{}, fail(400, "Expected title")
		}
		deps := []string{}
		for _, d := range r.DependsOn {
			if d == "" || len(d) > 200 {
				return contract.TaskDetail{}, fail(400, "Expected dependsOn")
			}
			deps = append(deps, d)
		}
		id, err := createTask(q, doc{"projectId": optAny(r.ProjectId), "title": r.Title, "expectedOutcome": optAny(r.ExpectedOutcome), "priority": optAny(r.Priority),
			"completionRequirements": rawAny(r.CompletionRequirements), "resultDestination": optAny(r.ResultDestination), "createdBy": actor(q, "operator"), "directedBy": director(q),
			"dependsOn": deps, "held": q.agent != "" || (r.Held != nil && *r.Held)})
		if err != nil {
			return contract.TaskDetail{}, err
		}
		return taskDetail(q, id)
	}))
	gatedObjOp("task_update", false, typedOp(func(q *Request, r *contract.TaskUpdateRequest) (contract.TaskDetail, error) {
		var none contract.TaskDetail
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		if _, err := taskRow(q, id); err != nil {
			return none, err
		}
		if r.Action != nil && q.agent != "" {
			return none, fail(400, "Use task_hold, task_release or task_cancel")
		}
		if r.Action != nil {
			trig := map[string]string{"pause": "pause", "resume": "resume", "cancel": "cancel", "fail": "operator_fail"}[*r.Action]
			if trig == "" {
				return none, fail(400, "Unknown task action")
			}
			if _, err := taskTransition(q, id, trig, ""); err != nil {
				return none, err
			}
			if trig == "resume" { // runs may have ended or a decision been withdrawn while it was paused
				if _, err := settleTask(q, id, ""); err != nil {
					return none, err
				}
				if _, err := settleIdleTask(q, id); err != nil {
					return none, err
				}
			}
		}
		for _, f := range []struct {
			name, col string
			v         *string
		}{{"title", "title", r.Title}, {"expectedOutcome", "expected_outcome", r.ExpectedOutcome}, {"priority", "priority", r.Priority}, {"resultDestination", "result_destination", r.ResultDestination}} {
			if f.v == nil {
				continue
			}
			var v any = *f.v
			if f.name == "priority" {
				var err error
				if v, err = priorityClass(v); err != nil {
					return none, err
				}
			} else if len([]rune(*f.v)) > 4000 {
				return none, fail(400, "Invalid "+f.name)
			}
			if err := exec(q, "UPDATE task SET "+f.col+"=$2,updated_at=now() WHERE id=$1", id, v); err != nil {
				return none, err
			}
		}
		return taskDetail(q, id)
	}))
	for _, x := range []struct{ name, trig string }{{"task_hold", "hold"}, {"task_release", "release"}, {"task_cancel", "cancel"}} {
		gatedObjOp(x.name, false, typedOp(func(q *Request, r *contract.TaskHoldRequest) (contract.TaskDetail, error) {
			if !idOK(r.TaskId) {
				return contract.TaskDetail{}, fail(400, "Expected taskId")
			}
			if _, err := taskTransition(q, r.TaskId, x.trig, ""); err != nil {
				return contract.TaskDetail{}, err
			}
			return taskDetail(q, r.TaskId)
		}))
	}
	gatedObjOp("task_get", true, typedOp(func(q *Request, r *contract.TaskGetRequest) (contract.TaskDetail, error) {
		if !idOK(r.TaskId) {
			return contract.TaskDetail{}, fail(400, "Expected taskId")
		}
		return taskDetail(q, r.TaskId)
	}))
	gatedObjOp("task_request_acceptance", false, typedOp(func(q *Request, r *contract.TaskRequestAcceptanceRequest) (contract.QuestionRow, error) {
		var none contract.QuestionRow
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		t, err := taskRow(q, id)
		if err != nil {
			return none, err
		}
		if astr(t["state"]) != "in_progress" {
			return none, fail(409, "Task is not in progress")
		}
		qid, err := requestAcceptance(q, t, r.ArtifactIds, r.Text, actor(q, "operator"))
		if err != nil {
			return none, err
		}
		return questionRow(q, id, qid)
	}))
	gatedObjOp("task_list", true, typedOp(func(q *Request, r *contract.TaskListRequest) (contract.TaskListReply, error) {
		c, p := []string{}, []any{}
		if v := deref(r.ProjectId); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf("t.project_id=$%d", len(p)))
		}
		if v := deref(r.State); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf(stateSQL+"=$%d", len(p)))
		}
		rs, err := rowsOf(q, "SELECT t.*,v.execution_count,v.executions_by_state,v.open_questions FROM task t JOIN task_view v ON v.task_id=t.id"+where(c)+" ORDER BY t.seq", p...)
		if err != nil {
			return contract.TaskListReply{}, err
		}
		out := []doc{}
		for _, t := range rs {
			v := taskRecord(t)
			v["progress"] = doc{"executions": t["execution_count"], "byState": t["executions_by_state"]}
			v["openQuestions"] = t["open_questions"]
			out = append(out, v)
		}
		tasks, err := asT[[]contract.TaskListItem](out)
		return contract.TaskListReply{Tasks: tasks}, err
	}))
	gatedObjOp("task_take", false, typedOp(func(q *Request, r *contract.TaskTakeRequest) (contract.TaskDetail, error) {
		var none contract.TaskDetail
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		t, err := taskRow(q, id)
		if err != nil {
			return none, err
		}
		p := q.person
		if p == nil || !mayAccessProjectID(p, astr(t["project_id"])) {
			return none, fail(403, "Take needs a grant on the task project")
		}
		if astr(t["directed_by"]) != p.Email {
			if err := takeTask(q, id, p.Email, ""); err != nil {
				return none, err
			}
			if err := put(q, "setting-change", doc{"id": newID(), "target": "task:" + id, "projectId": t["project_id"], "field": "directedBy", "previous": t["directed_by"], "value": p.Email, "actor": p.Email, "at": nowMs()}); err != nil {
				return none, err
			}
		}
		return taskDetail(q, id)
	}))
	gatedObjOp("questions_list", true, typedOp(func(q *Request, r *contract.QuestionsListRequest) (contract.QuestionsListReply, error) {
		qs, err := questionsList(q, deref(r.TaskId), deref(r.State), deref(r.Kind), r.ProjectIds...)
		if err != nil {
			return contract.QuestionsListReply{}, err
		}
		rows, err := asT[[]contract.QuestionRow](qs)
		return contract.QuestionsListReply{Questions: rows}, err
	}))
	// questions_withdraw: the owner (or full access) withdraws open questions that no longer need anyone, with one
	// setting-change audit row each. A cause is let go, so a problem still present is asked again by its source.
	gatedObjOp("questions_withdraw", false, typedOp(func(q *Request, r *contract.QuestionsWithdrawRequest) (contract.QuestionsWithdrawReply, error) {
		out := contract.QuestionsWithdrawReply{Withdrawn: []string{}, Skipped: []contract.QuestionSkipped{}}
		if err := ownerOnly(q, "Only the owner withdraws questions"); err != nil {
			return out, err
		}
		if strings.TrimSpace(r.Reason) == "" || len([]rune(r.Reason)) > 4000 {
			return out, fail(400, "Expected reason")
		}
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected question ids")
			}
			row, err := first(q, "SELECT project_id,state::text FROM question WHERE id=$1 FOR UPDATE", id)
			if err != nil {
				return out, err
			}
			if row == nil || row["state"] != "open" {
				state := "unknown"
				if row != nil {
					state = astr(row["state"])
				}
				out.Skipped = append(out.Skipped, contract.QuestionSkipped{Id: id, State: state})
				continue
			}
			w := doc{"reason": r.Reason, "actor": actor(q, "operator"), "at": nowMs()} // as lab_rating_withdraw records it
			if err := exec(q, "UPDATE question SET state='withdrawn',cause=NULL,data=coalesce(data,'{}'::jsonb)||jsonb_build_object('withdrawal',$2::jsonb) WHERE id=$1", id, jsonBytes(w)); err != nil {
				return out, err
			}
			if err := put(q, "setting-change", doc{"id": newID(), "target": "question:" + id, "projectId": row["project_id"], "field": "state", "previous": "open", "value": "withdrawn", "reason": r.Reason, "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
				return out, err
			}
			out.Withdrawn = append(out.Withdrawn, id)
		}
		publish(q)
		return out, nil
	}))
	gatedObjOp("questions_answer", false, typedOp(func(q *Request, r *contract.QuestionsAnswerRequest) (contract.QuestionRow, error) {
		var none contract.QuestionRow
		id, answer := r.QuestionId, r.Answer
		if !idOK(id) {
			return none, fail(400, "Expected questionId")
		}
		if answer == "" || len([]rune(answer)) > 4000 {
			return none, fail(400, "Expected answer")
		}
		// An agent's question is answered by delivering the reply to its session (agent tool reply); recording the
		// answer alone left the asker without it.
		if aq, err := first(q, "SELECT coalesce(q.project_id,s.data->>'projectId') AS project_id FROM question q LEFT JOIN document s ON s.kind='session' AND s.id=q.session_id WHERE q.id=$1 AND q.kind='agent_question'", id); err != nil {
			return none, err
		} else if aq != nil {
			// The reply reaches another session: the person must have that session's project, as for any project op.
			if p := q.person; p != nil && !fullAccess(p) && !mayAccessProjectID(p, astr(aq["project_id"])) {
				return none, errNotFound
			}
			if _, err := agentReply(q, doc{"questionId": id, "answer": answer}); err != nil {
				return none, err
			}
			return questionRow(q, "", id)
		}
		var ids []any
		if r.ArtifactIds != nil {
			ids = []any{} // an explicit empty list accepts nothing; absent takes the question's own
		}
		for _, x := range r.ArtifactIds {
			ids = append(ids, x)
		}
		if _, err := answerQuestion(q, id, answer, actor(q, "operator"), ids); err != nil {
			return none, err
		}
		return questionRow(q, "", id)
	}))
	gatedObjOp("executions_list", true, typedOp(func(q *Request, r *contract.ExecutionsListRequest) (contract.ExecutionsListReply, error) {
		es, err := executionsList(q, *r)
		return contract.ExecutionsListReply{Executions: es}, err
	}))
	gatedObjOp("task_page", true, func(q *Request) (any, error) { return taskPage(q, body(q)) })
	gatedObjOp("task_derived_check", true, typedOp(func(q *Request, _ *contract.TaskDerivedCheckRequest) (contract.TaskDerivedCheckReply, error) {
		var n int64
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*)::int FROM task").Scan(&n); err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		d, err := rowsOf(q, `SELECT task_id AS "taskId",stored,expected FROM task_derived_drift()`)
		if err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		drifted, err := asT[[]contract.TaskDerivedDrift](d)
		return contract.TaskDerivedCheckReply{Checked: n, Drifted: drifted}, err
	}))
}

func taskRecord(t doc) doc {
	v := doc{}
	for col, key := range map[string]string{"id": "id", "code": "code", "project_id": "projectId", "title": "title", "expected_outcome": "expectedOutcome", "state": "state", "wait_reason": "waitReason", "priority": "priority",
		"completion_requirements": "completionRequirements", "result_destination": "resultDestination", "delivery_method": "deliveryMethod", "accepted_by": "acceptedBy", "accepted_at": "acceptedAt",
		"accepted_artifact_ids": "acceptedArtifactIds", "created_by": "createdBy", "directed_by": "directedBy", "requested_by": "requestedBy", "taken_from": "takenFrom", "created_at": "createdAt",
		"updated_at": "updatedAt", "pause_reason": "pauseReason"} {
		v[key] = t[col]
	}
	return v
}

// taskDetail: taskGet as its contract type.
func taskDetail(q *Request, id string) (contract.TaskDetail, error) {
	t, err := taskGet(q, id)
	if err != nil {
		return contract.TaskDetail{}, err
	}
	return asT[contract.TaskDetail](t)
}

// questionRow: one question row as its contract type (404 when the row is not in the caller's scope); taskID, when set,
// must be its task.
func questionRow(q *Request, taskID, id string) (contract.QuestionRow, error) {
	x, err := first(q, "SELECT "+questionCols+" FROM question WHERE id=$1 AND ($2='' OR task_id=$2)", id, taskID)
	if err != nil || x == nil {
		if err == nil {
			err = errNotFound
		}
		return contract.QuestionRow{}, err
	}
	for _, k := range []string{"artifactIds", "cause"} { // absent, not null, as in questionsList
		if x[k] == nil {
			delete(x, k)
		}
	}
	return asT[contract.QuestionRow](x)
}

// idOK: a record id argument: non-empty, at most 200 characters (strArg's rule).
func idOK(id string) bool { return id != "" && len([]rune(id)) <= 200 }

// optAny and rawAny: optional request fields as the map values createTask reads (nil when absent).
func optAny(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func rawAny(m json.RawMessage) any {
	if len(m) == 0 {
		return nil
	}
	var v any
	json.Unmarshal(m, &v)
	return v
}

func findQuestion(q *Request, taskID, id string) (any, error) {
	qs, err := questionsList(q, taskID, "", "")
	if err != nil {
		return nil, err
	}
	for _, x := range qs {
		if x["id"] == id {
			return x, nil
		}
	}
	return nil, nil
}
