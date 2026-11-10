package main

// Area A: POST /api/hosts/receipts (SL6). Four envelopes: an account inspection result, a project tool call of a native
// agent, a child (delegation) request, and the status receipt that moves a command and its execution forward only.

import (
	"github.com/jackc/pgx/v5"
)

func init() {
	longRoute("POST /api/hosts/receipts", func(q *Request) (any, error) { return q.app.receipt(q) })
}

var receiptRank = map[string]int{"pending": 0, "accepted": 1, "started": 2, "unknown": 2, "completed": 3, "failed": 3, "stopped": 3}

// projectReceiptOps: the project operations a native agent may call through a receipt.
var projectReceiptOps = []string{"project_context", "catalogue", "pipeline_run", "direct_search", "pipeline_history", "pipeline_get", "question_ask", "question_history", "account_list"}

// projectReceipt runs one project operation for a native agent (projects area: the operation, its native-agent actor
// and the early return of a started pipeline run). Default: the registered operation in this transaction.
func projectReceipt(q *Request, operation string, args doc, agent doc) (any, error) {
	if operation == "account_list" {
		return agentAccountList(q, args, agent)
	}
	op, ok := projectOps[operation]
	if !ok {
		op, ok = operations[operation]
	}
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := &Request{app: q.app, r: q.r, tx: q.tx, host: q.host, body: []byte(js(args))}
	return op.h(sub)
}

func (a *App) receipt(q *Request) (any, error) {
	b := q.obj()
	var out any
	var ended string
	err := a.store.Tx(q.ctx(), nil, true, "", func(tx pgx.Tx) error {
		q.tx = tx
		var err error
		out, ended, err = a.receiptIn(q, b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if ended != "" {
		go pipelineOnExecution(a, ended)
	}
	return out, nil
}

// commandOf: the receipt's command, which must belong to the receipt's execution and host.
func commandOf(q *Request, b doc, msg string) (doc, error) {
	if err := ownedBody(q, b); err != nil {
		return nil, err
	}
	c, err := get(q, b["commandId"], "command")
	if err != nil {
		return nil, err
	}
	if c["executionId"] != b["executionId"] || c["hostId"] != b["hostId"] {
		return nil, fail(403, msg)
	}
	return c, nil
}

// ownedBody: the execution named in a host request runs on the requesting host.
func ownedBody(q *Request, b doc) error {
	if _, ok := b["hostId"].(string); !ok {
		return fail(403, "Host rejected")
	}
	if id := b["executionId"]; id != nil && id != "" {
		e, err := get(q, id, "execution")
		if err != nil {
			return err
		}
		if e["hostId"] != b["hostId"] {
			return fail(403, "Execution rejected")
		}
	}
	return nil
}

func (a *App) receiptIn(q *Request, b doc) (any, string, error) {
	if v := nonNil(b["accountObservation"], b["accountCatalogue"]); v != nil {
		if err := ownedBody(q, b); err != nil {
			return nil, "", err
		}
		c, err := get(q, b["commandId"], "command")
		if err != nil {
			return nil, "", err
		}
		o, p := mapOf(v), mapOf(c["payload"])
		if c["hostId"] != b["hostId"] || c["executionId"] != b["executionId"] || c["kind"] != "account-inspect" || p["accountId"] != o["accountId"] || p["binding"] != nonNil(o["binding"]) {
			return nil, "", fail(403, "Account inspection ownership rejected")
		}
		if b["accountCatalogue"] != nil {
			r, err := accountCatalogue(q, o, astr(p["nativeHostId"]))
			return r, "", err
		}
		r, err := accountReport(q, o, astr(p["nativeHostId"]))
		return r, "", err
	}
	if d := mapOf(b["delegation"]); d != nil && d["operation"] != nil {
		if err := ownedBody(q, b); err != nil {
			return nil, "", err
		}
		c, err := get(q, b["commandId"], "command")
		if err != nil {
			return nil, "", err
		}
		s, err := get(q, c["sessionId"], "session")
		if err != nil {
			return nil, "", err
		}
		if c["executionId"] != b["executionId"] || c["hostId"] != b["hostId"] || s["projectId"] == nil || d["projectId"] != s["projectId"] || !inList(projectReceiptOps, astr(d["operation"])) {
			return nil, "", fail(403, "Project grant rejected")
		}
		args := doc{}
		for k, v := range mapOf(d["arguments"]) {
			args[k] = v
		}
		args["projectId"] = s["projectId"]
		r, err := projectReceipt(q, astr(d["operation"]), args, doc{"kind": "native-agent", "executionId": b["executionId"], "sessionId": s["id"]})
		return r, "", err
	}
	if b["delegation"] != nil {
		c, err := commandOf(q, b, "Command rejected")
		if err != nil {
			return nil, "", err
		}
		r, err := a.delegationRequest(q, b, c)
		return r, "", err
	}
	c, err := commandOf(q, b, "Command rejected")
	if err != nil {
		return nil, "", err
	}
	status := astr(b["status"])
	if !inList([]string{"accepted", "started", "completed", "failed", "stopped", "unknown"}, status) {
		return nil, "", fail(400, "Invalid status")
	}
	cur := receiptRank[astr(c["status"])]
	if receiptRank[status] < cur || (cur == 3 && status != c["status"]) {
		return nil, "", fail(409, "Receipt regression")
	}
	if cur == 3 { // a repeated terminal receipt (an outbox retry) changes nothing: the gate and onReceipt ran once
		return doc{"ok": true}, "", nil
	}
	native, hasNative := b["nativeSessionId"].(string)
	hasNative = hasNative && native != ""
	if hasNative {
		e, err := get(q, b["executionId"], "execution")
		if err != nil {
			return nil, "", err
		}
		known, err := agentKnownNative(q, e)
		if err != nil {
			return nil, "", err
		}
		if known != "" && known != native {
			return nil, "", fail(409, "Native identity conflict")
		}
	}
	c["status"] = status
	if err := put(q, "command", c); err != nil {
		return nil, "", err
	}
	e, err := get(q, b["executionId"], "execution")
	if err != nil {
		return nil, "", err
	}
	wasFinal := legacyTerminal(e)
	kind := c["kind"]
	if !wasFinal && kind != "steer" && (kind != "stop" || status == "stopped") {
		e["state"] = status
	}
	e["updatedAt"] = nowMs()
	if status == "started" && e["startedAt"] == nil {
		e["startedAt"] = e["updatedAt"]
	}
	if kind != "steer" && (status == "completed" || status == "failed" || status == "stopped") && e["finishedAt"] == nil {
		e["finishedAt"] = e["updatedAt"]
	}
	if b["error"] != nil && b["error"] != "" && b["error"] != false {
		e["error"] = b["error"]
	}
	if (kind == "start" || kind == "script") && status == "completed" && !wasFinal {
		declared := anys(b["artifacts"])
		if len(declared) > 10000 {
			declared = declared[:10000]
		}
		gate, err := artifactGate(q, e, declared)
		if err != nil {
			return nil, "", err
		}
		for k, v := range gate {
			e[k] = v
		}
	}
	if err := put(q, "execution", e); err != nil {
		return nil, "", err
	}
	if status != "unknown" {
		if err := markObservation(q, e["id"], "confirmed"); err != nil {
			return nil, "", err
		}
	}
	if hasNative && kind == "start" {
		if len([]rune(native)) > 300 {
			return nil, "", fail(400, "Invalid text")
		}
		if err := agentCaptureNative(q, e, native); err != nil {
			return nil, "", err
		}
	}
	state := astr(e["state"])
	if state != "waiting" {
		reported := status
		if state == "failed" && status == "completed" {
			reported = "failed"
		}
		if err := agentOnReceipt(q, c, e, reported); err != nil {
			return nil, "", err
		}
		if legacyTerminal(e) {
			if err := agentPromote(q); err != nil {
				return nil, "", err
			}
		}
	}
	ended := ""
	if legacyTerminal(e) || state == "unknown" {
		ended = astr(e["id"])
	}
	return doc{"ok": true}, ended, nil
}

// agentAccountList: the subscriptions a native agent's work may use (its director's, or taken for its project), without
// host roots or bindings. Fixed: the TypeScript coordinator handed a native agent every subscription with every host's roots.
func agentAccountList(q *Request, args doc, agent doc) (any, error) {
	e, err := get(q, agent["executionId"], "execution")
	if err != nil {
		return nil, err
	}
	project := astr(args["projectId"])
	as, err := all(q, "managed-account", nil)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, a := range as {
		if a["holder"] != e["directedBy"] && !inList(strs(mapOf(a["take"])["projects"]), project) {
			continue
		}
		out = append(out, doc{"id": a["id"], "name": a["name"], "harness": a["harness"], "holder": a["holder"], "intake": a["intake"], "plan": a["plan"]})
	}
	return doc{"accounts": out}, nil
}
