// Record access (all/get/put with the typed execution and task adapters, docs/designs/fleet-data-foundation.md §7):
// documents by kind, and the legacy kinds execution, question and delegation served from their typed tables.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// ms: a millisecond time of a record field as a timestamp, or nil.
func ms(v any) any {
	switch n := v.(type) {
	case float64:
		return time.UnixMilli(int64(n))
	case int64:
		return time.UnixMilli(n)
	case int:
		return time.UnixMilli(int64(n))
	}
	return nil
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

var typedTable = map[string]string{"execution": "execution", "question": "question"}

// all: the records of a kind whose data contains filter, in insertion order.
func all(q *Request, kind string, filter doc) ([]doc, error) {
	f := doc{}
	for k, v := range filter {
		if v != nil {
			f[k] = v
		}
	}
	switch kind {
	case "execution":
		return rows(q, "SELECT "+execJSON+" FROM execution WHERE "+execKinds+" AND data @> $1 ORDER BY seq", js(f))
	case "question":
		return rows(q, "SELECT data FROM question WHERE legacy_kind='question' AND data @> $1 ORDER BY seq", js(f))
	case "delegation":
		return rows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL AND delegation @> $1 ORDER BY seq", js(f))
	}
	return rows(q, "SELECT data FROM document WHERE kind=$1 AND data @> $2 ORDER BY seq", kind, js(f))
}

// get: one record of a kind; a missing or non-string id is not found (404).
func get(q *Request, anyID any, kind string) (doc, error) {
	id := astr(anyID)
	if id == "" {
		return nil, errNotFound
	}
	switch kind {
	case "execution":
		return getExecution(q, id)
	case "question":
		return one(q, "SELECT data FROM question WHERE id=$1 AND legacy_kind='question'", id)
	case "delegation":
		return one(q, "SELECT delegation FROM execution WHERE delegation->>'id'=$1", id)
	}
	return one(q, "SELECT data FROM document WHERE id=$1 AND kind=$2", id, kind)
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
	case "execution":
		return putExecution(q, v)
	case "question":
		return putLegacyQuestion(q, v)
	case "delegation":
		tag, err := q.tx.Exec(q.ctx(), "UPDATE execution SET delegation=$2,parent_execution_id=coalesce(parent_execution_id,$3) WHERE id=$1", v["childExecutionId"], js(v), v["parentExecutionId"])
		if err == nil && tag.RowsAffected() == 0 {
			return fail(409, fmt.Sprint("Delegation child execution not found: ", v["childExecutionId"]))
		}
		return err
	}
	return putDocument(q, kind, v)
}

// execJSON: an execution record as the API shows it: the typed state and observation over the open remainder in data.
const execJSON = "data || jsonb_build_object('state',state,'observation',observation)"

var terminalTyped = map[string]bool{"succeeded": true, "failed": true, "cancelled": true}

// execKinds: the execution kinds served as 'execution' records (host-dispatched agent runs and host jobs).
const execKinds = "kind IN ('agent_run','host_job')"

// putExecution: the one writer of an execution record (kind agent_run or host_job). state is the typed execution state
// (stateOrder); observation is 'confirmed' unless the record says 'unconfirmed'. Both live in their columns only, then
// the record's task settles.
func putExecution(q *Request, e doc) error {
	id := astr(e["id"])
	var prev string
	if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce((SELECT state::text FROM execution WHERE id=$1),'')", id).Scan(&prev); err != nil {
		return err
	}
	state := firstString(e["state"], prev, "preparing")
	if _, ok := stateOrder[state]; !ok {
		return fmt.Errorf("execution %s: unknown state %q", id, state)
	}
	observation := "confirmed"
	if e["observation"] == "unconfirmed" {
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
		other, err := has(q, "SELECT 1 FROM execution WHERE session_id=$1 AND id<>$2 AND "+execKinds, astr(sess["id"]), id)
		if err != nil {
			return err
		}
		if !other {
			parent = astr(fo["executionId"])
		}
	}
	project := firstString(e["projectId"], sess["projectId"])
	task := firstString(e["deliveryId"], sess["deliveryId"], sess["taskId"])
	ac := mapOf(e["agentConfig"])
	harness := any(nil)
	if kind == "agent_run" {
		harness = harnessOf(e)
	}
	variant := any(nil)
	if e["experimentId"] != nil {
		variant = nilIf(astr(ac["id"]))
	}
	account := firstString(mapOf(e["effectiveAccount"])["accountId"], e["accountId"])
	cfg := js(doc{"profileId": nilIf(astr(ac["id"])), "profileSha256": e["profileSha256"], "effort": e["effort"]})
	data := doc{}
	for k, v := range e {
		if k != "state" && k != "observation" {
			data[k] = v
		}
	}
	err = exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,harness,model,config_ref,started_at,ended_at,data)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
	 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,task_id=coalesce(execution.task_id,excluded.task_id),
	  experiment_id=excluded.experiment_id,variant=excluded.variant,parent_execution_id=excluded.parent_execution_id,session_id=excluded.session_id,account_id=excluded.account_id,host_id=excluded.host_id,
	  harness=excluded.harness,model=excluded.model,config_ref=excluded.config_ref,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,
	  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
		id, kind, state, observation, nilIf(project), nilIf(task), e["experimentId"], variant, nilIf(parent), e["sessionId"], nilIf(account), e["hostId"], harness, e["model"], cfg, ms(e["startedAt"]), ms(e["finishedAt"]), js(data))
	if err != nil {
		return err
	}
	if d := astr(e["directedBy"]); d != "" {
		if err := exec(q, "UPDATE execution SET directed_by=$2 WHERE id=$1 AND directed_by IS DISTINCT FROM $2", id, d); err != nil {
			return err
		}
	}
	if terminalTyped[state] && !terminalTyped[prev] {
		if err := writeTotals(q, id); err != nil {
			return err
		}
		if prev != "" {
			if err := writeEnded(q, id); err != nil {
				return err
			}
		}
	}
	var t *string
	q.tx.QueryRow(q.ctx(), "SELECT e.task_id FROM execution e JOIN task k ON k.id=e.task_id WHERE e.id=$1", id).Scan(&t)
	if t != nil {
		_, err = settleTask(q, *t, "")
	}
	return err
}

func writeTotals(q *Request, id string) error {
	if err := exec(q, `INSERT INTO execution_totals(execution_id,project_id,wall_ms,wall_ms_basis,computed_at)
	 SELECT id,project_id,CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL THEN (extract(epoch FROM ended_at-started_at)*1000)::bigint END,CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL THEN 'measured' ELSE 'unavailable' END,now() FROM execution WHERE id=$1
	 ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,wall_ms=excluded.wall_ms,wall_ms_basis=excluded.wall_ms_basis,computed_at=excluded.computed_at`, id); err != nil {
		return err
	}
	return exec(q, `UPDATE execution_totals SET rate_limit_last=r.data->'data',rate_limit_last_basis='measured' FROM (SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND data->>'type'='limit_reading' ORDER BY seq DESC LIMIT 1) r WHERE execution_id=$1`, id)
}

// writeEnded: the end of an agent run in its thread, one event with a fixed id.
func writeEnded(q *Request, id string) error {
	return exec(q, `INSERT INTO document(kind,id,data,project_id) SELECT 'event','ended:'||id,jsonb_build_object('id','ended:'||id,'executionId',id,'sessionId',session_id,'type','execution.ended',
	 'timestamp',(extract(epoch FROM coalesce(ended_at,now()))*1000)::bigint,'data',jsonb_build_object('fleetNormalized',jsonb_build_object('blocks','[]'::jsonb))),project_id
	 FROM execution WHERE id=$1 AND kind='agent_run' ON CONFLICT(id) DO NOTHING`, id)
}

// putLegacyQuestion: an agent question record ('question') kept in the question table.
func putLegacyQuestion(q *Request, v doc) error {
	id := astr(v["id"])
	found, err := has(q, "SELECT 1 FROM question WHERE id=$1", id)
	if err != nil {
		return err
	}
	answered := astr(v["state"]) == "answered"
	if !found {
		askedBy := "agent"
		if e := astr(v["executionId"]); e != "" {
			askedBy = "execution:" + e
		}
		if _, err := askQuestion(q, doc{"id": id, "kind": "agent_question", "text": v["question"], "projectId": v["projectId"], "executionId": v["executionId"], "sessionId": v["sessionId"],
			"askedBy": askedBy, "legacyKind": "question", "data": v, "askedAt": v["createdAt"]}); err != nil {
			return err
		}
		if answered {
			by := firstString(v["answeredBy"], "operator")
			return exec(q, "UPDATE question SET state='answered',answer=$2,answered_by=$3,answered_at=$4 WHERE id=$1", id, v["answer"], by, ms(v["answeredAt"]))
		}
		return nil
	}
	if answered {
		if _, err := answerQuestion(q, id, astr(v["answer"]), firstString(v["answeredBy"], "operator"), nil); err != nil {
			return err
		}
	}
	return exec(q, "UPDATE question SET data=$2 WHERE id=$1", id, js(v))
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
func terminal(e doc) bool { return terminalTyped[astr(e["state"])] }

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

// putDocument writes a document row (put routes every non-typed kind here).
func putDocument(q *Request, kind string, v doc) error {
	if kind == "setting-change" && q.agent != "" { // an agent's change: the execution acts for its director (fleet-pm-agent.md §1)
		v["actor"], v["onBehalfOf"] = q.agent, q.person.Email
	}
	b, _ := json.Marshal(v)
	var project any
	if p, ok := v["projectId"].(string); ok {
		project = p
	}
	_, err := q.tx.Exec(q.ctx(), "INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind", kind, v["id"], b, project)
	return err
}

func getExecution(q *Request, id string) (doc, error) {
	return one(q, "SELECT "+execJSON+" FROM execution WHERE id=$1 AND "+execKinds, id)
}

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

func hostOf(q *Request) string { return q.host["hostId"].(string) }

// owned: the execution named in a host request runs on the requesting host.
func owned(q *Request, executionID *string) error {
	if executionID == nil || *executionID == "" {
		return nil
	}
	e, err := getExecution(q, *executionID)
	if err != nil {
		return err
	}
	if h, _ := e["hostId"].(string); h != hostOf(q) {
		return fail(403, "Execution rejected")
	}
	return nil
}
