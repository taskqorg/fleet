// Sessions (docs/designs/fleet-operator-app.md §6): create, profile, read with its queue, questions, handoffs and
// delegations, archive, message submit, the Conversations list and list membership.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"fleet/contract"
)

var sessionStates = []string{"idle", "preparing", "running", "waiting", "succeeded", "failed", "cancelled"}

// Aggregate visible history once: under RLS, JSON sessionId predicates cannot become index conditions.
// Correlating these document scans per session makes retained history multiply the list's work.
const sessionBase = `WITH session_messages AS MATERIALIZED (
 SELECT DISTINCT ON (data->>'sessionId') data->>'sessionId' AS session_id, data->>'text' AS first_message
 FROM document WHERE kind='message' ORDER BY data->>'sessionId',seq
), session_activity AS MATERIALIZED (
 SELECT data->>'sessionId' AS session_id,max(seq) AS last_seq
 FROM document WHERE kind IN ('message','event') GROUP BY data->>'sessionId'
), x AS (SELECT d.id, d.code, d.data, d.project_id,
 coalesce((SELECT e.state::text FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1),'idle') AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) AS harness,
 m.first_message,
 greatest(d.seq,coalesce((SELECT max(e.seq) FROM execution e WHERE e.session_id=d.id),0),coalesce(a.last_seq,0)) AS last_seq,
 (SELECT count(*) FROM question q WHERE q.session_id=d.id AND q.state='open') AS open_questions
 FROM document d LEFT JOIN session_messages m ON m.session_id=d.id LEFT JOIN session_activity a ON a.session_id=d.id
 WHERE d.kind='session')`

func sessionWhere(f doc, p *[]any, withState bool) ([]string, error) {
	if err := filterFields(f, "projectId", "state", "search", "archived"); err != nil {
		return nil, err
	}
	c := []string{"NOT (x.data ? 'archivedAt')"}
	if f["archived"] == true {
		c[0] = "x.data ? 'archivedAt'"
	}
	c, err := projectWhere(f, p, "x.project_id", c)
	if err != nil {
		return nil, err
	}
	if st, ok := f["state"].([]any); withState && ok && len(st) > 0 {
		ss := []string{}
		for _, x := range st {
			v, _ := x.(string)
			if !slices.Contains(sessionStates, v) {
				return nil, fail(400, "Invalid state")
			}
			ss = append(ss, v)
		}
		*p = append(*p, ss)
		c = append(c, fmt.Sprintf("x.state=ANY($%d)", len(*p)))
	}
	if s, ok := f["search"].(string); ok { // the list search syntax (search.go searchWhere)
		c = append(c, searchWhere(s, p, []string{"x.first_message", "x.id", "x.code"}, map[string]string{"agent": "coalesce(x.harness,x.data->'agentConfig'->>'harness')", "host": "coalesce((SELECT e.host_id FROM execution e WHERE e.session_id=x.id ORDER BY e.seq DESC LIMIT 1),x.data->>'hostId')", "project": "x.project_id", "state": "x.state"}, withState)...)
	}
	return c, nil
}

func sessionRow(x doc) doc {
	d := mapOf(x["data"])
	ac := mapOf(d["agentConfig"])
	title := any(nil)
	if t := []rune(astr(x["first_message"])); len(t) > 0 {
		title = string(t[:min(200, len(t))])
	}
	harness := x["harness"]
	if harness == nil {
		harness = ac["harness"]
	}
	return doc{"id": x["id"], "code": x["code"], "title": title, "state": x["state"], "harness": harness, "profileId": ac["id"], "projectId": x["project_id"], "createdAt": d["createdAt"],
		"openQuestions": x["open_questions"], "archivedAt": d["archivedAt"], "sortKey": []any{-num(x["last_seq"]), x["id"]}}
}

func sessionsPage(q *Request, b doc) (doc, error) {
	f := mapOf(b["filter"])
	if f == nil {
		f = doc{}
	}
	limit := pageLimit(b["limit"], 100)
	p := []any{}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return nil, err
	}
	if b["after"] != nil {
		a, ok := b["after"].([]any)
		if !ok || len(a) != 2 {
			return nil, fail(400, "Invalid after")
		}
		p = append(p, num(a[0]), fmt.Sprint(a[1]))
		c = append(c, fmt.Sprintf("(-x.last_seq,x.id) > ($%d::bigint,$%d)", len(p)-1, len(p)))
	}
	rs, err := rowsOf(q, fmt.Sprintf("%s SELECT * FROM x WHERE %s ORDER BY -x.last_seq,x.id LIMIT %d", sessionBase, strings.Join(c, " AND "), limit+1), p...)
	if err != nil {
		return nil, err
	}
	cp := []any{}
	cc, err := sessionWhere(f, &cp, false)
	if err != nil {
		return nil, err
	}
	counts := zeroCounts(sessionStates)
	cr, err := rowsOf(q, sessionBase+" SELECT x.state,count(*) AS n FROM x WHERE "+strings.Join(cc, " AND ")+" GROUP BY 1", cp...)
	if err != nil {
		return nil, err
	}
	for _, r := range cr {
		counts[astr(r["state"])] = r["n"]
	}
	page := []doc{}
	for i, r := range rs {
		if i < limit {
			page = append(page, sessionRow(r))
		}
	}
	var next any
	if len(rs) > limit {
		next = page[len(page)-1]["sortKey"]
	}
	return doc{"sessions": page, "nextAfter": next, "counts": counts, "total": total(counts)}, nil
}

func related(q *Request, d doc) (doc, error) {
	out := doc{}
	for k, v := range d {
		out[k] = v
	}
	for k, id := range map[string]any{"parentState": d["parentExecutionId"], "childState": d["childExecutionId"], "continuationState": d["continuationExecutionId"]} {
		e, err := maybe(q, id, "execution")
		if err != nil {
			return nil, err
		}
		out[k] = nil
		if e != nil {
			out[k] = e["state"]
		} else if k != "continuationState" && id != nil {
			return nil, errNotFound
		}
	}
	return out, nil
}

func delegationsWhere(q *Request, sql string, id string) ([]doc, error) {
	ds, err := rows(q, "SELECT delegation FROM execution WHERE delegation IS NOT NULL AND ("+sql+") ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, d := range ds {
		r, err := related(q, d)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func sessionGet(q *Request, sid string) (doc, error) {
	s, err := get(q, sid, "session")
	if err != nil {
		return nil, err
	}
	v, err := sessionView(q, sid)
	if err != nil {
		return nil, err
	}
	for k, x := range v {
		s[k] = x
	}
	s["sessionId"] = sid
	c, err := first(q, "SELECT code FROM document WHERE id=$1 AND kind='session'", sid)
	if err != nil {
		return nil, err
	}
	s["code"] = c["code"]
	if s["delegations"], err = delegationsWhere(q, "delegation->>'parentSessionId'=$1 OR delegation->>'childSessionId'=$1", sid); err != nil {
		return nil, err
	}
	if s["messages"], err = all(q, "message", doc{"sessionId": sid}); err != nil {
		return nil, err
	}
	s["executions"], err = sessionExecutions(q, sid)
	return s, err
}

func sessionProfile(q *Request, sid string, b doc) (doc, error) {
	s, err := get(q, sid, "session")
	if err != nil {
		return nil, err
	}
	for k := range b {
		if k != "profileId" && k != "account" && k != "workingDirectory" {
			return nil, fail(400, "Expected profileId only")
		}
	}
	pid, ok := b["profileId"].(string)
	if !ok {
		return nil, fail(400, "Expected profileId only")
	}
	if a, err := activeOf(q, sid); err != nil || a != nil {
		if err == nil {
			err = fail(409, "Execution active")
		}
		return nil, err
	}
	if a, ok := b["account"]; ok {
		if as, isStr := a.(string); !isStr || !accountPattern.MatchString(as) {
			return nil, fail(400, "Invalid named host account")
		}
	}
	if w, ok := b["workingDirectory"]; ok {
		ws, isStr := w.(string)
		if !isStr || !strings.HasPrefix(ws, "/") || strings.Contains(ws, "..") {
			return nil, fail(400, "Invalid working directory")
		}
		s["workingDirectory"] = ws
		if err := put(q, "session", s); err != nil {
			return nil, err
		}
	}
	if astr(s["nativeSessionId"]) != "" {
		return sessionTransition(q, doc{"sessionId": sid, "profileId": pid, "account": b["account"]})
	}
	p, err := readTrustedProfile(q, pid)
	if err != nil {
		return nil, err
	}
	if a, ok := b["account"]; ok {
		s["account"] = a
	} else {
		delete(s, "account")
	}
	s["agentConfig"] = p
	return doc{"sessionId": sid, "profileId": p["id"], "account": s["account"]}, put(q, "session", s)
}

// sessionArchive: one teardown command on the last execution's host.
func sessionArchive(q *Request, sid string) (doc, error) {
	s, err := get(q, sid, "session")
	if err != nil {
		return nil, err
	}
	es, err := sessionExecutions(q, sid)
	if err != nil {
		return nil, err
	}
	var last doc
	for _, e := range es {
		if !terminal(e) && !unconfirmed(e) {
			return nil, fail(409, "Execution active")
		}
		if astr(e["hostId"]) != "" {
			last = e
		}
	}
	// A teardown still on its way is the answer; a finished one (kept, or sent on delivery) does not stop a new one.
	if old, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"}); err != nil || len(old) > 0 {
		if err != nil {
			return nil, err
		}
		return old[0], nil
	}
	s["archivedAt"] = nowMs()
	if err := put(q, "session", s); err != nil {
		return nil, err
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "session:" + sid, "field": "archivedAt", "previous": nil, "value": s["archivedAt"], "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
		return nil, err
	}
	if last == nil {
		return doc{"sessionId": sid, "archivedAt": s["archivedAt"], "teardown": nil}, nil
	}
	return teardownCommand(q, s, last, false)
}

// teardownCommand: the teardown of a session's worktree on the host of its last execution. deleteBranch: the work was
// delivered (its task accepted), so the local branch goes even when squash-merged (git branch -D); the executor still
// keeps the worktree for any cleanup.keepWhen reason (uncommitted, unpushed, teardown-failed).
func teardownCommand(q *Request, s, last doc, deleteBranch bool) (doc, error) {
	sid := astr(s["id"])
	payload := doc{}
	d := mapOf(last["dispatch"])
	if d["workspaceKey"] != nil {
		payload["workspaceKey"] = d["workspaceKey"]
	}
	workingDirectory := d["workingDirectory"]
	if deleteBranch {
		payload["deleteBranch"] = true
	}
	if p := astr(d["projectId"]); p != "" {
		workspace := mapOf(last["workspaceSnapshot"])
		if workspace == nil {
			workspace = mapOf(mapOf(s["workspaceSnapshots"])[astr(last["hostId"])+":"+p+":"+astr(d["workspaceKey"])])
		}
		if workspace == nil {
			workspace = mapOf(mapOf(s["workspaceSnapshots"])[astr(last["hostId"])])
		}
		if workspace == nil || (workspace["projectId"] != nil && workspace["projectId"] != p) {
			return doc{"sessionId": sid, "cleanup": "unknown", "reason": "cleanup_context_unknown: original workspace settings unavailable"}, nil
		}
		payload["projectId"], payload["project"] = p, workspace["project"]
		workingDirectory = workspace["workingDirectory"]
	}
	if w := astr(workingDirectory); w != "" {
		payload["workingDirectory"] = w
	}
	c := doc{"id": newID(), "executionId": last["id"], "sessionId": sid, "hostId": last["hostId"], "kind": "teardown", "payload": payload, "status": "pending"}
	return c, put(q, "command", c)
}

// deliveredTeardown: an accepted task's sessions get their worktree teardown (fleet-system-management.md, Worktree on
// delivery). A session with a live execution, or a teardown still pending, is left alone; archiving it later sends one.
func deliveredTeardown(q *Request, taskID string) error {
	sids, err := queryRows(q, "SELECT DISTINCT session_id FROM execution WHERE task_id=$1 AND session_id IS NOT NULL", taskID)
	if err != nil {
		return err
	}
	for _, r := range sids {
		sid := astr(r["session_id"])
		s, err := get(q, sid, "session")
		if errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if old, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"}); err != nil || len(old) > 0 {
			if err != nil {
				return err
			}
			continue
		}
		es, err := sessionExecutions(q, sid)
		if err != nil {
			return err
		}
		var last doc
		active := false
		for _, e := range es {
			active = active || (!terminal(e) && !unconfirmed(e))
			if astr(e["hostId"]) != "" {
				last = e
			}
		}
		if active || last == nil {
			continue
		}
		if _, err := teardownCommand(q, s, last, true); err != nil {
			return err
		}
	}
	return nil
}

func sessionCreate(q *Request) (doc, error) {
	if !keysOnly(body(q)) {
		return nil, fail(400, "session_create takes no identity or linkage fields")
	}
	s := doc{"id": newID(), "createdAt": nowMs(), "directedBy": director(q)}
	if err := put(q, "session", s); err != nil {
		return nil, err
	}
	out := doc{"sessionId": s["id"]}
	for k, v := range s {
		out[k] = v
	}
	return out, nil
}

func sessionArg(b doc) (string, error) { return strArg(b, "sessionId", "sessionId", 0) }

func init() {
	gatedRoute("POST /api/sessions", func(q *Request) (any, error) { return sessionCreate(q) })
	gatedRoute("GET /api/sessions/{id}", func(q *Request) (any, error) { return sessionGet(q, q.r.PathValue("id")) })
	gatedRoute("POST /api/sessions/{id}/profile", func(q *Request) (any, error) { return sessionProfile(q, q.r.PathValue("id"), body(q)) })
	// POST /api/sessions/{id}/messages: exactly requestId and text (optional delivery); one execution per requestId.
	gatedRoute("POST /api/sessions/{id}/messages", func(q *Request) (any, error) {
		s, err := get(q, q.r.PathValue("id"), "session")
		if err != nil {
			return nil, err
		}
		var b map[string]json.RawMessage
		if json.Unmarshal(q.body, &b) != nil || b == nil {
			return nil, fail(400, "Expected requestId, text and optional delivery")
		}
		_, r := b["requestId"]
		_, t := b["text"]
		for k := range b {
			if k != "requestId" && k != "text" && k != "delivery" {
				r = false
			}
		}
		if !r || !t {
			return nil, fail(400, "Expected requestId, text and optional delivery")
		}
		v := body(q)
		for k, max := range map[string]int{"requestId": 200, "text": 32000} {
			if x, ok := v[k].(string); !ok || x == "" || len([]rune(x)) > max {
				return nil, fail(400, "Invalid text")
			}
		}
		return submit(q, doc{"sessionId": s["id"], "requestId": v["requestId"], "text": v["text"], "delivery": v["delivery"]})
	})
	gatedRoute("GET /api/executions/{id}/delegations", func(q *Request) (any, error) {
		id := q.r.PathValue("id")
		if _, err := get(q, id, "execution"); err != nil {
			return nil, err
		}
		ds, err := delegationsWhere(q, "delegation->>'parentExecutionId'=$1 OR delegation->>'childExecutionId'=$1 OR delegation->>'continuationExecutionId'=$1", id)
		return doc{"delegations": ds}, err
	})
	gatedObjOp("session_create", false, shaped[contract.SessionCreateRequest, contract.SessionCreateReply](func(q *Request) (any, error) { return sessionCreate(q) }))
	gatedObjOp("session_get", true, shaped[contract.SessionGetRequest, contract.SessionGetReply](func(q *Request) (any, error) {
		sid, err := sessionArg(body(q))
		if err != nil {
			return nil, err
		}
		return sessionGet(q, sid)
	}))
	gatedObjOp("session_profile", false, shaped[contract.SessionProfileRequest, contract.SessionProfileReply](func(q *Request) (any, error) {
		b := body(q)
		sid, err := sessionArg(b)
		if err != nil {
			return nil, err
		}
		delete(b, "sessionId")
		return sessionProfile(q, sid, b)
	}))
	gatedObjOp("session_archive", false, shaped[contract.SessionArchiveRequest, contract.SessionArchiveReply](func(q *Request) (any, error) {
		sid, err := sessionArg(body(q))
		if err != nil {
			return nil, err
		}
		return sessionArchive(q, sid)
	}))
	gatedObjOp("session_unarchive", false, shaped[contract.SessionUnarchiveRequest, contract.SessionUnarchiveReply](sessionUnarchive))
	gatedObjOp("sessions_list", true, func(q *Request) (any, error) { return sessionsPage(q, body(q)) })
	gatedObjOp("list_membership", true, func(q *Request) (any, error) {
		b := body(q)
		idsAny, ok := b["ids"].([]any)
		if !ok || len(idsAny) > 1000 {
			return nil, fail(400, "Expected at most 1000 ids")
		}
		lists, ok := b["lists"].([]any)
		if !ok || len(lists) < 1 || len(lists) > 2 {
			return nil, fail(400, "Expected one or two lists")
		}
		ids := strList(idsAny)
		out := []doc{}
		for _, x := range lists {
			l, _ := x.(map[string]any)
			f := mapOf(l["filter"])
			if f == nil {
				f = doc{}
			}
			switch astr(l["name"]) {
			case "tasks":
				rs, err := taskMembership(q, f, l["group"], l["sort"], ids)
				if err != nil {
					return nil, err
				}
				counts, groups, err := taskSummary(q, f, l["group"], false)
				if err != nil {
					return nil, err
				}
				out = append(out, doc{"name": "tasks", "rows": rs, "counts": counts, "groups": groups})
			case "sessions":
				p := []any{}
				c, err := sessionWhere(f, &p, true)
				if err != nil {
					return nil, err
				}
				p = append(p, ids)
				c = append(c, fmt.Sprintf("x.id=ANY($%d)", len(p)))
				rs, err := rowsOf(q, sessionBase+" SELECT * FROM x WHERE "+strings.Join(c, " AND "), p...)
				if err != nil {
					return nil, err
				}
				rows := []doc{}
				for _, r := range rs {
					rows = append(rows, sessionRow(r))
				}
				pg, err := sessionsPage(q, doc{"filter": f, "limit": 1.0})
				if err != nil {
					return nil, err
				}
				out = append(out, doc{"name": "sessions", "rows": rows, "counts": pg["counts"]})
			default:
				return nil, fail(400, "Unknown list")
			}
		}
		return doc{"lists": out}, nil
	})
	// /api/tools: the operation table as MCP tools, each call in its own transaction.
	gatedLongRoute("POST /api/tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet", operations, agentDescriptions) })
}

// sessionUnarchive: undo session_archive (owner or full access). The teardown already sent is not undone: a removed
// worktree is prepared again at the next start.
func sessionUnarchive(q *Request) (any, error) {
	if err := ownerOnly(q, "Only the owner restores archived sessions"); err != nil {
		return nil, err
	}
	b := body(q)
	sid, reason := astr(b["sessionId"]), strings.TrimSpace(astr(b["reason"]))
	if reason == "" {
		return nil, fail(400, "Expected reason")
	}
	s, err := get(q, sid, "session")
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fail(404, "Unknown session")
	}
	at, archived := s["archivedAt"]
	if !archived {
		return nil, fail(409, "Session is not archived")
	}
	delete(s, "archivedAt")
	s["id"] = sid
	if err := put(q, "session", s); err != nil {
		return nil, err
	}
	// A teardown no host has fetched yet would remove the worktree of a live session again.
	pending, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"})
	if err != nil {
		return nil, err
	}
	for _, x := range pending {
		x["status"] = "stopped"
		if err := put(q, "command", x); err != nil {
			return nil, err
		}
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "session:" + sid, "field": "archivedAt", "previous": at, "value": nil, "reason": reason, "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
		return nil, err
	}
	return doc{"sessionId": sid, "teardownsWithdrawn": len(pending)}, nil
}
