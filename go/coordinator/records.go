// Record access (all/get/put with the typed execution and task adapters, docs/designs/fleet-data-foundation.md §7):
// documents by kind, and the legacy kinds execution, question and delegation served from their typed tables.
package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
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
		return agentQuestions(q, f)
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
		return one(q, "SELECT "+agentQuestionJSON+" FROM question WHERE id=$1 AND kind='agent_question'", id)
	case "delegation":
		return one(q, "SELECT delegation FROM execution WHERE delegation->>'id'=$1", id)
	}
	return one(q, "SELECT data FROM document WHERE id=$1 AND kind=$2", id, kind)
}

func getForUpdate(q *Request, id, kind string) (doc, error) {
	if id == "" {
		return nil, errNotFound
	}
	return one(q, "SELECT data FROM document WHERE id=$1 AND kind=$2 FOR UPDATE", id, kind)
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
		return putQuestion(q, v)
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
// (stateOrder); observation stays 'unconfirmed' only while the state does not move. kind is fixed by the first write. Both live in their columns only, then
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
	profileID, profileRev, pipelineID, pipelineRev := definitionScalars(e["definitions"], doc{"profileId": nilIf(astr(ac["id"]))})
	stepID := nilIf(astr(e["stepId"]))
	if stepRowID := firstString(e["pipelineStepId"], e["pipelineRunId"]); stepRowID != "" && (pipelineID == nil || stepID == nil) {
		var pPipe, pStep *string
		var pRev *int64
		if err := q.tx.QueryRow(q.ctx(), "SELECT pipeline_id,pipeline_revision,step_id FROM execution WHERE id=$1", stepRowID).Scan(&pPipe, &pRev, &pStep); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
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
	err = exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,harness,model,config_ref,started_at,ended_at,data,directed_by,profile_id,profile_revision,pipeline_id,pipeline_revision,step_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
	 ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,task_id=coalesce(execution.task_id,excluded.task_id),
	  experiment_id=excluded.experiment_id,variant=excluded.variant,parent_execution_id=excluded.parent_execution_id,session_id=excluded.session_id,account_id=excluded.account_id,host_id=excluded.host_id,
	  harness=excluded.harness,model=excluded.model,config_ref=excluded.config_ref,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,
	  directed_by=coalesce(excluded.directed_by,execution.directed_by),profile_id=coalesce(excluded.profile_id,execution.profile_id),profile_revision=coalesce(excluded.profile_revision,execution.profile_revision),
	  pipeline_id=coalesce(excluded.pipeline_id,execution.pipeline_id),pipeline_revision=coalesce(excluded.pipeline_revision,execution.pipeline_revision),step_id=coalesce(excluded.step_id,execution.step_id),
	  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
		id, kind, state, observation, nilIf(project), nilIf(task), e["experimentId"], variant, nilIf(parent), e["sessionId"], nilIf(account), e["hostId"], harness, e["model"], cfg, msTime(e["startedAt"]), msTime(e["finishedAt"]), js(data), nilIf(firstString(e["directedBy"], sess["directedBy"])), profileID, profileRev, pipelineID, pipelineRev, stepID)
	if err != nil {
		return err
	}
	if e["definitions"] != nil {
		if err := exec(q, `UPDATE execution SET definitions=$2,profile_id=coalesce($3,profile_id),profile_revision=coalesce($4,profile_revision),
		 pipeline_id=coalesce($5,pipeline_id),pipeline_revision=coalesce($6,pipeline_revision) WHERE id=$1`,
			id, jsonBytes(e["definitions"]), profileID, profileRev, pipelineID, pipelineRev); err != nil {
			return err
		}
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
		if prev == "" && !terminalTyped[state] { // a new run
			if err := withdrawIdleDecision(q, *t); err != nil {
				return err
			}
		}
		if _, err = settleTask(q, *t, ""); err == nil && terminalTyped[state] && !terminalTyped[prev] {
			_, err = settleIdleTask(q, *t)
		}
	}
	if err == nil && q.app != nil {
		needDispatch := state == "preparing" && e["hostId"] == nil && kind == "agent_run"
		newlySucceeded := state == "succeeded" && !terminalTyped[prev]
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

// wallKnown: a run's wall time is measured when it has both ends, unless it was abandoned (it ended when given up).
const wallKnown = `started_at IS NOT NULL AND ended_at IS NOT NULL AND problem IS DISTINCT FROM 'unknown:abandoned'`

// writeTotals persists and merges leaf-execution totals so receipt and process.closed converge regardless of arrival order.
func writeTotals(q *Request, id string) error {
	var kind string
	if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce((SELECT kind::text FROM execution WHERE id=$1),'')", id).Scan(&kind); err != nil || kind == "" || kind == "pipeline_run" || kind == "pipeline_step" {
		return err
	}
	if err := exec(q, `INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,wall_ms,wall_ms_basis,computed_at)
	 SELECT id,project_id,directed_by,
	  CASE
	   WHEN account_id IS NOT NULL AND NOT coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('', account_id) m), false) THEN 'subscription'
	   WHEN kind IN ('agent_run','child_agent','tool_run') THEN 'metered'
	   ELSE 'unavailable'
	  END,
	  CASE WHEN `+wallKnown+` THEN greatest(0,(extract(epoch FROM ended_at-started_at)*1000)::bigint) END,
	  CASE WHEN `+wallKnown+` THEN 'measured' ELSE 'unavailable' END,
	  now()
	 FROM execution WHERE id=$1
	 ON CONFLICT(execution_id) DO UPDATE SET
	  project_id=coalesce(excluded.project_id,execution_totals.project_id),
	  directed_by=coalesce(excluded.directed_by,execution_totals.directed_by),
	  billing_basis=CASE WHEN execution_totals.billing_basis='metered' AND execution_totals.connection_cost IS NOT NULL THEN 'metered' ELSE excluded.billing_basis END,
	  wall_ms=CASE WHEN (SELECT problem FROM execution WHERE id=$1)='unknown:abandoned' THEN NULL ELSE coalesce(excluded.wall_ms,execution_totals.wall_ms) END,
	  wall_ms_basis=CASE WHEN (SELECT problem FROM execution WHERE id=$1)='unknown:abandoned' THEN 'unavailable' WHEN coalesce(excluded.wall_ms,execution_totals.wall_ms) IS NOT NULL THEN 'measured' ELSE 'unavailable' END,
	  computed_at=now()`, id); err != nil {
		return err
	}
	if err := exec(q, "SELECT fleet_write_model($1)", id); err != nil { // the model the harness reports (migration 45)
		return err
	}
	if err := exec(q, "SELECT fleet_write_tokens($1)", id); err != nil { // tokens from the harness's usage report (migration 40, 44, 50)
		return err
	}
	if err := exec(q, `UPDATE execution_totals SET rate_limit_last=r.data->'data',rate_limit_last_basis='measured' FROM (SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND data->>'type'='limit_reading' ORDER BY seq DESC LIMIT 1) r WHERE execution_id=$1`, id); err != nil {
		return err
	}
	return exec(q, `WITH pc AS (
	 SELECT data->'data'->'resources' AS r
	 FROM document
	 WHERE kind='event' AND data->>'executionId'=$1 AND data->>'type'='process.closed'
	 ORDER BY seq DESC LIMIT 1
	), parsed AS (
	 SELECT
	  jsonb_typeof(r)='object' AS has_obj,
	  fleet_token_count(r->'cpuMs') AS cpu_ms,
	  fleet_token_count(r->'peakMemoryBytes') AS peak_mem,
	  fleet_token_count(r->'activeMs') AS active_ms,
	  fleet_token_count(r->'turnCount') AS turn_count,
	  CASE WHEN jsonb_typeof(r->'cpuMissingReason')='string' THEN r->>'cpuMissingReason' END AS cpu_reason,
	  CASE WHEN jsonb_typeof(r->'memoryMissingReason')='string' THEN r->>'memoryMissingReason' END AS mem_reason,
	  CASE WHEN $2='agent_run' AND jsonb_typeof(r)='object' THEN
	   jsonb_strip_nulls(jsonb_build_object(
	    'byTool', CASE WHEN jsonb_typeof(r->'toolCalls')='object' THEN r->'toolCalls' ELSE '{}'::jsonb END,
	    'other', CASE WHEN jsonb_typeof(r->'toolCallsOther')='object' THEN r->'toolCallsOther' END
	   ))
	  END AS tool_calls
	 FROM pc
	)
	UPDATE execution_totals t SET
	 cpu_ms=coalesce(p.cpu_ms,t.cpu_ms),
	 cpu_ms_basis=CASE WHEN coalesce(p.cpu_ms,t.cpu_ms) IS NOT NULL THEN 'measured' ELSE t.cpu_ms_basis END,
	 cpu_missing_reason=CASE
	  WHEN coalesce(p.cpu_ms,t.cpu_ms) IS NOT NULL THEN NULL
	  WHEN p.has_obj THEN coalesce(p.cpu_reason,t.cpu_missing_reason)
	  ELSE coalesce(t.cpu_missing_reason,'executor_version')
	 END,
	 peak_memory_bytes=coalesce(p.peak_mem,t.peak_memory_bytes),
	 peak_memory_bytes_basis=CASE WHEN coalesce(p.peak_mem,t.peak_memory_bytes) IS NOT NULL THEN 'measured' ELSE t.peak_memory_bytes_basis END,
	 memory_missing_reason=CASE
	  WHEN coalesce(p.peak_mem,t.peak_memory_bytes) IS NOT NULL THEN NULL
	  WHEN p.has_obj THEN coalesce(p.mem_reason,t.memory_missing_reason)
	  ELSE coalesce(t.memory_missing_reason,'executor_version')
	 END,
	 active_ms=coalesce(p.active_ms,t.active_ms),
	 active_ms_basis=CASE WHEN coalesce(p.active_ms,t.active_ms) IS NOT NULL THEN 'measured' ELSE t.active_ms_basis END,
	 turn_count=coalesce(p.turn_count,t.turn_count),
	 turn_count_basis=CASE WHEN coalesce(p.turn_count,t.turn_count) IS NOT NULL THEN 'measured' ELSE t.turn_count_basis END,
	 tool_calls=coalesce(p.tool_calls,t.tool_calls),
	 tool_calls_basis=CASE WHEN coalesce(p.tool_calls,t.tool_calls) IS NOT NULL THEN 'measured' ELSE t.tool_calls_basis END,
	 computed_at=now()
	FROM parsed p
	WHERE t.execution_id=$1`, id, kind)
}

// writeEnded: the end of an agent run in its thread, one event with a fixed id.
func writeEnded(q *Request, id string) error {
	return exec(q, `INSERT INTO document(kind,id,data,project_id) SELECT 'event','ended:'||id,jsonb_build_object('id','ended:'||id,'executionId',id,'sessionId',session_id,'type','execution.ended',
	 'timestamp',(extract(epoch FROM coalesce(ended_at,now()))*1000)::bigint,'data',jsonb_build_object('fleetNormalized',jsonb_build_object('blocks','[]'::jsonb))),project_id
	 FROM execution WHERE id=$1 AND kind='agent_run' ON CONFLICT(id) DO NOTHING`, id)
}

// agentQuestionJSON: an agent question record ('question') from its columns; data holds the open rest (delivery).
const agentQuestionJSON = `data || jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,
 'question',text,'state',state,'answer',answer,'answeredBy',answered_by,'answeredAt',(extract(epoch FROM answered_at)*1000)::bigint,'createdAt',(extract(epoch FROM asked_at)*1000)::bigint))`

// agentQuestionColumns: the record fields all() filters on, by column.
var agentQuestionColumns = map[string]string{"executionId": "execution_id", "sessionId": "session_id", "projectId": "project_id", "requestId": "request_id", "state": "state::text"}

func agentQuestions(q *Request, f doc) ([]doc, error) {
	where, args := "kind='agent_question'", []any{}
	for _, k := range sortedKeysOf(f) {
		col, ok := agentQuestionColumns[k]
		if !ok {
			return nil, fmt.Errorf("question filter on %s", k)
		}
		args = append(args, f[k])
		where += fmt.Sprintf(" AND %s=$%d", col, len(args))
	}
	return rows(q, "SELECT "+agentQuestionJSON+" FROM question WHERE "+where+" ORDER BY seq", args...)
}

// putQuestion: an agent question record ('question'); its fields live in the question columns, data keeps the rest.
func putQuestion(q *Request, v doc) error {
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
		if _, err := askQuestion(q, doc{"id": id, "kind": "agent_question", "text": v["question"], "taskId": v["taskId"], "projectId": v["projectId"], "executionId": v["executionId"], "sessionId": v["sessionId"],
			"askedBy": askedBy, "requestId": v["requestId"], "data": questionRest(v), "askedAt": v["createdAt"]}); err != nil {
			return err
		}
		if answered {
			by := firstString(v["answeredBy"], "operator")
			return exec(q, "UPDATE question SET state='answered',answer=$2,answered_by=$3,answered_at=$4 WHERE id=$1", id, v["answer"], by, msTime(v["answeredAt"]))
		}
		return nil
	}
	if answered {
		if _, err := answerQuestion(q, id, astr(v["answer"]), firstString(v["answeredBy"], "operator"), nil); err != nil {
			return err
		}
	}
	return exec(q, "UPDATE question SET data=$2,project_id=coalesce(project_id,$3) WHERE id=$1", id, js(questionRest(v)), nilIf(astr(v["projectId"])))
}

// agentQuestionFields: the record fields agentQuestionJSON builds from columns (migration 32 strips the same from data).
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
	if err == nil && q.app != nil {
		switch kind {
		case "command":
			if astr(v["status"]) == "pending" {
				if hid := astr(v["hostId"]); hid != "" {
					onEnd(q.tx, func(committed bool) {
						if committed {
							q.app.wakeHost(hid)
						}
					})
				}
			}
		case "host", "managed-account", "queued-message":
			onEnd(q.tx, func(committed bool) {
				if committed {
					q.app.wakeDispatch()
				}
			})
		}
	}
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
