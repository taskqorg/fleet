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

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"fleet/contract"
	"fleet/coordinator/db"
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

// taskRow: the task's row as a record (column names), for callers outside this file; errNotFound when absent.
func taskRow(q *Request, id string) (doc, error) {
	t, err := first(q, "SELECT * FROM task WHERE id=$1", id)
	if err == nil && t == nil {
		return nil, errNotFound
	}
	return t, err
}

// taskLoad: the task's row; errNotFound when absent.
func taskLoad(q *Request, id string) (db.Task, error) {
	t, err := q.db().TaskGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errNotFound
	}
	return t, err
}

func waitReason(q *Request, id string) (string, error) {
	if ok, err := q.db().TaskHasOpenBlockingQuestion(q.ctx(), &id); err != nil || ok {
		return "question", err
	}
	if ok, err := openDependency(q, id); err != nil || ok {
		return "dependency", err
	}
	return "", nil
}

func openDependency(q *Request, id string) (bool, error) {
	return q.db().TaskHasOpenDependency(q.ctx(), id)
}

func taskTransition(q *Request, id, trigger, reason string) (string, error) {
	t, err := taskLoad(q, id)
	if err != nil {
		return "", err
	}
	rule := taskTransitions[trigger]
	state := string(t.State)
	if !slices.Contains(rule.from, state) {
		return "", fail(409, fmt.Sprintf("Task %s cannot %s", state, strings.Replace(trigger, "_", " ", 1)))
	}
	if trigger == "first_execution" {
		started, err := q.db().TaskHasStartedExecution(q.ctx(), &id)
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
	if trigger == "release" && deref(t.PauseReason) != "held" {
		return "", fail(409, "Paused by a person: resume it")
	}
	to := rule.to
	if to == "previous" && t.StateBeforePause != nil {
		to = string(*t.StateBeforePause)
	}
	set := db.TaskSetStateParams{ID: id, State: db.TaskState(to)}
	if to == "waiting" {
		if reason == "" {
			if reason, err = waitReason(q, id); err != nil {
				return "", err
			}
		}
		set.WaitReason = strPtr(reason)
	}
	if trigger == "pause" || trigger == "hold" {
		set.StateBeforePause = &t.State
	}
	if trigger == "hold" {
		set.PauseReason = strPtr("held")
	}
	return to, q.db().TaskSetState(q.ctx(), set)
}

// taskAllowsWork fences a new task-bound turn against a concurrent cancellation.
func taskAllowsWork(q *Request, id string) error {
	if id == "" {
		return nil
	}
	state, err := q.db().TaskStateForShare(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == db.TaskStateCancelled {
		return fail(409, "Task is cancelled")
	}
	return nil
}

func cancelTask(q *Request, id string) error {
	// Dispatch uses the same fence before claims and promotions. Admitted turns
	// hold the task row until they commit, so the sweep includes them.
	if err := acquireTxLock(q, DISPATCH); err != nil {
		return err
	}
	if _, err := taskTransition(q, id, "cancel", ""); err != nil {
		return err
	}
	// The scoped transition above checked the operator's authority. The
	// selected work may have coordinator-owned commands without a project.
	_, err := unscoped(q, func() (struct{}, error) { return struct{}{}, cancelTaskWork(q, id) })
	return err
}

func cancelTaskWork(q *Request, id string) error {
	linked, err := q.db().TaskCancelLinkedExecutions(q.ctx(), &id)
	if err != nil {
		return err
	}
	for _, e := range linked {
		eid := e.ID
		if e.Kind == db.ExecutionKindPipelineRun {
			owned, _, err := pipelineCancelIn(q, eid)
			if pipelineFormatUnsupported(err) {
				// A historical plan cannot run. Its retained native executions
				// are handled below without decoding or resuming the old plan.
				if _, err := executionTransition(q, eid, executionEvent{To: "cancelled", Observation: "unconfirmed"}); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if owned == "" {
				continue
			}
			eid = owned
		} else if active, err := getExecution(q, eid); err != nil || terminal(active) {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := requestExecutionStop(q, eid, true); err != nil {
			return err
		}
	}
	if err := q.db().TaskCancelQueuedMessages(q.ctx(), &id); err != nil {
		return err
	}
	return q.db().TaskDeleteWatches(q.ctx(), &id)
}

// settleTask moves an open task toward target through the transitions whose guards hold now.
func settleTask(q *Request, id, target string) (string, error) {
	t, err := taskLoad(q, id)
	if err != nil {
		return "", err
	}
	state := string(t.State)
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
	t, err := taskLoad(q, id)
	if err != nil {
		return err
	}
	if deref(t.DirectedBy) == taker {
		return nil
	}
	if err := q.db().TaskSetDirector(q.ctx(), db.TaskSetDirectorParams{ID: id, DirectedBy: &taker, TakenFrom: strPtr(firstString(deref(t.DirectedBy), from))}); err != nil {
		return err
	}
	return q.db().TaskExecutionsSetDirector(q.ctx(), db.TaskExecutionsSetDirectorParams{TaskID: &id, DirectedBy: &taker})
}

// questionAsk: a new question. ProjectID and TaskID "" are none (TaskID is then its execution's); Options is JSON or
// nil; AskedAt nil is now; Data is a JSON object (nil: {}).
type questionAsk struct {
	ID, Kind, Text                             string
	ProjectID, TaskID                          string
	ExecutionID, SessionID, AskedBy, RequestID *string
	Options, Data                              []byte
	AskedAt                                    *time.Time
}

// askQuestion: v has kind, text and optional id, projectId, taskId, executionId, sessionId, options, askedBy, requestId,
// data, askedAt (milliseconds); the record form of askQuestionAs for callers holding one.
func askQuestion(q *Request, v doc) (string, error) {
	var r struct {
		ID          string          `json:"id"`
		Kind        string          `json:"kind"`
		Text        string          `json:"text"`
		ProjectID   string          `json:"projectId"`
		TaskID      string          `json:"taskId"`
		ExecutionID *string         `json:"executionId"`
		SessionID   *string         `json:"sessionId"`
		AskedBy     *string         `json:"askedBy"`
		RequestID   *string         `json:"requestId"`
		Options     json.RawMessage `json:"options"`
		Data        json.RawMessage `json:"data"`
		AskedAt     any             `json:"askedAt"`
	}
	if err := json.Unmarshal(jsonBytes(v), &r); err != nil {
		return "", fmt.Errorf("question: %w", err)
	}
	a := questionAsk{ID: r.ID, Kind: r.Kind, Text: r.Text, ProjectID: r.ProjectID, TaskID: r.TaskID, ExecutionID: r.ExecutionID, SessionID: r.SessionID, AskedBy: r.AskedBy, RequestID: r.RequestID}
	if len(r.Options) > 0 && string(r.Options) != "null" {
		a.Options = r.Options
	}
	if len(r.Data) > 0 && string(r.Data) != "null" {
		a.Data = r.Data
	}
	if t, ok := msTime(r.AskedAt).(time.Time); ok {
		a.AskedAt = &t
	}
	return askQuestionAs(q, a)
}

// askQuestionAs asks a; a task's question (but an acceptance) settles its task.
func askQuestionAs(q *Request, a questionAsk) (string, error) {
	if a.TaskID == "" && deref(a.ExecutionID) != "" {
		e, err := q.db().TaskExecutionLink(q.ctx(), *a.ExecutionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if err == nil {
			a.TaskID = deref(e.TaskID)
			if a.ProjectID == "" {
				a.ProjectID = deref(e.ProjectID)
			}
		}
	}
	if a.ID == "" {
		a.ID = newID()
	}
	if a.Data == nil {
		a.Data = []byte("{}")
	}
	var at pgtype.Timestamptz
	if a.AskedAt != nil {
		at = pgtype.Timestamptz{Time: *a.AskedAt, Valid: true}
	}
	if err := q.db().TaskQuestionInsert(q.ctx(), db.TaskQuestionInsertParams{ID: a.ID, Kind: db.QuestionKind(a.Kind), ProjectID: strPtr(a.ProjectID), TaskID: strPtr(a.TaskID),
		ExecutionID: a.ExecutionID, SessionID: a.SessionID, Text: a.Text, Options: a.Options, AskedBy: a.AskedBy, AskedAt: at, RequestID: a.RequestID, Data: a.Data}); err != nil {
		return "", err
	}
	if a.TaskID != "" && a.Kind != "acceptance" {
		if _, err := settleTask(q, a.TaskID, ""); err != nil {
			return "", err
		}
	}
	return a.ID, nil
}

// settleIdleTask: no task stays in progress with nothing that can move it. When it is idle (TaskIdle): if the run that
// ended last succeeded, its result goes to the operator for acceptance; otherwise the operator gets a decision that
// blocks the task, so it shows in Needs you: fail the task, or (when an earlier run succeeded) accept that result. A
// retry is a new run, which withdraws the decision (withdrawIdleDecision). Task 2273393d: its only run failed before it
// started and the task sat in progress with nothing to show it. Data foundation §3 task states. Legacy delivery tasks
// are included: nothing writes or reads delivery_method any more. Runs on every execution end, after an answer, on
// resume, and for every task at coordinator start (settleIdleTasks). Reports whether it asked.
func settleIdleTask(q *Request, id string) (bool, error) {
	// A newer failed run supersedes an unanswered success prompt; history remains queryable.
	superseded, err := q.db().TaskSupersededAcceptances(q.ctx(), &id)
	if err != nil {
		return false, err
	}
	if err := questionsWithdraw(q, superseded); err != nil {
		return false, err
	}
	t, err := q.db().TaskIdle(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var report struct {
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
	}
	if len(t.Report) > 0 {
		_ = json.Unmarshal(t.Report, &report)
	}
	if t.Latest == "succeeded" && report.Outcome != "failed" {
		_, err = requestAcceptance(q, acceptTask{ID: t.ID, Title: t.Title, ProjectID: t.ProjectID}, nil, nil, "fleet")
		return err == nil, err
	}
	options := []string{idleFail}
	if t.Succeeded {
		options = append(options, idleAccept)
	}
	what := "The last run of " + t.Title + " " + t.Latest
	if report.Outcome == "failed" {
		what = "Work was reported incomplete: " + report.Summary
	}
	if t.Latest == "" {
		what = t.Title + " is in progress with no run"
	}
	_, err = askQuestionAs(q, questionAsk{Kind: "operator_decision", TaskID: id, ProjectID: deref(t.ProjectID), ExecutionID: strPtr(t.LatestID), SessionID: t.LatestSession,
		AskedBy: strPtr("fleet"), Options: jsonBytes(options), Data: []byte(`{"idle":true}`),
		Text: what + ". Retry it with a new message in its session, or answer."})
	return err == nil, err
}

func init() {
	// At start: every idle task, one transaction each, so one bad task neither rolls back nor blocks the others.
	onStart(func(ctx context.Context, a *App) {
		var ids []string
		if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
			ids, err = q.db().TaskOpenIDs(q.ctx())
			return
		}); err != nil {
			log.Printf("settle idle tasks: %v", err)
			return
		}
		n := 0
		for _, id := range ids {
			asked := false
			if err := runTx(ctx, a, nil, true, "", func(q *Request) (err error) { asked, err = settleIdleTask(q, id); return }); err != nil {
				log.Printf("settle idle task %s: %v", id, err)
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
	idle, err := q.db().TaskIdleDecisions(q.ctx(), &id)
	if err != nil {
		return err
	}
	return questionsWithdraw(q, idle)
}

// acceptanceExecution validates the selected work, independently of who requests or answers acceptance: the task's
// latest run, or with earlier its latest successful one; nil when the task has no run.
func acceptanceExecution(q *Request, taskID string, earlier bool) (*db.TaskAcceptanceExecutionRow, error) {
	e, err := q.db().TaskAcceptanceExecution(q.ctx(), db.TaskAcceptanceExecutionParams{TaskID: &taskID, Earlier: earlier})
	if errors.Is(err, pgx.ErrNoRows) {
		if earlier {
			return nil, fail(409, "No successful earlier work result is available")
		}
		return nil, nil // Results may be supplied without a Fleet execution.
	}
	if err != nil {
		return nil, err
	}
	if e.Outcome == "failed" || e.State == db.ExecutionStateFailed || e.State == db.ExecutionStateCancelled {
		return nil, fail(409, "Latest work did not succeed; retry it or explicitly select an available earlier result")
	}
	return &e, nil
}

// acceptTask: the task an acceptance is asked for; EarlierResult selects its latest successful run over its latest.
type acceptTask struct {
	ID, Title     string
	ProjectID     *string
	EarlierResult bool
}

// requestAcceptance asks the task's acceptance question with the given artifacts (default: its stored originals). An open
// acceptance question is reused and updated, so an explicit request after the automatic one never leaves a second open.
func requestAcceptance(q *Request, t acceptTask, artifactIDs []string, text *string, askedBy string) (string, error) {
	selected, err := acceptanceExecution(q, t.ID, t.EarlierResult)
	if err != nil {
		return "", err
	}
	var selectedID *string
	if selected != nil {
		selectedID = &selected.ID
	}
	ids := []string{}
	if artifactIDs != nil {
		ids = append(ids, artifactIDs...)
	} else {
		rs, err := q.db().TaskAcceptanceArtifacts(q.ctx(), db.TaskAcceptanceArtifactsParams{TaskID: &t.ID, ExecutionID: selectedID})
		if err != nil {
			return "", err
		}
		ids = append(ids, rs...)
	}
	body := "Accept the result of: " + t.Title
	if text != nil {
		body = string([]rune(*text)[:min(len([]rune(*text)), 2000)])
	}
	open, err := q.db().TaskOpenAcceptance(q.ctx(), &t.ID)
	if err == nil {
		return open, q.db().TaskAcceptanceAskAgain(q.ctx(), db.TaskAcceptanceAskAgainParams{ID: open, Text: body, ArtifactIds: jsonBytes(ids), AskedBy: &askedBy})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	data := struct {
		ArtifactIds   []string `json:"artifactIds"`
		EarlierResult *bool    `json:"earlierResult"`
	}{ArtifactIds: ids}
	if t.EarlierResult {
		data.EarlierResult = &t.EarlierResult
	}
	return askQuestionAs(q, questionAsk{Kind: "acceptance", TaskID: t.ID, ProjectID: deref(t.ProjectID), Text: body, Options: jsonBytes([]string{"accepted", "rejected"}),
		AskedBy: &askedBy, ExecutionID: selectedID, Data: jsonBytes(data)})
}

// questionRecord: the question's row as a record (column names), as the answer checks and callers outside this file
// read it; nil when absent.
func questionRecord(q *Request, id string) (doc, error) {
	return first(q, "SELECT * FROM question WHERE id=$1", id)
}

// questionData: the fields of a question's data the answer path reads.
type questionData struct {
	EarlierResult bool  `json:"earlierResult"`
	Idle          bool  `json:"idle"`
	ArtifactIds   []any `json:"artifactIds"`
}

// answerQuestion: one answer wins; an acceptance completes (or keeps in progress) its task, other answers unblock it.
// runAnswerChecks and runAnswerAfter apply the Lab and budget rules of the question. The reply is the question's record.
func answerQuestion(q *Request, id, answer, by string, artifactIDs []any) (doc, error) {
	cur, err := q.db().TaskQuestion(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.State == db.QuestionStateAnswered {
		if deref(cur.Answer) != answer {
			return nil, fail(409, "Question already answered differently")
		}
		// A replay (a client retry) keeps the owner-only rule of a key request; the answer itself was checked when given.
		if cur.Kind == db.QuestionKindSecret && (q.agent != "" || q.person == nil || !fullAccess(q.person)) {
			return nil, fail(403, "Only the owner answers a key request")
		}
		return questionRecord(q, id)
	}
	if cur.State != db.QuestionStateOpen {
		return nil, fail(409, "Question withdrawn")
	}
	var data questionData
	if len(cur.Data) > 0 {
		_ = json.Unmarshal(cur.Data, &data)
	}
	if cur.Kind == db.QuestionKindAcceptance {
		if _, err := acceptanceExecution(q, deref(cur.TaskID), data.EarlierResult); err != nil {
			return nil, err
		}
		if answer != "accepted" && answer != "rejected" {
			return nil, fail(400, "Acceptance answer is accepted or rejected")
		}
	}
	if data.Idle && answer != idleFail && answer != idleAccept {
		return nil, fail(400, "Answer "+idleFail+", or "+idleAccept+" when offered; retry with a new message in the session")
	}
	qu, err := questionRecord(q, id)
	if err != nil {
		return nil, err
	}
	if err := runAnswerChecks(q, qu, answer); err != nil { // Lab and budget answers
		return nil, err
	}
	moved, err := questionTransition(q, id, questionEvent{To: "answered", Answer: &answer, AnsweredBy: &by, AnsweredNow: true})
	if err != nil {
		return nil, err
	}
	if !moved {
		now, _ := q.db().TaskQuestion(q.ctx(), id)
		if now.State == db.QuestionStateAnswered && deref(now.Answer) == answer {
			return questionRecord(q, id)
		}
		if now.State == db.QuestionStateAnswered {
			return nil, fail(409, "Question already answered differently")
		}
		return nil, fail(409, "Question withdrawn")
	}
	if err := runAnswerAfter(q, id); err != nil {
		return nil, err
	}
	if t := deref(cur.TaskID); t != "" {
		if cur.Kind == db.QuestionKindAcceptance {
			if answer == "accepted" {
				if _, err := taskTransition(q, t, "accepted", ""); err != nil {
					return nil, err
				}
				ids := []string{}
				src := artifactIDs
				if src == nil {
					src = data.ArtifactIds
				}
				for _, x := range src {
					if s, ok := x.(string); ok {
						ids = append(ids, s)
					}
				}
				if err := q.db().TaskAccept(q.ctx(), db.TaskAcceptParams{ID: t, AcceptedBy: &by, ArtifactIds: ids}); err != nil {
					return nil, err
				}
				if err := deliveredTeardown(q, t); err != nil {
					return nil, err
				}
			} else if _, err := taskTransition(q, t, "rejected", ""); err != nil {
				return nil, err
			}
		} else {
			target := ""
			if data.Idle && answer == idleFail {
				target = "failed"
			}
			if _, err := settleTask(q, t, target); err != nil {
				return nil, err
			}
			if data.Idle && answer == idleAccept {
				task, err := taskLoad(q, t)
				if err != nil {
					return nil, err
				}
				if _, err := requestAcceptance(q, acceptTask{ID: task.ID, Title: task.Title, ProjectID: task.ProjectID, EarlierResult: true}, nil, nil, by); err != nil {
					return nil, err
				}
			}
			if _, err := settleIdleTask(q, t); err != nil { // an answered decision may leave the task with nothing to move it
				return nil, err
			}
		}
	}
	return questionRecord(q, id)
}

// excludeCheck: an excludeProjectIds list holds no empty id (a JSON null decodes to one), so an exclusion is never skipped
// silently; '-' is no project.
func excludeCheck(ids []string) error {
	if slices.Contains(ids, "") {
		return fail(400, "excludeProjectIds: an empty id; '-' is work outside projects")
	}
	return nil
}

// projectSplit: a project id list ('-' is no project) as its named projects and whether it names no project; named is
// nil (no condition) when ids is empty.
func projectSplit(ids []string) (named []string, null bool) {
	if len(ids) == 0 {
		return nil, false
	}
	named = slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
	return named, len(named) < len(ids)
}

// optTime: an optional timestamp as its JSON text (timeText); nil stays nil.
func optTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := timeText(*t)
	return &s
}

// rawJSON: a jsonb value as a JSON field; SQL NULL is JSON null.
func rawJSON(b []byte) json.RawMessage {
	if b == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// questionsList: projects keeps those projects only, exclude leaves those out ('-' is no project in both; OR-34).
func questionsList(q *Request, taskID, state, kind string, projects, exclude []string) ([]contract.QuestionRow, error) {
	return questionRows(q, db.TaskQuestionsParams{TaskID: strPtr(taskID), State: strPtr(state), Kind: strPtr(kind)}, projects, exclude)
}

func questionRows(q *Request, p db.TaskQuestionsParams, projects, exclude []string) ([]contract.QuestionRow, error) {
	p.Projects, p.ProjectsNull = projectSplit(projects)
	p.Excluded, p.ExcludeNull = projectSplit(exclude)
	rs, err := q.db().TaskQuestions(q.ctx(), p)
	if err != nil {
		return nil, err
	}
	out := make([]contract.QuestionRow, 0, len(rs))
	for _, r := range rs {
		if string(r.ArtifactIds) == "null" {
			r.ArtifactIds = nil
		}
		if string(r.Cause) == "null" {
			r.Cause = nil
		}
		out = append(out, contract.QuestionRow{Id: r.ID, Code: r.Code, Kind: contract.QuestionKind(r.Kind), ProjectId: r.ProjectID, TaskId: r.TaskID, ExecutionId: r.ExecutionID,
			SessionId: r.SessionID, Text: r.Text, Options: rawJSON(r.Options), AskedBy: r.AskedBy, AskedAt: timeText(r.AskedAt), Answer: r.Answer, AnsweredBy: r.AnsweredBy,
			AnsweredAt: optTime(r.AnsweredAt), State: r.State, ArtifactIds: r.ArtifactIds, Cause: r.Cause}) // artifactIds and cause: absent, not null, as in the TypeScript view
	}
	return out, nil
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

// projectNotIn: the condition "col is not one of ids" ('-' is no project) appended to c; none when ids is empty. A row
// without a project stays unless '-' is among ids.
func projectNotIn(c []string, p *[]any, col string, ids []string) []string {
	named := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
	return notInProjects(c, p, col, named, len(named) < len(ids))
}

// notInProjects: the conditions "col is none of named" and, with null, "col is set" appended to c.
func notInProjects(c []string, p *[]any, col string, named []string, null bool) []string {
	if len(named) > 0 {
		*p = append(*p, named)
		c = append(c, fmt.Sprintf("NOT coalesce(%s=ANY($%d),false)", col, len(*p)))
	}
	if null {
		c = append(c, col+" IS NOT NULL")
	}
	return c
}

func where(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c, " AND ")
}

// newTask: task_create's fields; CreatedBy "execution:<id>" also records that execution and its session.
type newTask struct {
	ProjectID, ExpectedOutcome, Priority, ResultDestination *string
	Title, CreatedBy, DirectedBy                            string
	CompletionRequirements                                  json.RawMessage
	DependsOn                                               []string
	Held                                                    bool // a Held task: paused before its first queue (fleet-pm-agent.md §4)
}

func createTask(q *Request, v newTask) (string, error) {
	if strings.TrimSpace(v.Title) == "" || len([]rune(v.Title)) > 500 {
		return "", fail(400, "Expected task title")
	}
	id := newID()
	pr := "normal"
	if v.Priority != nil {
		pr = *v.Priority
	}
	priority, err := priorityClass(pr)
	if err != nil {
		return "", err
	}
	cr := []byte(v.CompletionRequirements)
	if len(cr) == 0 || string(cr) == "null" {
		cr = []byte("{}")
	}
	if pid := deref(v.ProjectID); pid != "" {
		if retired, err := projectRetired(q, pid); err != nil || retired {
			if err == nil {
				err = fail(404, "Unknown project")
			}
			return "", err
		}
	}
	var createdByExec, createdBySess *string
	if eid, ok := strings.CutPrefix(v.CreatedBy, "execution:"); ok && eid != "" {
		createdByExec = &eid
		sid, err := unscoped(q, func() (*string, error) {
			s, err := q.db().TaskExecutionSession(q.ctx(), eid)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return s, err
		})
		if err != nil {
			return "", err
		}
		createdBySess = strPtr(sid)
	}
	if err := q.db().TaskInsert(q.ctx(), db.TaskInsertParams{ID: id, ProjectID: v.ProjectID, Title: v.Title, ExpectedOutcome: v.ExpectedOutcome, CompletionRequirements: cr,
		ResultDestination: v.ResultDestination, CreatedBy: strPtr(v.CreatedBy), CreatedByExecutionID: createdByExec, CreatedBySessionID: createdBySess, Held: v.Held,
		DirectedBy: strPtr(v.DirectedBy), Priority: priority}); err != nil {
		return "", err
	}
	for _, d := range v.DependsOn {
		if err := q.db().TaskDependencyAdd(q.ctx(), db.TaskDependencyAddParams{TaskID: id, DependsOn: d, ProjectID: v.ProjectID}); err != nil {
			return "", err
		}
	}
	return id, nil
}

func executionsList(q *Request, f contract.ExecutionsListRequest) ([]contract.ExecutionRow, error) {
	page, err := executionsPage(q, f)
	return page.Executions, err
}

func executionsPage(q *Request, f contract.ExecutionsListRequest) (contract.ExecutionsListReply, error) {
	empty := contract.ExecutionsListReply{}
	if f.Limit != nil && (*f.Limit < 1 || *f.Limit > 1000) {
		return empty, fail(400, "limit must be 1..1000")
	}
	if f.After != nil && (f.Limit == nil || *f.After == "") {
		return empty, fail(400, "after requires limit and a nonempty cursor")
	}
	if err := excludeCheck(f.ExcludeProjectIds); err != nil {
		return empty, err
	}
	p := db.TaskExecutionsPageParams{TaskID: strPtr(f.TaskId), ProjectID: strPtr(f.ProjectId), ParentExecutionID: strPtr(f.ParentExecutionId), SessionID: strPtr(f.SessionId),
		Problem: f.Problem != nil && *f.Problem, NeedsYou: f.NeedsYou != nil && *f.NeedsYou}
	if s := systemOf(f.System, deref(f.ParentExecutionId) != ""); s != nil {
		p.System = string(*s)
	}
	p.Projects, p.ProjectsNull = projectSplit(f.ProjectIds)
	p.Excluded, p.ExcludeNull = projectSplit(f.ExcludeProjectIds)
	if f.After != nil {
		// The cursor is a row of this same list.
		at := p
		at.ID = f.After
		cursor, err := q.db().TaskExecutionsPage(q.ctx(), at)
		if err != nil {
			return empty, err
		}
		if len(cursor) == 0 {
			return empty, fail(400, "Invalid execution cursor")
		}
		p.AfterSeq, p.AfterID = &cursor[0].Seq, f.After
	}
	if f.Limit != nil {
		lim := int32(*f.Limit + 1)
		p.Lim = &lim
	}
	rs, err := q.db().TaskExecutionsPage(q.ctx(), p)
	if err != nil {
		return empty, err
	}
	es := make([]contract.ExecutionRow, 0, len(rs))
	for _, r := range rs {
		e := contract.ExecutionRow{Id: r.ID, Kind: string(r.Kind), State: contract.ExecutionState(r.State), Observation: string(r.Observation), LastSeenAt: optTime(r.LastSeenAt),
			ProjectId: r.ProjectID, TaskId: r.TaskID, ExperimentId: r.ExperimentID, Variant: r.Variant, ParentExecutionId: r.ParentExecutionID, SessionId: r.SessionID,
			AccountId: r.AccountID, HostId: r.HostID, EnvironmentId: r.EnvironmentID, Harness: r.Harness, HarnessVersion: r.HarnessVersion, Model: r.Model,
			ConfigRef: rawJSON(r.ConfigRef), Problem: r.Problem, StartedAt: optTime(r.StartedAt), SystemActor: r.SystemActor}
		if r.EndAt.Valid {
			e.EndedAt = optTime(&r.EndAt.Time)
		}
		if err := json.Unmarshal(rawJSON(r.Definitions), &e.Definitions); err != nil {
			return empty, err
		}
		if r.Report != nil {
			if err := json.Unmarshal(r.Report, &e.Report); err != nil {
				return empty, err
			}
		}
		es = append(es, e)
	}
	reply := contract.ExecutionsListReply{Executions: es}
	if f.Limit != nil {
		// Metadata is present only when the caller opts in to paging.
		more := len(es) > int(*f.Limit)
		reply.HasMore = &more
		if more {
			reply.Executions = es[:*f.Limit]
			cursor := reply.Executions[len(reply.Executions)-1].Id
			reply.NextAfter = &cursor
		}
	}
	return reply, nil
}

// taskItem: a task row as the list shows it, with its view's progress and open questions.
func taskItem(t db.Task, count *int64, byState, open []byte) contract.TaskListItem {
	return contract.TaskListItem{Id: t.ID, Code: t.Code, ProjectId: t.ProjectID, Title: t.Title, ExpectedOutcome: t.ExpectedOutcome, State: contract.TaskState(t.State),
		WaitReason: t.WaitReason, Priority: t.Priority, CompletionRequirements: rawJSON(t.CompletionRequirements), ResultDestination: t.ResultDestination,
		DeliveryMethod: rawJSON(t.DeliveryMethod), AcceptedBy: t.AcceptedBy, AcceptedAt: optTime(t.AcceptedAt), AcceptedArtifactIds: t.AcceptedArtifactIds,
		CreatedBy: t.CreatedBy, DirectedBy: t.DirectedBy, RequestedBy: t.RequestedBy, TakenFrom: t.TakenFrom, CreatedAt: timeText(t.CreatedAt),
		UpdatedAt: timeText(t.UpdatedAt), PauseReason: t.PauseReason,
		Progress: contract.TaskProgress{Executions: rawJSON(jsonBytes(count)), ByState: rawJSON(byState)}, OpenQuestions: rawJSON(open)}
}

// taskDetail: task_get's task with its progress, latest (or accepted) result, resources, runs, dependencies and questions.
func taskDetail(q *Request, id string) (contract.TaskDetail, error) {
	var none contract.TaskDetail
	t, err := taskLoad(q, id)
	if err != nil {
		return none, err
	}
	v, err := q.db().TaskView(q.ctx(), &id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, err
	}
	it := taskItem(t, v.ExecutionCount, v.ExecutionsByState, v.OpenQuestions)
	d := contract.TaskDetail{Id: it.Id, Code: it.Code, ProjectId: it.ProjectId, Title: it.Title, ExpectedOutcome: it.ExpectedOutcome, State: it.State, WaitReason: it.WaitReason,
		Priority: it.Priority, CompletionRequirements: it.CompletionRequirements, ResultDestination: it.ResultDestination, DeliveryMethod: it.DeliveryMethod,
		AcceptedBy: it.AcceptedBy, AcceptedAt: it.AcceptedAt, AcceptedArtifactIds: it.AcceptedArtifactIds, CreatedBy: it.CreatedBy, DirectedBy: it.DirectedBy,
		RequestedBy: it.RequestedBy, TakenFrom: it.TakenFrom, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, PauseReason: it.PauseReason,
		Progress: it.Progress, OpenQuestions: it.OpenQuestions, LatestResult: rawJSON(v.LatestResult)}
	// A completed task presents its explicitly accepted collection in selection
	// order. Missing/hidden/unretained selections cannot turn a later diagnostic
	// into the accepted result. Unaccepted work keeps task_view version ordering.
	if t.State == db.TaskStateCompleted && t.AcceptedAt != nil && t.AcceptedArtifactIds != nil {
		selected, err := resourceLineageQuery(q, func() ([]byte, error) {
			r, err := q.db().TaskAcceptedResult(q.ctx(), db.TaskAcceptedResultParams{TaskID: &id, Column2: jsonBytes(t.AcceptedArtifactIds)})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return r, err
		})
		if err != nil {
			return none, err
		}
		d.LatestResult = rawJSON(selected)
	}
	rollup, err := taskResourceRollup(q, id)
	if err != nil {
		return none, err
	}
	d.Cost, d.Resources = rollup.Total.Cost, &rollup
	execs, err := executionsList(q, contract.ExecutionsListRequest{TaskId: &id})
	if err != nil {
		return none, err
	}
	d.Executions = make([]contract.ExecutionRow, 0, len(execs))
	for _, ex := range execs {
		if ex.Kind != "tool_run" {
			d.Executions = append(d.Executions, ex)
		}
	}
	deps, err := q.db().TaskDependencies(q.ctx(), id)
	if err != nil {
		return none, err
	}
	d.Dependencies = make([]contract.TaskDependency, 0, len(deps))
	for _, x := range deps {
		d.Dependencies = append(d.Dependencies, contract.TaskDependency{Id: x.ID, Kind: x.Kind, Code: x.Code})
	}
	d.Questions, err = questionsList(q, id, "", "", nil, nil)
	return d, err
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

// taskSorts: the second sort key inside a group (owner review P7), ascending numbers; "-" before a name is descending.
// The default is recently changed first. Migrations 47 and 61 index each sort for the priority and project groupings and for no grouping;
// ponytail: the other groupings sort the filtered tasks, add their indexes when those lists outgrow a scan.
var taskSorts = map[string]string{"changed": "(extract(epoch FROM t.updated_at AT TIME ZONE 'UTC')*1000)::float8", "created": "(t.seq::float8)"}

const defaultTaskSort = "-changed"

// defaultTaskGroup is the grouping a view without one uses (view-url.ts DEFAULT_TASK_GROUP).
const defaultTaskGroup = "project"

func taskSortOf(v any) (string, error) {
	s, ok := v.(string)
	if v != nil && !ok {
		return "", fail(400, "sort must be a string")
	}
	if s == "" {
		s = defaultTaskSort
	}
	expr, ok := taskSorts[strings.TrimPrefix(s, "-")]
	if !ok {
		return "", fail(400, "sort must be changed, created, -changed or -created")
	}
	if strings.HasPrefix(s, "-") {
		return "(-" + expr + ")", nil
	}
	return expr, nil
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
		case "excludeProjectId": // empty: nothing is left out
			_, ok = v.([]any)
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
	if err := filterFields(f, "projectId", "excludeProjectId", "state", "search"); err != nil {
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

// projectWhere: the projectId filter (a list; null is work outside projects) and the excludeProjectId exclusion (the
// person's ignored projects, OR-34; a project in both is left out) as conditions on col.
func projectWhere(f doc, p *[]any, col string, c []string) ([]string, error) {
	if pv, ok := f["projectId"]; ok {
		ids, null, err := projectList(pv, "projectId")
		if err != nil {
			return nil, err
		}
		o := []string{}
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
	if xv, ok := f["excludeProjectId"]; ok {
		ids, null, err := projectList(xv, "excludeProjectId")
		if err != nil {
			return nil, err
		}
		if null {
			ids = append(ids, "-")
		}
		c = projectNotIn(c, p, col, ids) // '-' is no project too, as in the excludeProjectIds of the other operations
	}
	return c, nil
}

// projectList: a list filter's project ids and whether it holds null (work outside projects).
func projectList(v any, field string) ([]string, bool, error) {
	ps, _ := v.([]any)
	ids, null := []string{}, false
	for _, x := range ps {
		switch v := x.(type) {
		case string:
			ids = append(ids, v)
		case nil:
			null = true
		default:
			return nil, false, fail(400, "Invalid "+field)
		}
	}
	return ids, null, nil
}

const taskListCols = "t.id,t.code,t.project_id,t.title,t.state,t.pause_reason,t.wait_reason,t.priority,t.created_at,t.updated_at,t.seq,d.agent,d.host,d.started_at,coalesce(d.open_questions,0) AS open_questions,t.state_at,coalesce(d.executions,0) AS executions,d.last_agent"

func taskListRow(x doc) doc {
	return doc{"id": x["id"], "code": x["code"], "projectId": x["project_id"], "title": x["title"], "state": x["state"], "pauseReason": x["pause_reason"], "waitReason": x["wait_reason"], "priority": x["priority"], "agent": x["agent"], "host": x["host"],
		"startedAt": x["started_at"], "createdAt": x["created_at"], "updatedAt": x["updated_at"], "openQuestions": x["open_questions"],
		"stateAt": x["state_at"], "executions": x["executions"], "lastAgent": x["last_agent"], "sortKey": []any{x["g"], num(x["sk"]), x["id"]}}
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

// taskPage: task_page. The filter is read as sent (f): a null projectId means work outside projects, which the typed
// list of strings cannot hold.
func taskPage(q *Request, r *contract.TaskPageRequest, f doc) (contract.TaskPageReply, error) {
	out, err := taskPageDoc(q, r, f)
	return replyAs[contract.TaskPageReply](out, err)
}

func taskPageDoc(q *Request, r *contract.TaskPageRequest, f doc) (doc, error) {
	if f == nil {
		f = doc{}
	}
	_, G, err := groupOf(optAny(r.Group))
	if err != nil {
		return nil, err
	}
	S, err := taskSortOf(optAny(r.Sort))
	if err != nil {
		return nil, err
	}
	limit := 100
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 100)
	}
	p := []any{}
	c, err := taskWhere(f, &p, true)
	if err != nil {
		return nil, err
	}
	// No grouping orders by the sort key alone, so the sort's own index serves it (a constant group key would not).
	none := G.sql == taskGroups["none"].sql
	if r.After != nil {
		var a []any
		if json.Unmarshal(jsonBytes(r.After), &a) != nil || len(a) != 3 {
			return nil, fail(400, "Invalid after")
		}
		if none {
			p = append(p, num(a[1]), fmt.Sprint(a[2]))
			c = append(c, fmt.Sprintf("(%s,t.id) > ($%d::float8,$%d)", S, len(p)-1, len(p)))
		} else {
			p = append(p, fmt.Sprint(a[0]), num(a[1]), fmt.Sprint(a[2]))
			c = append(c, fmt.Sprintf("(%s,%s,t.id) > ($%d,$%d::float8,$%d)", G.sql, S, len(p)-2, len(p)-1, len(p)))
		}
	}
	g, lim := G.sql, strconv.Itoa(limit+1)
	order, outer := g+",", "t.g,"
	if none {
		order, outer = "", ""
	}
	var sql string
	if G.derived {
		sql = "SELECT " + taskListCols + "," + g + " AS g," + S + " AS sk FROM task t LEFT JOIN task_derived d ON d.task_id=t.id" + where(c) + " ORDER BY " + order + S + ",t.id LIMIT " + lim
	} else {
		sql = "SELECT " + taskListCols + ",t.g," + S + " AS sk FROM (SELECT t.*," + g + " AS g FROM task t" + where(c) + " ORDER BY " + order + S + ",t.id LIMIT " + lim + ") t LEFT JOIN task_derived d ON d.task_id=t.id ORDER BY " + outer + S + ",t.id"
	}
	rs, err := rowsOf(q, sql, p...)
	if err != nil {
		return nil, err
	}
	counts, groups, err := taskSummary(q, f, optAny(r.Group), r.GroupCounts != nil && *r.GroupCounts)
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

func taskMembership(q *Request, f doc, group, sort any, ids []string) ([]doc, error) {
	_, G, err := groupOf(group)
	if err != nil {
		return nil, err
	}
	S, err := taskSortOf(sort)
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
	rs, err := rowsOf(q, "SELECT "+taskListCols+","+G.sql+" AS g,"+S+" AS sk FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"+where(c), p...)
	out := []doc{}
	for _, r := range rs {
		out = append(out, taskListRow(r))
	}
	return out, err
}

// settingChangeRecord: a setting-change audit row (the operator's take of a task, a withdrawn question).
type settingChangeRecord struct {
	ID        string  `json:"id"`
	Target    string  `json:"target"`
	ProjectID *string `json:"projectId"`
	Field     string  `json:"field"`
	Previous  any     `json:"previous"`
	Value     any     `json:"value"`
	Reason    string  `json:"reason,omitempty"`
	Actor     string  `json:"actor"`
	At        int64   `json:"at"`
}

func putSettingChange(q *Request, c settingChangeRecord) error {
	rec, err := asT[doc](c)
	if err != nil {
		return err
	}
	return put(q, "setting-change", rec)
}

func init() {
	op("task_create", func(q *Request, r *contract.TaskCreateRequest) (contract.TaskDetail, error) {
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
		id, err := createTask(q, newTask{ProjectID: strPtr(r.ProjectId), Title: r.Title, ExpectedOutcome: r.ExpectedOutcome, Priority: r.Priority,
			CompletionRequirements: r.CompletionRequirements, ResultDestination: r.ResultDestination, CreatedBy: actor(q, "operator"), DirectedBy: director(q),
			DependsOn: deps, Held: q.agent != "" || (r.Held != nil && *r.Held)})
		if err != nil {
			return contract.TaskDetail{}, err
		}
		return taskDetail(q, id)
	})
	op("task_update", func(q *Request, r *contract.TaskUpdateRequest) (contract.TaskDetail, error) {
		var none contract.TaskDetail
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		if _, err := taskLoad(q, id); err != nil {
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
			if trig == "cancel" {
				if err := cancelTask(q, id); err != nil {
					return none, err
				}
			} else if _, err := taskTransition(q, id, trig, ""); err != nil {
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
		set := db.TaskUpdateFieldsParams{ID: id}
		for _, f := range []struct {
			name string
			v    *string
			to   **string
		}{{"title", r.Title, &set.Title}, {"expectedOutcome", r.ExpectedOutcome, &set.ExpectedOutcome}, {"priority", r.Priority, &set.Priority}, {"resultDestination", r.ResultDestination, &set.ResultDestination}} {
			if f.v == nil {
				continue
			}
			v := *f.v
			if f.name == "priority" {
				var err error
				if v, err = priorityClass(v); err != nil {
					return none, err
				}
			} else if len([]rune(v)) > 4000 {
				return none, fail(400, "Invalid "+f.name)
			}
			*f.to = &v
		}
		if set.Title != nil || set.ExpectedOutcome != nil || set.Priority != nil || set.ResultDestination != nil {
			if err := q.db().TaskUpdateFields(q.ctx(), set); err != nil {
				return none, err
			}
		}
		return taskDetail(q, id)
	})
	for _, x := range []struct{ name, trig string }{{"task_hold", "hold"}, {"task_release", "release"}, {"task_cancel", "cancel"}} {
		op(x.name, func(q *Request, r *contract.TaskHoldRequest) (contract.TaskDetail, error) {
			if !idOK(r.TaskId) {
				return contract.TaskDetail{}, fail(400, "Expected taskId")
			}
			if x.trig == "cancel" {
				if err := cancelTask(q, r.TaskId); err != nil {
					return contract.TaskDetail{}, err
				}
			} else if _, err := taskTransition(q, r.TaskId, x.trig, ""); err != nil {
				return contract.TaskDetail{}, err
			}
			return taskDetail(q, r.TaskId)
		})
	}
	op("task_get", func(q *Request, r *contract.TaskGetRequest) (contract.TaskDetail, error) {
		if !idOK(r.TaskId) {
			return contract.TaskDetail{}, fail(400, "Expected taskId")
		}
		return taskDetail(q, r.TaskId)
	})
	op("task_request_acceptance", func(q *Request, r *contract.TaskRequestAcceptanceRequest) (contract.QuestionRow, error) {
		var none contract.QuestionRow
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		t, err := taskLoad(q, id)
		if err != nil {
			return none, err
		}
		if t.State != db.TaskStateInProgress {
			return none, fail(409, "Task is not in progress")
		}
		qid, err := requestAcceptance(q, acceptTask{ID: t.ID, Title: t.Title, ProjectID: t.ProjectID}, r.ArtifactIds, r.Text, actor(q, "operator"))
		if err != nil {
			return none, err
		}
		return questionRow(q, id, qid)
	})
	op("task_list", func(q *Request, r *contract.TaskListRequest) (contract.TaskListReply, error) {
		rs, err := q.db().TaskList(q.ctx(), db.TaskListParams{ProjectID: strPtr(r.ProjectId), State: strPtr(r.State)})
		if err != nil {
			return contract.TaskListReply{}, err
		}
		tasks := make([]contract.TaskListItem, 0, len(rs))
		for _, t := range rs {
			tasks = append(tasks, taskItem(t.Task, t.ExecutionCount, t.ExecutionsByState, t.OpenQuestions))
		}
		return contract.TaskListReply{Tasks: tasks}, nil
	})
	op("task_take", func(q *Request, r *contract.TaskTakeRequest) (contract.TaskDetail, error) {
		var none contract.TaskDetail
		id := r.TaskId
		if !idOK(id) {
			return none, fail(400, "Expected taskId")
		}
		t, err := taskLoad(q, id)
		if err != nil {
			return none, err
		}
		p := q.person
		if p == nil || !mayAccessProjectID(p, deref(t.ProjectID)) {
			return none, fail(403, "Take needs a grant on the task project")
		}
		if deref(t.DirectedBy) != p.Email {
			if err := takeTask(q, id, p.Email, ""); err != nil {
				return none, err
			}
			if err := putSettingChange(q, settingChangeRecord{ID: newID(), Target: "task:" + id, ProjectID: t.ProjectID, Field: "directedBy", Previous: t.DirectedBy, Value: p.Email, Actor: p.Email, At: nowMs()}); err != nil {
				return none, err
			}
		}
		return taskDetail(q, id)
	})
	op("questions_list", func(q *Request, r *contract.QuestionsListRequest) (contract.QuestionsListReply, error) {
		if err := excludeCheck(r.ExcludeProjectIds); err != nil {
			return contract.QuestionsListReply{}, err
		}
		qs, err := questionsList(q, deref(r.TaskId), deref(r.State), deref(r.Kind), r.ProjectIds, r.ExcludeProjectIds)
		return contract.QuestionsListReply{Questions: qs}, err
	})
	// questions_withdraw: the owner (or full access) withdraws open questions that no longer need anyone, with one
	// setting-change audit row each. A cause is let go, so a problem still present is asked again by its source.
	op("questions_withdraw", func(q *Request, r *contract.QuestionsWithdrawRequest) (contract.QuestionsWithdrawReply, error) {
		out := contract.QuestionsWithdrawReply{Withdrawn: []string{}, Skipped: []contract.QuestionSkipped{}}
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
			row, err := q.db().TaskQuestionLock(q.ctx(), id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
			if err != nil || row.State != db.QuestionStateOpen {
				state := "unknown"
				if err == nil {
					state = string(row.State)
				}
				out.Skipped = append(out.Skipped, contract.QuestionSkipped{Id: id, State: state})
				continue
			}
			by, at := actor(q, "operator"), nowMs()
			w := struct { // as lab_rating_withdraw records it
				Reason string `json:"reason"`
				Actor  string `json:"actor"`
				At     int64  `json:"at"`
			}{r.Reason, by, at}
			if _, err := questionTransition(q, id, questionEvent{To: "withdrawn", Withdrawal: jsonBytes(w)}); err != nil {
				return out, err
			}
			if err := putSettingChange(q, settingChangeRecord{ID: newID(), Target: "question:" + id, ProjectID: row.ProjectID, Field: "state", Previous: "open", Value: "withdrawn", Reason: r.Reason, Actor: by, At: nowMs()}); err != nil {
				return out, err
			}
			out.Withdrawn = append(out.Withdrawn, id)
		}
		publish(q)
		return out, nil
	})
	op("questions_answer", func(q *Request, r *contract.QuestionsAnswerRequest) (contract.QuestionRow, error) {
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
		project, err := q.db().TaskAgentQuestionProject(q.ctx(), id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return none, err
		}
		if err == nil {
			// The reply reaches another session: the person must have that session's project, as for any project op.
			if p := q.person; p != nil && !fullAccess(p) && !mayAccessProjectID(p, project) {
				return none, errNotFound
			}
			reply := make(doc) // agentReply's request record
			reply["questionId"], reply["answer"] = id, answer
			if _, err := agentReply(q, reply); err != nil {
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
	})
	op("executions_list", func(q *Request, r *contract.ExecutionsListRequest) (contract.ExecutionsListReply, error) {
		return executionsPage(q, *r)
	})
	op("task_page", func(q *Request, r *contract.TaskPageRequest) (contract.TaskPageReply, error) {
		var raw struct {
			Filter doc `json:"filter"`
		}
		json.Unmarshal(q.body, &raw)
		return taskPage(q, r, raw.Filter)
	})
	op("task_derived_check", func(q *Request, _ *contract.TaskDerivedCheckRequest) (contract.TaskDerivedCheckReply, error) {
		n, err := q.db().TaskCount(q.ctx())
		if err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		// task_derived_drift() returns a table, which the sqlc schema (db/schema.sql) does not declare.
		d, err := rowsOf(q, `SELECT task_id AS "taskId",stored,expected FROM task_derived_drift()`)
		if err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		drifted, err := asT[[]contract.TaskDerivedDrift](d)
		return contract.TaskDerivedCheckReply{Checked: n, Drifted: drifted}, err
	})
}

// questionRow: one question row as its contract type (404 when the row is not in the caller's scope); taskID, when set,
// must be its task.
func questionRow(q *Request, taskID, id string) (contract.QuestionRow, error) {
	rs, err := questionRows(q, db.TaskQuestionsParams{ID: &id, TaskID: strPtr(taskID)}, nil, nil)
	if err != nil {
		return contract.QuestionRow{}, err
	}
	if len(rs) == 0 {
		return contract.QuestionRow{}, errNotFound
	}
	return rs[0], nil
}

// idOK: a record id argument: non-empty, at most 200 characters (strArg's rule).
func idOK(id string) bool { return id != "" && len([]rune(id)) <= 200 }

// optAny and rawAny: optional request fields as record values (nil when absent).
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

// findQuestion: the question id of the task as its row, nil when it is not one.
func findQuestion(q *Request, taskID, id string) (any, error) {
	rs, err := questionRows(q, db.TaskQuestionsParams{ID: &id, TaskID: strPtr(taskID)}, nil, nil)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

// Task columns of the search syntax for task_page (tasks.go taskWhere); agent and host are subqueries, so every task
// query can use them without a join. agent: the harness of the task's latest execution (task_derived.last_agent;
// task_derived.agent is set only while one runs). The search queries (queries/entities.sql) mirror them.
var taskSearchText = []string{"t.title", "t.id", "t.code"}
var taskSearchFields = map[string]string{"agent": "(SELECT last_agent FROM task_derived WHERE task_id=t.id)", "host": "(SELECT host FROM task_derived WHERE task_id=t.id)",
	"project": "t.project_id", "state": stateSQL}
