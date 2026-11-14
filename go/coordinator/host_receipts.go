package main

// Host receipts (SL6), one route per envelope of contracts/host-protocol.schema.json: the status receipt that moves a
// command and its execution forward only (Receipt), an account inspection result (AccountReceipt), a project tool call
// of a native agent (ProjectReceipt) and a child agent request (ChildReceipt). Each body is type-checked against its
// struct first; unknown fields are refused.

import (
	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

func init() {
	longRoute("POST /api/hosts/receipts", func(q *Request) (any, error) {
		return typedReceiptLocks(q, receiptLocks, q.app.statusReceiptIn)
	})
	longRoute("POST /api/hosts/accounts/report", func(q *Request) (any, error) {
		return typedReceipt(q, noEnd(accountReceiptIn))
	})
	longRoute("POST /api/hosts/project-operations", func(q *Request) (any, error) {
		return typedReceipt(q, noEnd(projectReceiptIn))
	})
	longRoute("POST /api/hosts/delegations", func(q *Request) (any, error) {
		return typedReceipt(q, noEnd(q.app.childReceiptIn))
	})
}

func receiptLocks(b *contract.Receipt) []string {
	switch b.Status {
	case contract.ReceiptStatusCompleted, contract.ReceiptStatusFailed, contract.ReceiptStatusStopped:
		return []string{GLOBAL, DISPATCH}
	default:
		return []string{"execution:" + b.ExecutionId}
	}
}

// typedReceipt: a receipt route under GLOBAL.
func typedReceipt[T any, P interface {
	*T
	Validate() error
}](q *Request, fn func(q *Request, t *T) (any, string, error)) (any, error) {
	return typedReceiptLocks[T, P](q, nil, fn)
}

// typedReceiptLocks: decode/validate the closed contract struct, then run fn in one transaction acquiring
// canonical locks from locksFn (defaulting to GLOBAL); an execution fn names as ended starts its pipeline step.
func typedReceiptLocks[T any, P interface {
	*T
	Validate() error
}](q *Request, locksFn func(*T) []string, fn func(q *Request, t *T) (any, string, error)) (any, error) {
	t, err := decodeValid[T, P](q.body, true)
	if err != nil {
		return nil, err
	}
	locks := []string{GLOBAL}
	if locksFn != nil {
		locks = locksFn(t)
	}
	var out any
	var ended string
	err = q.app.store.TxLocks(q.ctx(), nil, true, locks, func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		var err error
		out, ended, err = fn(q, t)
		return err
	})
	if err != nil {
		return nil, err
	}
	if ended != "" {
		go pipelineOnExecution(q.app, ended)
	}
	return out, nil
}

func noEnd[T any](fn func(q *Request, t *T) (any, error)) func(q *Request, t *T) (any, string, error) {
	return func(q *Request, t *T) (any, string, error) { r, err := fn(q, t); return r, "", err }
}

// receiptState: the execution state a command receipt status reports ('unknown' reports none).
var receiptState = map[string]string{"accepted": "preparing", "started": "running", "completed": "succeeded", "failed": "failed", "stopped": "cancelled"}

var receiptRank = map[string]int{"pending": 0, string(contract.ReceiptStatusAccepted): 1, string(contract.ReceiptStatusStarted): 2, string(contract.ReceiptStatusUnknown): 2,
	string(contract.ReceiptStatusCompleted): 3, string(contract.ReceiptStatusFailed): 3, string(contract.ReceiptStatusStopped): 3} // pending: a command not yet received

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
	if operation == "connections_key_request" { // its question shows in the asking agent's conversation
		sub.agent = "execution:" + astr(agent["executionId"])
	}
	return op.h(sub)
}

// commandOf: the receipt's command, which must belong to the receipt's execution and host.
func commandOf(q *Request, hostID, executionID, commandID, msg string) (doc, error) {
	if err := ownedBody(q, hostID, executionID); err != nil {
		return nil, err
	}
	c, err := get(q, commandID, "command")
	if err != nil {
		return nil, err
	}
	if c["executionId"] != executionID || c["hostId"] != hostID {
		return nil, fail(403, msg)
	}
	return c, nil
}

// ownedBody: the execution named in a host request runs on the requesting host.
func ownedBody(q *Request, hostID, executionID string) error {
	if hostID == "" {
		return fail(403, "Host rejected")
	}
	if executionID != "" {
		e, err := get(q, executionID, "execution")
		if err != nil {
			return err
		}
		if e["hostId"] != hostID {
			return fail(403, "Execution rejected")
		}
	}
	return nil
}

// accountReceiptIn: POST /api/hosts/accounts/report (contract.AccountReceipt), an account-inspect command's result.
func accountReceiptIn(q *Request, b *contract.AccountReceipt) (any, error) {
	if (b.AccountObservation == nil) == (b.AccountCatalogue == nil) {
		return nil, fail(400, "Expected accountObservation or accountCatalogue")
	}
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Account inspection ownership rejected")
	if err != nil {
		return nil, err
	}
	p := mapOf(c["payload"])
	var accountID string
	var binding any
	if o := b.AccountObservation; o != nil {
		accountID, binding = o.AccountId, o.Binding
	} else if b.AccountCatalogue.Binding != nil {
		accountID, binding = b.AccountCatalogue.AccountId, *b.AccountCatalogue.Binding
	} else {
		accountID = b.AccountCatalogue.AccountId
	}
	if c["kind"] != string(contract.CommandKindAccountInspect) || p["accountId"] != accountID || p["binding"] != binding {
		return nil, fail(403, "Account inspection ownership rejected")
	}
	if b.AccountCatalogue != nil {
		return accountCatalogue(q, b.AccountCatalogue, astr(p["nativeHostId"]))
	}
	return accountReport(q, b.AccountObservation, astr(p["nativeHostId"]))
}

// projectReceiptIn: POST /api/hosts/project-operations (contract.ProjectReceipt), a project operation of a native agent.
func projectReceiptIn(q *Request, b *contract.ProjectReceipt) (any, error) {
	d := &b.Delegation
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Project grant rejected")
	if err != nil {
		return nil, err
	}
	s, err := get(q, c["sessionId"], "session")
	if err != nil {
		return nil, err
	}
	if s["projectId"] == nil || d.ProjectId != s["projectId"] {
		return nil, fail(403, "Project grant rejected")
	}
	args := doc{}
	for k, v := range docOf(d.Arguments) {
		args[k] = v
	}
	args["projectId"] = s["projectId"]
	return projectReceipt(q, string(d.Operation), args, doc{"kind": "native-agent", "executionId": b.ExecutionId, "sessionId": s["id"]})
}

// childReceiptIn: POST /api/hosts/delegations (contract.ChildReceipt), a child agent request.
func (a *App) childReceiptIn(q *Request, b *contract.ChildReceipt) (any, error) {
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
	if err != nil {
		return nil, err
	}
	return a.delegationRequest(q, &b.Delegation, c)
}

// statusReceiptIn: POST /api/hosts/receipts (contract.Receipt), a command's status.
func (a *App) statusReceiptIn(q *Request, b *contract.Receipt) (any, string, error) {
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
	if err != nil {
		return nil, "", err
	}
	status := string(b.Status) // an enum value: Validate refused any other
	cur := receiptRank[astr(c["status"])]
	if receiptRank[status] < cur || (cur == 3 && status != c["status"]) {
		return nil, "", fail(409, "Receipt regression")
	}
	if cur == 3 { // a repeated terminal receipt (an outbox retry) changes nothing: the gate and onReceipt ran once
		return contract.ReceiptReply{Ok: true}, "", nil
	}
	native := deref(b.NativeSessionId)
	hasNative := native != ""
	if hasNative {
		e, err := get(q, b.ExecutionId, "execution")
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
	e, err := get(q, b.ExecutionId, "execution")
	if err != nil {
		return nil, "", err
	}
	wasFinal := terminal(e)
	kind := c["kind"]
	lost := false
	confirmed := false
	if !wasFinal && kind != "steer" && (kind != "stop" || status == "stopped") {
		if s, ok := receiptState[status]; ok {
			e["state"], e["observation"] = s, "confirmed"
			confirmed = true
		} else { // 'unknown': the host lost track; the state stays, unconfirmed
			e["observation"], lost = "unconfirmed", true
		}
	}
	// A control acknowledgement is not execution evidence; an unknown stop cannot clear uncertainty.
	if !wasFinal && kind == "stop" && status == "unknown" {
		e["observation"], lost = "unconfirmed", true
	}
	e["updatedAt"] = nowMs()
	if status == "started" && e["startedAt"] == nil {
		e["startedAt"] = e["updatedAt"]
	}
	if kind != "steer" && (status == "completed" || status == "failed" || status == "stopped") && e["finishedAt"] == nil {
		e["finishedAt"] = e["updatedAt"]
	}
	if deref(b.Error) != "" {
		e["error"] = *b.Error
	}
	if (kind == "start" || kind == "script") && status == "completed" && !wasFinal {
		// Every declaration reaches the gate: a dropped one would let a required output go unwaited.
		gate, err := artifactGate(q, e, b.Artifacts)
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
	if confirmed {
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
			if txHasLock(q.tx, DISPATCH) || txHasLock(q.tx, GLOBAL) {
				if err := agentPromote(q); err != nil {
					return nil, "", err
				}
			} else {
				onEnd(q.tx, func(committed bool) {
					if committed {
						a.wakeDispatch()
					}
				})
			}
		}
	}
	if lost {
		onEnd(q.tx, func(committed bool) {
			if committed {
				a.wakeDispatch()
			}
		})
	}
	ended := ""
	if terminal(e) || lost {
		ended = astr(e["id"])
	}
	return contract.ReceiptReply{Ok: true}, ended, nil
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
