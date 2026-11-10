// Record access of area B (all/get/put with the typed execution and task adapters, docs/designs/fleet-data-foundation.md §7):
// documents by kind, and the legacy kinds execution, question and delegation served from their typed tables.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func gs(d doc, k string) string { v, _ := d[k].(string); return v }

func gm(d doc, k string) doc { v, _ := d[k].(map[string]any); return v }

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

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
		return rows(q, "SELECT data FROM execution WHERE legacy_kind='execution' AND data @> $1 ORDER BY seq", js(f))
	case "question":
		return rows(q, "SELECT data FROM question WHERE legacy_kind='question' AND data @> $1 ORDER BY seq", js(f))
	case "delegation":
		return rows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL AND delegation @> $1 ORDER BY seq", js(f))
	}
	return rows(q, "SELECT data FROM document WHERE kind=$1 AND data @> $2 ORDER BY seq", kind, js(f))
}

func get(q *Request, id, kind string) (doc, error) {
	switch kind {
	case "execution":
		return getExecution(q, id)
	case "question":
		return one(q, "SELECT data FROM question WHERE id=$1 AND legacy_kind='question'", id)
	case "delegation":
		return one(q, "SELECT delegation FROM execution WHERE delegation->>'id'=$1", id)
	}
	return getDoc(q, id, kind)
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
		return putExecutionRecord(q, v)
	case "question":
		return putLegacyQuestion(q, v)
	case "delegation":
		tag, err := q.tx.Exec(q.ctx(), "UPDATE execution SET delegation=$2,parent_execution_id=coalesce(parent_execution_id,$3) WHERE id=$1", v["childExecutionId"], js(v), v["parentExecutionId"])
		if err == nil && tag.RowsAffected() == 0 {
			return fail(409, fmt.Sprint("Delegation child execution not found: ", v["childExecutionId"]))
		}
		return err
	}
	return putDoc(q, kind, v)
}

func legacyState(legacy, previous string) (string, string) {
	switch legacy {
	case "queued", "accepted", "pending":
		return "preparing", "confirmed"
	case "started", "running":
		return "running", "confirmed"
	case "waiting":
		return "waiting", "confirmed"
	case "completed":
		return "succeeded", "confirmed"
	case "failed", "quota", "unavailable":
		return "failed", "confirmed"
	case "stopped", "cancelled":
		return "cancelled", "confirmed"
	}
	if previous == "" {
		previous = "preparing"
	}
	return previous, "unconfirmed"
}

var terminalTyped = map[string]bool{"succeeded": true, "failed": true, "cancelled": true}

// putExecutionRecord: writes the execution record, then settles its task.
func putExecutionRecord(q *Request, e doc) error {
	id := gs(e, "id")
	var prev string
	q.tx.QueryRow(q.ctx(), "SELECT coalesce((SELECT state::text FROM execution WHERE id=$1),'')", id).Scan(&prev)
	state, observation := legacyState(gs(e, "state"), prev)
	kind := "agent_run"
	if gs(e, "actor") == "program-worker" {
		kind = "tool_run"
		if gs(e, "deliveryId") != "" {
			kind = "pipeline_step"
		}
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
		parent = gs(sess, "createdByExecutionId")
	}
	if fo := gm(sess, "forkOf"); parent == "" && gs(fo, "executionId") != "" {
		other, err := has(q, "SELECT 1 FROM execution WHERE session_id=$1 AND id<>$2 AND legacy_kind='execution'", gs(sess, "id"), id)
		if err != nil {
			return err
		}
		if !other {
			parent = gs(fo, "executionId")
		}
	}
	project := firstString(e["projectId"], sess["projectId"])
	task := firstString(e["deliveryId"], sess["deliveryId"], sess["taskId"])
	ac := gm(e, "agentConfig")
	harness := any(nil)
	if h := gs(ac, "harness"); h != "" {
		harness = h
	} else if kind == "agent_run" {
		harness = "codex"
	}
	variant := any(nil)
	if e["experimentId"] != nil {
		variant = nilIf(gs(ac, "id"))
	}
	account := firstString(gm(e, "effectiveAccount")["accountId"], e["accountId"])
	cfg := js(doc{"profileId": nilIf(gs(ac, "id")), "profileSha256": e["profileSha256"], "effort": e["effort"]})
	err = exec(q, `INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,harness,model,config_ref,started_at,ended_at,legacy_kind,legacy_id,data)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'execution',$1,$18)
	 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,task_id=coalesce(execution.task_id,excluded.task_id),
	  experiment_id=excluded.experiment_id,variant=excluded.variant,parent_execution_id=excluded.parent_execution_id,session_id=excluded.session_id,account_id=excluded.account_id,host_id=excluded.host_id,
	  harness=excluded.harness,model=excluded.model,config_ref=excluded.config_ref,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,
	  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
		id, kind, state, observation, nilIf(project), nilIf(task), e["experimentId"], variant, nilIf(parent), e["sessionId"], nilIf(account), e["hostId"], harness, e["model"], cfg, ms(e["startedAt"]), ms(e["finishedAt"]), js(e))
	if err != nil {
		return err
	}
	if d := gs(e, "directedBy"); d != "" {
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

func firstString(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
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
	id := gs(v, "id")
	found, err := has(q, "SELECT 1 FROM question WHERE id=$1", id)
	if err != nil {
		return err
	}
	answered := gs(v, "state") == "answered"
	if !found {
		askedBy := "agent"
		if e := gs(v, "executionId"); e != "" {
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
		if _, err := answerQuestion(q, id, gs(v, "answer"), firstString(v["answeredBy"], "operator"), nil); err != nil {
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

func terminalLegacy(e doc) bool {
	switch gs(e, "state") {
	case "completed", "failed", "stopped":
		return true
	}
	return false
}

var _ = strings.TrimSpace
