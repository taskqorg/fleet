package main

import (
	"context"
	"errors"
	"fleet/obs"
	"github.com/jackc/pgx/v5"
)

func validTraceparent(v any) bool { _, _, ok := obs.ParseTraceparent(astr(v)); return ok }

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

// commandTrace records a durable dispatch edge only once. Its parent and identity are coordinator-owned.
func commandTrace(q *Request, c doc) error {
	if validTraceparent(c["traceparent"]) {
		return nil
	}
	old, err := maybe(q, c["id"], "command")
	if err != nil {
		return err
	}
	if old != nil { // An old command remains explicitly unlinked; updating its receipt must not invent history.
		if validTraceparent(old["traceparent"]) {
			c["traceparent"], c["run"] = old["traceparent"], old["run"]
		}
		return nil
	}
	e, err := maybe(q, c["executionId"], "execution")
	if err != nil {
		return err
	}
	if e == nil {
		return nil
	}
	tp := astr(e["traceparent"])
	if !validTraceparent(tp) {
		tp = originTrace(q)
	}
	var pid, tid, aid, experiment *string
	if err := q.tx.QueryRow(q.ctx(), "SELECT project_id,task_id,account_id,experiment_id FROM execution WHERE id=$1", c["executionId"]).Scan(&pid, &tid, &aid, &experiment); err != nil {
		return err
	}
	val := func(p *string) string {
		if p != nil {
			return *p
		}
		return ""
	}
	run := doc{"projectId": val(pid), "taskId": val(tid), "executionId": astr(c["executionId"]), "accountId": val(aid), "experimentId": val(experiment), "actor": firstString(e["traceActor"], e["directedBy"], "fleet")}
	ctx := obs.WithRun(obs.WithRemote(context.Background(), tp), obs.Run{ProjectID: val(pid), TaskID: val(tid), ExecutionID: astr(c["executionId"]), AccountID: val(aid), ExperimentID: val(experiment), Actor: astr(run["actor"])})
	ctx, s := obs.Start(ctx, "dispatch.command", obs.KindInternal)
	s.Set("fleet.command.kind", astr(c["kind"]))
	c["traceparent"], c["run"] = obs.Traceparent(ctx), run
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
		var tp *string
		if err := q.tx.QueryRow(q.ctx(), "SELECT data->>'traceparent' FROM execution WHERE id=$1", parent).Scan(&tp); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if tp != nil && validTraceparent(*tp) {
			return *tp, nil
		}
	}
	return originTrace(q), nil
}

func executionTraceData(q *Request, v doc, data any) (doc, error) {
	out := doc{}
	for k, x := range mapOf(data) {
		out[k] = x
	}
	tp, err := executionOrigin(q, firstString(v["parentExecutionId"], v["pipelineStepId"], v["pipelineRunId"]))
	if err != nil {
		return nil, err
	}
	out["traceparent"], out["traceActor"] = tp, actor(q, "fleet")
	return out, nil
}
