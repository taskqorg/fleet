package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// rows: the first column of each row, a jsonb object, in query order.
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

func getExecution(q *Request, id any) (doc, error) {
	return one(q, "SELECT data FROM execution WHERE id=$1 AND legacy_kind='execution'", id)
}

// POST /api/sessions (app.ts session_create): directed by the request person.
func (a *App) sessionCreate(q *Request) (any, error) {
	s := doc{"id": uuid(), "createdAt": time.Now().UnixMilli(), "directedBy": q.person.Email}
	b, _ := json.Marshal(s)
	if _, err := q.tx.Exec(q.ctx(), "INSERT INTO document(kind,id,data,project_id) VALUES('session',$1,$2,NULL) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind", s["id"], b); err != nil {
		return nil, err
	}
	s["sessionId"] = s["id"]
	return s, nil
}

// GET /api/sessions/:id (app.ts session_get with agent-tools.ts sessionView): the session, its queue, questions, handoffs
// (without text), transitions, forks, delegations with the states of their executions, messages and executions.
func (a *App) sessionGet(q *Request) (any, error) {
	s, err := one(q, "SELECT data FROM document WHERE id=$1 AND kind='session'", q.params[0])
	if err != nil {
		return nil, err
	}
	id, _ := s["id"].(string)
	bySession := func(kind string) ([]doc, error) {
		return rows(q, "SELECT data FROM document WHERE kind=$1 AND data->>'sessionId'=$2 ORDER BY seq", kind, id)
	}
	queued, err := bySession("queued-message")
	if err != nil {
		return nil, err
	}
	waiting := []any{}
	for _, m := range queued {
		if m["state"] == "queued" {
			waiting = append(waiting, m["id"])
		}
	}
	for _, m := range queued {
		m["queued"], m["position"] = true, nil
		for i, w := range waiting {
			if w == m["id"] {
				m["position"] = i + 1
				break
			}
		}
	}
	filter, _ := json.Marshal(doc{"sessionId": id})
	questions, err := rows(q, "SELECT data FROM question WHERE legacy_kind='question' AND data @> $1 ORDER BY seq", string(filter))
	if err != nil {
		return nil, err
	}
	handoffs, err := bySession("handoff")
	if err != nil {
		return nil, err
	}
	for _, h := range handoffs {
		delete(h, "text")
	}
	transitions, err := bySession("session-transition")
	if err != nil {
		return nil, err
	}
	forkFilter, _ := json.Marshal(doc{"sourceSessionId": id})
	forks, err := rows(q, "SELECT data FROM document WHERE kind='session-fork' AND data @> $1 ORDER BY seq", string(forkFilter))
	if err != nil {
		return nil, err
	}
	all, err := rows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL ORDER BY seq")
	if err != nil {
		return nil, err
	}
	delegations := []doc{}
	state := func(id any) (any, error) {
		e, err := getExecution(q, id)
		if err != nil {
			return nil, err
		}
		return e["state"], nil
	}
	for _, d := range all {
		if d["parentSessionId"] != id && d["childSessionId"] != id {
			continue
		}
		if d["parentState"], err = state(d["parentExecutionId"]); err != nil {
			return nil, err
		}
		if d["childState"], err = state(d["childExecutionId"]); err != nil {
			return nil, err
		}
		d["continuationState"] = nil
		if c, ok := d["continuationExecutionId"].(string); ok && c != "" {
			if d["continuationState"], err = state(c); err != nil {
				return nil, err
			}
		}
		delegations = append(delegations, d)
	}
	messages, err := bySession("message")
	if err != nil {
		return nil, err
	}
	executions, err := rows(q, "SELECT data FROM execution WHERE legacy_kind='execution' AND session_id=$1 AND data->>'sessionId'=$1 ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	for k, v := range (doc{"queued": queued, "questions": questions, "handoffs": handoffs, "transitions": transitions, "forks": forks,
		"sessionId": id, "delegations": delegations, "messages": messages, "executions": executions}) {
		s[k] = v
	}
	return s, nil
}

// GET /api/executions/:id/events (app.ts execution_events): in seq order (insertion order on a tie), without execution.ended.
func (a *App) eventsGet(q *Request) (any, error) {
	if _, err := getExecution(q, q.params[0]); err != nil {
		return nil, err
	}
	es, err := rows(q, "SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND (data->>'type') IS DISTINCT FROM 'execution.ended' ORDER BY (data->'seq')::numeric, seq", q.params[0])
	return doc{"events": es}, err
}

// marshal: JSON as JSON.stringify writes it (no HTML escaping of <, > and &).
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	err := e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), err
}
