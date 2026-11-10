package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type doc = map[string]any

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func now() int64 { return time.Now().UnixMilli() }

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

func getDoc(q *Request, id, kind string) (doc, error) {
	return one(q, "SELECT data FROM document WHERE id=$1 AND kind=$2", id, kind)
}

func putDoc(q *Request, kind string, v doc) error {
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
	return one(q, "SELECT data FROM execution WHERE id=$1 AND legacy_kind='execution'", id)
}

// Legacy state of the API record -> typed execution state (packages/coordinator/executions.ts mapLegacyState).
var typedState = map[string]string{"queued": "preparing", "accepted": "preparing", "pending": "preparing", "started": "running",
	"running": "running", "waiting": "waiting", "completed": "succeeded", "failed": "failed", "stopped": "cancelled", "cancelled": "cancelled"}

// putExecution writes the typed row with the API record in data (agent runs only: the slice has no other kind).
func putExecution(q *Request, e doc) error {
	legacy, _ := e["state"].(string)
	state, observation := typedState[legacy], "confirmed"
	if state == "" {
		observation = "unconfirmed"
		if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce((SELECT state::text FROM execution WHERE id=$1),'preparing')", e["id"]).Scan(&state); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(e)
	cfg, _ := json.Marshal(doc{"profileId": nil, "profileSha256": nil, "effort": e["effort"]})
	ms := func(k string) any {
		if v, ok := e[k].(float64); ok {
			return time.UnixMilli(int64(v))
		}
		if v, ok := e[k].(int64); ok {
			return time.UnixMilli(v)
		}
		return nil
	}
	_, err := q.tx.Exec(q.ctx(), `INSERT INTO execution(id,kind,state,observation,session_id,host_id,harness,model,config_ref,started_at,ended_at,legacy_kind,legacy_id,data,directed_by,project_id)
	 VALUES($1,'agent_run',$2,$3,$4,$5,'codex',$6,$7,$8,$9,'execution',$1,$10,$11,$12)
	 ON CONFLICT(id) DO UPDATE SET state=excluded.state,project_id=coalesce(execution.project_id,excluded.project_id),observation=excluded.observation,host_id=excluded.host_id,model=excluded.model,
	  started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,directed_by=excluded.directed_by,
	  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
		e["id"], state, observation, e["sessionId"], e["hostId"], e["model"], cfg, ms("startedAt"), ms("finishedAt"), b, e["directedBy"], nilIf(firstString(e["projectId"], gm(e, "dispatch")["projectId"])))
	return err
}

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

func (a *App) sessionCreate(q *Request) (any, error) {
	s := doc{"id": uuid(), "createdAt": now(), "directedBy": q.person.Email}
	if err := putDoc(q, "session", s); err != nil {
		return nil, err
	}
	s["sessionId"] = s["id"]
	return s, nil
}

func (a *App) sessionGet(q *Request) (any, error) {
	s, err := getDoc(q, q.params[0], "session")
	if err != nil {
		return nil, err
	}
	s["sessionId"] = s["id"]
	if s["messages"], err = rows(q, "SELECT data FROM document WHERE kind='message' AND data->>'sessionId'=$1 ORDER BY seq", s["id"]); err != nil {
		return nil, err
	}
	s["executions"], err = rows(q, "SELECT data FROM execution WHERE session_id=$1 AND legacy_kind='execution' ORDER BY seq", s["id"])
	return s, err
}

// POST /api/sessions/:id/messages (SL4): exactly requestId and text (optional delivery); one execution per requestId.
func (a *App) message(q *Request) (any, error) {
	s, err := getDoc(q, q.params[0], "session")
	if err != nil {
		return nil, err
	}
	var b map[string]json.RawMessage
	if json.Unmarshal(q.body, &b) != nil || b == nil {
		return nil, fail(400, "Expected requestId, text and optional delivery")
	}
	for k := range b {
		if k != "requestId" && k != "text" && k != "delivery" {
			return nil, fail(400, "Expected requestId, text and optional delivery")
		}
	}
	text := func(k string, max int) (string, error) {
		var v string
		if json.Unmarshal(b[k], &v) != nil || v == "" || len([]rune(v)) > max {
			return "", fail(400, "Invalid text")
		}
		return v, nil
	}
	requestID, err := text("requestId", 200)
	if err != nil {
		return nil, err
	}
	input, err := text("text", 32000)
	if err != nil {
		return nil, err
	}
	sid := s["id"].(string)
	if m, err := one(q, "SELECT data FROM document WHERE kind='message' AND data->>'sessionId'=$1 AND data->>'requestId'=$2", sid, requestID); err == nil {
		return getExecution(q, m["executionId"].(string))
	} else if !errors.Is(err, errNotFound) {
		return nil, err
	}
	// Late choice: queue time records only constraints; dispatch picks the host on a host poll.
	id := uuid()
	e := doc{"id": id, "executionId": id, "sessionId": sid, "hostId": nil, "state": "queued", "createdAt": now(), "directedBy": s["directedBy"],
		"model": "gpt-6.1-sol", "effort": "low",
		"dispatch": doc{"harness": "codex", "projectId": nil, "account": nil, "host": nil, "last": nil, "input": input, "payload": nil}}
	if err := putExecution(q, e); err != nil {
		return nil, err
	}
	if err := putDoc(q, "message", doc{"id": uuid(), "sessionId": sid, "executionId": id, "requestId": requestID, "text": input, "sentBy": director(q)}); err != nil {
		return nil, err
	}
	return e, nil
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
