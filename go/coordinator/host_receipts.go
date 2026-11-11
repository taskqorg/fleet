package main

// Host receipts (SL6), one route per envelope of contracts/host-protocol.schema.json: the status receipt that moves a
// command and its execution forward only (Receipt), an account inspection result (AccountReceipt), a project tool call
// of a native agent (ProjectReceipt) and a child agent request (ChildReceipt). Each body is type-checked against its
// struct first; unknown fields are refused.

import (
	"bytes"
	"encoding/json"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

func init() {
	longRoute("POST /api/hosts/receipts", func(q *Request) (any, error) {
		return typedStatus(q)
	})
	longRoute("POST /api/hosts/accounts/report", func(q *Request) (any, error) {
		return typedReceipt(q, func(t *contract.AccountReceipt) bool { return t.AccountObservation != nil || t.AccountCatalogue != nil }, accountReceiptIn)
	})
	longRoute("POST /api/hosts/project-operations", func(q *Request) (any, error) {
		return typedReceipt(q, func(t *contract.ProjectReceipt) bool { return t.Delegation.Operation != "" }, projectReceiptIn)
	})
	longRoute("POST /api/hosts/delegations", func(q *Request) (any, error) {
		return typedReceipt(q, func(t *contract.ChildReceipt) bool { return t.Delegation.ProfileId != "" }, q.app.childReceiptIn)
	})
}

// typedStatus: /api/hosts/receipts, a command status (contract.Receipt).
func typedStatus(q *Request) (any, error) {
	var t contract.Receipt
	dec := json.NewDecoder(bytes.NewReader(q.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil || t.Status == "" {
		return nil, fail(400, "Invalid body for this receipt route")
	}
	return q.app.receipt(q, q.app.statusReceiptIn)
}

// typedReceipt: a split receipt route. The body decodes into its closed contract struct (unknown fields refused) and must carry its envelope (present), then fn runs as a receipt.
func typedReceipt[T any](q *Request, present func(*T) bool, fn func(q *Request, b doc) (any, error)) (any, error) {
	var t T
	dec := json.NewDecoder(bytes.NewReader(q.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil || !present(&t) {
		return nil, fail(400, "Invalid body for this receipt route")
	}
	return q.app.receipt(q, func(q *Request, b doc) (any, string, error) { r, err := fn(q, b); return r, "", err })
}

// receiptState: the execution state a command receipt status reports ('unknown' reports none).
var receiptState = map[string]string{"accepted": "preparing", "started": "running", "completed": "succeeded", "failed": "failed", "stopped": "cancelled"}

var receiptRank = map[string]int{"pending": 0, string(contract.ReceiptStatusAccepted): 1, string(contract.ReceiptStatusStarted): 2, string(contract.ReceiptStatusUnknown): 2,
	string(contract.ReceiptStatusCompleted): 3, string(contract.ReceiptStatusFailed): 3, string(contract.ReceiptStatusStopped): 3} // pending: a command not yet received

// projectReceiptOps: the project operations a native agent may call through a receipt.
var projectReceiptOps = func() []string {
	out := []string{}
	for _, o := range []contract.ProjectOperation{contract.ProjectOperationProjectContext, contract.ProjectOperationCatalogue, contract.ProjectOperationPipelineRun,
		contract.ProjectOperationDirectSearch, contract.ProjectOperationPipelineHistory, contract.ProjectOperationPipelineGet, contract.ProjectOperationQuestionAsk,
		contract.ProjectOperationQuestionHistory, contract.ProjectOperationAccountList} {
		out = append(out, string(o))
	}
	return out
}()

// projectReceipt runs one project operation for a native agent (projects area: the operation, its native-agent actor
// and the early return of a started pipeline run). Otherwise the registered operation runs in this transaction.
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

func (a *App) receipt(q *Request, fn func(q *Request, b doc) (any, string, error)) (any, error) {
	b := q.obj()
	var out any
	var ended string
	err := a.store.Tx(q.ctx(), nil, true, "", func(tx pgx.Tx) error {
		q.tx = tx
		var err error
		out, ended, err = fn(q, b)
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

// accountReceiptIn: POST /api/hosts/accounts/report (contract.AccountReceipt), an account-inspect command's result.
func accountReceiptIn(q *Request, b doc) (any, error) {
	if err := ownedBody(q, b); err != nil {
		return nil, err
	}
	c, err := get(q, b["commandId"], "command")
	if err != nil {
		return nil, err
	}
	v := nonNil(b["accountObservation"], b["accountCatalogue"])
	o, p := mapOf(v), mapOf(c["payload"])
	if v == nil || c["hostId"] != b["hostId"] || c["executionId"] != b["executionId"] || c["kind"] != string(contract.CommandKindAccountInspect) || p["accountId"] != o["accountId"] || p["binding"] != nonNil(o["binding"]) {
		return nil, fail(403, "Account inspection ownership rejected")
	}
	if b["accountCatalogue"] != nil {
		return accountCatalogue(q, o, astr(p["nativeHostId"]))
	}
	return accountReport(q, o, astr(p["nativeHostId"]))
}

// projectReceiptIn: POST /api/hosts/project-operations (contract.ProjectReceipt), a project operation of a native agent.
func projectReceiptIn(q *Request, b doc) (any, error) {
	d := mapOf(b["delegation"])
	if err := ownedBody(q, b); err != nil {
		return nil, err
	}
	c, err := get(q, b["commandId"], "command")
	if err != nil {
		return nil, err
	}
	s, err := get(q, c["sessionId"], "session")
	if err != nil {
		return nil, err
	}
	if c["executionId"] != b["executionId"] || c["hostId"] != b["hostId"] || s["projectId"] == nil || d["projectId"] != s["projectId"] || !inList(projectReceiptOps, astr(d["operation"])) {
		return nil, fail(403, "Project grant rejected")
	}
	args := doc{}
	for k, v := range mapOf(d["arguments"]) {
		args[k] = v
	}
	args["projectId"] = s["projectId"]
	return projectReceipt(q, astr(d["operation"]), args, doc{"kind": "native-agent", "executionId": b["executionId"], "sessionId": s["id"]})
}

// childReceiptIn: POST /api/hosts/delegations (contract.ChildReceipt), a child agent request.
func (a *App) childReceiptIn(q *Request, b doc) (any, error) {
	c, err := commandOf(q, b, "Command rejected")
	if err != nil {
		return nil, err
	}
	return a.delegationRequest(q, b, c)
}

// statusReceiptIn: POST /api/hosts/receipts (contract.Receipt), a command's status.
func (a *App) statusReceiptIn(q *Request, b doc) (any, string, error) {
	c, err := commandOf(q, b, "Command rejected")
	if err != nil {
		return nil, "", err
	}
	status := astr(b["status"])
	switch contract.ReceiptStatus(status) {
	case contract.ReceiptStatusAccepted, contract.ReceiptStatusStarted, contract.ReceiptStatusCompleted, contract.ReceiptStatusFailed, contract.ReceiptStatusStopped, contract.ReceiptStatusUnknown:
	default:
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
	wasFinal := terminal(e)
	kind := c["kind"]
	lost := false
	if !wasFinal && kind != "steer" && (kind != "stop" || status == "stopped") {
		if s, ok := receiptState[status]; ok {
			e["state"], e["observation"] = s, "confirmed"
		} else { // 'unknown': the host lost track; the state stays, unconfirmed
			e["observation"], lost = "unconfirmed", true
		}
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
		// Every declaration reaches the gate: a dropped one would let a required output go unwaited.
		gate, err := artifactGate(q, e, anys(b["artifacts"]))
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
		if terminal(e) {
			if err := agentPromote(q); err != nil {
				return nil, "", err
			}
		}
	}
	ended := ""
	if terminal(e) || lost {
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
