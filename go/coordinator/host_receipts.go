package main

// Host receipts (SL6), one route per envelope of contracts/host-protocol.schema.json: the status receipt that moves a
// command and its execution forward only (Receipt), an account inspection result (AccountReceipt), a project tool call
// of a native agent (ProjectReceipt) and a child agent request (ChildReceipt). Each body is type-checked against its
// struct first; unknown fields are refused.

import (
	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	hostRoute(contract.RouteReceipts, func(q *Request, b *contract.Receipt) (contract.ReceiptReply, error) {
		out, err := receiptTx(q, true, receiptLocks(b), func() (any, string, error) { return q.app.statusReceiptIn(q, b) })
		r, _ := out.(contract.ReceiptReply)
		return r, err
	})
	hostRoute(contract.RouteAccountsReport, func(q *Request, b *contract.AccountReceipt) (any, error) {
		return receiptTx(q, true, []string{GLOBAL}, func() (any, string, error) { r, err := accountReceiptIn(q, b); return r, "", err })
	})
	hostRoute(contract.RouteProjectOperations, projectReceiptRoute)
	hostRoute(contract.RouteDelegations, func(q *Request, b *contract.ChildReceipt) (any, error) {
		return receiptTx(q, true, []string{GLOBAL}, func() (any, string, error) { r, err := q.app.childReceiptIn(q, b); return r, "", err })
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

// receiptTx runs fn in one transaction: a write transaction acquiring locks, or a read transaction without advisory
// locks; an execution fn names as ended starts its pipeline step.
func receiptTx(q *Request, write bool, locks []string, fn func() (any, string, error)) (any, error) {
	var out any
	var ended string
	err := q.app.store.TxLocks(q.ctx(), nil, write, locks, func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		var err error
		out, ended, err = fn()
		return err
	})
	if err != nil {
		return nil, err
	}
	_ = ended // an ended step's run is woken by its executionTransition
	return out, nil
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
	op, ok := projectOperation(operation)
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := &Request{app: q.app, r: q.r, tx: q.tx, host: q.host, body: []byte(js(args))}
	if operation == "connections_key_request" { // its question shows in the asking agent's conversation
		sub.agent = "execution:" + astr(agent["executionId"])
	}
	if g := opGates[operation]; g != nil {
		if err := g(sub); err != nil {
			return nil, err
		}
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

// projectReceiptRoute: an operation registered with read=true (project_context, catalogue, ...) runs in a read
// transaction without GLOBAL, as on the ops and agent-tool paths; a slow read must not queue every host poll and receipt
// behind it. Every other operation keeps the GLOBAL write transaction.
func projectReceiptRoute(q *Request, b *contract.ProjectReceipt) (any, error) {
	op, ok := projectOperation(string(b.Delegation.Operation))
	read := ok && op.read
	locks := []string{GLOBAL}
	if read {
		locks = nil
	}
	return receiptTx(q, !read, locks, func() (any, string, error) {
		r, err := projectReceiptIn(q, b)
		return r, "", err
	})
}

// projectOperation: the registered operation a native agent's project operation runs (projects area first, then the
// shared operation table). account_list has its own native-agent path and is not registered here.
func projectOperation(name string) (registeredOp, bool) {
	if name == "account_list" {
		return registeredOp{}, false
	}
	if op, ok := projectOps[name]; ok {
		return op, true
	}
	op, ok := operations[name]
	return op, ok
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
	if txHasLock(q.tx, GLOBAL) {
		sids, err := q.db().HostReceiptSessions(q.ctx(), b.ExecutionId)
		if err != nil {
			return nil, "", err
		}
		locks := []string{"execution:" + b.ExecutionId}
		for _, sid := range sids {
			locks = append(locks, "session:"+sid)
		}
		if err := acquireTxLock(q, locks...); err != nil {
			return nil, "", err
		}
	}
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
	if err != nil {
		return nil, "", err
	}
	if err := lockExecutionTask(q, b.ExecutionId); err != nil {
		return nil, "", err
	}
	// Match executionStop's row lock so an overlapping started/accepted
	// receipt cannot overwrite a durable stop request.
	if err := q.db().HostReceiptLockExecution(q.ctx(), b.ExecutionId); err != nil {
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
	// Once an execution is terminal, withheld pending work no longer owns
	// a delivery or cleanup obligation. Keep started commands for late receipts.
	if e["taskStopRequested"] == true && terminal(e) {
		if _, err := commandStop(q, commandStopFilter{executionID: &b.ExecutionId, statuses: []db.CommandStatus{db.CommandStatusPending}, kinds: stopWithheldKinds}); err != nil {
			return nil, "", err
		}
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
