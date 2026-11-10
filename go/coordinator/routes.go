package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
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
	_, err := q.tx.Exec(q.ctx(), `INSERT INTO execution(id,kind,state,observation,session_id,host_id,harness,model,config_ref,started_at,ended_at,legacy_kind,legacy_id,data,directed_by)
	 VALUES($1,'agent_run',$2,$3,$4,$5,'codex',$6,$7,$8,$9,'execution',$1,$10,$11)
	 ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,host_id=excluded.host_id,model=excluded.model,
	  started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,directed_by=excluded.directed_by,
	  problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
		e["id"], state, observation, e["sessionId"], e["hostId"], e["model"], cfg, ms("startedAt"), ms("finishedAt"), b, e["directedBy"])
	return err
}

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

// POST /api/ops/:name: only hosts_register is in the slice (owner registers a host with a minted secret).
var hostIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func (a *App) op(q *Request) (any, error) {
	if q.params[0] != "hosts_register" {
		return nil, fail(404, "Unknown operation")
	}
	if q.person == nil || !q.person.Owner {
		return nil, fail(403, "Only the owner manages hosts")
	}
	var b struct {
		ID string `json:"id"`
	}
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	if !hostIDPattern.MatchString(b.ID) {
		return nil, fail(400, "Expected host id")
	}
	if _, err := getDoc(q, "host:"+b.ID, "host"); err == nil {
		return nil, fail(409, "Host exists")
	} else if !errors.Is(err, errNotFound) {
		return nil, err
	}
	sb := make([]byte, 32)
	rand.Read(sb)
	secret := hex.EncodeToString(sb)
	if secretHash(secret) == secretHash(a.browserSecret) {
		return nil, fail(400, "A host secret must differ from the browser secret")
	}
	view := doc{"name": b.ID, "holder": q.person.Email, "platform": "unknown", "roles": []string{}, "intake": "all", "harnesses": doc{}, "createdAt": now()}
	row := doc{"id": "host:" + b.ID, "hostId": b.ID, "secretHash": secretHash(secret)}
	for k, v := range view {
		row[k] = v
	}
	if err := putDoc(q, "host", row); err != nil {
		return nil, err
	}
	view["id"] = b.ID
	return doc{"host": view, "secret": secret}, nil
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
	if err := putDoc(q, "message", doc{"id": uuid(), "sessionId": sid, "executionId": id, "requestId": requestID, "text": input}); err != nil {
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

const instructions = "Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents."

// POST /api/hosts/poll (SL5): record the observation; the default host claims queued executions (dispatch in its simplest
// case: no subscriptions); reply with the pending commands of the polling host only.
func (a *App) poll(q *Request) (any, error) {
	var b PollRequest
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	host := hostOf(q)
	obs := doc{"id": host, "holder": q.host["holder"], "lastSeenAt": now()}
	switch b.Platform {
	case "darwin", "win32", "linux":
		obs["platform"] = b.Platform
	}
	if b.Capabilities != nil {
		obs["capabilities"] = b.Capabilities
	}
	if err := putDoc(q, "host-observation", obs); err != nil {
		return nil, err
	}
	if host == a.hostID {
		// ponytail: the default host takes every queued execution; ranking over subscriptions and hosts is out of the slice.
		queued, err := rows(q, "SELECT data FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run' ORDER BY seq")
		if err != nil {
			return nil, err
		}
		for _, e := range queued {
			if e["state"] != "queued" || e["dispatch"] == nil || e["stopRequested"] == true {
				continue
			}
			e["hostId"] = host
			d := e["dispatch"].(doc)
			c := doc{"id": uuid(), "executionId": e["id"], "sessionId": e["sessionId"], "hostId": host, "kind": "start", "status": "pending",
				"payload": doc{"harness": "codex", "text": d["input"], "setup": "run", "model": e["model"], "effort": e["effort"], "instructions": instructions}}
			if err := putExecution(q, e); err != nil {
				return nil, err
			}
			if err := putDoc(q, "command", c); err != nil {
				return nil, err
			}
		}
	}
	cs, err := rows(q, "SELECT data FROM document WHERE kind='command' AND data->>'hostId'=$1 AND data->>'status'='pending' ORDER BY seq", host)
	return doc{"commands": cs}, err
}

var rank = map[string]int{"pending": 0, "accepted": 1, "started": 2, "unknown": 2, "completed": 3, "failed": 3, "stopped": 3}

// POST /api/hosts/receipts (SL6): a status receipt moves the command and execution forward only.
func (a *App) receipt(q *Request) (any, error) {
	var b Receipt
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	if err := owned(q, &b.ExecutionId); err != nil {
		return nil, err
	}
	c, err := getDoc(q, b.CommandId, "command")
	if err != nil {
		return nil, err
	}
	if c["executionId"] != b.ExecutionId || c["hostId"] != hostOf(q) {
		return nil, fail(403, "Command rejected")
	}
	status := string(b.Status)
	next, ok := rank[status]
	if !ok || status == "pending" {
		return nil, fail(400, "Invalid status")
	}
	cur := rank[c["status"].(string)]
	if next < cur || (cur == 3 && status != c["status"]) {
		return nil, fail(409, "Receipt regression")
	}
	if cur == 3 { // a repeated terminal receipt (an outbox retry) changes nothing
		return doc{"ok": true}, nil
	}
	c["status"] = status
	if err := putDoc(q, "command", c); err != nil {
		return nil, err
	}
	e, err := getExecution(q, b.ExecutionId)
	if err != nil {
		return nil, err
	}
	kind := c["kind"]
	if st, _ := e["state"].(string); st != "completed" && st != "failed" && st != "stopped" && kind != "steer" && (kind != "stop" || status == "stopped") {
		e["state"] = status
	}
	e["updatedAt"] = now()
	if status == "started" && e["startedAt"] == nil {
		e["startedAt"] = e["updatedAt"]
	}
	if rank[status] == 3 && kind != "steer" {
		e["finishedAt"] = e["updatedAt"]
		if b.Error != nil {
			e["error"] = *b.Error
		}
	}
	return doc{"ok": true}, putExecution(q, e)
}

// POST /api/hosts/events (SL7, SL11): the whole batch is validated, then stored; each event once, its data unchanged.
func (a *App) events(q *Request) (any, error) {
	var b HostEvents
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	var raw struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	json.Unmarshal(q.body, &raw)
	if err := owned(q, &b.ExecutionId); err != nil {
		return nil, err
	}
	if b.Events == nil || len(b.Events) > 1000 {
		return nil, fail(400, "Invalid events")
	}
	e, err := getExecution(q, b.ExecutionId)
	if err != nil {
		return nil, err
	}
	type prepared struct {
		id    string
		seq   int64
		value []byte
	}
	batch := []prepared{}
	ids, seqs := map[string]string{}, map[int64]string{}
	for i, ev := range b.Events {
		if ev.Id == "" || len(ev.Id) > 300 || ev.Type == "execution.ended" || ev.Seq < 0 || ev.Seq > 1<<53-1 {
			return nil, fail(400, "Invalid event identity")
		}
		id := "event:" + b.ExecutionId + ":" + ev.Id
		v := raw.Events[i]
		v["id"], _ = json.Marshal(id)
		v["executionId"], _ = json.Marshal(b.ExecutionId)
		v["sessionId"], _ = json.Marshal(e["sessionId"])
		value, _ := json.Marshal(v)
		var same, collision bool
		if err := q.tx.QueryRow(q.ctx(), `SELECT coalesce((SELECT data=$2::jsonb FROM document WHERE id=$1 AND kind IN ('event','event-delta')),true),
		 EXISTS(SELECT 1 FROM document WHERE kind IN ('event','event-delta') AND data->>'executionId'=$3 AND data->'seq'=to_jsonb($4::bigint) AND id<>$1)`,
			id, string(value), b.ExecutionId, ev.Seq).Scan(&same, &collision); err != nil {
			return nil, err
		}
		if !same {
			return nil, fail(409, "Event conflict")
		}
		if collision {
			// ponytail: transport-conflict records and recovery are out of the slice; a taken seq refuses the batch.
			return nil, fail(409, "Sequence conflict")
		}
		if old, ok := ids[id]; ok && old != string(value) {
			return nil, fail(409, "Batch event conflict")
		}
		ids[id] = string(value)
		if old, ok := seqs[ev.Seq]; ok && old != id {
			return nil, fail(409, "Batch sequence conflict")
		}
		seqs[ev.Seq] = id
		batch = append(batch, prepared{id, ev.Seq, value})
	}
	for _, p := range batch {
		kind := "event"
		var d struct {
			Data *EventData `json:"data"`
		}
		json.Unmarshal(p.value, &d)
		if d.Data != nil && d.Data.FleetNormalized != nil && d.Data.FleetNormalized.DeltaOf != nil {
			kind = "event-delta"
		}
		if _, err := q.tx.Exec(q.ctx(), "INSERT INTO document(kind,id,data) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=now() WHERE document.kind=excluded.kind AND document.data<>excluded.data", kind, p.id, string(p.value)); err != nil {
			return nil, err
		}
	}
	return doc{"ok": true}, nil
}

// GET /api/executions/:id/events (SL7): in seq order, without execution.ended.
func (a *App) eventsGet(q *Request) (any, error) {
	if _, err := getExecution(q, q.params[0]); err != nil {
		return nil, err
	}
	es, err := rows(q, "SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND data->>'type'<>'execution.ended' ORDER BY (data->>'seq')::bigint", q.params[0])
	return doc{"events": es}, err
}
