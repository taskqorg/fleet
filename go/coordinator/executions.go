package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// the legacy-record layer and the execution model (docs/designs/fleet-data-foundation.md §1, §7). get/put/all
// read and write a legacy kind inside the request's transaction (q.tx): 'execution' and 'delegation' live on the execution
// table, every other kind is a document.

func isNotFound(err error) bool { var he *HTTPError; return errors.As(err, &he) && he.Code == 404 }

// nonNil: v unless nil, else alt (JavaScript ??).
func nonNil(v ...any) any {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
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
	ms, err := queryRows(q, "SELECT project_id,task_id,session_id,host_id,harness FROM execution WHERE id=$1", parentID)
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
	return rows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId','')='' AND coalesce(delegation->>'state','')<>'suppressed' ORDER BY seq")
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

func previousState(q *Request, id any) (string, error) {
	var s string
	err := q.tx.QueryRow(q.ctx(), "SELECT state::text FROM execution WHERE id=$1", id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// Execution seams: execution_stop, budget-blocked connections, the budget answer hook, the artifact gate on
// a completed start, and settling an execution that waits for its artifacts.

func init() {
	operation("execution_stop", false, func(q *Request) (any, error) { return executionStop(q, q.obj()["executionId"]) })
	route("POST /api/executions/{id}/stop", func(q *Request) (any, error) { return executionStop(q, q.r.PathValue("id")) })
}

// executionStop: one stop command; an execution no host can confirm is settled here.
func executionStop(q *Request, id any) (any, error) {
	e, err := get(q, id, "execution")
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
		c = doc{"id": newID(), "executionId": e["id"], "sessionId": e["sessionId"], "hostId": e["hostId"], "kind": "stop", "payload": doc{}, "status": "pending"}
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
	if !known && !terminal(e) {
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
		e["state"], e["stopDetail"] = "cancelled", "stopped before dispatch"
		if e["hostId"] != nil {
			e["stopDetail"] = "host never registered"
		}
		if c, err = get(q, c["id"], "command"); err != nil {
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
	rs, err := queryRows(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit,period,action FROM budget WHERE scope='connection' AND action='stop'`)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rs {
		v, _, err := spend(q, r)
		if err != nil {
			return nil, err
		}
		if f, ok := v.(float64); ok && f >= num(r["limit"]) {
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

// settleWaiting: a waiting execution finishes once its artifacts are settled.
func settleWaiting(ctx context.Context, a *App, id string) error {
	return settleWaitingAs(ctx, a, id, "")
}

// settleWaitingAs: expired (placement's retry window passed) fails the execution even while artifacts are pending.
func settleWaitingAs(ctx context.Context, a *App, id, expired string) error {
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
		if expired == "" && len(r.Pending) > 0 && len(r.Failed) == 0 && len(r.Missing) == 0 {
			return nil
		}
		failed := expired != "" || len(r.Failed) > 0 || len(r.Missing) > 0 || len(r.Pending) > 0
		for _, k := range []string{"waitReason", "waitingSince", "pendingArtifacts", "declaredArtifacts"} {
			delete(e, k)
		}
		e["state"], e["updatedAt"] = "succeeded", nowMs()
		status := "completed" // the receipt the start command settles with
		if failed {
			e["state"], status = "failed", "failed"
			msg := "Required artifacts missing or failed"
			if expired != "" {
				msg = expired
			}
			e["error"] = doc{"code": "artifact-required", "message": msg, "missing": r.Missing, "failed": r.Failed, "pending": r.Pending}
		}
		if err := put(q, "execution", e); err != nil {
			return err
		}
		cs, err := all(q, "command", doc{"executionId": e["id"], "kind": "start"})
		if err != nil {
			return err
		}
		if len(cs) > 0 {
			if err := agentOnReceipt(q, cs[0], e, status); err != nil {
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

// stopExecution: execution_stop in its own transaction (Lab cell stop).
func stopExecution(ctx context.Context, a *App, id string) error {
	return a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
		_, err := executionStop(&Request{app: a, tx: tx, r: (&http.Request{}).WithContext(ctx)}, id)
		return err
	})
}

// Routes and operations the first Go port left out:
// execution_differences with GET /api/executions/{id}/differences, GET /api/reconciliation and GET /test-sites.

const differenceTextLimit = 131072

// executionDifferences compares the stored files of two executions of one session.
func executionDifferences(q *Request, id, previousID string) (any, error) {
	if len(previousID) > 300 {
		previousID = previousID[:300]
	}
	current, err := get(q, id, "execution")
	if err != nil {
		return nil, err
	}
	previous, err := get(q, previousID, "execution")
	if err != nil {
		return nil, err
	}
	if astr(current["sessionId"]) != astr(previous["sessionId"]) {
		return nil, fail(403, "Different session")
	}
	before, err := executionArtifacts(q, astr(previous["id"]))
	if err != nil {
		return nil, err
	}
	after, err := executionArtifacts(q, astr(current["id"]))
	if err != nil {
		return nil, err
	}
	byPath := func(xs []doc) map[string]doc {
		m := map[string]doc{}
		for _, x := range xs {
			m[astr(x["path"])] = x
		}
		return m
	}
	left, right := byPath(before), byPath(after)
	var paths []string
	for p := range left {
		paths = append(paths, p)
	}
	for p := range right {
		if _, ok := left[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	lines := func(x doc) ([]string, error) {
		if x == nil {
			return []string{}, nil
		}
		b, err := artifactBytes(q.ctx(), q.app, astr(x["artifactId"]), differenceTextLimit)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(b), "\n"), nil
	}
	small := func(x doc) bool { n, _ := numOK(x["size"]); return x == nil || n <= differenceTextLimit }
	ref := func(x doc) any {
		if x == nil {
			return nil
		}
		return doc{"id": x["id"], "sha256": x["sha256"]}
	}
	textual := regexp.MustCompile(`(?i)\.(html|css)$`)
	files := []doc{}
	for _, p := range paths {
		l, r := left[p], right[p]
		var change any
		if textual.MatchString(p) && small(l) && small(r) {
			b, err := lines(l)
			if err != nil {
				return nil, err
			}
			a, err := lines(r)
			if err != nil {
				return nil, err
			}
			start, end := 0, 0
			for start < len(b) && start < len(a) && b[start] == a[start] {
				start++
			}
			for end < len(b)-start && end < len(a)-start && b[len(b)-1-end] == a[len(a)-1-end] {
				end++
			}
			change = doc{"firstLine": start + 1, "removed": b[start : len(b)-end], "added": a[start : len(a)-end]}
		}
		state := "modified"
		switch {
		case l == nil:
			state = "added"
		case r == nil:
			state = "removed"
		case astr(l["sha256"]) == astr(r["sha256"]):
			state = "unchanged"
		}
		files = append(files, doc{"path": p, "textChange": change, "state": state, "previous": ref(l), "current": ref(r)})
	}
	return doc{"sessionId": current["sessionId"], "previousExecutionId": previous["id"], "executionId": current["id"], "files": files}, nil
}

var testSiteFile = regexp.MustCompile(`^[a-zA-Z0-9_-]+\.(html|css)$`)

func init() {
	gatedObjOp("execution_differences", true, func(q *Request) (any, error) {
		b := body(q)
		return executionDifferences(q, astr(b["executionId"]), astr(b["previous"]))
	})
	gatedRoute("GET /api/executions/{id}/differences", func(q *Request) (any, error) {
		return executionDifferences(q, q.r.PathValue("id"), q.r.URL.Query().Get("previous"))
	})
	gatedRoute("GET /api/reconciliation", func(q *Request) (any, error) {
		r, err := first(q, "SELECT data FROM document WHERE kind='reconciliation' ORDER BY seq DESC LIMIT 1")
		if err != nil {
			return nil, err
		}
		if r == nil {
			return doc{"reconciliation": nil}, nil
		}
		return doc{"reconciliation": r["data"]}, nil
	})
	// The example sites an agent builds (docs/website-example.md), served sandboxed from FLEET_TEST_SITE_ROOT.
	gatedRoute("GET /test-sites/{project}/{file}", func(q *Request) (any, error) {
		root, project, name := os.Getenv("FLEET_TEST_SITE_ROOT"), q.r.PathValue("project"), q.r.PathValue("file")
		if root == "" || (project != "hello-world" && project != "hello-world-alt") || !testSiteFile.MatchString(name) {
			return nil, fail(404, "Unknown test site")
		}
		base, err := filepath.EvalSymlinks(filepath.Join(root, project))
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(base, "current", name))
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		if !strings.HasPrefix(path, base+string(filepath.Separator)) {
			return nil, fail(403, "Test-site path rejected")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		return raw(func(w http.ResponseWriter) error {
			w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src data:; frame-ancestors 'self'")
			media := "text/html"
			if strings.HasSuffix(name, ".css") {
				media = "text/css"
			}
			w.Header().Set("Content-Type", media)
			_, err := w.Write(b)
			return err
		}), nil
	})
}
