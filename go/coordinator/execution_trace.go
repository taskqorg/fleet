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
	onEnd(q.tx, func(committed bool) {
		if committed {
			s.End(nil)
		} else {
			s.End(errors.New("transaction rolled back"))
		}
	})
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
func commandTrace(q *Request, c rawObject) error {
	if validTraceparent(c.text("traceparent")) {
		return nil
	}
	if id := c.text("id"); id != "" {
		old, err := q.db().TaskTraceCommand(q.ctx(), id)
		if err == nil { // An old command remains explicitly unlinked; updating its receipt must not invent history.
			if old.Traceparent != nil && validTraceparent(*old.Traceparent) {
				c["traceparent"], c["run"] = jsonBytes(*old.Traceparent), json.RawMessage(old.Run)
				if old.Run == nil {
					c["run"] = json.RawMessage("null")
				}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	eid := c.text("executionId")
	if eid == "" {
		return nil
	}
	e, err := q.db().TaskTraceExecution(q.ctx(), eid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tp := e.Traceparent
	if !validTraceparent(tp) {
		tp = originTrace(q)
	}
	actor := e.Actor
	if actor == "" {
		actor = "fleet"
	}
	run := commandRun{ProjectID: deref(e.ProjectID), TaskID: deref(e.TaskID), ExecutionID: eid, AccountID: deref(e.AccountID), ExperimentID: deref(e.ExperimentID), Actor: actor}
	ctx := obs.WithRun(obs.WithRemote(context.Background(), tp), obs.Run{ProjectID: run.ProjectID, TaskID: run.TaskID, ExecutionID: eid, AccountID: run.AccountID, ExperimentID: run.ExperimentID, Actor: actor})
	ctx, s := obs.Start(ctx, "dispatch.command", obs.KindInternal)
	s.Set("fleet.command.kind", c.text("kind"))
	c["traceparent"], c["run"] = jsonBytes(obs.Traceparent(ctx)), jsonBytes(run)
	onEnd(q.tx, func(committed bool) {
		if committed {
			s.End(nil)
		} else {
			s.End(errors.New("transaction rolled back"))
		}
	})
	return nil
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
