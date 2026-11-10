package main

// Area A glue to areas B, C and D: execution_stop, budget-blocked connections, the budget answer hook, the artifact gate on
// a completed start, and settling an execution that waits for its artifacts.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func init() {
	operation("execution_stop", false, func(q *Request) (any, error) { return executionStop(q, q.obj()["executionId"]) })
	route("POST /api/executions/{id}/stop", func(q *Request) (any, error) { return executionStop(q, q.r.PathValue("id")) })
	cStopExecution = func(ctx context.Context, a *App, id string) error {
		return a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
			_, err := executionStop(&Request{app: a, tx: tx, r: (&http.Request{}).WithContext(ctx)}, id)
			return err
		})
	}
	cBlockedConnections = blockedConnections
	answerHooks = append(answerHooks, answerHook{
		check: func(q *Request, question doc, answer string) error {
			return budgetAnswerCheck(question, answer, q.person)
		},
		after: budgetAnswerAfter,
	})
	hookPipelineOnExecution = pipelineOnExecution
	hookArtifactGate = artifactGate
	artifactSettled = settleWaiting
}

// executionStop (app.ts execution_stop): one stop command; an execution no host can confirm is settled here.
func executionStop(q *Request, id any) (any, error) {
	e, err := aget(q, id, "execution")
	if err != nil {
		return nil, err
	}
	stops, err := all(q, "command", doc{"executionId": e["id"], "kind": "stop"})
	if err != nil {
		return nil, err
	}
	var c doc
	if len(stops) > 0 {
		c = stops[0]
	} else {
		c = doc{"id": uuid(), "executionId": e["id"], "sessionId": e["sessionId"], "hostId": e["hostId"], "kind": "stop", "payload": doc{}, "status": "pending"}
		if err := put(q, "command", c); err != nil {
			return nil, err
		}
	}
	e["stopRequested"] = true
	known := false
	if h := astr(e["hostId"]); h != "" {
		if known, err = knownHost(q, h); err != nil {
			return nil, err
		}
	}
	if !known && !legacyTerminal(e) {
		pending, err := all(q, "command", doc{"executionId": e["id"], "status": "pending"})
		if err != nil {
			return nil, err
		}
		for _, x := range pending {
			x["status"] = "stopped"
			if err := put(q, "command", x); err != nil {
				return nil, err
			}
		}
		e["state"], e["stopDetail"] = "stopped", "stopped before dispatch"
		if e["hostId"] != nil {
			e["stopDetail"] = "host never registered"
		}
		if c, err = aget(q, c["id"], "command"); err != nil {
			return nil, err
		}
	}
	if err := put(q, "execution", e); err != nil {
		return nil, err
	}
	out, err := stopOutcome(q, e)
	if err != nil {
		return nil, err
	}
	r := doc{}
	for k, v := range c {
		r[k] = v
	}
	r["stop"] = out
	return r, nil
}

// blockedConnections: connections whose stop budget is reached; the gateway refuses their calls.
func blockedConnections(q *Request) (map[string]bool, error) {
	rs, err := aMaps(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit,period,action FROM budget WHERE scope='connection' AND action='stop'`)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rs {
		v, _, err := spend(q, r)
		if err != nil {
			return nil, err
		}
		if f, ok := v.(float64); ok && f >= anum(r["limit"]) {
			out[astr(r["scope_id"])] = true
		}
	}
	return out, nil
}

func declaredNames(declared []any) []string {
	out := []string{}
	for _, d := range declared {
		if s, ok := d.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// artifactGate: a completed start waits for its required artifacts; a missing or failed one fails it.
func artifactGate(q *Request, e doc, declared []any) (doc, error) {
	r, err := artifactRequirement(q.ctx(), q.app, astr(e["id"]), declaredNames(declared))
	if err != nil {
		return nil, err
	}
	if len(r.Missing) > 0 || len(r.Failed) > 0 {
		return doc{"state": "failed", "error": doc{"code": "artifact-required", "message": "Required artifacts missing or failed", "missing": r.Missing, "failed": r.Failed}}, nil
	}
	if len(r.Pending) > 0 {
		return doc{"state": "waiting", "waitReason": "artifacts", "waitingSince": nowMs(), "pendingArtifacts": r.Pending, "declaredArtifacts": declared}, nil
	}
	return doc{}, nil
}

// settleWaiting (app.ts settle): a waiting execution finishes once its artifacts are settled.
func settleWaiting(ctx context.Context, a *App, id string) error {
	ended := ""
	err := a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
		q := &Request{app: a, tx: tx, r: (&http.Request{}).WithContext(ctx)}
		e, err := maybe(q, id, "execution")
		if err != nil || e == nil || e["state"] != "waiting" || e["waitReason"] != "artifacts" {
			return err
		}
		r, err := artifactRequirement(ctx, a, id, declaredNames(anys(e["declaredArtifacts"])))
		if err != nil {
			return err
		}
		if len(r.Pending) > 0 && len(r.Failed) == 0 && len(r.Missing) == 0 {
			return nil
		}
		failed := len(r.Failed) > 0 || len(r.Missing) > 0 || len(r.Pending) > 0
		for _, k := range []string{"waitReason", "waitingSince", "pendingArtifacts", "declaredArtifacts"} {
			delete(e, k)
		}
		e["state"], e["updatedAt"] = "completed", nowMs()
		if failed {
			e["state"] = "failed"
			e["error"] = doc{"code": "artifact-required", "message": "Required artifacts missing or failed", "missing": r.Missing, "failed": r.Failed, "pending": r.Pending}
		}
		if err := put(q, "execution", e); err != nil {
			return err
		}
		cs, err := all(q, "command", doc{"executionId": e["id"], "kind": "start"})
		if err != nil {
			return err
		}
		if len(cs) > 0 {
			if err := agentOnReceipt(q, cs[0], e, astr(e["state"])); err != nil {
				return err
			}
			if err := agentPromote(q); err != nil {
				return err
			}
		}
		ended = id
		return nil
	})
	if err == nil && ended != "" {
		go pipelineOnExecution(a, ended)
	}
	return err
}
