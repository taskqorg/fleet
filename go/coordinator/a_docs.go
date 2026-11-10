package main

// Area A: the legacy-record layer of packages/coordinator/store.ts and the execution model of executions.ts. get/put/all
// read and write a legacy kind inside the request's transaction (q.tx): 'execution' and 'delegation' live on the execution
// table, every other kind is a document. Other areas may call these; worker D merges duplicates at cutover.

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// cQuery: rows as column maps.
// aRows: the first column of each row, a jsonb object.
func aRows(q *Request, sql string, args ...any) ([]doc, error) {
	rs, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, func(r pgx.CollectableRow) (doc, error) {
		var d doc
		return d, r.Scan(&d)
	})
	if out == nil {
		out = []doc{}
	}
	return out, err
}

func isNotFound(err error) bool { var he *HTTPError; return errors.As(err, &he) && he.Code == 404 }

func astr(v any) string { s, _ := v.(string); return s }

// nonNil: v unless nil, else alt (JavaScript ??).
func nonNil(v ...any) any {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
}

func aMsTime(v any) any {
	f, ok := v.(float64)
	if !ok {
		if i, ok2 := v.(int64); ok2 {
			f, ok = float64(i), true
		}
	}
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return time.UnixMilli(int64(f)).UTC()
}

// createExecution inserts a typed row unless its id or request_id exists; true when this call inserted one.
func createExecution(q *Request, v doc) (bool, error) {
	if v["state"] == nil {
		v["state"] = "preparing"
	}
	var cfg any
	if v["configRef"] != nil {
		cfg = js(v["configRef"])
	}
	data := v["data"]
	if data == nil {
		data = doc{}
	}
	tag, err := q.tx.Exec(q.ctx(), `INSERT INTO execution(id,kind,state,project_id,task_id,parent_execution_id,session_id,host_id,harness,model,config_ref,data,experiment_id,variant,account_id,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT DO NOTHING`,
		v["id"], v["kind"], v["state"], v["projectId"], v["taskId"], v["parentExecutionId"], v["sessionId"], v["hostId"], v["harness"], v["model"], cfg, js(data), v["experimentId"], v["variant"], v["accountId"], v["requestId"])
	return err == nil && tag.RowsAffected() > 0, err
}

func markObservation(q *Request, id any, observation string) error {
	return exec(q, "UPDATE execution SET observation=$2::observation,last_seen_at=CASE WHEN $2::text='confirmed' THEN now() ELSE last_seen_at END WHERE id=$1", id, observation)
}

func setHarnessVersion(q *Request, id any, version string) error {
	return exec(q, "UPDATE execution SET harness_version=$2 WHERE id=$1 AND harness_version IS DISTINCT FROM $2", id, version)
}

// recordChildren: one child_agent row per child thread a harness reports, under the parent.
func recordChildren(q *Request, parentID string, children []any) error {
	ms, err := cQuery(q, "SELECT project_id,task_id,session_id,host_id,harness FROM execution WHERE id=$1", parentID)
	if err != nil || len(ms) == 0 {
		return err
	}
	p := ms[0]
	for _, x := range children {
		c, _ := x.(map[string]any)
		thread, ok := c["threadId"].(string)
		if !ok {
			continue
		}
		binding, _ := c["binding"].(map[string]any)
		var model any
		if m, ok := binding["model"].(string); ok {
			model = m
		}
		var cfg any
		if c["binding"] != nil {
			cfg = c["binding"]
		}
		if _, err := createExecution(q, doc{"id": parentID + ":child:" + thread, "kind": "child_agent", "state": "running", "projectId": p["project_id"], "taskId": p["task_id"],
			"parentExecutionId": parentID, "sessionId": p["session_id"], "hostId": p["host_id"], "harness": p["harness"], "model": model, "configRef": cfg,
			"data": doc{"threadId": thread, "parentThreadId": c["parentThreadId"], "tool": c["tool"]}}); err != nil {
			return err
		}
	}
	return nil
}

func executionEnded(q *Request, id any) (bool, error) {
	var x bool
	err := q.tx.QueryRow(q.ctx(), "SELECT EXISTS(SELECT 1 FROM execution WHERE id=$1 AND ended_at IS NOT NULL)", id).Scan(&x)
	return x, err
}

var stateOrder = map[string]int{"preparing": 0, "waiting": 1, "running": 1, "succeeded": 2, "failed": 2, "cancelled": 2}

// transition moves a typed state forward only.
func transition(q *Request, id, state string) (doc, error) {
	if _, ok := stateOrder[state]; !ok {
		return nil, fail(400, "Unknown execution state "+state)
	}
	cur, err := previousState(q, id)
	if err != nil {
		return nil, err
	}
	if cur == "" {
		return nil, errNotFound
	}
	if cur == state {
		return doc{"state": cur}, nil
	}
	if terminalTyped[cur] || stateOrder[state] < stateOrder[cur] {
		return nil, fail(409, fmt.Sprintf("Execution %s cannot become %s", cur, state))
	}
	if err := exec(q, `UPDATE execution SET state=$2,problem=CASE WHEN $2::execution_state='succeeded' THEN NULL ELSE problem END,ended_at=CASE WHEN $3 THEN coalesce(ended_at,now()) ELSE ended_at END,started_at=CASE WHEN $2::execution_state IN ('running','waiting') THEN coalesce(started_at,now()) ELSE started_at END WHERE id=$1`, id, state, terminalTyped[state]); err != nil {
		return nil, err
	}
	if terminalTyped[state] {
		if err := writeTotals(q, id); err != nil {
			return nil, err
		}
		if err := writeEnded(q, id); err != nil {
			return nil, err
		}
	}
	return doc{"state": state}, nil
}

// detectProblems marks open executions bound to a host, account or environment nothing registered.
func detectProblems(q *Request, hosts []string, environments map[string][]string) error {
	return exec(q, `WITH found AS (SELECT e.id,CASE
    WHEN e.host_id IS NOT NULL AND NOT (e.host_id=ANY($1) OR EXISTS (SELECT 1 FROM document h WHERE h.kind='host-observation' AND h.id=e.host_id)) THEN 'unregistered host '||e.host_id
    WHEN e.account_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM document a WHERE a.kind='managed-account' AND a.id=e.account_id) THEN 'unregistered account '||e.account_id
    WHEN e.project_id IS NOT NULL AND e.host_id IS NOT NULL AND jsonb_array_length(coalesce($2::jsonb->e.project_id,'[]'))>0 AND NOT ($2::jsonb->e.project_id) ? e.host_id THEN 'no registered environment of project '||e.project_id||' on host '||e.host_id
   END AS problem FROM execution e WHERE e.state NOT IN ('succeeded','failed','cancelled') AND e.kind<>'child_agent')
   UPDATE execution x SET problem=found.problem FROM found WHERE x.id=found.id AND x.problem IS DISTINCT FROM found.problem`, hosts, js(environments))
}

func openDelegations(q *Request) ([]doc, error) {
	return aRows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId','')='' AND coalesce(delegation->>'state','')<>'suppressed' ORDER BY seq")
}

// legacyTerminal: a finished legacy execution state.
func legacyTerminal(e doc) bool {
	s := astr(e["state"])
	return s == "completed" || s == "failed" || s == "stopped"
}

func strs(v any) []string {
	xs, _ := v.([]any)
	out := []string{}
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func inList(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// aget: get with any id; a missing or non-string id is 404.
func aget(q *Request, id any, kind string) (doc, error) {
	s, ok := id.(string)
	if !ok || s == "" {
		return nil, errNotFound
	}
	return get(q, s, kind)
}

func previousState(q *Request, id any) (string, error) {
	var s string
	err := q.tx.QueryRow(q.ctx(), "SELECT state::text FROM execution WHERE id=$1", id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return s, err
}
