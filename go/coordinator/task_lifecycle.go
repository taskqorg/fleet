package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const (
	taskWakeQueue  = "task-lifecycle"
	taskWakeCohort = 48
	recoveryLimit  = 2
	recoveryWindow = 90 * time.Minute

	recoveryContinuationBlocked = "continuation_blocked"
)

func init() {
	kitRegister(&kitQueue{
		Name: taskWakeQueue, Period: 5 * time.Second, Limit: taskWakeCohort,
		Due: taskWakesDue, Step: applyTaskWake,
		Retry: kitRetry{
			Table: "task_wake", Key: "id", Guard: "disposition IS NULL AND revision=$5 AND attempts=$2-1",
			Class: func(error) string { return "task wake reconciliation failed" },
		},
	})
	onStart(func(ctx context.Context, a *App) {
		everyWake(ctx, "task-lifecycle-audit", time.Minute, nil, true, func(ctx context.Context, _ bool) error {
			return errors.Join(auditAutomaticDeadlines(ctx, a), auditTaskLifecycle(ctx, a))
		})
	})
}

func taskWakesDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().TaskWakeDue(q.ctx(), db.TaskWakeDueParams{DueAt: now, Limit: int32(limit)})
	var claims []*kitClaim
	for _, r := range rows {
		claims = append(claims, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Revision}})
	}
	return claims, err
}

func wakeTask(q *Request, id string) error {
	if err := q.db().TaskWakeDirty(q.ctx(), id); err != nil {
		return err
	}
	if err := kitSchedule(q, taskWakeQueue, "task:"+id, time.Now()); err != nil {
		return err
	}
	return kitNotify(q, taskWakeQueue)
}

func wakeBlocker(q *Request, id string) error {
	if err := q.db().TaskWakeBlocker(q.ctx(), id); err != nil {
		return err
	}
	if err := kitSchedule(q, taskWakeQueue, "blocker:"+id, time.Now()); err != nil {
		return err
	}
	return kitNotify(q, taskWakeQueue)
}

func auditAutomaticDeadlines(ctx context.Context, a *App) error {
	now := time.Now()
	var ids []string
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		ids, err = q.db().TaskAutomaticOverdue(q.ctx(), taskWakeCohort)
		return err
	}); err != nil {
		return err
	}
	var stops error
	for _, id := range ids {
		stops = errors.Join(stops, a.store.Write(ctx, nil, []string{GLOBAL, DISPATCH}, func(q *Request) error {
			return stopAutomaticTurn(q, now, id, now, false)
		}))
	}
	return stops
}

// auditTaskLifecycle starts from source facts and repairs missing custody. The work kit still owns admission.
func auditTaskLifecycle(ctx context.Context, a *App) error {
	var answers, tasks []string
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		var err error
		answers, err = q.db().TaskWakeAuditAnswers(q.ctx(), taskWakeCohort)
		if err != nil {
			return err
		}
		tasks, err = q.db().TaskWakeAuditTasks(q.ctx(), taskWakeCohort)
		return err
	}); err != nil {
		return err
	}
	if len(answers)+len(tasks) == 0 {
		return nil
	}
	log.Printf("task lifecycle audit: missing wakes for %d answers and %d tasks (bounded scan)", len(answers), len(tasks))
	var failed error
	if len(tasks) > 0 {
		failed = a.store.Write(ctx, nil, []string{GLOBAL}, restoreMissedTaskDeliveries)
	}
	for _, id := range answers {
		failed = errors.Join(failed, a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			if err := q.db().TaskWakeAnswer(q.ctx(), id); err != nil {
				return err
			}
			return kitNotify(q, taskWakeQueue)
		}))
	}
	for _, id := range tasks {
		failed = errors.Join(failed, a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			return wakeTask(q, id)
		}))
	}
	return failed
}

func applyTaskWake(q *Request, now time.Time, c *kitClaim) error {
	if err := kitLock(q, GLOBAL, DISPATCH); err != nil {
		return err
	}
	w, err := q.db().TaskWakeGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return errKitSkip
	}
	if err != nil {
		return err
	}
	if w.Disposition != nil || w.NextAttemptAt.After(now) || w.Revision != c.Guard[0] {
		return errKitSkip
	}
	if int(w.Attempts) >= kitFailingAttempts() && w.TaskID != nil {
		t, err := taskLoad(q, *w.TaskID)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		if err == nil && t.State == db.TaskStatePaused {
			return kitSchedule(q, taskWakeQueue, w.ID, now.Add(time.Minute))
		}
		if err == nil && openTask[string(t.State)] {
			if err := askTaskRecovery(q, t, "", "", "Fleet could not reconcile this task after repeated failures.", "wake_failed"); err != nil {
				return err
			}
		}
		return q.db().TaskWakeFinish(q.ctx(), db.TaskWakeFinishParams{ID: w.ID, Disposition: ptr("reconciliation exhausted")})
	}
	var disposition string
	var due time.Time
	switch w.Kind {
	case "blocker":
		disposition, due, err = wakeDependents(q, now, w)
	case "reply":
		disposition, due, err = deliverTaskReply(q, now, w)
	case "task":
		disposition, due, err = decideTaskAction(q, now, w)
	}
	if err != nil {
		return err
	}
	if !due.IsZero() {
		return kitSchedule(q, taskWakeQueue, w.ID, due)
	}
	return q.db().TaskWakeFinish(q.ctx(), db.TaskWakeFinishParams{ID: w.ID, Disposition: &disposition})
}

func wakeDependents(q *Request, now time.Time, w db.TaskWake) (string, time.Time, error) {
	ids, err := q.db().TaskWakeDependents(q.ctx(), db.TaskWakeDependentsParams{
		Blocker: w.SourceID, AfterID: deref(w.CursorID), Limit: taskWakeCohort,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	for _, id := range ids {
		if err := wakeTask(q, id); err != nil {
			return "", time.Time{}, err
		}
	}
	if len(ids) < taskWakeCohort {
		return "dependents scheduled", time.Time{}, nil
	}
	return "", now, q.db().TaskWakeAdvance(q.ctx(), db.TaskWakeAdvanceParams{ID: w.ID, CursorID: ptr(ids[len(ids)-1])})
}

func deliverTaskReply(q *Request, now time.Time, w db.TaskWake) (string, time.Time, error) {
	question, err := q.db().TaskQuestion(q.ctx(), w.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "question unavailable", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if question.State != db.QuestionStateAnswered {
		return "question no longer answered", time.Time{}, nil
	}
	sid := deref(question.SessionID)
	if err := kitLock(q, "session:"+sid); err != nil {
		return "", time.Time{}, err
	}
	receipt, conflict, err := questionReplyReceipt(q, sid, w.ID, questionReplyText(question.ID, question.Text, deref(question.Answer)))
	if err != nil || receipt != "" {
		return receipt, time.Time{}, err
	}
	s, err := sessionMaybe(q, sid)
	if err != nil {
		return "", time.Time{}, err
	}
	taskID := deref(question.TaskID)
	if s != nil {
		// A side conversation may have asked on the task's behalf. It resumes in that conversation.
		taskID = firstString(taskID, s.DeliveryID, s.TaskID)
	}
	t := db.Task{ProjectID: question.ProjectID}
	if taskID != "" {
		t, err = taskLoad(q, taskID)
		if errors.Is(err, errNotFound) || err == nil && !openTask[string(t.State)] {
			return "task closed", time.Time{}, nil
		}
		if err != nil {
			return "", time.Time{}, err
		}
		if t.State == db.TaskStatePaused {
			return "", now.Add(time.Minute), nil
		}
		if reason, err := waitReason(q, taskID); err != nil || reason != "" {
			return "", now.Add(5 * time.Second), err
		}
	}
	if conflict {
		return "reply receipt needs recovery", time.Time{}, askTaskRecovery(q, t, deref(question.ExecutionID), sid,
			"The answer is retained, but an incompatible or cancelled message occupies its delivery receipt.", "reply")
	}
	if s == nil || !taskSessionAuthorized(q, s) ||
		(t.ID != "" && deref(t.ProjectID) != s.ProjectID) ||
		firstString(deref(question.ProjectID), s.ProjectID) != s.ProjectID ||
		firstString(s.DeliveryID, s.TaskID, taskID) != taskID {
		return "asking session requires recovery", time.Time{}, askTaskRecovery(q, t, "", "",
			"The answer is retained, but the asking session is unavailable or its authority changed.", "reply")
	}
	asking, err := executionMaybe(q, deref(question.ExecutionID))
	if err != nil {
		return "", time.Time{}, err
	}
	if asking == nil || asking.SessionID != sid {
		return "asking execution unavailable", time.Time{}, askTaskRecovery(q, t, deref(question.ExecutionID), sid,
			"The answer is retained, but Fleet cannot prove ownership of the asking execution.", "reply")
	}
	if taskID != "" && firstString(s.DeliveryID, s.TaskID) != taskID {
		pending, err := q.db().TaskHasPendingWork(q.ctx(), &taskID)
		if err != nil || pending {
			return "", now.Add(5 * time.Second), err
		}
	}
	if asking.unconfirmed() && !now.Before(w.CreatedAt.Add(recoveryWindow)) {
		return "asking turn ownership requires recovery", time.Time{}, askTaskRecovery(q, t, asking.ID, sid,
			"The answer is retained and the asking execution's closure is still unconfirmed.", "reply")
	}
	busy, err := taskSessionBusy(q, sid)
	if err != nil || busy || !asking.terminal() || asking.unconfirmed() {
		return "", now.Add(5 * time.Second), err
	}
	_, err = submitQuestionReply(q, sid, question.ID, question.Text, deref(question.Answer))
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Code < 500 {
			return "reply requires recovery", time.Time{}, askTaskRecovery(q, t, asking.ID, sid, he.Msg, "reply")
		}
		return "", time.Time{}, err
	}
	if t.ID != "" {
		err = wakeTask(q, t.ID)
	}
	return "ordinary message admitted", time.Time{}, err
}

func questionReplyReceipt(q *Request, sid, requestID, text string) (string, bool, error) {
	sent, err := messagesBySession(q, sid, &requestID)
	if err != nil {
		return "", false, err
	}
	if len(sent) > 0 {
		if sent[0].Text != text {
			return "", true, nil
		}
		return "ordinary message admitted", false, nil
	}
	queued, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, RequestID: &requestID})
	if err != nil || len(queued) == 0 {
		return "", false, err
	}
	if queued[0].Text != text || queued[0].State == "cancelled" {
		return "", true, nil
	}
	return "ordinary message recorded", false, nil
}

func taskSessionAuthorized(q *Request, s *agentSession) bool {
	return s.ArchivedAt == nil && s.DirectorAccessRevokedAt == nil &&
		mayAccessProjectID(q.peopleSnapshot().person(s.DirectedBy), s.ProjectID)
}

func taskSessionBusy(q *Request, sid string) (bool, error) {
	if active, err := agentActive(q, sid); err != nil || active != nil {
		return active != nil, err
	}
	for _, state := range []string{"steering", "queued"} {
		rows, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, State: &state})
		if err != nil || len(rows) > 0 {
			return len(rows) > 0, err
		}
	}
	return false, nil
}

func decideTaskAction(q *Request, now time.Time, w db.TaskWake) (string, time.Time, error) {
	t, err := taskLoad(q, w.SourceID)
	if errors.Is(err, errNotFound) || err == nil && !openTask[string(t.State)] {
		return "task closed", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	recovery, err := q.db().TaskRecoveryGet(q.ctx(), t.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, err
	}
	proof, err := q.db().TaskDeliveryProof(q.ctx(), t.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, err
	}
	if err := withdrawResolvedTaskRecovery(q, t, proof); err != nil {
		return "", time.Time{}, err
	}
	blocked, err := waitReason(q, t.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	deadlineWake, err := reconcileAutomaticTurns(q, now, t, recovery, proof)
	if err != nil {
		return "", time.Time{}, err
	}
	if t.State == db.TaskStatePaused {
		return "operator pause", deadlineWake, nil
	}
	if blocked == "dependency" {
		unresolvable, err := q.db().TaskUnresolvableDependency(q.ctx(), t.ID)
		if err != nil {
			return "", time.Time{}, err
		}
		if unresolvable {
			return "dependency decision", deadlineWake, askTaskRecovery(q, t, "", "",
				"A required task failed, was cancelled or is unavailable. Resolve the dependency or close this task.", "dependency")
		}
	}
	if blocked != "" {
		_, err := settleTask(q, t.ID, "")
		return blocked, deadlineWake, err
	}
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &t.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	if pending {
		return "", now.Add(15 * time.Second), nil
	}
	latest, err := q.db().TaskAcceptanceExecution(q.ctx(), db.TaskAcceptanceExecutionParams{TaskID: &t.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, err
	}
	e, err := executionMaybe(q, latest.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	if e != nil {
		e.DirectedBy = deref(latest.DirectedBy)
	}
	reply, err := q.db().TaskWakePendingReply(q.ctx(), db.TaskWakePendingReplyParams{TaskID: &t.ID, SessionID: latest.SessionID})
	if err != nil || reply {
		return "", now.Add(5 * time.Second), err
	}
	if proof.TaskID != "" {
		return decideDeliveredTask(q, now, t, e, proof)
	}
	stage, err := taskDeliveryStages(q, []string{t.ID})
	if err != nil {
		return "", time.Time{}, err
	}
	switch stage[t.ID] {
	case contract.DeliveryStagePrFailed, contract.DeliveryStageMqFailed:
		return "blocked delivery decision", time.Time{}, deliveredTaskDecision(q, t, e, "Delivery checks failed. Inspect the existing pull request before authorizing corrective work.")
	case "":
	default:
		return "", now.Add(time.Minute), nil
	}
	opened, err := settleIdleTask(q, t.ID)
	if err != nil || opened {
		return "result or decision offered", time.Time{}, err
	}
	if latest.Kind != db.ExecutionKindAgentRun || latest.State != db.ExecutionStateSucceeded ||
		latest.Observation != db.ObservationConfirmed || latest.Outcome == "failed" || e == nil {
		wakeDispatchAfter(q)
		return "existing task lifecycle", time.Time{}, nil
	}
	open, err := q.db().TaskOpenAcceptances(q.ctx(), &t.ID)
	if err != nil || len(open) > 0 {
		return "acceptance pending", time.Time{}, err
	}
	if latest.Outcome == "succeeded" {
		return "result requires recovery", time.Time{}, askTaskRecovery(q, t, e.ID, e.SessionID,
			"The worker completed its report, but Fleet cannot offer the result under the current acceptance policy.", "result")
	}
	rejections, err := q.db().TaskDeliveryRejections(q.ctx(), &t.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	if slices.ContainsFunc(rejections, func(r db.TaskDeliveryRejectionsRow) bool { return r.ExecutionID == e.ID }) {
		return "result rejected", time.Time{}, askTaskRecovery(q, t, e.ID, e.SessionID,
			"The latest result was rejected and needs an explicit decision.", "result")
	}
	return recoverMissingTaskResult(q, now, t, e, recovery)
}

func recoverMissingTaskResult(q *Request, now time.Time, t db.Task, e *agentExecution, r db.TaskRecovery) (string, time.Time, error) {
	s, err := taskContinuationSession(q, t, e)
	if err != nil {
		return blockedResultContinuation(q, t, e, err)
	}
	if r.TaskID == "" {
		// The episode begins when the task is eligible. A dependency or operator hold cannot spend its first slot's time.
		if err := q.db().TaskRecoveryStart(q.ctx(), db.TaskRecoveryStartParams{
			TaskID: t.ID, ProjectID: t.ProjectID, Deadline: now.Add(recoveryWindow),
		}); err != nil {
			return "", time.Time{}, err
		}
		r, err = q.db().TaskRecoveryGet(q.ctx(), t.ID)
		if err != nil {
			return "", time.Time{}, err
		}
	}
	progress, err := q.db().TaskRecoveryProgress(q.ctx(), &t.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	if reason := recoveryStopReason(now, r, progress); reason != "" {
		return "recovery decision", time.Time{}, askTaskRecovery(q, t, e.ID, e.SessionID, reason, "missing_result")
	}
	input := fmt.Sprintf("Fleet recovery for task %s. Turn %s ended without a completion report. Resume the existing task and worktree from retained work. Read back any uncertain external action before attempting it again. Finish outstanding acceptance checks and record the result with worker_done. Automatic recovery turn %d of %d.",
		firstString(deref(t.Code), t.Title), e.ID, r.Turns+1, recoveryLimit)
	continued, err := submitTaskContinuation(q, s, fmt.Sprintf("recover:%s:%d:%d", t.ID, r.Generation, r.Turns+1), input)
	if err != nil {
		return blockedResultContinuation(q, t, e, err)
	}
	if continued == nil {
		return "", now.Add(5 * time.Second), nil
	}
	err = q.db().TaskRecoveryAdmit(q.ctx(), db.TaskRecoveryAdmitParams{TaskID: t.ID, ExecutionID: &continued.ID, Progress: progress})
	return "", now.Add(5 * time.Second), err
}

func blockedResultContinuation(q *Request, t db.Task, e *agentExecution, err error) (string, time.Time, error) {
	var he *HTTPError
	if errors.As(err, &he) && he.Code < 500 {
		// The old execution remains the evidence link. The old session is not a recovery target.
		return "continuation prerequisite", time.Time{}, askTaskRecovery(q, t, e.ID, "", he.Msg, recoveryContinuationBlocked)
	}
	return "", time.Time{}, err
}

func recoveryStopReason(now time.Time, r db.TaskRecovery, progress []string) string {
	switch {
	case !now.Before(r.Deadline):
		return "Fleet reached the 90-minute automatic recovery deadline."
	case r.Turns >= recoveryLimit:
		return "Fleet used both automatic recovery turns without a complete result."
	case r.Turns > 0 && !slices.ContainsFunc(progress, func(s string) bool { return !slices.Contains(r.Progress, s) }):
		return "The automatic recovery turn ended without new verified progress."
	}
	return ""
}

func taskAutomaticStartAllowed(q *Request, executionID string) (bool, time.Time, error) {
	check, err := q.db().TaskAutomaticStartAllowed(q.ctx(), executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, time.Time{}, nil
	}
	return check.Allowed, check.Deadline, err
}

func reconcileAutomaticTurns(q *Request, now time.Time, t db.Task, r db.TaskRecovery, proof db.TaskDeliveryResult) (time.Time, error) {
	checks, err := q.db().TaskDeliveryOwnedChecks(q.ctx(), t.ID)
	if err != nil {
		return time.Time{}, err
	}
	if r.ExecutionID != nil {
		e, err := executionMaybe(q, *r.ExecutionID)
		if err != nil {
			return time.Time{}, err
		}
		if e != nil && !e.terminal() {
			checks = append(checks, db.TaskDeliveryOwnedChecksRow{ExecutionID: e.ID, Deadline: r.Deadline})
		}
	}
	var due time.Time
	for _, check := range checks {
		superseded := proof.TaskID != "" && (check.VerificationKey == "" || check.VerificationKey != proof.VerificationKey)
		if err := stopAutomaticTurn(q, now, check.ExecutionID, check.Deadline, superseded); err != nil {
			return time.Time{}, err
		}
		if now.Before(check.Deadline) && (due.IsZero() || check.Deadline.Before(due)) {
			due = check.Deadline
		}
	}
	return due, nil
}

func stopAutomaticTurn(q *Request, now time.Time, id string, deadline time.Time, superseded bool) error {
	expired := !now.Before(deadline)
	if !expired && !superseded {
		return nil
	}
	if err := lockExecutionTask(q, id); err != nil {
		return err
	}
	if err := q.db().DispExecLock(q.ctx(), id); err != nil {
		return err
	}
	e, err := executionMaybe(q, id)
	if err != nil || e == nil || e.terminal() || isTrue(e.StopRequested) {
		return err
	}
	if !expired {
		if e.State != "preparing" {
			return nil
		}
		if e.HostID != nil {
			neverHandedOut, err := q.db().DispStartNeverHandedOut(q.ctx(), &id)
			if err != nil || !neverHandedOut {
				return err
			}
		}
	}
	reason := "superseded"
	if expired {
		reason = "deadline"
	}
	_, err = requestExecutionStopWithPolicy(q, id, false, reason)
	return err
}

func admitTaskContinuation(q *Request, t db.Task, e *agentExecution, requestID, input string) (*agentExecution, error) {
	s, err := taskContinuationSession(q, t, e)
	if err != nil {
		return nil, err
	}
	return submitTaskContinuation(q, s, requestID, input)
}

func taskContinuationSession(q *Request, t db.Task, e *agentExecution) (*agentSession, error) {
	if e == nil || e.SessionID == "" || !e.terminal() || e.unconfirmed() {
		return nil, fail(409, "Fleet cannot prove ownership has ended for the previous task session.")
	}
	if err := kitLock(q, "session:"+e.SessionID); err != nil {
		return nil, err
	}
	s, err := sessionMaybe(q, e.SessionID)
	if err != nil {
		return nil, err
	}
	if s == nil || s.ArchivedAt != nil {
		return nil, fail(409, "The previous task session is missing or archived.")
	}
	if e.DirectedBy != s.DirectedBy {
		return nil, fail(409, "The previous run and session have different directing actors.")
	}
	if !taskSessionAuthorized(q, s) {
		return nil, fail(409, "The previous session's director lacks current authority for the project.")
	}
	if s.TaskID != t.ID || s.DeliveryID != "" && s.DeliveryID != t.ID ||
		s.ProjectID != deref(t.ProjectID) || s.PipelineRunID != "" || s.BotID != "" {
		return nil, fail(409, "The previous session's task or project scope cannot authorize this continuation.")
	}
	return s, nil
}

func submitTaskContinuation(q *Request, s *agentSession, requestID, input string) (*agentExecution, error) {
	if busy, err := taskSessionBusy(q, s.ID); err != nil || busy {
		return nil, err
	}
	d, err := agentSubmit(q, &contract.MessageSendRequest{SessionId: &s.ID, RequestId: &requestID, Text: &input}, "wake")
	return d.execution, err
}

func askTaskRecovery(q *Request, t db.Task, eid, sid, reason, kind string) error {
	if t.ID == "" {
		_, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", SessionID: strPtr(sid), ProjectID: deref(t.ProjectID), DirectToOwner: true, AskedBy: ptr("fleet"),
			Text: reason + " Review the conversation and open a new turn when it is safe."})
		return err
	}
	if _, err := q.db().TaskRecoveryDecision(q.ctx(), db.TaskRecoveryDecisionParams{TaskID: &t.ID, Cause: kind}); !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	text := reason + " Review retained work before resuming in the task conversation, or choose “fail task”."
	if kind == "dependency" {
		text = reason + " Choose “fail task” to close without a result."
	}
	options := []string{idleFail}
	if kind == "missing_result" {
		options = append(options, idleRetryResult)
		text = reason + " Choose “retry result” to authorize a new bounded recovery, or “fail task” to close without a result."
	}
	if kind == recoveryContinuationBlocked {
		text = reason + " Have an authorized task operator continue in a session linked to this task with the required project access, or choose “fail task”."
		if _, err := retainedRecoveryResult(q, t.ID, eid); err == nil {
			options = append(options, idleOfferRetainedResult)
			text += " If the retained result satisfies all task checks, choose “offer retained result” to send it for acceptance."
		} else {
			var conflict *HTTPError
			if !errors.As(err, &conflict) || conflict.Code != 409 {
				return err
			}
		}
	}
	_, err := askQuestionAs(q, questionAsk{
		Kind: "operator_decision", TaskID: t.ID, ProjectID: deref(t.ProjectID), SessionID: strPtr(sid),
		ExecutionID: strPtr(eid), AskedBy: ptr("fleet"), DirectToOwner: true, Text: text,
		Options: jsonBytes(options), Data: jsonBytes(questionData{Idle: true, TaskRecovery: kind}),
	})
	return err
}

func retainedRecoveryResult(q *Request, taskID, executionID string) (taskAcceptanceResult, error) {
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &taskID)
	if err != nil {
		return taskAcceptanceResult{}, err
	}
	result, err := eligibleTaskResult(q, taskID, false, pending, false, nil)
	if err != nil {
		return taskAcceptanceResult{}, err
	}
	if result.id != executionID || len(result.artifacts) == 0 {
		return taskAcceptanceResult{}, fail(409, "The decision no longer matches an eligible retained result")
	}
	return result, nil
}

func taskRecoveryOperator(q *Request, id string) (db.Task, error) {
	t, err := taskLoad(q, id)
	if err != nil {
		return t, err
	}
	if q.person == nil || q.agent != "" || !mayAccessProjectID(q.person, deref(t.ProjectID)) ||
		(!fullAccess(q.person) && q.person.Email != deref(t.DirectedBy)) {
		return t, fail(403, "Only the task director or Fleet owner can authorize result recovery")
	}
	if t.State == db.TaskStatePaused || !openTask[string(t.State)] {
		return t, fail(409, "The task must be open and unpaused")
	}
	return t, nil
}

func authorizeTaskResultRetry(q *Request, id string) error {
	t, err := taskRecoveryOperator(q, id)
	if err != nil {
		return err
	}
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &id)
	if err != nil {
		return err
	}
	if pending {
		return fail(409, "The task still has pending work")
	}
	if err := q.db().TaskRecoveryReauthorize(q.ctx(), db.TaskRecoveryReauthorizeParams{TaskID: id, Deadline: time.Now().Add(recoveryWindow)}); err != nil {
		return err
	}
	return audit(q, "task:"+id, "recovery_reauthorized", nil, nil, auditExtra{ProjectID: t.ProjectID})
}

func withdrawResolvedTaskRecovery(q *Request, t db.Task, proof db.TaskDeliveryResult) error {
	decisions, err := q.db().TaskGeneratedRecoveryDecisions(q.ctx(), &t.ID)
	if err != nil || len(decisions) == 0 {
		return err
	}
	if proof.TaskID == "" {
		requirement, err := taskLiveCheck(t)
		if err != nil || requirement != "" {
			return nil
		}
		if _, err := acceptanceResult(q, t.ID, false, false, true); err != nil {
			var conflict *HTTPError
			if errors.As(err, &conflict) && conflict.Code == 409 {
				return nil
			}
			return err
		}
		var cleared []string
		for _, decision := range decisions {
			if decision.Cause == "missing_result" || decision.Cause == "result" || decision.Cause == recoveryContinuationBlocked {
				cleared = append(cleared, decision.ID)
			}
		}
		return questionsWithdraw(q, cleared)
	}
	pr, err := retainedDelivery(proof)
	if err != nil {
		return err
	}
	switch pr.Stage {
	case "closed", "ambiguous", string(contract.DeliveryStagePrFailed), string(contract.DeliveryStageMqFailed):
		return nil
	}
	var resolved []string
	for _, decision := range decisions {
		switch decision.Cause {
		case "missing_result", "result", recoveryContinuationBlocked:
			resolved = append(resolved, decision.ID)
		case "delivery":
			if !pr.Delivered {
				resolved = append(resolved, decision.ID)
				continue
			}
			requirement, err := taskLiveCheck(t)
			if err != nil {
				continue
			}
			verified, err := taskDeliveryVerified(q, t, requirement, pr)
			if err != nil {
				return err
			}
			if !verified {
				_, err := q.db().TaskDeliveryVerificationGet(q.ctx(), proof.VerificationKey)
				if errors.Is(err, pgx.ErrNoRows) && proof.VerificationKey == deliveryCheckKey(t.ID, requirement, pr) {
					resolved = append(resolved, decision.ID)
				} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				continue
			}
			selected, err := acceptanceResult(q, t.ID, false, false, !deliverySuppliesResult(t, pr))
			if err != nil {
				var conflict *HTTPError
				if errors.As(err, &conflict) && conflict.Code == 409 {
					continue
				}
				return err
			}
			rejected, err := deliveryRejectedEarlier(q, t.ID, selected.id, pr)
			if err != nil {
				return err
			}
			if !rejected {
				resolved = append(resolved, decision.ID)
			}
		}
	}
	return questionsWithdraw(q, resolved)
}

func validDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil && value == strings.ToLower(value)
}

// taskNextAction projects current ownership from the same retained facts used for admission.
func taskNextAction(q *Request, t db.Task, detail contract.TaskDetail) (*contract.TaskNextAction, error) {
	makeAction := func(kind contract.TaskNextActionKind, owner, ref, release string) *contract.TaskNextAction {
		return &contract.TaskNextAction{Kind: kind, Owner: owner, Reference: ref, ReleaseCondition: release}
	}
	director := firstString(deref(t.DirectedBy), ownerEmail(q))
	if !openTask[string(t.State)] {
		return nil, nil
	}
	if t.State == db.TaskStatePaused {
		return makeAction(contract.TaskNextActionKindPause, firstString(deref(t.HeldBy), director), t.ID, "An authorized operator resumes the task."), nil
	}
	for _, e := range detail.Executions {
		if e.Kind != "child_agent" && !executionEnds(string(e.State)) {
			return makeAction(contract.TaskNextActionKindRun, "coordinator", e.Id, "The active execution is placed or its result is observed."), nil
		}
	}
	if id, err := q.db().TaskFirstPendingMessage(q.ctx(), &t.ID); err == nil {
		return makeAction(contract.TaskNextActionKindRun, "coordinator", id, "The admitted message is promoted."), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var offer *contract.TaskNextAction
	for _, question := range detail.Questions {
		if question.State != "open" {
			continue
		}
		holder := firstString(deref(question.AddressedTo), director)
		switch question.Kind {
		case "agent_question", "operator_decision", "secret":
			return makeAction(contract.TaskNextActionKindQuestion, holder, question.Id, "The addressee answers or an authorized operator withdraws the question."), nil
		case "acceptance":
			offer = makeAction(contract.TaskNextActionKindAcceptance, holder, question.Id, "The reviewer accepts or rejects the offered result.")
		}
	}
	if id, err := q.db().TaskDependencyPending(q.ctx(), t.ID); err == nil {
		return makeAction(contract.TaskNextActionKindDependency, director, id, "The required task completes or an authorized operator resolves the dependency."), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if offer != nil {
		return offer, nil
	}
	wake, wakeErr := q.db().TaskWakeForAction(q.ctx(), &t.ID)
	if wakeErr != nil && !errors.Is(wakeErr, pgx.ErrNoRows) {
		return nil, wakeErr
	}
	if proof, err := q.db().TaskDeliveryProof(q.ctx(), t.ID); err == nil {
		pr, err := retainedDelivery(proof)
		if err != nil {
			return nil, err
		}
		action := makeAction(contract.TaskNextActionKindDelivery, "coordinator", pr.URL, "The required delivery and live verification are confirmed.")
		return action, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if wake.ID != "" {
		action := makeAction(contract.TaskNextActionKindReconcile, "coordinator", wake.ID, "Fleet reconciles the retained fact against current work.")
		action.Attempts = ptr(int64(wake.Attempts))
		action.NextAttemptAt = ptr(wake.NextAttemptAt.UTC().Format(time.RFC3339))
		action.Error = wake.LastError
		return action, nil
	}
	if t.State == db.TaskStateQueued {
		return makeAction(contract.TaskNextActionKindDispatch, "coordinator", t.ID, "A suitable worker and its prerequisites become available."), nil
	}
	return makeAction(contract.TaskNextActionKindUnknown, "coordinator", t.ID, "The lifecycle auditor confirms or repairs the next action."), nil
}

func init() {
	op("task_lifecycle_check", func(q *Request, _ *contract.TaskLifecycleCheckRequest) (contract.TaskLifecycleCheckReply, error) {
		var result contract.TaskLifecycleCheckReply
		health, err := q.db().TaskWakeHealth(q.ctx())
		if err != nil {
			return result, err
		}
		tasks, err := q.db().TaskWakeAuditTasks(q.ctx(), taskWakeCohort)
		if err != nil {
			return result, err
		}
		answers, err := q.db().TaskWakeAuditAnswers(q.ctx(), taskWakeCohort)
		if err != nil {
			return result, err
		}
		result.ObservedAt = time.Now().UTC().Format(time.RFC3339)
		result.Pending, result.Failing = health.Pending, health.Failing
		if health.OldestDueAt.Valid {
			result.OldestDueAt = ptr(health.OldestDueAt.Time.UTC().Format(time.RFC3339))
		}
		result.SampleLimit = taskWakeCohort
		result.MissingTaskWakes, result.MissingReplyWakes = int64(len(tasks)), int64(len(answers))
		return result, nil
	})
}
