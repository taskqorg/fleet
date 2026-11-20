// Record access (all/get/put with the typed execution and task adapters, docs/designs/fleet-data-foundation.md §7):
// documents by kind, and the legacy kinds execution, question and delegation served from their typed tables.
package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func exec(q *Request, sql string, args ...any) error {
	_, err := q.tx.Exec(q.ctx(), sql, args...)
	return err
}

// rowsOf: every row of sql as an object keyed by column name.
func rowsOf(q *Request, sql string, args ...any) ([]doc, error) {
	return rows(q, "SELECT to_jsonb(r) FROM ("+sql+") r", args...)
}

// first: the first row of sql, nil when none.
func first(q *Request, sql string, args ...any) (doc, error) {
	rs, err := rowsOf(q, sql, args...)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

func has(q *Request, sql string, args ...any) (bool, error) {
	var ok bool
	err := q.tx.QueryRow(q.ctx(), "SELECT EXISTS("+sql+")", args...).Scan(&ok)
	return ok, err
}

// all: the records of a kind whose data contains filter, in insertion order.
func all(q *Request, kind string, filter doc) ([]doc, error) {
	f := doc{}
	for k, v := range filter {
		if v != nil {
			f[k] = v
		}
	}
	if t := convTables[kind]; t != nil {
		return convAll(q, t, f)
	}
	switch kind {
	case "execution":
		return records(q.db().EntExecutionRecords(q.ctx(), jsonBytes(f)))
	case "question":
		return agentQuestions(q, f)
	case "delegation":
		return records(q.db().EntDelegationRecords(q.ctx(), jsonBytes(f)))
	}
	return nil, fmt.Errorf("unknown record kind %s", kind)
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

// get: one record of a kind; a missing or non-string id is not found (404).
func get(q *Request, anyID any, kind string) (doc, error) {
	id := astr(anyID)
	if id == "" {
		return nil, errNotFound
	}
	if t := convTables[kind]; t != nil {
		return convGet(q, t, id, false)
	}
	switch kind {
	case "execution":
		return getExecution(q, id)
	case "question":
		return record(q.db().EntAgentQuestion(q.ctx(), id))
	case "delegation":
		return record(q.db().EntDelegationRecord(q.ctx(), id))
	}
	return nil, fmt.Errorf("unknown record kind %s", kind)
}

func getForUpdate(q *Request, id, kind string) (doc, error) {
	if id == "" {
		return nil, errNotFound
	}
	if t := convTables[kind]; t != nil {
		return convGet(q, t, id, true)
	}
	return nil, fmt.Errorf("unknown record kind %s", kind)
}

// maybe: get, nil when absent.
func maybe(q *Request, id any, kind string) (doc, error) {
	sid, _ := id.(string)
	if sid == "" {
		return nil, nil
	}
	d, err := get(q, sid, kind)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return d, err
}

func put(q *Request, kind string, v doc) error {
	switch kind {
	case "command":
		if err := commandTrace(q, v); err != nil {
			return err
		}
	case "queued-message":
		if !validTraceparent(v["traceparent"]) {
			v["traceparent"] = originTrace(q)
			v["traceActor"] = actor(q, "fleet")
		}
	case "execution":
		return putExecution(q, v)
	case "question":
		return putQuestion(q, v)
	case "delegation":
		n, err := q.db().EntDelegationPut(q.ctx(), db.EntDelegationPutParams{ID: astr(v["childExecutionId"]), Delegation: jsonBytes(v), ParentExecutionID: strp(v, "parentExecutionId")})
		if err == nil && n == 0 {
			return fail(409, fmt.Sprint("Delegation child execution not found: ", v["childExecutionId"]))
		}
		return err
	}
	if t := convTables[kind]; t != nil {
		return convPut(q, kind, t, v)
	}
	return fmt.Errorf("unknown record kind %s", kind)
}

// execJSON: an execution record as the API shows it: the typed state, observation and last contact over the open remainder in data.
const execJSON = "data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at)"

// execKinds: the execution kinds served as 'execution' records (host-dispatched agent runs and host jobs).
const execKinds = "kind IN ('agent_run','host_job')"

// definitionScalars extracts typed agent profile and pipeline definition identifiers for indexed columns.
func definitionScalars(defs any, cfg doc) (profileID, profileRev, pipelineID, pipelineRev any) {
	if defs != nil {
		var items []map[string]any
		if b := jsonBytes(defs); len(b) > 0 {
			_ = json.Unmarshal(b, &items)
		}
		for _, d := range items {
			k := astr(d["kind"])
			id := astr(d["id"])
			if id == "" {
				continue
			}
			rev, hasRev := intOK(d["revision"])
			switch k {
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
	}
	if profileID == nil {
		if pid := astr(cfg["profileId"]); pid != "" {
			profileID = pid
		}
	}
	return profileID, profileRev, pipelineID, pipelineRev
}

// putExecution: the one writer of an execution record (kind agent_run or host_job). state is the typed execution state
// (executionTransitions, moved by executionTransition); observation stays 'unconfirmed' only while the state does not move. kind is fixed by the first write. Both live in their columns only, then
// the record's task settles.
func putExecution(q *Request, e doc) error {
	id := astr(e["id"])
	prev, err := q.db().EntExecutionStateOf(q.ctx(), id)
	if err != nil {
		return err
	}
	state := firstString(e["state"], prev, "preparing")
	if _, ok := executionTransitions[state]; !ok {
		return fmt.Errorf("execution %s: unknown state %q", id, state)
	}
	observation := "confirmed" // a writer that moves the state knows it
	if e["observation"] == "unconfirmed" && state == prev {
		observation = "unconfirmed"
	}
	kind := "agent_run"
	if astr(e["actor"]) == "program-worker" {
		kind = "host_job"
	}
	sess, err := maybe(q, e["sessionId"], "session")
	if err != nil {
		return err
	}
	if sess == nil {
		sess = doc{}
	}
	handoff, err := maybe(q, e["handoffId"], "handoff")
	if err != nil {
		return err
	}
	parent := firstString(e["parentExecutionId"], e["continuationOf"], e["pipelineStepId"], e["pipelineRunId"])
	if parent == "" && handoff != nil {
		if ts, _ := handoff["turns"].([]any); len(ts) > 0 {
			parent, _ = ts[len(ts)-1].(string)
		}
	}
	if parent == "" {
		parent = astr(sess["createdByExecutionId"])
	}
	if fo := mapOf(sess["forkOf"]); parent == "" && astr(fo["executionId"]) != "" {
		other, err := q.db().EntSessionHasOtherRun(q.ctx(), db.EntSessionHasOtherRunParams{SessionID: ptr(astr(sess["id"])), ID: id})
		if err != nil {
			return err
		}
		if !other {
			parent = astr(fo["executionId"])
		}
	}
	if prev == "" && !validTraceparent(e["traceparent"]) {
		tp, err := executionOrigin(q, parent)
		if err != nil {
			return err
		}
		e["traceparent"], e["traceActor"] = tp, actor(q, "fleet")
	}
	project := firstString(e["projectId"], sess["projectId"])
	task := firstString(e["deliveryId"], sess["deliveryId"], sess["taskId"])
	ac := mapOf(e["agentConfig"])
	harness := any(nil)
	if kind == "agent_run" {
		harness = harnessOf(e)
	}
	experiment, variant, err := executionLabContext(q, parent, project, e["experimentId"], e["variant"])
	if err != nil {
		return err
	}
	account := firstString(mapOf(e["effectiveAccount"])["accountId"], e["accountId"])
	cfg := js(doc{"profileId": nilIf(astr(ac["id"])), "profileSha256": e["profileSha256"], "effort": e["effort"]})
	profileID, profileRev, pipelineID, pipelineRev := definitionScalars(e["definitions"], doc{"profileId": nilIf(astr(ac["id"]))})
	stepID := nilIf(astr(e["stepId"]))
	if stepRowID := firstString(e["pipelineStepId"], e["pipelineRunId"]); stepRowID != "" && (pipelineID == nil || stepID == nil) {
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
	data := doc{}
	for k, v := range e {
		if k != "state" && k != "observation" {
			data[k] = v
		}
	}
	directed := nilIf(firstString(e["directedBy"], sess["directedBy"]))
	inserted := false
	if prev == "" {
		inserted, err = executionInsert(q, executionRow(doc{"id": id, "kind": kind, "state": state, "observation": observation, "projectId": nilIf(project), "taskId": nilIf(task),
			"experimentId": experiment, "variant": variant, "parentExecutionId": nilIf(parent), "sessionId": e["sessionId"], "accountId": nilIf(account), "hostId": e["hostId"],
			"harness": harness, "model": e["model"], "configRef": json.RawMessage(cfg), "startedAt": e["startedAt"], "endedAt": e["finishedAt"], "data": data,
			"directedBy": directed, "profileId": profileID, "profileRevision": profileRev, "pipelineId": pipelineID, "pipelineRevision": pipelineRev, "stepId": stepID}))
		if err != nil {
			return err
		}
	}
	if !inserted {
		if err := q.db().EntExecutionUpdate(q.ctx(), db.EntExecutionUpdateParams{ID: id, Observation: db.Observation(observation), ProjectID: strPtr(project),
			TaskID: strPtr(task), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: strPtr(parent), SessionID: strp(e, "sessionId"),
			AccountID: strPtr(account), HostID: strp(e, "hostId"), Harness: strPtr(harness), Model: strp(e, "model"), ConfigRef: []byte(cfg),
			StartedAt: nullTime(e["startedAt"]), EndedAt: nullTime(e["finishedAt"]), Data: jsonBytes(data), DirectedBy: strPtr(directed), ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev), StepID: strPtr(stepID)}); err != nil {
			return err
		}
	}
	if e["definitions"] != nil {
		if err := q.db().EntExecutionDefinitions(q.ctx(), db.EntExecutionDefinitionsParams{ID: id, Definitions: jsonBytes(e["definitions"]), ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev)}); err != nil {
			return err
		}
	}
	if d := astr(e["directedBy"]); d != "" {
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
		needDispatch := state == "preparing" && e["hostId"] == nil && kind == "agent_run"
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
func agentQuestions(q *Request, f doc) ([]doc, error) {
	var p db.EntAgentQuestionsParams
	cols := map[string]**string{"executionId": &p.ExecutionID, "sessionId": &p.SessionID, "projectId": &p.ProjectID, "requestId": &p.RequestID, "state": &p.State}
	for _, k := range sortedKeysOf(f) {
		col, ok := cols[k]
		s, text := f[k].(string)
		if !ok || !text {
			return nil, fmt.Errorf("question filter on %s", k)
		}
		*col = &s
	}
	return records(q.db().EntAgentQuestions(q.ctx(), p))
}

// putQuestion: an agent question record ('question'); its fields live in the question columns, data keeps the rest.
func putQuestion(q *Request, v doc) error {
	id := astr(v["id"])
	found, err := q.db().EntQuestionExists(q.ctx(), id)
	if err != nil {
		return err
	}
	answered := astr(v["state"]) == "answered"
	if !found {
		askedBy := "agent"
		if e := astr(v["executionId"]); e != "" {
			askedBy = "execution:" + e
		}
		if _, err := askQuestion(q, doc{"id": id, "kind": "agent_question", "text": v["question"], "taskId": v["taskId"], "projectId": v["projectId"], "executionId": v["executionId"], "sessionId": v["sessionId"],
			"askedBy": askedBy, "requestId": v["requestId"], "data": questionRest(v), "askedAt": v["createdAt"]}); err != nil {
			return err
		}
		if answered {
			by := firstString(v["answeredBy"], "operator")
			_, err := questionTransition(q, id, questionEvent{To: "answered", Answer: strp(v, "answer"), AnsweredBy: &by, AnsweredAt: nullTime(v["answeredAt"])})
			return err
		}
		return nil
	}
	if answered {
		if _, err := answerQuestion(q, id, astr(v["answer"]), firstString(v["answeredBy"], "operator"), nil); err != nil {
			return err
		}
	}
	return q.db().EntQuestionRest(q.ctx(), db.EntQuestionRestParams{ID: id, Data: jsonBytes(questionRest(v)), ProjectID: strPtr(v["projectId"])})
}

// agentQuestionFields: the record fields EntAgentQuestion builds from columns (queries/entities.sql) (migration 32 strips the same from data).
var agentQuestionFields = []string{"taskId", "id", "executionId", "sessionId", "projectId", "requestId", "question", "state", "answer", "answeredBy", "answeredAt", "createdAt"}

// questionRest: the agent question fields without a column.
func questionRest(v doc) doc {
	d := doc{}
	for k, x := range v {
		if !inList(agentQuestionFields, k) {
			d[k] = x
		}
	}
	return d
}

func strArg(b doc, k, name string, max int) (string, error) {
	v, ok := b[k].(string)
	if !ok || v == "" || (max > 0 && len([]rune(v)) > max) {
		return "", fail(400, "Expected "+name)
	}
	return v, nil
}

func body(q *Request) doc {
	var b doc
	json.Unmarshal(q.body, &b)
	if b == nil {
		b = doc{}
	}
	return b
}

// terminal: an execution record in a final state.
func terminal(e doc) bool { return executionEnds(astr(e["state"])) }

// unconfirmed: the host has not confirmed the execution's state (a receipt 'unknown', a silent host).
func unconfirmed(e doc) bool { return e["observation"] == "unconfirmed" }

func rows(q *Request, sql string, args ...any) ([]doc, error) {
	rs, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, func(r pgx.CollectableRow) (doc, error) {
		var d doc
		err := r.Scan(&d)
		return d, err
	})
	if out == nil {
		out = []doc{}
	}
	return out, err
}

func one(q *Request, sql string, args ...any) (doc, error) {
	ds, err := rows(q, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, errNotFound
	}
	return ds[0], nil
}

func getExecution(q *Request, id string) (doc, error) {
	return record(q.db().EntExecutionRecord(q.ctx(), id))
}

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

func hostOf(q *Request) string { return q.host["hostId"].(string) }

// taskRow: the task's row as a record (column names), for callers outside this file; errNotFound when absent.
func taskRow(q *Request, id string) (doc, error) {
	t, err := first(q, "SELECT * FROM task WHERE id=$1", id)
	if err == nil && t == nil {
		return nil, errNotFound
	}
	return t, err
}

// questionRecord: the question's row as a record (column names), as the answer checks and callers outside this file
// read it; nil when absent.
func questionRecord(q *Request, id string) (doc, error) {
	return first(q, "SELECT * FROM question WHERE id=$1", id)
}
