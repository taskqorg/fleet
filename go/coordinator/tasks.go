// Tasks and questions (docs/designs/fleet-data-foundation.md §2): state only through transition(), one typed question table with one
// answer path, the operator-app task list (page, counts, groups, membership) and the task operations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/obs"
)

var taskStates = []string{"queued", "in_progress", "waiting", "paused", "completed", "failed", "cancelled"}

// listStates: the states of lists and counts; "held" is a paused task with pause_reason 'held' (fleet-pm-agent.md §4).
var listStates = append([]string{"held"}, taskStates...)

var openTask = map[string]bool{"queued": true, "in_progress": true, "waiting": true, "paused": true}

type taskRule struct {
	from []string
	to   string
}

// taskTransitions: the states (schema.sql, task_state) a task may move each state to.
// A paused task resumes or releases to its state before pause (queued, in_progress or waiting).
var taskTransitions = map[string][]string{
	"queued":      {"in_progress", "waiting", "paused", "cancelled"},
	"in_progress": {"in_progress", "waiting", "paused", "completed", "failed", "cancelled"},
	"waiting":     {"in_progress", "waiting", "paused", "failed", "cancelled"},
	"paused":      {"queued", "in_progress", "waiting", "cancelled"},
	"completed":   nil,
	"failed":      nil,
	"cancelled":   nil,
}

var taskRules = map[string]taskRule{
	"first_execution": {[]string{"queued"}, "in_progress"},
	"blocked":         {[]string{"queued", "in_progress", "waiting"}, "waiting"},
	"unblocked":       {[]string{"waiting"}, "in_progress"},
	"accepted":        {[]string{"in_progress"}, "completed"},
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

// taskLoad: the task's row; errNotFound when absent.
func taskLoad(q *Request, id string) (db.Task, error) {
	t, err := q.db().TaskGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		logTaskLoadNotFound(q, id, err)
		return t, errNotFound
	}
	return t, err
}

type taskNotFoundDiag struct {
	txProjectIDs   string
	txPerson       string
	txDeleted      string
	exists         bool
	rowProjectID   string
	tombstoned     bool
	projectDeleted bool
	diagErr        error
}

// inspectTaskVisibility reads the current transaction's RLS/deleted settings and probes whether id exists outside RLS
// or deleted filtering in the same snapshot, then restores the caller's transaction settings.
func inspectTaskVisibility(q *Request, id string) (out taskNotFoundDiag) {
	if q == nil || q.tx == nil {
		return out
	}
	ctx := q.ctx()
	d := q.db()
	vis, err := d.TxVisibility(ctx)
	if err != nil {
		out.diagErr = err
		return out
	}
	out.txProjectIDs, out.txPerson, out.txDeleted = vis.ProjectIds, vis.Person, vis.Deleted
	if vis.ProjectIds != "*" || vis.Person != "" {
		if err := d.TxSetScope(ctx, db.TxSetScopeParams{ProjectIds: "*", Person: ""}); err != nil {
			out.diagErr = err
			return out
		}
		defer func() {
			if rerr := d.TxSetScope(ctx, db.TxSetScopeParams{ProjectIds: vis.ProjectIds, Person: vis.Person}); rerr != nil && out.diagErr == nil {
				out.diagErr = rerr
			}
		}()
	}
	if vis.Deleted != "on" {
		if err := d.TxShowDeleted(ctx, "on"); err != nil {
			out.diagErr = err
			return out
		}
		defer func() {
			if rerr := d.TxShowDeleted(ctx, vis.Deleted); rerr != nil && out.diagErr == nil {
				out.diagErr = rerr
			}
		}()
	}
	row, err := d.TaskNotFoundDiag(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out
	}
	if err != nil {
		out.diagErr = err
		return out
	}
	out.exists = true
	out.rowProjectID = row.ProjectID
	out.tombstoned = row.Tombstoned
	out.projectDeleted = row.ProjectDeleted
	return out
}

// logTaskLoadNotFound records structured diagnostic fields and span attributes on a taskLoad 404 without leaking secrets.
func logTaskLoadNotFound(q *Request, id string, loadErr error) {
	var personEmail, actorName, route, hostID string
	var full, accessTxSynced bool
	expectedScope := "*"
	ctx := context.Background()
	if q != nil {
		ctx = q.ctx()
		if q.person != nil {
			personEmail = q.person.Email
		}
		full = fullAccess(q.person)
		actorName = actor(q, "")
		if sc := scopeOf(q.person); sc != nil {
			expectedScope = sc.ProjectIDs
		}
		accessTxSynced = q.tx != nil && q.accessTx == q.tx
		if q.r != nil && q.r.URL != nil {
			route = q.r.URL.Path
		}
		if q.host != nil {
			hostID = q.host.HostID
		}
	}
	diag := inspectTaskVisibility(q, id)
	deletedVisible := diag.txDeleted == "on"
	if span := obs.From(ctx); span != nil {
		span.Set("fleet.task.not_found.task_id", id)
		span.Set("fleet.task.not_found.person", personEmail)
		span.Set("fleet.task.not_found.actor", actorName)
		span.Set("fleet.task.not_found.expected_project_ids", expectedScope)
		span.Set("fleet.task.not_found.project_ids", diag.txProjectIDs)
		span.Set("fleet.task.not_found.tx_person", diag.txPerson)
		span.Set("fleet.task.not_found.deleted_visible", strconv.FormatBool(deletedVisible))
		span.Set("fleet.task.not_found.exists", strconv.FormatBool(diag.exists))
		span.Set("fleet.task.not_found.tombstoned", strconv.FormatBool(diag.tombstoned))
		span.Set("fleet.task.not_found.project_deleted", strconv.FormatBool(diag.projectDeleted))
		if diag.exists {
			span.Set("fleet.task.not_found.row_project_id", diag.rowProjectID)
		}
		span.Set("fleet.task.not_found.err", loadErr.Error())
		if diag.diagErr != nil {
			span.Set("fleet.task.not_found.diag_err", diag.diagErr.Error())
		}
	}
	attrs := []slog.Attr{
		slog.String("task_id", id),
		slog.String("person", personEmail),
		slog.String("actor", actorName),
		slog.Bool("full_access", full),
		slog.String("expected_project_ids", expectedScope),
		slog.String("project_ids", diag.txProjectIDs),
		slog.String("tx_person", diag.txPerson),
		slog.Bool("deleted_visible", deletedVisible),
		slog.Bool("access_tx_synced", accessTxSynced),
		slog.Bool("exists", diag.exists),
		slog.Bool("tombstoned", diag.tombstoned),
		slog.Bool("project_deleted", diag.projectDeleted),
	}
	if diag.exists {
		attrs = append(attrs, slog.String("row_project_id", diag.rowProjectID))
	}
	if route != "" {
		attrs = append(attrs, slog.String("route", route))
	}
	if hostID != "" {
		attrs = append(attrs, slog.String("host_id", hostID))
	}
	attrs = append(attrs, slog.String("err", loadErr.Error()))
	if diag.diagErr != nil {
		attrs = append(attrs, slog.String("diag_err", diag.diagErr.Error()))
	}
	slog.LogAttrs(ctx, slog.LevelWarn, "taskLoad: not found", attrs...)
}

// taskRow: the task's row as a record (column names); errNotFound when absent.
func taskRow(q *Request, id string) (json.RawMessage, error) {
	return record(q.db().TaskRowRecord(q.ctx(), id))
}

// questionRecord: the question's row as a record (column names), as the answer checks and callers outside this file
// read it; nil when absent.
func questionRecord(q *Request, id string) (json.RawMessage, error) {
	d, err := record(q.db().QuestionRowRecord(q.ctx(), id))
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return d, err
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
	rule := taskRules[trigger]
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
	if !slices.Contains(taskTransitions[state], to) {
		return "", fail(409, fmt.Sprintf("Task %s cannot become %s", state, to))
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
		} else if active, err := getExecution(q, eid); err != nil || active.terminal() {
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
		if state != "waiting" || reason != "" && deref(t.WaitReason) != reason {
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

// settleIdleTask reconciles offers and generated decisions against the task's current evidence.
// Only eligible results and observed failures that need an operator can create an inbox item.
func settleIdleTask(q *Request, id string) (bool, error) {
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &id)
	if err != nil {
		return false, err
	}
	open, err := q.db().TaskOpenAcceptances(q.ctx(), &id)
	if err != nil {
		return false, err
	}
	var superseded []string
	for _, question := range open {
		var data questionData
		if len(question.Data) > 0 {
			if err := json.Unmarshal(question.Data, &data); err != nil {
				superseded = append(superseded, question.ID)
				continue
			}
		}
		selected, err := acceptanceResult(q, id, data.EarlierResult, pending, deref(question.AskedBy) == "fleet")
		var conflict *HTTPError
		if err != nil && (!errors.As(err, &conflict) || conflict.Code != 409) {
			return false, err
		}
		if err != nil || selected.id != deref(question.ExecutionID) || !selected.matches(data.ArtifactIds) {
			superseded = append(superseded, question.ID)
		}
	}
	if err := questionsWithdraw(q, superseded); err != nil {
		return false, err
	}
	latest, err := q.db().TaskAcceptanceExecution(q.ctx(), db.TaskAcceptanceExecutionParams{TaskID: &id})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	abandoned := deref(latest.Problem) == "unknown:abandoned"
	needsDecision := abandoned || latest.Observation == db.ObservationConfirmed &&
		(latest.State == db.ExecutionStateFailed || latest.State == db.ExecutionStateCancelled ||
			latest.State == db.ExecutionStateSucceeded && latest.Outcome == "failed")
	if pending || !needsDecision {
		if err := withdrawIdleDecision(q, id); err != nil {
			return false, err
		}
	}
	if _, err := settleTask(q, id, ""); err != nil || pending {
		return false, err
	}
	if !needsDecision && (latest.State != db.ExecutionStateSucceeded || latest.Observation != db.ObservationConfirmed) {
		return false, nil
	}
	t, err := q.db().TaskIdle(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !needsDecision {
		_, err = requestAcceptance(q, acceptTask{ID: t.ID, Title: t.Title, ProjectID: t.ProjectID}, nil, nil, "fleet")
		var ineligible *HTTPError
		if errors.As(err, &ineligible) && ineligible.Code == 409 {
			return false, nil
		}
		return err == nil, err
	}
	options := []string{idleFail}
	if _, err := acceptanceResult(q, id, true, false, true); err == nil {
		options = append(options, idleAccept)
	} else {
		var conflict *HTTPError
		if !errors.As(err, &conflict) || conflict.Code != 409 {
			return false, err
		}
	}
	what := "The last run of " + t.Title + " " + t.Latest
	switch {
	case abandoned:
		what = "The last run was abandoned. Its outcome is unknown"
	case latest.Outcome == "failed":
		what = "The worker reported that the task is incomplete"
	}
	next := ". Choose “fail task” to close the task without a result"
	if t.LatestSession != nil {
		next += ", or retry with a new message in its conversation"
	}
	if slices.Contains(options, idleAccept) {
		next += ". Choose “accept earlier result” to review the earlier result"
	}
	_, err = askQuestionAs(q, questionAsk{Kind: "operator_decision", TaskID: id, ProjectID: deref(t.ProjectID), ExecutionID: strPtr(t.LatestID), SessionID: t.LatestSession,
		AskedBy: strPtr("fleet"), Options: jsonBytes(options), Data: []byte(`{"idle":true}`),
		Text: what + next + "."})
	return err == nil, err
}

func init() {
	// At start: every idle task, one transaction each, so one bad task neither rolls back nor blocks the others.
	onStart(func(ctx context.Context, a *App) {
		var ids []string
		if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
			ids, err = q.db().TaskOpenIDs(q.ctx())
			return
		}); err != nil {
			log.Printf("settle idle tasks: %v", err)
			return
		}
		n := 0
		for _, id := range ids {
			asked := false
			if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) (err error) { asked, err = settleIdleTask(q, id); return }); err != nil {
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

type taskAcceptanceResult struct {
	id        string
	artifacts []string
	required  []string
}

// acceptanceResult is shared by automatic and direct requests, stale-question settlement and answers.
// When requireReport is true (automatic Fleet offers), an agent_run requires a successful work report;
// explicit operator/agent requests still require confirmed success, no pending work, stored required artifacts,
// and no failed work report.
func acceptanceResult(q *Request, taskID string, earlier, pending, requireReport bool) (taskAcceptanceResult, error) {
	var result taskAcceptanceResult
	if pending {
		return result, fail(409, "Task work or a queued continuation is still pending")
	}
	e, err := q.db().TaskAcceptanceExecution(q.ctx(), db.TaskAcceptanceExecutionParams{TaskID: &taskID, Earlier: earlier})
	if errors.Is(err, pgx.ErrNoRows) {
		if earlier {
			return result, fail(409, "No successful earlier work result is available")
		}
		// Results may be supplied without a Fleet execution.
	} else {
		if err != nil {
			return result, err
		}
		if e.Outcome == "failed" {
			return result, fail(409, "The worker reported that the task is incomplete")
		}
		if e.State != db.ExecutionStateSucceeded || e.Observation != db.ObservationConfirmed {
			return result, fail(409, "Latest work has no confirmed successful result")
		}
		if requireReport && e.Kind == db.ExecutionKindAgentRun && e.Outcome != "succeeded" {
			return result, fail(409, "The agent process finished without a successful work report")
		}
		requirements, err := artifactRequirement(withTx(q), q.app, e.ID, nil)
		if err != nil {
			return result, err
		}
		if len(requirements.Missing)+len(requirements.Failed)+len(requirements.Pending) > 0 {
			return result, fail(409, "Required artifacts are unavailable for this execution")
		}
		result.id = e.ID
		result.required = requirements.storedRequired
	}
	result.artifacts, err = q.db().TaskAcceptanceArtifacts(q.ctx(), db.TaskAcceptanceArtifactsParams{TaskID: &taskID, ExecutionID: strPtr(result.id)})
	if err == nil && !result.matches(result.artifacts) {
		return taskAcceptanceResult{}, fail(409, "Required artifacts are unavailable for this execution")
	}
	return result, err
}

func (r taskAcceptanceResult) matches(selected []string) bool {
	for _, id := range selected {
		if !slices.Contains(r.artifacts, id) {
			return false
		}
	}
	for _, id := range r.required {
		if !slices.Contains(selected, id) {
			return false
		}
	}
	return true
}

// acceptTask: the task an acceptance is asked for; EarlierResult selects its latest successful run over its latest.
type acceptTask struct {
	ID, Title     string
	ProjectID     *string
	EarlierResult bool
}

// requestAcceptance asks the task's acceptance question with the given artifacts (default: its stored originals). An open
// acceptance question matching the exact execution and artifacts is reused and updated; obsolete ones are withdrawn.
func requestAcceptance(q *Request, t acceptTask, artifactIDs []string, text *string, askedBy string) (string, error) {
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &t.ID)
	if err != nil {
		return "", err
	}
	selected, err := acceptanceResult(q, t.ID, t.EarlierResult, pending, askedBy == "fleet")
	if err != nil {
		return "", err
	}
	ids := append([]string{}, selected.artifacts...)
	if artifactIDs != nil {
		if !selected.matches(artifactIDs) {
			return "", fail(409, "Acceptance must select stored originals and include all required artifacts")
		}
		ids = append([]string{}, artifactIDs...)
	}
	body := "Accept the result of: " + t.Title
	if text != nil {
		body = string([]rune(*text)[:min(len([]rune(*text)), 2000)])
	}
	open, err := q.db().TaskOpenAcceptances(q.ctx(), &t.ID)
	if err != nil {
		return "", err
	}
	for _, question := range open {
		var data questionData
		decodeErr := error(nil)
		if len(question.Data) > 0 {
			decodeErr = json.Unmarshal(question.Data, &data)
		}
		if decodeErr == nil && deref(question.ExecutionID) == selected.id && data.EarlierResult == t.EarlierResult && slices.Equal(data.ArtifactIds, ids) {
			return question.ID, q.db().TaskAcceptanceAskAgain(q.ctx(), db.TaskAcceptanceAskAgainParams{ID: question.ID, Text: body, ArtifactIds: jsonBytes(ids), AskedBy: &askedBy})
		}
		if err := questionsWithdraw(q, []string{question.ID}); err != nil {
			return "", err
		}
	}
	data := struct {
		ArtifactIds   []string `json:"artifactIds"`
		EarlierResult *bool    `json:"earlierResult"`
	}{ArtifactIds: ids}
	if t.EarlierResult {
		data.EarlierResult = &t.EarlierResult
	}
	return askQuestionAs(q, questionAsk{Kind: "acceptance", TaskID: t.ID, ProjectID: deref(t.ProjectID), Text: body, Options: jsonBytes([]string{"accepted", "rejected"}),
		AskedBy: &askedBy, ExecutionID: strPtr(selected.id), Data: jsonBytes(data)})
}

// questionData: the fields of a question's data the answer path reads.
type questionData struct {
	EarlierResult bool     `json:"earlierResult"`
	Idle          bool     `json:"idle"`
	ArtifactIds   []string `json:"artifactIds"`
}

// answerQuestion: one answer wins; an acceptance completes (or keeps in progress) its task, other answers unblock it.
// runAnswerChecks and runAnswerAfter apply the Lab and budget rules of the question. The reply is the question's record.
func answerQuestion(q *Request, id, answer, by string, artifactIDs []any) (json.RawMessage, error) {
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
	var decodeErr error
	if len(cur.Data) > 0 {
		decodeErr = json.Unmarshal(cur.Data, &data)
	}
	if cur.Kind == db.QuestionKindAcceptance && answer != "accepted" && answer != "rejected" {
		return nil, fail(400, "Acceptance answer is accepted or rejected")
	}
	if cur.Kind == db.QuestionKindAcceptance && answer == "accepted" {
		if decodeErr != nil {
			return nil, fail(409, "The offer has invalid result data")
		}
		pending, err := q.db().TaskHasPendingWork(q.ctx(), cur.TaskID)
		if err != nil {
			return nil, err
		}
		selected, err := acceptanceResult(q, deref(cur.TaskID), data.EarlierResult, pending, deref(cur.AskedBy) == "fleet")
		if err != nil {
			return nil, err
		}
		if selected.id != deref(cur.ExecutionID) || !selected.matches(data.ArtifactIds) {
			return nil, fail(409, "The offer no longer matches the eligible result")
		}
		if artifactIDs != nil {
			ids := make([]string, 0, len(artifactIDs))
			for _, artifact := range artifactIDs {
				id, ok := artifact.(string)
				if !ok || !slices.Contains(data.ArtifactIds, id) {
					return nil, fail(409, "Acceptance must select artifacts from the offer")
				}
				ids = append(ids, id)
			}
			if !selected.matches(ids) {
				return nil, fail(409, "Acceptance must include all required artifacts")
			}
			data.ArtifactIds = ids
		}
	}
	if cur.Kind == db.QuestionKindOperatorDecision && data.Idle && answer != idleFail && answer != idleAccept {
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
				ids = append(ids, data.ArtifactIds...)
				if err := q.db().TaskAccept(q.ctx(), db.TaskAcceptParams{ID: t, AcceptedBy: &by, ArtifactIds: ids}); err != nil {
					return nil, err
				}
				if err := deliveredTeardown(q, t); err != nil {
					return nil, err
				}
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
func questionsList(q *Request, taskID, state, kind string, projects, exclude []string, limit *int64) ([]contract.QuestionRow, error) {
	return questionRows(q, db.TaskQuestionsParams{TaskID: strPtr(taskID), State: strPtr(state), Kind: strPtr(kind), Lim: limit}, projects, exclude)
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

// taskCompletionReportMissing is a read-only explanation of the latest agent run's evidence.
func taskCompletionReportMissing(q *Request, t db.Task) (bool, error) {
	if !openTask[string(t.State)] || t.AcceptedAt != nil {
		return false, nil
	}
	latest, err := q.db().TaskAcceptanceExecution(q.ctx(), db.TaskAcceptanceExecutionParams{TaskID: &t.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || latest.Kind != db.ExecutionKindAgentRun || latest.State != db.ExecutionStateSucceeded ||
		latest.Observation != db.ObservationConfirmed || latest.Outcome != "" {
		return false, err
	}
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &t.ID)
	if err != nil || pending {
		return false, err
	}
	offers, err := q.db().TaskOpenAcceptances(q.ctx(), &t.ID)
	if err != nil {
		return false, err
	}
	for _, offer := range offers {
		var data questionData
		if len(offer.Data) > 0 && json.Unmarshal(offer.Data, &data) != nil {
			continue
		}
		if !data.EarlierResult && deref(offer.AskedBy) == "fleet" {
			continue
		}
		selected, err := acceptanceResult(q, t.ID, data.EarlierResult, false, deref(offer.AskedBy) == "fleet")
		var conflict *HTTPError
		if errors.As(err, &conflict) && conflict.Code == 409 {
			continue
		}
		if err != nil || selected.id == deref(offer.ExecutionID) && selected.matches(data.ArtifactIds) {
			return false, err
		}
	}
	return true, nil
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
	missingReport, err := taskCompletionReportMissing(q, t)
	if err != nil {
		return none, err
	}
	if missingReport {
		d.CompletionReportMissing = ptr(true)
	}
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
	d.Questions, err = questionsList(q, id, "", "", nil, nil, nil)
	return d, err
}

func likePattern(s string) string {
	r := strings.NewReplacer(`%`, `\%`, `_`, `\_`, `\`, `\\`)
	return "%" + r.Replace(strings.ToLower(strings.TrimSpace(s))) + "%"
}

// taskListDBRow: a task_page and list_membership row (sqlTaskListCols) with its group and sort keys.
type taskListDBRow struct {
	ID                string  `db:"id"`
	Code              *string `db:"code"`
	ProjectID         *string `db:"project_id"`
	Title             string  `db:"title"`
	State             string  `db:"state"`
	PauseReason       *string `db:"pause_reason"`
	WaitReason        *string `db:"wait_reason"`
	Priority          string  `db:"priority"`
	CreatedAt         string  `db:"created_at"`
	UpdatedAt         string  `db:"updated_at"`
	Agent             *string `db:"agent"`
	Host              *string `db:"host"`
	StartedAt         *string `db:"started_at"`
	OpenQuestions     int64   `db:"open_questions"`
	StateAt           *string `db:"state_at"`
	Executions        int64   `db:"executions"`
	LastAgent         *string `db:"last_agent"`
	ActiveState       *string `db:"active_state"`
	ActiveStartedAt   *string `db:"active_started_at"`
	ActiveObservation *string `db:"active_observation"`
	ActiveLastSeenAt  *string `db:"active_last_seen_at"`
	ActiveStalled     bool    `db:"active_stalled"`
	G                 string  `db:"g"`
	Sk                float64 `db:"sk"`
}

func (x taskListDBRow) row() contract.TaskRow {
	stalled := x.ActiveStalled
	return contract.TaskRow{Id: x.ID, Code: x.Code, ProjectId: x.ProjectID, Title: x.Title, State: x.State, PauseReason: x.PauseReason,
		WaitReason: x.WaitReason, Priority: x.Priority, Agent: x.Agent, Host: x.Host, StartedAt: x.StartedAt, CreatedAt: x.CreatedAt,
		UpdatedAt: x.UpdatedAt, OpenQuestions: x.OpenQuestions, StateAt: x.StateAt, Executions: x.Executions, LastAgent: x.LastAgent,
		ActiveState: x.ActiveState, ActiveStartedAt: x.ActiveStartedAt, ActiveObservation: x.ActiveObservation,
		ActiveLastSeenAt: x.ActiveLastSeenAt, ActiveStalled: &stalled, SortKey: sortKey(x.G, x.Sk, x.ID)}
}

// taskFilter: a task list's filter as sent (rowFilterOf).
func taskFilter(raw json.RawMessage) (rowFilter, error) {
	return rowFilterOf(raw, "projectId", "excludeProjectId", "state", "search")
}

// taskListRows: the task rows of an assembled list statement.
func taskListRows(q *Request, sql string, p []any, err error) ([]contract.TaskRow, error) {
	if err != nil {
		return nil, err
	}
	rs, err := typedRows[taskListDBRow](q, sql, p)
	out := make([]contract.TaskRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.row())
	}
	return out, err
}

func pageLimit(v any, def int) int {
	if v == nil {
		return def
	}
	return int(math.Max(1, math.Min(500, math.Floor(num(v)))))
}

func zeroCounts(states []string) map[string]int64 {
	c := map[string]int64{}
	for _, s := range states {
		c[s] = 0
	}
	return c
}

func total(c map[string]int64) int64 {
	t := int64(0)
	for _, v := range c {
		t += v
	}
	return t
}

// listCount: a count of a list's rows by group key and state (sqlTaskStateCounts, sqlTaskGroups), with the group's label.
type listCount struct {
	G     string  `db:"g"`
	State string  `db:"state"`
	N     int64   `db:"n"`
	Label *string `db:"label"`
}

func taskSummary(q *Request, f rowFilter, group *string, groupCounts bool) (map[string]int64, []contract.ListGroup, error) {
	name, G, err := groupOf(group)
	if err != nil {
		return nil, nil, err
	}
	sql, p, err := sqlTaskStateCounts(f, nil)
	if err != nil {
		return nil, nil, err
	}
	rs, err := typedRows[listCount](q, sql, p)
	if err != nil {
		return nil, nil, err
	}
	byState := zeroCounts(listStates)
	for _, r := range rs {
		byState[r.State] = r.N
	}
	groups := []contract.ListGroup{}
	if name == "none" {
		return byState, groups, nil
	}
	sql, p = sqlTaskGroups(f, name, G)
	gr, err := typedRows[listCount](q, sql, p)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range gr {
		groups = append(groups, contract.ListGroup{Key: r.G, Label: r.Label, Count: r.N})
	}
	if !groupCounts || len(groups) == 0 {
		return byState, groups, nil
	}
	by := map[string]map[string]int64{}
	for _, g := range groups {
		by[g.Key] = zeroCounts(listStates)
	}
	if sql, p, err = sqlTaskStateCounts(f, &G); err != nil {
		return nil, nil, err
	}
	if rs, err = typedRows[listCount](q, sql, p); err != nil {
		return nil, nil, err
	}
	for _, r := range rs {
		if c := by[r.G]; c != nil {
			c[r.State] = r.N
		}
	}
	for i := range groups {
		groups[i].Counts = by[groups[i].Key]
	}
	return byState, groups, nil
}

// taskPage: task_page. The filter is read as sent (raw): a null projectId means work outside projects, which the typed
// list of strings cannot hold.
func taskPage(q *Request, r *contract.TaskPageRequest, raw json.RawMessage) (contract.TaskPageReply, error) {
	var out contract.TaskPageReply
	f, err := taskFilter(raw)
	if err != nil {
		return out, err
	}
	_, G, err := groupOf(r.Group)
	if err != nil {
		return out, err
	}
	S, err := taskSortOf(r.Sort)
	if err != nil {
		return out, err
	}
	limit := 100
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 100)
	}
	after, err := listAfter(r.After)
	if err != nil {
		return out, err
	}
	sql, p, err := sqlTaskPage(f, G, S, after, limit+1)
	rs, err := taskListRows(q, sql, p, err)
	if err != nil {
		return out, err
	}
	if out.Counts, out.Groups, err = taskSummary(q, f, r.Group, r.GroupCounts != nil && *r.GroupCounts); err != nil {
		return out, err
	}
	out.Tasks = rs[:min(limit, len(rs))]
	if len(rs) > limit {
		out.NextAfter = jsonBytes(out.Tasks[len(out.Tasks)-1].SortKey)
	}
	out.Total = total(out.Counts)
	return out, nil
}

func taskMembership(q *Request, f rowFilter, group, sort *string, ids []string) ([]contract.TaskRow, error) {
	_, G, err := groupOf(group)
	if err != nil {
		return nil, err
	}
	S, err := taskSortOf(sort)
	if err != nil {
		return nil, err
	}
	sql, p, err := sqlTaskRows(f, G, S, ids)
	return taskListRows(q, sql, p, err)
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
			if err := audit(q, "task:"+id, "directedBy", t.DirectedBy, p.Email, auditExtra{ProjectID: t.ProjectID}); err != nil {
				return none, err
			}
		}
		return taskDetail(q, id)
	})
	op("questions_list", func(q *Request, r *contract.QuestionsListRequest) (contract.QuestionsListReply, error) {
		if err := excludeCheck(r.ExcludeProjectIds); err != nil {
			return contract.QuestionsListReply{}, err
		}
		if r.Limit != nil && *r.Limit <= 0 {
			return contract.QuestionsListReply{}, fail(400, "Expected positive integer limit")
		}
		qs, err := questionsList(q, deref(r.TaskId), deref(r.State), deref(r.Kind), r.ProjectIds, r.ExcludeProjectIds, r.Limit)
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
			if err := audit(q, "question:"+id, "state", "open", "withdrawn", auditExtra{ProjectID: row.ProjectID, Reason: r.Reason}); err != nil {
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
			if _, err := agentReplyTo(q, &contract.ReplyRequest{QuestionId: &id, Answer: jsonBytes(answer)}); err != nil {
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
			Filter json.RawMessage `json:"filter"`
		}
		json.Unmarshal(q.body, &raw)
		return taskPage(q, r, raw.Filter)
	})
	op("task_derived_check", func(q *Request, _ *contract.TaskDerivedCheckRequest) (contract.TaskDerivedCheckReply, error) {
		n, err := q.db().TaskCount(q.ctx())
		if err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		d, err := q.db().TaskDerivedDrift(q.ctx())
		if err != nil {
			return contract.TaskDerivedCheckReply{}, err
		}
		out := contract.TaskDerivedCheckReply{Checked: n}
		return out, json.Unmarshal(d, &out.Drifted)
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
