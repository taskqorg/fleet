package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"fleet/obs"
	"github.com/jackc/pgx/v5"
)

// validTraceparent: v (a record field) is a well-formed traceparent string.
func validTraceparent(v any) bool {
	s, _ := v.(string)
	_, _, ok := obs.ParseTraceparent(s)
	return ok
}

// originTrace is persisted before asynchronous placement; retries never substitute a poll's trace.
func originTrace(q *Request) string {
	if tp := obs.Traceparent(q.ctx()); validTraceparent(tp) {
		return tp
	}
	_, s := obs.Start(q.ctx(), "execution.schedule", obs.KindInternal)
	afterCommit(q, func() { s.End(nil) })
	afterRollback(q, func() { s.End(errors.New("transaction rolled back")) })
	return "00-" + s.TraceID + "-" + s.SpanID + "-01"
}

// commandRun: the run attributes a command carries for its spans (the command's "run" field).
type commandRun struct {
	AccountID    string `json:"accountId"`
	Actor        string `json:"actor"`
	ExecutionID  string `json:"executionId"`
	ExperimentID string `json:"experimentId"`
	ProjectID    string `json:"projectId"`
	TaskID       string `json:"taskId"`
}

// commandTrace records a durable dispatch edge only once. Its parent and identity are coordinator-owned.
func commandTrace(q *Request, id, executionID, kind string, traceparent *string, run json.RawMessage) (*string, json.RawMessage, error) {
	if validTraceparent(deref(traceparent)) {
		return traceparent, run, nil
	}
	if id != "" {
		old, err := q.db().TaskTraceCommand(q.ctx(), id)
		if err == nil { // An old command remains explicitly unlinked; updating its receipt must not invent history.
			if old.Traceparent != nil && validTraceparent(*old.Traceparent) {
				r := json.RawMessage(old.Run)
				if old.Run == nil {
					r = json.RawMessage("null")
				}
				return old.Traceparent, r, nil
			}
			return traceparent, run, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
	}
	if executionID == "" {
		return traceparent, run, nil
	}
	e, err := q.db().TaskTraceExecution(q.ctx(), executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return traceparent, run, nil
	}
	if err != nil {
		return nil, nil, err
	}
	tp := e.Traceparent
	if !validTraceparent(tp) {
		tp = originTrace(q)
	}
	actor := e.Actor
	if actor == "" {
		actor = "fleet"
	}
	cr := commandRun{ProjectID: deref(e.ProjectID), TaskID: deref(e.TaskID), ExecutionID: executionID, AccountID: deref(e.AccountID), ExperimentID: deref(e.ExperimentID), Actor: actor}
	ctx := obs.WithRun(obs.WithRemote(context.Background(), tp), obs.Run{ProjectID: cr.ProjectID, TaskID: cr.TaskID, ExecutionID: executionID, AccountID: cr.AccountID, ExperimentID: cr.ExperimentID, Actor: actor})
	ctx, s := obs.Start(ctx, "dispatch.command", obs.KindInternal)
	s.Set("fleet.command.kind", kind)
	newTP := obs.Traceparent(ctx)
	afterCommit(q, func() { s.End(nil) })
	afterRollback(q, func() { s.End(errors.New("transaction rolled back")) })
	return &newTP, jsonBytes(cr), nil
}

// executionOrigin follows a durable parent only for detached work without an incoming span.
func executionOrigin(q *Request, parent string) (string, error) {
	if tp := obs.Traceparent(q.ctx()); validTraceparent(tp) {
		return tp, nil
	}
	if parent != "" {
		tp, err := q.db().TaskTraceOf(q.ctx(), parent)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if validTraceparent(tp) {
			return tp, nil
		}
	}
	return originTrace(q), nil
}

// executionTraceData: an execution's data (a JSON object, or none) with its trace origin and actor.
// parent is the execution it follows (a parent, a pipeline step or run).
func executionTraceData(q *Request, parent string, data json.RawMessage) (rawObject, error) {
	out := rawObject{}
	var d rawObject
	if json.Unmarshal(data, &d) == nil { // a non-object is no data
		maps.Copy(out, d)
	}
	tp, err := executionOrigin(q, parent)
	if err != nil {
		return nil, err
	}
	out["traceparent"], out["traceActor"] = jsonBytes(tp), jsonBytes(actor(q, "fleet"))
	return out, nil
}
