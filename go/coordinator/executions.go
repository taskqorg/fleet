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
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// Records and the execution model (docs/designs/fleet-data-foundation.md §1, §7). get/put/all read and write a record
// kind inside the request's transaction (q.tx): 'execution' and 'delegation' live on the execution table, 'question' on the
// question table, every other kind is a document.

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
	operation("execution_stop", false, typedOp(func(q *Request, r *contract.ExecutionStopRequest) (contract.ExecutionStopReply, error) {
		return executionStop(q, r.ExecutionId)
	}))
	route("POST /api/executions/{id}/stop", func(q *Request) (any, error) { return executionStop(q, q.r.PathValue("id")) })
}

// executionStop: one stop command; an execution no host can confirm is settled here.
func executionStop(q *Request, id string) (contract.ExecutionStopReply, error) {
	var rep contract.ExecutionStopReply
	e, err := get(q, id, "execution")
	if err != nil {
		return rep, err
	}
	stops, err := all(q, "command", doc{"executionId": e["id"], "kind": "stop"})
	if err != nil {
		return rep, err
	}
	var c doc
	if len(stops) > 0 {
		c = stops[0]
	} else {
		c = doc{"id": newID(), "executionId": e["id"], "sessionId": e["sessionId"], "hostId": e["hostId"], "kind": "stop", "payload": doc{}, "status": "pending"}
		if err := put(q, "command", c); err != nil {
			return rep, err
		}
	}
	e["stopRequested"] = true
	known := false
	if h := astr(e["hostId"]); h != "" {
		if known, err = knownHost(q, h); err != nil {
			return rep, err
		}
	}
	if !known && !terminal(e) {
		pending, err := all(q, "command", doc{"executionId": e["id"], "status": "pending"})
		if err != nil {
			return rep, err
		}
		for _, x := range pending {
			x["status"] = "stopped"
			if err := put(q, "command", x); err != nil {
				return rep, err
			}
		}
		e["state"], e["stopDetail"] = "cancelled", "stopped before dispatch"
		if e["hostId"] != nil {
			e["stopDetail"] = "host never registered"
		}
		if c, err = get(q, c["id"], "command"); err != nil {
			return rep, err
		}
	}
	if err := put(q, "execution", e); err != nil {
		return rep, err
	}
	out, err := stopOutcome(q, e)
	if err != nil {
		return rep, err
	}
	return contract.ExecutionStopReply{Id: astr(c["id"]), ExecutionId: astr(c["executionId"]), SessionId: strPtr(c["sessionId"]), HostId: strPtr(c["hostId"]),
		Kind: astr(c["kind"]), Payload: recOf(c["payload"]), Status: astr(c["status"]), Stop: out}, nil
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

// declaredNames: the declared artifact names, each once, in declaration order; non-strings are not names.
func declaredNames(declared []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, s := range declared {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// artifactGate: a completed start waits for its required artifacts; a missing or failed one fails it.
func artifactGate(q *Request, e doc, declared []string) (doc, error) {
	names := declaredNames(declared)
	r, err := artifactRequirement(q.ctx(), q.app, astr(e["id"]), names)
	if err != nil {
		return nil, err
	}
	if len(r.Missing) > 0 || len(r.Failed) > 0 {
		return doc{"state": "failed", "error": doc{"code": "artifact-required", "message": "Required artifacts missing or failed", "missing": r.Missing, "failed": r.Failed}}, nil
	}
	if len(r.Pending) > 0 {
		return doc{"state": "waiting", "waitReason": "artifacts", "waitingSince": nowMs(), "pendingArtifacts": r.Pending, "declaredArtifacts": names}, nil
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
		r, err := artifactRequirement(ctx, a, id, declaredNames(strs(e["declaredArtifacts"])))
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
func executionDifferences(q *Request, id, previousID string) (contract.ExecutionDifferencesReply, error) {
	var rep contract.ExecutionDifferencesReply
	if len(previousID) > 300 {
		previousID = previousID[:300]
	}
	current, err := get(q, id, "execution")
	if err != nil {
		return rep, err
	}
	previous, err := get(q, previousID, "execution")
	if err != nil {
		return rep, err
	}
	if astr(current["sessionId"]) != astr(previous["sessionId"]) {
		return rep, fail(403, "Different session")
	}
	before, err := executionArtifacts(q, astr(previous["id"]))
	if err != nil {
		return rep, err
	}
	after, err := executionArtifacts(q, astr(current["id"]))
	if err != nil {
		return rep, err
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
	ref := func(x doc) *contract.FileRef {
		if x == nil {
			return nil
		}
		return &contract.FileRef{Id: astr(x["id"]), Sha256: strPtr(x["sha256"])}
	}
	textual := regexp.MustCompile(`(?i)\.(html|css)$`)
	files := []contract.DiffFile{}
	for _, p := range paths {
		l, r := left[p], right[p]
		var change *contract.TextChange
		if textual.MatchString(p) && small(l) && small(r) {
			b, err := lines(l)
			if err != nil {
				return rep, err
			}
			a, err := lines(r)
			if err != nil {
				return rep, err
			}
			start, end := 0, 0
			for start < len(b) && start < len(a) && b[start] == a[start] {
				start++
			}
			for end < len(b)-start && end < len(a)-start && b[len(b)-1-end] == a[len(a)-1-end] {
				end++
			}
			change = &contract.TextChange{FirstLine: int64(start + 1), Removed: b[start : len(b)-end], Added: a[start : len(a)-end]}
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
		files = append(files, contract.DiffFile{Path: p, TextChange: change, State: state, Previous: ref(l), Current: ref(r)})
	}
	return contract.ExecutionDifferencesReply{SessionId: strPtr(current["sessionId"]), PreviousExecutionId: strPtr(previous["id"]), ExecutionId: astr(current["id"]), Files: files}, nil
}

var testSiteFile = regexp.MustCompile(`^[a-zA-Z0-9_-]+\.(html|css)$`)

func init() {
	gatedObjOp("execution_differences", true, typedOp(func(q *Request, r *contract.ExecutionDifferencesRequest) (contract.ExecutionDifferencesReply, error) {
		return executionDifferences(q, r.ExecutionId, r.Previous)
	}))
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

// lostAfter: an unfinished run whose host lost track of it (observation unconfirmed) or that has not answered its
// stop is ended after this long without a receipt; no host will finish it and its questions cannot be acted on.
var lostAfter = envMs("FLEET_LOST_AFTER_MS", int(time.Hour/time.Millisecond))

func init() {
	gatedObjOp("executions_abandon", false, typedOp(func(q *Request, r *contract.ExecutionsAbandonRequest) (contract.ExecutionsAbandonReply, error) {
		out := contract.ExecutionsAbandonReply{Abandoned: []string{}, Skipped: []contract.ExecutionSkipped{}}
		if q.person != nil && !fullAccess(q.person) {
			return out, fail(403, "Only the owner abandons executions")
		}
		if strings.TrimSpace(r.Reason) == "" || len([]rune(r.Reason)) > 4000 {
			return out, fail(400, "Expected reason")
		}
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected execution ids")
			}
			state, err := executionAbandon(q, id, r.Reason, actor(q, "operator"))
			if err != nil {
				return out, err
			}
			if state != "" {
				out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: state})
				continue
			}
			out.Abandoned = append(out.Abandoned, id)
		}
		publish(q)
		return out, agentPromote(q)
	}))
	gatedObjOp("executions_dismiss", false, typedOp(func(q *Request, r *contract.ExecutionsDismissRequest) (contract.ExecutionsDismissReply, error) {
		out := contract.ExecutionsDismissReply{Dismissed: []string{}, Skipped: []contract.ExecutionSkipped{}}
		if q.person != nil && !fullAccess(q.person) {
			return out, fail(403, "Only the owner dismisses execution problems")
		}
		if strings.TrimSpace(r.Reason) == "" || len([]rune(r.Reason)) > 4000 {
			return out, fail(400, "Expected reason")
		}
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected execution ids")
			}
		}
		by := actor(q, "operator")
		for _, id := range r.Ids {
			row, err := first(q, "SELECT state::text AS state,project_id,problem,EXISTS (SELECT 1 FROM document d WHERE d.kind='problem-dismissal' AND d.id=execution.id) AS dismissed FROM execution WHERE id=$1 FOR UPDATE", id)
			if err != nil {
				return out, err
			}
			if row == nil || !terminalTyped[astr(row["state"])] || row["problem"] == nil || row["dismissed"] == true {
				state := "unknown"
				if row != nil {
					state = astr(row["state"])
				}
				if row != nil && row["dismissed"] == true {
					state = "dismissed"
				}
				out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: state})
				continue
			}
			// Its own record: engines put execution data wholesale, which would drop a field there.
			d := doc{"id": id, "problem": row["problem"], "reason": r.Reason, "actor": by, "at": nowMs()}
			if err := put(q, "problem-dismissal", d); err != nil {
				return out, err
			}
			if err := put(q, "setting-change", doc{"id": newID(), "target": "execution:" + id, "projectId": row["project_id"], "field": "problem", "previous": row["problem"], "value": "dismissed", "reason": r.Reason, "actor": by, "at": d["at"]}); err != nil {
				return out, err
			}
			out.Dismissed = append(out.Dismissed, id)
		}
		publish(q)
		return out, nil
	}))
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "lost", lostAfter/12, func(ctx context.Context) error {
			return runTx(ctx, a, nil, true, "", func(q *Request) error {
				// lostCond: still lost, re-checked under the row lock so a receipt that just arrived wins.
				const lostCond = `state IN ('preparing','running','waiting') AND (
				 (kind IN ('agent_run','host_job') AND (observation='unconfirmed' OR (coalesce((data->>'stopRequested')::boolean,false) AND EXISTS (SELECT 1 FROM document c
				  WHERE c.kind='command' AND c.data->>'executionId'=execution.id AND c.data->>'kind'='stop' AND c.data->>'status' IN ('pending','unknown'))))
				  AND coalesce(to_timestamp((data->>'updatedAt')::float8/1000), started_at, updated_at) < now() - make_interval(secs => $1))
				 OR (kind='pipeline_step' AND observation='unconfirmed' AND coalesce(started_at, updated_at) < now() - make_interval(secs => $1)
				  AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1)))))`
				rs, err := queryRows(q, "SELECT id FROM execution WHERE "+lostCond, lostAfter.Seconds())
				if err != nil {
					return err
				}
				n := 0
				for _, x := range rs {
					still, err := has(q, "SELECT 1 FROM execution WHERE id=$2 AND "+lostCond+" FOR UPDATE SKIP LOCKED", lostAfter.Seconds(), x["id"])
					if err != nil {
						return err
					}
					if !still {
						continue
					}
					if _, err := executionAbandon(q, astr(x["id"]), "no host receipt for "+lostAfter.String()+" after the host lost track or the stop went unanswered", "fleet"); err != nil {
						return err
					}
					n++
				}
				if n == 0 {
					return nil
				}
				publish(q)
				return agentPromote(q)
			})
		})
	})
}

// executionAbandon ends an unfinished agent run or host job no host will finish: failed with problem unknown:abandoned
// (the outcome is unknown), pending commands settled, the parent woken, its open questions withdrawn, one audit row;
// the caller promotes queued work once after its batch. It returns the state of an
// execution it leaves alone ("unknown": not an agent run or host job), "" when it ended it.
func executionAbandon(q *Request, id, reason, by string) (string, error) {
	row, err := first(q, "SELECT state::text AS state,kind::text AS kind,project_id FROM execution WHERE id=$1 FOR UPDATE", id)
	if err != nil || row == nil {
		return "unknown", err
	}
	if k := astr(row["kind"]); k != "agent_run" && k != "host_job" {
		// Only a pipeline step whose run row is gone: no engine will put it again, so the typed transition holds.
		// Other kinds belong to their engine (a run's steps, child agents, tool calls) and are left alone.
		orphan, err := has(q, "SELECT 1 FROM execution s WHERE s.id=$1 AND s.kind='pipeline_step' AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(s.parent_execution_id,split_part(s.id,':step:',1)))", id)
		if err != nil {
			return "", err
		}
		if !orphan {
			return "unknown", nil
		}
		if terminalTyped[astr(row["state"])] {
			return astr(row["state"]), nil
		}
		if _, err := transition(q, id, "failed"); err != nil {
			return "", err
		}
		if err := exec(q, `UPDATE execution SET problem='unknown:abandoned',observation='unconfirmed',data=data||jsonb_build_object('stopDetail','abandoned','error','abandoned: '||$2::text) WHERE id=$1`, id, reason); err != nil {
			return "", err
		}
		return "", put(q, "setting-change", doc{"id": newID(), "target": "execution:" + id, "projectId": row["project_id"], "field": "state", "previous": row["state"], "value": "failed", "reason": reason, "actor": by, "at": nowMs()})
	}
	e, err := get(q, id, "execution")
	if err != nil {
		return "", err
	}
	if terminal(e) {
		return astr(e["state"]), nil
	}
	if err := exec(q, `UPDATE document SET data=data||'{"status":"stopped","stopDetail":"abandoned"}', updated_at=now() WHERE kind='command' AND data->>'executionId'=$1 AND data->>'status' IN ('pending','unknown')`, id); err != nil {
		return "", err
	}
	prev := astr(e["state"])
	now := nowMs()
	e["state"], e["stopRequested"], e["stopDetail"], e["error"], e["updatedAt"], e["finishedAt"] = "failed", true, "abandoned", "abandoned: "+reason, now, now
	if err := put(q, "execution", e); err != nil {
		return "", err
	}
	if err := exec(q, "UPDATE execution SET problem='unknown:abandoned' WHERE id=$1", id); err != nil {
		return "", err
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "execution:" + id, "projectId": e["projectId"], "field": "state", "previous": prev, "value": "failed", "reason": reason, "actor": by, "at": now}); err != nil {
		return "", err
	}
	starts, err := all(q, "command", doc{"executionId": id, "kind": "start"})
	if err != nil {
		return "", err
	}
	if len(starts) > 0 {
		if err := agentOnReceipt(q, starts[0], e, "failed"); err != nil {
			return "", err
		}
	}
	// Its open questions have no living asker any more.
	return "", exec(q, "UPDATE question SET state='withdrawn',cause=NULL WHERE execution_id=$1 AND state='open'", id)
}
