// Tasks and questions (docs/designs/fleet-data-foundation.md §2): state only through transition(), one typed question table with one
// answer path, the operator-app task list (page, counts, groups, membership) and the task operations.
package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
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
	if ok, err := has(q, "SELECT 1 FROM question WHERE task_id=$1 AND state='open' AND kind IN ('operator_decision','agent_question')", id); err != nil || ok {
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

// askQuestion: v has kind, text and optional id, projectId, taskId, executionId, sessionId, options, askedBy, legacyKind, data, askedAt.
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
	if err := exec(q, "INSERT INTO question(id,kind,project_id,task_id,execution_id,session_id,text,options,asked_by,asked_at,legacy_kind,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,coalesce($10::timestamptz,now()),$11,$12)",
		id, v["kind"], nilIf(projectID), nilIf(taskID), v["executionId"], v["sessionId"], v["text"], options, v["askedBy"], msTime(v["askedAt"]), v["legacyKind"], js(data)); err != nil {
		return "", err
	}
	if taskID != "" && v["kind"] != "acceptance" {
		if _, err := settleTask(q, taskID, ""); err != nil {
			return "", err
		}
	}
	return id, nil
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
		return qu, nil
	}
	if astr(qu["state"]) != "open" {
		return nil, fail(409, "Question withdrawn")
	}
	if astr(qu["kind"]) == "acceptance" && answer != "accepted" && answer != "rejected" {
		return nil, fail(400, "Acceptance answer is accepted or rejected")
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
			} else if _, err := taskTransition(q, t, "rejected", ""); err != nil {
				return nil, err
			}
		} else if _, err := settleTask(q, t, ""); err != nil {
			return nil, err
		}
	}
	return first(q, "SELECT * FROM question WHERE id=$1", id)
}

const questionCols = `id,kind,project_id AS "projectId",task_id AS "taskId",execution_id AS "executionId",session_id AS "sessionId",text,options,asked_by AS "askedBy",asked_at AS "askedAt",
 answer,answered_by AS "answeredBy",answered_at AS "answeredAt",state,data->'artifactIds' AS "artifactIds",data->'cause' AS cause`

func questionsList(q *Request, taskID, state, kind string) ([]doc, error) {
	c, p := []string{}, []any{}
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

const taskViewCols = `id,project_id AS "projectId",title,expected_outcome AS "expectedOutcome",state,wait_reason AS "waitReason",priority,completion_requirements AS "completionRequirements",
 result_destination AS "resultDestination",delivery_method AS "deliveryMethod",accepted_by AS "acceptedBy",accepted_at AS "acceptedAt",accepted_artifact_ids AS "acceptedArtifactIds",
 created_by AS "createdBy",directed_by AS "directedBy",requested_by AS "requestedBy",taken_from AS "takenFrom",created_at AS "createdAt",updated_at AS "updatedAt",legacy_kind AS "legacyKind",legacy_id AS "legacyId",pause_reason AS "pauseReason"`

const executionCols = `id,kind,state,observation,last_seen_at AS "lastSeenAt",project_id AS "projectId",task_id AS "taskId",experiment_id AS "experimentId",variant,parent_execution_id AS "parentExecutionId",
 session_id AS "sessionId",account_id AS "accountId",host_id AS "hostId",environment_id AS "environmentId",harness,harness_version AS "harnessVersion",model,config_ref AS "configRef",problem,
 started_at AS "startedAt",ended_at AS "endedAt"`

func executionsList(q *Request, f doc) ([]doc, error) {
	c, p := []string{}, []any{}
	for _, x := range [][2]string{{"taskId", "task_id"}, {"projectId", "project_id"}, {"parentExecutionId", "parent_execution_id"}, {"sessionId", "session_id"}} {
		if v := astr(f[x[0]]); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf("%s=$%d", x[1], len(p)))
		}
	}
	if f["problem"] == true {
		c = append(c, "problem IS NOT NULL")
	}
	return rowsOf(q, "SELECT "+executionCols+" FROM execution"+where(c)+" ORDER BY seq", p...)
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
	if t["executions"], err = executionsList(q, doc{"taskId": id}); err != nil {
		return nil, err
	}
	if t["dependencies"], err = rowsOf(q, "SELECT depends_on_task_id AS id,kind FROM task_dependency WHERE task_id=$1", id); err != nil {
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

func taskWhere(f doc, p *[]any, withState bool) ([]string, error) {
	c := []string{}
	if pv, ok := f["projectId"]; ok {
		ps, isList := pv.([]any)
		if !isList {
			ps = []any{pv}
		}
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
			o = append(o, fmt.Sprintf("t.project_id=ANY($%d)", len(*p)))
		}
		if null {
			o = append(o, "t.project_id IS NULL")
		}
		if len(o) > 0 {
			c = append(c, "("+strings.Join(o, " OR ")+")")
		}
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
	if s, ok := f["search"].(string); ok && strings.TrimSpace(s) != "" {
		*p = append(*p, likePattern(s))
		c = append(c, fmt.Sprintf("(lower(t.title) LIKE $%d OR lower(t.id) LIKE $%d)", len(*p), len(*p)))
	}
	return c, nil
}

const taskListCols = "t.id,t.project_id,t.title,t.state,t.pause_reason,t.wait_reason,t.priority,t.created_at,t.updated_at,t.seq,d.agent,d.host,d.started_at,coalesce(d.open_questions,0) AS open_questions"

func taskListRow(x doc) doc {
	return doc{"id": x["id"], "projectId": x["project_id"], "title": x["title"], "state": x["state"], "pauseReason": x["pause_reason"], "waitReason": x["wait_reason"], "priority": x["priority"], "agent": x["agent"], "host": x["host"],
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
	gatedObjOp("task_create", false, func(q *Request) (any, error) {
		b := body(q)
		title, err := strArg(b, "title", "title", 500)
		if err != nil {
			return nil, err
		}
		deps := []string{}
		if xs, ok := b["dependsOn"].([]any); ok {
			for _, x := range xs {
				d, _ := x.(string)
				if d == "" || len(d) > 200 {
					return nil, fail(400, "Expected dependsOn")
				}
				deps = append(deps, d)
			}
		}
		id, err := createTask(q, doc{"projectId": b["projectId"], "title": title, "expectedOutcome": b["expectedOutcome"], "priority": b["priority"], "completionRequirements": b["completionRequirements"],
			"resultDestination": b["resultDestination"], "createdBy": actor(q, "operator"), "directedBy": director(q), "dependsOn": deps, "held": q.agent != "" || b["held"] == true})
		if err != nil {
			return nil, err
		}
		return taskGet(q, id)
	})
	gatedObjOp("task_update", false, func(q *Request) (any, error) {
		b := body(q)
		id, err := strArg(b, "taskId", "taskId", 200)
		if err != nil {
			return nil, err
		}
		if _, err := taskRow(q, id); err != nil {
			return nil, err
		}
		if _, ok := b["action"]; ok && q.agent != "" {
			return nil, fail(400, "Use task_hold, task_release or task_cancel")
		}
		if a, ok := b["action"]; ok {
			trig := map[string]string{"pause": "pause", "resume": "resume", "cancel": "cancel", "fail": "operator_fail"}[fmt.Sprint(a)]
			if trig == "" {
				return nil, fail(400, "Unknown task action")
			}
			if _, err := taskTransition(q, id, trig, ""); err != nil {
				return nil, err
			}
		}
		for _, kc := range [][2]string{{"title", "title"}, {"expectedOutcome", "expected_outcome"}, {"priority", "priority"}, {"resultDestination", "result_destination"}} {
			v, ok := b[kc[0]]
			if !ok {
				continue
			}
			if kc[0] == "priority" {
				if v, err = priorityClass(v); err != nil {
					return nil, err
				}
			} else if s, ok := v.(string); !ok || len([]rune(s)) > 4000 {
				return nil, fail(400, "Invalid "+kc[0])
			}
			if err := exec(q, "UPDATE task SET "+kc[1]+"=$2,updated_at=now() WHERE id=$1", id, v); err != nil {
				return nil, err
			}
		}
		return taskGet(q, id)
	})
	for name, trig := range map[string]string{"task_hold": "hold", "task_release": "release", "task_cancel": "cancel"} {
		gatedObjOp(name, false, func(q *Request) (any, error) {
			id, err := strArg(body(q), "taskId", "taskId", 200)
			if err != nil {
				return nil, err
			}
			if _, err := taskTransition(q, id, trig, ""); err != nil {
				return nil, err
			}
			return taskGet(q, id)
		})
	}
	gatedObjOp("task_get", true, func(q *Request) (any, error) {
		id, err := strArg(body(q), "taskId", "taskId", 200)
		if err != nil {
			return nil, err
		}
		return taskGet(q, id)
	})
	gatedObjOp("task_request_acceptance", false, func(q *Request) (any, error) {
		b := body(q)
		id, err := strArg(b, "taskId", "taskId", 200)
		if err != nil {
			return nil, err
		}
		t, err := taskRow(q, id)
		if err != nil {
			return nil, err
		}
		if astr(t["state"]) != "in_progress" {
			return nil, fail(409, "Task is not in progress")
		}
		ids, ok := b["artifactIds"].([]any)
		if !ok {
			rs, err := rowsOf(q, "SELECT coalesce(legacy_id,id) AS id FROM artifact WHERE task_id=$1 AND state='stored' AND role='original' ORDER BY seq", id)
			if err != nil {
				return nil, err
			}
			ids = []any{}
			for _, r := range rs {
				ids = append(ids, r["id"])
			}
		}
		text := "Accept the result of: " + astr(t["title"])
		if s, ok := b["text"].(string); ok {
			text = string([]rune(s)[:min(len([]rune(s)), 2000)])
		}
		qid, err := askQuestion(q, doc{"kind": "acceptance", "taskId": id, "projectId": t["project_id"], "text": text, "options": []string{"accepted", "rejected"}, "askedBy": actor(q, "operator"), "data": doc{"artifactIds": ids}})
		if err != nil {
			return nil, err
		}
		return findQuestion(q, id, qid)
	})
	gatedObjOp("task_list", true, func(q *Request) (any, error) {
		b := body(q)
		c, p := []string{}, []any{}
		if v := astr(b["projectId"]); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf("t.project_id=$%d", len(p)))
		}
		if v := astr(b["state"]); v != "" {
			p = append(p, v)
			c = append(c, fmt.Sprintf(stateSQL+"=$%d", len(p)))
		}
		rs, err := rowsOf(q, "SELECT t.*,v.execution_count,v.executions_by_state,v.open_questions FROM task t JOIN task_view v ON v.task_id=t.id"+where(c)+" ORDER BY t.seq", p...)
		if err != nil {
			return nil, err
		}
		out := []doc{}
		for _, t := range rs {
			v := taskRecord(t)
			v["progress"] = doc{"executions": t["execution_count"], "byState": t["executions_by_state"]}
			v["openQuestions"] = t["open_questions"]
			out = append(out, v)
		}
		return doc{"tasks": out}, nil
	})
	gatedObjOp("task_take", false, func(q *Request) (any, error) {
		id, err := strArg(body(q), "taskId", "taskId", 200)
		if err != nil {
			return nil, err
		}
		t, err := taskRow(q, id)
		if err != nil {
			return nil, err
		}
		p := q.person
		if p == nil || !mayAccessProjectID(p, astr(t["project_id"])) {
			return nil, fail(403, "Take needs a grant on the task project")
		}
		if astr(t["directed_by"]) != p.Email {
			if err := takeTask(q, id, p.Email, ""); err != nil {
				return nil, err
			}
			if err := put(q, "setting-change", doc{"id": newID(), "target": "task:" + id, "projectId": t["project_id"], "field": "directedBy", "previous": t["directed_by"], "value": p.Email, "actor": p.Email, "at": nowMs()}); err != nil {
				return nil, err
			}
		}
		return taskGet(q, id)
	})
	gatedObjOp("questions_list", true, func(q *Request) (any, error) {
		b := body(q)
		qs, err := questionsList(q, astr(b["taskId"]), astr(b["state"]), astr(b["kind"]))
		return doc{"questions": qs}, err
	})
	gatedObjOp("questions_answer", false, func(q *Request) (any, error) {
		b := body(q)
		id, err := strArg(b, "questionId", "questionId", 200)
		if err != nil {
			return nil, err
		}
		answer, err := strArg(b, "answer", "answer", 4000)
		if err != nil {
			return nil, err
		}
		// An agent's question is answered by delivering the reply to its session (agent tool reply); recording the
		// answer alone left the asker without it.
		if aq, err := first(q, "SELECT coalesce(q.project_id,s.data->>'projectId') AS project_id FROM question q LEFT JOIN document s ON s.kind='session' AND s.id=q.session_id WHERE q.id=$1 AND q.legacy_kind='question'", id); err != nil {
			return nil, err
		} else if aq != nil {
			// The reply reaches another session: the person must have that session's project, as for any project op.
			if p := q.person; p != nil && !fullAccess(p) && !mayAccessProjectID(p, astr(aq["project_id"])) {
				return nil, errNotFound
			}
			if _, err := agentReply(q, doc{"questionId": id, "answer": answer}); err != nil {
				return nil, err
			}
			return findQuestion(q, "", id)
		}
		ids, _ := b["artifactIds"].([]any)
		if _, err := answerQuestion(q, id, answer, actor(q, "operator"), ids); err != nil {
			return nil, err
		}
		return findQuestion(q, "", id)
	})
	gatedObjOp("executions_list", true, func(q *Request) (any, error) {
		es, err := executionsList(q, body(q))
		return doc{"executions": es}, err
	})
	gatedObjOp("task_page", true, func(q *Request) (any, error) { return taskPage(q, body(q)) })
	gatedObjOp("task_derived_check", true, func(q *Request) (any, error) {
		var n int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*)::int FROM task").Scan(&n); err != nil {
			return nil, err
		}
		d, err := rowsOf(q, `SELECT task_id AS "taskId",stored,expected FROM task_derived_drift()`)
		return doc{"checked": n, "drifted": d}, err
	})
}

func taskRecord(t doc) doc {
	v := doc{}
	for col, key := range map[string]string{"id": "id", "project_id": "projectId", "title": "title", "expected_outcome": "expectedOutcome", "state": "state", "wait_reason": "waitReason", "priority": "priority",
		"completion_requirements": "completionRequirements", "result_destination": "resultDestination", "delivery_method": "deliveryMethod", "accepted_by": "acceptedBy", "accepted_at": "acceptedAt",
		"accepted_artifact_ids": "acceptedArtifactIds", "created_by": "createdBy", "directed_by": "directedBy", "requested_by": "requestedBy", "taken_from": "takenFrom", "created_at": "createdAt",
		"updated_at": "updatedAt", "legacy_kind": "legacyKind", "legacy_id": "legacyId", "pause_reason": "pauseReason"} {
		v[key] = t[col]
	}
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
