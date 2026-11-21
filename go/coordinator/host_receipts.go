package main

// Host receipts (SL6), one route per envelope of contracts/host-protocol.schema.json: the status receipt that moves a
// command and its execution forward only (Receipt), an account inspection result (AccountReceipt), a project tool call
// of a native agent (ProjectReceipt) and a child agent request (ChildReceipt). Each body is type-checked against its
// struct first; unknown fields are refused.

import (
	"encoding/json"
	"fleet/contract"
	"fleet/coordinator/db"
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
	run := func(_ *Request) error {
		var err error
		out, ended, err = fn()
		return err
	}
	var err error
	if write {
		err = q.app.store.Write(q.ctx(), q, locks, run)
	} else {
		err = q.app.store.Read(q.ctx(), q, run)
	}
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
// args: the operation's arguments, an open object that goes on to the named operation.
func projectReceipt(q *Request, operation string, args map[string]json.RawMessage, executionID string) (any, error) {
	if operation == "account_list" {
		project, _ := jsonText(args["projectId"])
		return agentAccountList(q, project, executionID)
	}
	op, ok := projectOperation(operation)
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := &Request{app: q.app, r: q.r, tx: q.tx, host: q.host, body: []byte(js(jsonValue(jsonBytes(args))))}
	if operation == "connections_key_request" { // its question shows in the asking agent's conversation
		sub.agent = "execution:" + executionID
	}
	if g := opGates[operation]; g != nil {
		if err := g(sub); err != nil {
			return nil, err
		}
	}
	return op.h(sub)
}

// commandOf: the receipt's command, which must belong to the receipt's execution and host.
func commandOf(q *Request, hostID, executionID, commandID, msg string) (agentCommand, error) {
	var none agentCommand
	if err := ownedBody(q, hostID, executionID); err != nil {
		return none, err
	}
	c, err := commandGet(q, commandID)
	if err != nil {
		return none, err
	}
	if c.ExecutionID != executionID || c.HostID != hostID {
		return none, fail(403, msg)
	}
	return c, nil
}

// ownedBody: the execution named in a host request runs on the requesting host.
func ownedBody(q *Request, hostID, executionID string) error {
	if hostID == "" {
		return fail(403, "Host rejected")
	}
	if executionID != "" {
		e, err := recGet[agentExecution](q, recExecution, executionID)
		if err != nil {
			return err
		}
		if deref(e.HostID) != hostID {
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
	var p struct {
		AccountID    *string `json:"accountId"`
		Binding      *string `json:"binding"`
		NativeHostID string  `json:"nativeHostId"`
	}
	json.Unmarshal(c.Payload, &p) // a field of another type reads as absent, and fails the match below
	var accountID string
	var binding *string
	if o := b.AccountObservation; o != nil {
		accountID, binding = o.AccountId, &o.Binding
	} else {
		accountID, binding = b.AccountCatalogue.AccountId, b.AccountCatalogue.Binding
	}
	sameBinding := (binding == nil) == (p.Binding == nil) && deref(binding) == deref(p.Binding)
	if c.Kind != string(contract.CommandKindAccountInspect) || p.AccountID == nil || *p.AccountID != accountID || !sameBinding {
		return nil, fail(403, "Account inspection ownership rejected")
	}
	if b.AccountCatalogue != nil {
		return accountCatalogue(q, b.AccountCatalogue, p.NativeHostID)
	}
	return accountReport(q, b.AccountObservation, p.NativeHostID)
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
	s, err := sessionByID(q, c.SessionID)
	if err != nil {
		return nil, err
	}
	if s.ProjectID == "" || d.ProjectId != s.ProjectID {
		return nil, fail(403, "Project grant rejected")
	}
	args := recOf(d.Arguments)
	args["projectId"] = jsonBytes(s.ProjectID)
	return projectReceipt(q, string(d.Operation), args, b.ExecutionId)
}

// childReceiptIn: POST /api/hosts/delegations (contract.ChildReceipt), a child agent request.
func (a *App) childReceiptIn(q *Request, b *contract.ChildReceipt) (any, error) {
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
	if err != nil {
		return nil, err
	}
	return a.delegationChild(q, &b.Delegation, c)
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
	cur := receiptRank[c.Status]
	if receiptRank[status] < cur || (cur == 3 && status != c.Status) {
		return nil, "", fail(409, "Receipt regression")
	}
	if cur == 3 { // a repeated terminal receipt (an outbox retry) changes nothing: the gate and onReceipt ran once
		return contract.ReceiptReply{Ok: true}, "", nil
	}
	native := deref(b.NativeSessionId)
	hasNative := native != ""
	if hasNative {
		e, err := recGet[agentExecution](q, recExecution, b.ExecutionId)
		if err != nil {
			return nil, "", err
		}
		known, err := agentKnownNative(q, &e)
		if err != nil {
			return nil, "", err
		}
		if known != "" && known != native {
			return nil, "", fail(409, "Native identity conflict")
		}
	}
	c.Status = status
	if err := commandPut(q, &c); err != nil {
		return nil, "", err
	}
	e, err := recGet[agentExecution](q, recExecution, b.ExecutionId)
	if err != nil {
		return nil, "", err
	}
	wasFinal := e.terminal()
	kind := c.Kind
	lost := false
	confirmed := false
	if !wasFinal && kind != "steer" && (kind != "stop" || status == "stopped") {
		if s, ok := receiptState[status]; ok {
			e.State, e.Observation = s, "confirmed"
			confirmed = true
		} else { // 'unknown': the host lost track; the state stays, unconfirmed
			e.Observation, lost = "unconfirmed", true
		}
	}
	// A control acknowledgement is not execution evidence; an unknown stop cannot clear uncertainty.
	if !wasFinal && kind == "stop" && status == "unknown" {
		e.Observation, lost = "unconfirmed", true
	}
	e.UpdatedAt = nowMs()
	if status == "started" && !jsonGiven(e.StartedAt) {
		e.StartedAt = jsonBytes(e.UpdatedAt)
	}
	if kind != "steer" && (status == "completed" || status == "failed" || status == "stopped") && !jsonGiven(e.FinishedAt) {
		e.FinishedAt = jsonBytes(e.UpdatedAt)
	}
	if deref(b.Error) != "" {
		e.Error = *b.Error
	}
	if (kind == "start" || kind == "script") && status == "completed" && !wasFinal {
		// Every declaration reaches the gate: a dropped one would let a required output go unwaited.
		gate, err := artifactGate(q, e.ID, b.Artifacts)
		if err != nil {
			return nil, "", err
		}
		var record rawObject // the gate sets fields agentExecution does not name (an error object, the wait)
		if err := json.Unmarshal(jsonBytes(e), &record); err != nil {
			return nil, "", err
		}
		for k, v := range gate {
			record[k] = v
		}
		if e, err = asT[agentExecution](record); err != nil {
			return nil, "", err
		}
	}
	if err := recPut(q, recExecution, &e); err != nil {
		return nil, "", err
	}
	// Once an execution is terminal, withheld pending work no longer owns
	// a delivery or cleanup obligation. Keep started commands for late receipts.
	if isTrue(e.TaskStopRequested) && e.terminal() {
		if _, err := commandStop(q, commandStopFilter{executionID: &b.ExecutionId, statuses: []db.CommandStatus{db.CommandStatusPending}, kinds: stopWithheldKinds}); err != nil {
			return nil, "", err
		}
	}
	if confirmed {
		if err := markObservation(q, e.ID, "confirmed"); err != nil {
			return nil, "", err
		}
	}
	if hasNative && kind == "start" {
		if len([]rune(native)) > 300 {
			return nil, "", fail(400, "Invalid text")
		}
		if err := agentCaptureNative(q, &e, native); err != nil {
			return nil, "", err
		}
	}
	if e.State != "waiting" {
		reported := status
		if e.State == "failed" && status == "completed" {
			reported = "failed"
		}
		if err := agentCommandReceipt(q, &c, &e, reported); err != nil {
			return nil, "", err
		}
		if e.terminal() {
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
	if e.terminal() || lost {
		ended = e.ID
	}
	return contract.ReceiptReply{Ok: true}, ended, nil
}

// agentAccountList: the subscriptions a native agent's work may use (its director's, or taken for its project), without
// host roots or bindings. Fixed: the TypeScript coordinator handed a native agent every subscription with every host's roots.
func agentAccountList(q *Request, project, executionID string) (any, error) {
	e, err := recGet[agentExecution](q, recExecution, executionID)
	if err != nil {
		return nil, err
	}
	accounts, err := accountsAll(q)
	if err != nil {
		return nil, err
	}
	// agentListedAccount: what a native agent sees of a subscription.
	type agentListedAccount struct {
		ID      string          `json:"id"`
		Name    *string         `json:"name"`
		Harness *string         `json:"harness"`
		Holder  *string         `json:"holder"`
		Intake  *string         `json:"intake"`
		Plan    json.RawMessage `json:"plan"`
	}
	out := []agentListedAccount{}
	for _, a := range accounts {
		if deref(a.Holder) != e.DirectedBy && !inList(a.takeProjects(), project) {
			continue
		}
		out = append(out, agentListedAccount{ID: a.ID, Name: a.Name, Harness: a.Harness, Holder: a.Holder, Intake: a.Intake, Plan: a.Plan})
	}
	return struct {
		Accounts []agentListedAccount `json:"accounts"`
	}{out}, nil
}
