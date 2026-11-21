// Records (docs/designs/fleet-data-foundation.md §7, fleet-standard.md S2): the record shape of the conversation, host
// and account kinds (queries/records.sql) and of executions, questions and delegations (queries/entities.sql), read and
// written through each kind's typed queries. A caller names the kind's recTable and its own type, and converts once at
// the call: recGet, recLocked, recMaybe, recAll, recPut.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// recTable: one record kind's queries. get and locked read a record by id (locked: FOR UPDATE), all the records whose
// record contains the filter (a JSON object), in insertion order; put writes a record and may complete it (a trace).
type recTable struct {
	kind   string
	get    func(q *Request, id string) ([]byte, error)
	locked func(q *Request, id string) ([]byte, error)
	all    func(q *Request, filter []byte) ([][]byte, error)
	put    func(q *Request, rec rawObject) error
}

// recQ: a typed query as a call on the request's transaction.
func recQ[A, R any](f func(*db.Queries, context.Context, A) (R, error)) func(*Request, A) (R, error) {
	return func(q *Request, a A) (R, error) { return f(q.db(), q.ctx(), a) }
}

// recW: a record write (queries/records.sql): the record needs an id; wake: a new or changed record of the kind may let a
// queued run start (queued messages, hosts, subscriptions).
func recW(kind string, f func(*db.Queries, context.Context, []byte) error, wake bool) func(*Request, rawObject) error {
	return func(q *Request, r rawObject) error {
		if r.text("id") == "" {
			return fmt.Errorf("%s record without an id", kind)
		}
		if err := f(q.db(), q.ctx(), jsonBytes(r)); err != nil {
			return err
		}
		if wake && q.app != nil {
			afterCommit(q, func() { q.app.wakeDispatch() })
		}
		return nil
	}
}

// recStateful: a write of a kind whose state column is a state machine (host_states.go, key_states.go): the write
// leaves the state alone (a new row starts in the column's default, active), and a record naming another state is moved
// there by the machine's transition.
func recStateful(write func(*Request, rawObject) error, move func(q *Request, id, to string) error) func(*Request, rawObject) error {
	return func(q *Request, r rawObject) error {
		if err := write(q, r); err != nil {
			return err
		}
		if want := r.text("state"); want != "" {
			return move(q, r.text("id"), want)
		}
		return nil
	}
}

var (
	recSession = &recTable{kind: "session", get: recQ((*db.Queries).RecSessionGet), locked: recQ((*db.Queries).RecSessionGetLocked),
		all: recQ((*db.Queries).RecSessionAll), put: recW("session", (*db.Queries).RecSessionPut, false)}
	recMessage = &recTable{kind: "message", get: recQ((*db.Queries).RecMessageGet), all: recQ((*db.Queries).RecMessageAll),
		put: recW("message", (*db.Queries).RecMessagePut, false)}
	recEvent         = &recTable{kind: "event", all: recQ((*db.Queries).RecEventAll)}
	recEventAny      = &recTable{kind: "event", all: recQ((*db.Queries).RecEventAnyAll)} // both kinds, the deltas too
	recQueuedMessage = &recTable{kind: "queued-message", get: recQ((*db.Queries).RecQueuedMessageGet), all: recQ((*db.Queries).RecQueuedMessageAll),
		put: queuedPut}
	recCommand = &recTable{kind: "command", get: recQ((*db.Queries).RecCommandGet), all: recQ((*db.Queries).RecCommandAll), put: commandRecPut}
	recHandoff = &recTable{kind: "handoff", get: recQ((*db.Queries).RecHandoffGet), all: recQ((*db.Queries).RecHandoffAll),
		put: recW("handoff", (*db.Queries).RecHandoffPut, false)}
	recSessionFork = &recTable{kind: "session-fork", get: recQ((*db.Queries).RecSessionForkGet), all: recQ((*db.Queries).RecSessionForkAll),
		put: recW("session-fork", (*db.Queries).RecSessionForkPut, false)}
	recSessionTransition = &recTable{kind: "session-transition", get: recQ((*db.Queries).RecSessionTransitionGet),
		all: recQ((*db.Queries).RecSessionTransitionAll), put: recW("session-transition", (*db.Queries).RecSessionTransitionPut, false)}
	recWorkerDone = &recTable{kind: "worker-done", get: recQ((*db.Queries).RecWorkerDoneGet), all: recQ((*db.Queries).RecWorkerDoneAll),
		put: recW("worker-done", (*db.Queries).RecWorkerDonePut, false)}
	recHost = &recTable{kind: "host", get: recQ((*db.Queries).RecHostGet), all: recQ((*db.Queries).RecHostAll),
		put: recStateful(recW("host", (*db.Queries).RecHostPut, true), hostTransition)}
	recHostObservation = &recTable{kind: "host-observation", get: recQ((*db.Queries).RecHostObservationGet), all: recQ((*db.Queries).RecHostObservationAll),
		put: recW("host-observation", (*db.Queries).RecHostObservationPut, false)}
	recManagedAccount = &recTable{kind: "managed-account", get: recQ((*db.Queries).RecManagedAccountGet), all: recQ((*db.Queries).RecManagedAccountAll),
		put: recW("managed-account", (*db.Queries).RecManagedAccountPut, true)}
	recAccountInspection = &recTable{kind: "account-inspection", get: recQ((*db.Queries).RecAccountInspectionGet),
		all: recQ((*db.Queries).RecAccountInspectionAll), put: recW("account-inspection", (*db.Queries).RecAccountInspectionPut, false)}
	recAccountCheck = &recTable{kind: "account-check", get: recQ((*db.Queries).RecAccountCheckGet), all: recQ((*db.Queries).RecAccountCheckAll),
		put: recW("account-check", (*db.Queries).RecAccountCheckPut, false)}
	recAccountCatalogue = &recTable{kind: "account-catalogue", get: recQ((*db.Queries).RecAccountCatalogueGet), all: recQ((*db.Queries).RecAccountCatalogueAll),
		put: recW("account-catalogue", (*db.Queries).RecAccountCataloguePut, false)}
	recAPIConnection = &recTable{kind: "api-connection", get: recQ((*db.Queries).RecApiConnectionGet), locked: recQ((*db.Queries).RecApiConnectionGetLocked),
		all: recQ((*db.Queries).RecApiConnectionAll), put: recW("api-connection", (*db.Queries).RecApiConnectionPut, false)}
	recConnectionKey = &recTable{kind: "connection-key", get: recQ((*db.Queries).RecConnectionKeyGet), all: recQ((*db.Queries).RecConnectionKeyAll),
		put: recStateful(recW("connection-key", (*db.Queries).RecConnectionKeyPut, false), connectionKeyTransition)}
	recRunToken = &recTable{kind: "run-token", get: recQ((*db.Queries).RecRunTokenGet), all: recQ((*db.Queries).RecRunTokenAll),
		put: recW("run-token", (*db.Queries).RecRunTokenPut, false)}
	recExecution = &recTable{kind: "execution", get: recQ((*db.Queries).EntExecutionRecord), all: recQ((*db.Queries).EntExecutionRecords),
		put: func(q *Request, r rawObject) error { return putExecution(q, r) }}
	recQuestion = &recTable{kind: "question", get: recQ((*db.Queries).EntAgentQuestion), all: agentQuestions,
		put: func(q *Request, r rawObject) error { return putQuestion(q, r) }}
	recDelegation = &recTable{kind: "delegation", get: recQ((*db.Queries).EntDelegationRecord), all: recQ((*db.Queries).EntDelegationRecords),
		put: putDelegation}
)

func recDecode[T any](b []byte) (T, error) {
	var t T
	err := json.Unmarshal(b, &t)
	return t, err
}

func recRead[T any](q *Request, read func(*Request, string) ([]byte, error), id string) (T, error) {
	var none T
	if id == "" {
		return none, errNotFound
	}
	b, err := read(q, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, errNotFound
	}
	if err != nil {
		return none, err
	}
	return recDecode[T](b)
}

// recGet: one record of a kind as T; a missing or empty id is not found (404).
func recGet[T any](q *Request, t *recTable, id string) (T, error) { return recRead[T](q, t.get, id) }

// recLocked: recGet, the row locked for update.
func recLocked[T any](q *Request, t *recTable, id string) (T, error) {
	return recRead[T](q, t.locked, id)
}

// recMaybe: recGet, nil when absent.
func recMaybe[T any](q *Request, t *recTable, id string) (*T, error) {
	v, err := recGet[T](q, t, id)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// recAll: the records of a kind whose fields equal filter's (a struct of omitempty fields; a null field filters nothing).
func recAll[T any](q *Request, t *recTable, filter any) ([]T, error) {
	f := rawObject{}
	if filter != nil {
		if err := json.Unmarshal(jsonBytes(filter), &f); err != nil {
			return nil, fmt.Errorf("%s filter: %w", t.kind, err)
		}
	}
	for k, v := range f {
		if string(v) == "null" {
			delete(f, k)
		}
	}
	if f == nil {
		f = rawObject{}
	}
	bs, err := t.all(q, jsonBytes(f))
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(bs))
	for _, b := range bs {
		v, err := recDecode[T](b)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// recPut writes *v as a record of the kind and reads back what the write completed (a trace, a default) into it.
func recPut[T any](q *Request, t *recTable, v *T) error {
	var r rawObject
	if err := json.Unmarshal(jsonBytes(*v), &r); err != nil || r == nil {
		return fmt.Errorf("%s record is not an object", t.kind)
	}
	if err := t.put(q, r); err != nil {
		return err
	}
	return json.Unmarshal(jsonBytes(r), v)
}

// queuedPut: a queued message keeps the trace of the request that queued it.
func queuedPut(q *Request, r rawObject) error {
	if !validTraceparent(r.text("traceparent")) {
		r["traceparent"], r["traceActor"] = jsonBytes(originTrace(q)), jsonBytes(actor(q, "fleet"))
	}
	return recW("queued-message", (*db.Queries).RecQueuedMessagePut, true)(q, r)
}

// commandCols: the command record fields with a column (queries/records.sql); payload holds the others.
var commandCols = map[string]bool{"id": true, "executionId": true, "sessionId": true, "hostId": true, "projectId": true, "kind": true,
	"status": true, "stopDetail": true, "handedOut": true, "traceparent": true}

// commandRecPut: a command record through its state owner (commands.go): a new command is created with its status, an
// existing one keeps its status unless the record names another, which commandTransition moves it to.
func commandRecPut(q *Request, r rawObject) error {
	id := r.text("id")
	if id == "" {
		return fmt.Errorf("command record without an id")
	}
	if err := commandTrace(q, r); err != nil {
		return err
	}
	rest := rawObject{}
	for k, x := range r {
		if !commandCols[k] {
			rest[k] = x
		}
	}
	kind, status := r.ptr("kind"), r.ptr("status")
	if kind == nil {
		return fmt.Errorf("command %s without a kind", id)
	}
	prev, err := commandStatus(q, id)
	if err != nil {
		return err
	}
	if status == nil {
		p := firstString(prev, "pending")
		status = &p
	}
	if _, ok := commandTransitions[*status]; !ok {
		return fmt.Errorf("command %s: unknown status %q", id, *status)
	}
	var handed *bool
	if b, ok := r.val("handedOut").(bool); ok {
		handed = &b
	}
	if err := commandCreate(q, db.CommandUpsertParams{ID: id, ExecutionID: r.ptr("executionId"), SessionID: r.ptr("sessionId"), HostID: r.ptr("hostId"),
		ProjectID: r.ptr("projectId"), Kind: *kind, Status: db.CommandStatus(*status), StopDetail: r.ptr("stopDetail"), HandedOut: handed,
		Traceparent: r.ptr("traceparent"), Payload: jsonBytes(rest)}); err != nil {
		return err
	}
	if prev != "" && prev != *status {
		if err := commandTransition(q, id, *status, r.ptr("stopDetail")); err != nil {
			return err
		}
	} else if prev != "" {
		if err := commandDetail(q, id, r.ptr("stopDetail")); err != nil {
			return err
		}
	}
	if hid := r.ptr("hostId"); *status == "pending" && hid != nil && q.app != nil {
		afterCommit(q, func() { q.app.wakeHost(*hid) })
	}
	return nil
}

// putDelegation: a delegation record, keyed by its child execution.
func putDelegation(q *Request, r rawObject) error {
	child := r.text("childExecutionId")
	n, err := q.db().EntDelegationPut(q.ctx(), db.EntDelegationPutParams{ID: child, Delegation: jsonBytes(r), ParentExecutionID: r.ptr("parentExecutionId")})
	if err == nil && n == 0 {
		return fail(409, "Delegation child execution not found: "+child)
	}
	return err
}

// ptr: field k as a string, nil when absent, null or not a string.
func (r rawObject) ptr(k string) *string {
	if s, ok := r.str(k); ok {
		return &s
	}
	return nil
}

func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// definitionScalars extracts typed agent profile and pipeline definition identifiers for indexed columns; profile is the
// profile id when the definitions name none.
func definitionScalars(defs any, profile string) (profileID, profileRev, pipelineID, pipelineRev any) {
	var items []rawObject
	if b := jsonBytes(defs); len(b) > 0 {
		_ = json.Unmarshal(b, &items)
	}
	for _, d := range items {
		id := d.text("id")
		if id == "" {
			continue
		}
		rev, hasRev := intOK(d.val("revision"))
		switch d.text("kind") {
		case "agent":
			if profileID == nil {
				profileID = id
				if hasRev {
					profileRev = rev
				}
			}
		case "pipeline":
			if pipelineID == nil {
				pipelineID = id
				if hasRev {
					pipelineRev = rev
				}
			}
		}
	}
	if profileID == nil && profile != "" {
		profileID = profile
	}
	return profileID, profileRev, pipelineID, pipelineRev
}

// executionConfig: an execution's config_ref (its keys sorted, as stored).
type executionConfig struct {
	Effort        json.RawMessage `json:"effort"`
	ProfileID     *string         `json:"profileId"`
	ProfileSha256 json.RawMessage `json:"profileSha256"`
}

// putExecution: the one writer of an execution record (kind agent_run or host_job). state is the typed execution state
// (executionTransitions, moved by executionTransition); observation stays 'unconfirmed' only while the state does not move. kind is fixed by the first write. Both live in their columns only, then
// the record's task settles.
func putExecution(q *Request, e rawObject) error {
	id := e.text("id")
	prev, err := q.db().EntExecutionStateOf(q.ctx(), id)
	if err != nil {
		return err
	}
	state := firstString(e.text("state"), prev, "preparing")
	if _, ok := executionTransitions[state]; !ok {
		return fmt.Errorf("execution %s: unknown state %q", id, state)
	}
	observation := "confirmed" // a writer that moves the state knows it
	if e.text("observation") == "unconfirmed" && state == prev {
		observation = "unconfirmed"
	}
	kind := "agent_run"
	if e.text("actor") == "program-worker" {
		kind = "host_job"
	}
	sp, err := recMaybe[rawObject](q, recSession, e.text("sessionId"))
	if err != nil {
		return err
	}
	sess := rawObject{}
	if sp != nil {
		sess = *sp
	}
	handoff, err := recMaybe[agentHandoff](q, recHandoff, e.text("handoffId"))
	if err != nil {
		return err
	}
	parent := firstString(e.text("parentExecutionId"), e.text("continuationOf"), e.text("pipelineStepId"), e.text("pipelineRunId"))
	if parent == "" && handoff != nil && len(handoff.Turns) > 0 {
		parent = handoff.Turns[len(handoff.Turns)-1]
	}
	if parent == "" {
		parent = sess.text("createdByExecutionId")
	}
	if fo := sess.object("forkOf"); parent == "" && fo.text("executionId") != "" {
		other, err := q.db().EntSessionHasOtherRun(q.ctx(), db.EntSessionHasOtherRunParams{SessionID: ptr(sess.text("id")), ID: id})
		if err != nil {
			return err
		}
		if !other {
			parent = fo.text("executionId")
		}
	}
	if prev == "" && !validTraceparent(e.text("traceparent")) {
		tp, err := executionOrigin(q, parent)
		if err != nil {
			return err
		}
		e["traceparent"], e["traceActor"] = jsonBytes(tp), jsonBytes(actor(q, "fleet"))
	}
	project := firstString(e.text("projectId"), sess.text("projectId"))
	task := firstString(e.text("deliveryId"), sess.text("deliveryId"), sess.text("taskId"))
	profile := e.object("agentConfig").text("id")
	harness := ""
	if kind == "agent_run" {
		harness = harnessOfProfile(e["agentConfig"])
	}
	experiment, variant, err := executionLabContext(q, parent, project, e.val("experimentId"), e.val("variant"))
	if err != nil {
		return err
	}
	account := firstString(e.object("effectiveAccount").text("accountId"), e.text("accountId"))
	cfg := jsonBytes(executionConfig{Effort: e["effort"], ProfileID: strPtr(profile), ProfileSha256: e["profileSha256"]})
	profileID, profileRev, pipelineID, pipelineRev := definitionScalars(e.val("definitions"), profile)
	stepID := nilIf(e.text("stepId"))
	if stepRowID := firstString(e.text("pipelineStepId"), e.text("pipelineRunId")); stepRowID != "" && (pipelineID == nil || stepID == nil) {
		p, err := q.db().EntExecutionPipelineRef(q.ctx(), stepRowID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		pPipe, pRev, pStep := p.PipelineID, p.PipelineRevision, p.StepID
		if pipelineID == nil && pPipe != nil {
			pipelineID = *pPipe
			if pRev != nil {
				pipelineRev = *pRev
			}
		}
		if stepID == nil && pStep != nil {
			stepID = *pStep
		}
	}
	data := rawObject{}
	for k, v := range e {
		if k != "state" && k != "observation" {
			data[k] = v
		}
	}
	directed := firstString(e.text("directedBy"), sess.text("directedBy"))
	inserted := false
	if prev == "" {
		obs := db.Observation(observation)
		inserted, err = executionInsert(q, db.ExecutionInsertParams{ID: id, Kind: db.ExecutionKind(kind), State: db.ExecutionState(state), Observation: &obs,
			ProjectID: strPtr(project), TaskID: strPtr(task), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: strPtr(parent),
			SessionID: strPtr(e.val("sessionId")), AccountID: strPtr(account), HostID: strPtr(e.val("hostId")), Harness: strPtr(harness), Model: strPtr(e.val("model")),
			ConfigRef: cfg, StartedAt: nullTime(e.val("startedAt")), EndedAt: nullTime(e.val("finishedAt")), Data: jsonBytes(data), DirectedBy: strPtr(directed),
			ProfileID: strPtr(profileID), ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev), StepID: strPtr(stepID)})
		if err != nil {
			return err
		}
	}
	if !inserted {
		if err := q.db().EntExecutionUpdate(q.ctx(), db.EntExecutionUpdateParams{ID: id, Observation: db.Observation(observation), ProjectID: strPtr(project),
			TaskID: strPtr(task), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: strPtr(parent), SessionID: e.ptr("sessionId"),
			AccountID: strPtr(account), HostID: e.ptr("hostId"), Harness: strPtr(harness), Model: e.ptr("model"), ConfigRef: cfg,
			StartedAt: nullTime(e.val("startedAt")), EndedAt: nullTime(e.val("finishedAt")), Data: jsonBytes(data), DirectedBy: strPtr(directed), ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev), StepID: strPtr(stepID)}); err != nil {
			return err
		}
	}
	if e.has("definitions") {
		if err := q.db().EntExecutionDefinitions(q.ctx(), db.EntExecutionDefinitionsParams{ID: id, Definitions: e["definitions"], ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev)}); err != nil {
			return err
		}
	}
	if d := e.text("directedBy"); d != "" {
		if err := q.db().EntExecutionDirectedBy(q.ctx(), db.EntExecutionDirectedByParams{ID: id, DirectedBy: d}); err != nil {
			return err
		}
	}
	if !inserted {
		if state != prev {
			if _, err := executionTransition(q, id, executionEvent{To: state, KeepEnd: true}); err != nil {
				return err
			}
		}
	} else if executionEnds(state) {
		if err := writeTotals(q, id); err != nil {
			return err
		}
	}
	t, err := q.db().EntExecutionTaskOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		t, err = nil, nil
	}
	if err != nil {
		return err
	}
	if t != nil {
		if prev == "" && !executionEnds(state) { // a new run
			if err := withdrawIdleDecision(q, *t); err != nil {
				return err
			}
		}
		if _, err = settleTask(q, *t, ""); err == nil && executionEnds(state) && !executionEnds(prev) {
			_, err = settleIdleTask(q, *t)
		}
	}
	if err == nil && q.app != nil {
		needDispatch := state == "preparing" && !e.has("hostId") && kind == "agent_run"
		newlySucceeded := state == "succeeded" && !executionEnds(prev)
		if needDispatch || newlySucceeded {
			onEnd(q.tx, func(committed bool) {
				if !committed {
					return
				}
				q.app.wakeDispatch()
				if newlySucceeded {
					q.app.wakeAllHosts()
				}
			})
		}
	}
	return err
}

// lockExecutionForTotals establishes parent-before-totals row order for every totals writer.
// Execution ownership is authoritative, including a projectless NULL owner.
func lockExecutionForTotals(q *Request, id string) (string, error) {
	kind, err := q.db().EntExecutionLockKind(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// writeConfirmedUsage projects accepted native usage without inventing completion or process resources.
func writeConfirmedUsage(q *Request, id string) error {
	kind, err := lockExecutionForTotals(q, id)
	if err != nil || kind == "" || kind == "pipeline_run" || kind == "pipeline_step" {
		return err
	}
	if err := q.db().EntTotalsConfirmedUsage(q.ctx(), id); err != nil {
		return err
	}
	return writeUsage(q, id)
}

// writeUsage: the model, tokens and native observations the harness reported, into the execution's totals row.
func writeUsage(q *Request, id string) error {
	if err := q.db().EntTotalsModel(q.ctx(), id); err != nil {
		return err
	}
	if err := q.db().EntTotalsTokens(q.ctx(), id); err != nil {
		return err
	}
	return q.db().EntTotalsNativeObservations(q.ctx(), id)
}

// writeTotals persists and merges leaf-execution totals so receipt and process.closed converge regardless of arrival order.
func writeTotals(q *Request, id string) error {
	kind, err := lockExecutionForTotals(q, id)
	if err != nil || kind == "" || kind == "pipeline_run" || kind == "pipeline_step" {
		return err
	}
	if err := q.db().EntTotalsWrite(q.ctx(), id); err != nil {
		return err
	}
	if err := writeUsage(q, id); err != nil {
		return err
	}
	if err := q.db().EntTotalsRateLimit(q.ctx(), id); err != nil {
		return err
	}
	return q.db().EntTotalsProcessClosed(q.ctx(), db.EntTotalsProcessClosedParams{ID: id, Kind: kind})
}

// writeEnded: the end of an agent run in its thread, one event with a fixed id.
func writeEnded(q *Request, id string) error {
	return q.db().EntExecutionEnded(q.ctx(), id)
}

// agentQuestions: the agent question records whose fields equal filter's, by column (executionId, sessionId, projectId,
// requestId, state).
func agentQuestions(q *Request, filter []byte) ([][]byte, error) {
	var f rawObject
	if err := json.Unmarshal(filter, &f); err != nil {
		return nil, err
	}
	var p db.EntAgentQuestionsParams
	cols := map[string]**string{"executionId": &p.ExecutionID, "sessionId": &p.SessionID, "projectId": &p.ProjectID, "requestId": &p.RequestID, "state": &p.State}
	for k := range f {
		col, ok := cols[k]
		s, text := f.str(k)
		if !ok || !text {
			return nil, fmt.Errorf("question filter on %s", k)
		}
		*col = &s
	}
	return q.db().EntAgentQuestions(q.ctx(), p)
}

// putQuestion: an agent question record ('question'); its fields live in the question columns, data keeps the rest.
func putQuestion(q *Request, v rawObject) error {
	id := v.text("id")
	found, err := q.db().EntQuestionExists(q.ctx(), id)
	if err != nil {
		return err
	}
	answered := v.text("state") == "answered"
	if !found {
		askedBy := "agent"
		if e := v.text("executionId"); e != "" {
			askedBy = "execution:" + e
		}
		a := questionAsk{ID: id, Kind: "agent_question", Text: v.text("question"), TaskID: v.text("taskId"), ProjectID: v.text("projectId"),
			ExecutionID: v.ptr("executionId"), SessionID: v.ptr("sessionId"), AskedBy: &askedBy, RequestID: v.ptr("requestId"), Data: jsonBytes(questionRest(v))}
		if t, ok := msTime(v.val("createdAt")).(time.Time); ok {
			a.AskedAt = &t
		}
		if _, err := askQuestionAs(q, a); err != nil {
			return err
		}
		if answered {
			by := firstString(v.text("answeredBy"), "operator")
			_, err := questionTransition(q, id, questionEvent{To: "answered", Answer: v.ptr("answer"), AnsweredBy: &by, AnsweredAt: nullTime(v.val("answeredAt"))})
			return err
		}
		return nil
	}
	if answered {
		if _, err := answerQuestion(q, id, v.text("answer"), firstString(v.text("answeredBy"), "operator"), nil); err != nil {
			return err
		}
	}
	return q.db().EntQuestionRest(q.ctx(), db.EntQuestionRestParams{ID: id, Data: jsonBytes(questionRest(v)), ProjectID: strPtr(v.val("projectId"))})
}

// agentQuestionFields: the record fields EntAgentQuestion builds from columns (queries/entities.sql) (migration 32 strips the same from data).
var agentQuestionFields = []string{"taskId", "id", "executionId", "sessionId", "projectId", "requestId", "question", "state", "answer", "answeredBy", "answeredAt", "createdAt"}

// questionRest: the agent question fields without a column.
func questionRest(v rawObject) rawObject {
	d := rawObject{}
	for k, x := range v {
		if !inList(agentQuestionFields, k) {
			d[k] = x
		}
	}
	return d
}

// body: the request body as its decoded JSON value, {} when it is none.
func body(q *Request) any {
	var b any
	json.Unmarshal(q.body, &b)
	if b == nil {
		return struct{}{}
	}
	return b
}

// terminal: an execution record in a final state.
func terminal(e doc) bool { s, _ := e["state"].(string); return executionEnds(s) }

// unconfirmed: the host has not confirmed the execution's state (a receipt 'unknown', a silent host).
func unconfirmed(e doc) bool { return e["observation"] == "unconfirmed" }

func exec(q *Request, sql string, args ...any) error {
	_, err := q.tx.Exec(q.ctx(), sql, args...)
	return err
}

// records: JSON records as read, in order; none is an empty list.
func records(bs [][]byte, err error) ([]doc, error) {
	out := []doc{}
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		var d doc
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// record: one JSON record as read; none is not found (404).
func record(b []byte, err error) (doc, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	var d doc
	return d, json.Unmarshal(b, &d)
}

func getExecution(q *Request, id string) (doc, error) { return recGet[doc](q, recExecution, id) }

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

// taskRow: the task's row as a record (column names); errNotFound when absent.
func taskRow(q *Request, id string) (doc, error) { return record(q.db().TaskRowRecord(q.ctx(), id)) }

// questionRecord: the question's row as a record (column names), as the answer checks and callers outside this file
// read it; nil when absent.
func questionRecord(q *Request, id string) (doc, error) {
	d, err := record(q.db().QuestionRowRecord(q.ctx(), id))
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return d, err
}

// recWrite writes v as a record of the kind, for a caller that does not read back what the write completed.
func recWrite(q *Request, t *recTable, v any) error { return recPut(q, t, &v) }

// recFind: recGet, the zero record (a nil map) when absent.
func recFind[T any](q *Request, t *recTable, id string) (T, error) {
	v, err := recGet[T](q, t, id)
	if errors.Is(err, errNotFound) {
		return v, nil
	}
	return v, err
}
