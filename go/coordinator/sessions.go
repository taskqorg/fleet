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
	"fleet/coordinator/db"
)

var sessionStates = []string{"idle", "preparing", "running", "waiting", "succeeded", "failed", "cancelled"}

// sessionState: a session's state, its latest execution's (idle without one); nav_counts.go navSessionBase shares it.
const sessionState = `coalesce((SELECT e.state::text FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1),'idle')`

// sessionBase: every session as a list row. The first message and the activity are read per session by its indexed
// session_id (migration 77): activity_at is the latest change of the session, its messages and events, or the start or
// end of its executions (the default sort). Not execution.updated_at: host receipts touch it on every observation,
// which is not conversation activity.
var sessionBase = `WITH x AS (SELECT d.id, d.code, ` + convTables["session"].record() + ` AS data, d.project_id, d.seq, nullif(d.title,'') AS title,
 d.archived_at, d.actor, d.host_id, d.payload->'agentConfig'->>'harness' AS agent_harness,
 ` + sessionState + ` AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) AS harness,
 (SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1) AS first_message,
 greatest(d.updated_at,ex.last_at,(SELECT max(m.updated_at) FROM message m WHERE m.session_id=d.id),(SELECT max(v.updated_at) FROM event v WHERE v.session_id=d.id AND NOT v.delta)) AS activity_at,
 coalesce(ex.n,0) AS executions,
 (SELECT count(*) FROM question q WHERE q.session_id=d.id AND q.state='open') AS open_questions
 FROM session d
 LEFT JOIN LATERAL (SELECT count(*) AS n,max(greatest(e.started_at,e.ended_at)) AS last_at FROM execution e WHERE e.session_id=d.id) ex ON true)`

// sessionAgent: the harness of the last execution, else the configured one (the row's harness mark and the agent group).
const sessionAgent = "coalesce(x.harness,x.agent_harness)"

// sessionGroups: the first sort key (sessions_list group). Byte order (COLLATE "C") so the client's key comparison
// (app/src/lists.ts compareKeys) places a live row where the server would. State groups follow attention order.
var sessionGroups = map[string]string{
	"none":    `left(x.id,0)`,
	"project": `(coalesce(x.project_id,'~') COLLATE "C")`,
	"agent":   `(coalesce(` + sessionAgent + `,'~') COLLATE "C")`,
	"state":   `(CASE x.state WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'running' THEN '3' WHEN 'preparing' THEN '4' WHEN 'idle' THEN '5' WHEN 'succeeded' THEN '6' ELSE '7' END)`,
}
var sessionGroupLabels = map[string]string{"project": "x.project_id", "agent": sessionAgent, "state": "x.state"}

// sessionSorts: the second sort key inside a group, ascending numbers; "-" before a name is descending.
var sessionSorts = map[string]string{"activity": "(extract(epoch FROM x.activity_at)*1000)::float8", "created": "(x.seq::float8)"}

const defaultSessionGroup, defaultSessionSort = "project", "-activity"

// sessionOrder: a sessions list's group and sort (name and expressions); an unknown one is a 400, never a silent default.
type sessionOrder struct{ name, g, s string }

func sessionOrderOf(group, sort any) (sessionOrder, error) {
	g, ok := group.(string)
	if group == nil {
		g, ok = defaultSessionGroup, true
	}
	G, found := sessionGroups[g]
	if !ok || !found {
		return sessionOrder{}, fail(400, "group must be project, state, agent or none")
	}
	s, ok := sort.(string)
	if sort == nil {
		s, ok = defaultSessionSort, true
	}
	S, found := sessionSorts[strings.TrimPrefix(s, "-")]
	if !ok || !found {
		return sessionOrder{}, fail(400, "sort must be activity, created, -activity or -created")
	}
	if strings.HasPrefix(s, "-") {
		S = "(-" + S + ")"
	}
	return sessionOrder{g, G, S}, nil
}

// cols: the group and sort keys of each row; by: the row order (ids in byte order too, like the client).
func (o sessionOrder) cols() string { return o.g + " AS g," + o.s + " AS sk" }
func (o sessionOrder) by() string   { return o.g + "," + o.s + `,x.id COLLATE "C"` }

func sessionWhere(f doc, p *[]any, withState bool) ([]string, error) {
	if err := filterFields(f, "projectId", "excludeProjectId", "state", "search", "archived"); err != nil {
		return nil, err
	}
	// Host command sessions (program-worker: pipeline script steps, readiness checks, host tools) hold one host job and
	// no conversation; Runners and executions list them, the Conversations list does not.
	c := []string{"x.archived_at IS NULL", "coalesce(x.actor,'')<>'program-worker'"}
	if f["archived"] == true {
		c[0] = "x.archived_at IS NOT NULL"
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
		c = append(c, searchWhere(s, p, []string{"x.title", "x.first_message", "x.id", "x.code"}, map[string]string{"agent": sessionAgent, "host": "coalesce((SELECT e.host_id FROM execution e WHERE e.session_id=x.id ORDER BY e.seq DESC LIMIT 1),x.host_id)", "project": "x.project_id", "state": "x.state"}, withState)...)
	}
	return c, nil
}

func sessionRow(x doc) doc {
	d := mapOf(x["data"])
	ac := mapOf(d["agentConfig"])
	// The stored title (session_rename, or the generated one: session_titles.go); until it exists, the first message.
	title := any(nil)
	if t := []rune(firstString(x["title"], x["first_message"])); len(t) > 0 {
		title = string(t[:min(200, len(t))])
	}
	harness := x["harness"]
	if harness == nil {
		harness = ac["harness"]
	}
	return doc{"id": x["id"], "code": x["code"], "title": title, "state": x["state"], "harness": harness, "profileId": ac["id"], "projectId": x["project_id"], "createdAt": d["createdAt"],
		"activityAt": x["activity_at"], "executions": x["executions"], "openQuestions": x["open_questions"], "archivedAt": d["archivedAt"], "sortKey": []any{x["g"], num(x["sk"]), x["id"]}}
}

// sessionSummary: counts per state (without the state filter, for the quick filter) and the groups of the filtered list,
// in one pass over sessionBase: rows match the filter without states, and the groups count those that also match the states.
func sessionSummary(q *Request, f doc, o sessionOrder) (doc, []doc, error) {
	p := []any{}
	cc, err := sessionWhere(f, &p, false)
	if err != nil {
		return nil, nil, err
	}
	gc, err := sessionWhere(f, &p, true)
	if err != nil {
		return nil, nil, err
	}
	label := "NULL::text"
	if l, ok := sessionGroupLabels[o.name]; ok {
		label = l
	}
	rs, err := rowsOf(q, sessionBase+" SELECT x.state,"+o.g+" AS g,min("+label+") AS label,count(*) AS n,count(*) FILTER (WHERE "+strings.Join(gc, " AND ")+") AS ng FROM x WHERE "+strings.Join(cc, " AND ")+" GROUP BY 1,2 ORDER BY 2", p...)
	if err != nil {
		return nil, nil, err
	}
	counts, groups := zeroCounts(sessionStates), []doc{}
	for _, r := range rs {
		counts[astr(r["state"])] = num(counts[astr(r["state"])]) + num(r["n"])
		if o.name == "none" || num(r["ng"]) == 0 {
			continue
		}
		// Rows come ordered by group key, so a group's rows (one per state) are adjacent.
		if n := len(groups); n > 0 && groups[n-1]["key"] == r["g"] {
			groups[n-1]["count"] = num(groups[n-1]["count"]) + num(r["ng"])
			if groups[n-1]["label"] == nil {
				groups[n-1]["label"] = r["label"]
			}
			continue
		}
		groups = append(groups, doc{"key": r["g"], "label": r["label"], "count": num(r["ng"])})
	}
	return counts, groups, nil
}

func sessionsPage(q *Request, b doc) (doc, error) {
	f := mapOf(b["filter"])
	if f == nil {
		f = doc{}
	}
	o, err := sessionOrderOf(b["group"], b["sort"])
	if err != nil {
		return nil, err
	}
	limit := pageLimit(b["limit"], 100)
	p := []any{}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return nil, err
	}
	if b["after"] != nil {
		a, ok := b["after"].([]any)
		if !ok || len(a) != 3 {
			return nil, fail(400, "Invalid after")
		}
		p = append(p, fmt.Sprint(a[0]), num(a[1]), fmt.Sprint(a[2]))
		c = append(c, fmt.Sprintf(`(%s,%s,x.id COLLATE "C") > ($%d,$%d::float8,$%d)`, o.g, o.s, len(p)-2, len(p)-1, len(p)))
	}
	rs, err := rowsOf(q, fmt.Sprintf("%s SELECT *,%s FROM x WHERE %s ORDER BY %s LIMIT %d", sessionBase, o.cols(), strings.Join(c, " AND "), o.by(), limit+1), p...)
	if err != nil {
		return nil, err
	}
	counts, groups, err := sessionSummary(q, f, o)
	if err != nil {
		return nil, err
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
	return doc{"sessions": page, "nextAfter": next, "counts": counts, "total": total(counts), "groups": groups}, nil
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
	c, err := first(q, "SELECT code FROM session WHERE id=$1", sid)
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
	if s["executions"], err = sessionExecutions(q, sid); err != nil {
		return nil, err
	}
	res, err := sessionResourceRollup(q, sid)
	if err != nil {
		return nil, err
	}
	s["cost"], s["resources"] = res.Total.Cost, res
	return s, nil
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
	// A removed host never fetches it: its worktrees went with it, and a pending command would keep purge waiting.
	if live, err := knownHost(q, astr(last["hostId"])); err != nil || !live {
		return doc{"sessionId": sid, "cleanup": "skipped", "reason": "host-gone"}, err
	}
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
	if s["branch"] == false { // a session started without a branch (presets.go): its worktree was detached
		payload["branch"] = false
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

// errSessionCreateKeys: session_create takes an empty object.
var errSessionCreateKeys = fail(400, "session_create takes no identity or linkage fields")

func sessionCreate(q *Request) (doc, error) {
	s := doc{"id": newID(), "createdAt": nowMs(), "directedBy": director(q)}
	if callerReadOnly(q) { // an agent of a read-only conversation creates read-only sessions (presets.go)
		s["access"], s["branch"] = "read", false
	}
	if err := put(q, "session", s); err != nil {
		return nil, err
	}
	out := doc{"sessionId": s["id"]}
	for k, v := range s {
		out[k] = v
	}
	return out, nil
}

// sessionArg: a required sessionId, not empty.
func sessionArg(sid string) (string, error) {
	if sid == "" {
		return "", fail(400, "Expected sessionId")
	}
	return sid, nil
}

func init() {
	route("GET /api/executions/{id}/delegations", func(q *Request) (any, error) {
		id := q.r.PathValue("id")
		if _, err := get(q, id, "execution"); err != nil {
			return nil, err
		}
		ds, err := delegationsWhere(q, "delegation->>'parentExecutionId'=$1 OR delegation->>'childExecutionId'=$1 OR delegation->>'continuationExecutionId'=$1", id)
		return doc{"delegations": ds}, err
	})
	op("session_create", func(q *Request, r *contract.SessionCreateRequest) (contract.SessionCreateReply, error) {
		if len(*r) > 0 {
			return contract.SessionCreateReply{}, errSessionCreateKeys
		}
		return replyAs[contract.SessionCreateReply](sessionCreate(q))
	})
	op("session_get", func(q *Request, r *contract.SessionGetRequest) (contract.SessionGetReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionGetReply{}, err
		}
		return replyAs[contract.SessionGetReply](sessionGet(q, sid))
	})
	op("session_profile", func(q *Request, r *contract.SessionProfileRequest) (contract.SessionProfileReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionProfileReply{}, err
		}
		b := docOf(r)
		delete(b, "sessionId")
		return replyAs[contract.SessionProfileReply](sessionProfile(q, sid, b))
	})
	op("session_archive", func(q *Request, r *contract.SessionArchiveRequest) (contract.SessionArchiveReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionArchiveReply{}, err
		}
		return replyAs[contract.SessionArchiveReply](sessionArchive(q, sid))
	})
	op("session_unarchive", sessionUnarchive)
	op("sessions_list", func(q *Request, r *contract.SessionsListRequest) (contract.SessionsListReply, error) {
		// Read as sent: a null projectId item means sessions outside projects, which the typed list cannot hold.
		return replyAs[contract.SessionsListReply](sessionsPage(q, body(q)))
	})
	op("list_membership", func(q *Request, b *contract.ListMembershipRequest) (contract.ListMembershipReply, error) {
		out, err := listMembership(q, b)
		return replyAs[contract.ListMembershipReply](out, err)
	})
	// /api/tools: the operation table as MCP tools, each call in its own transaction.
	longRoute("POST /api/tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet", operations) })
}

// listMembership: which of ids each named list (tasks, sessions) holds, with the list's counts and groups.
func listMembership(q *Request, b *contract.ListMembershipRequest) (doc, error) {
	if len(b.Ids) > 1000 {
		return nil, fail(400, "Expected at most 1000 ids")
	}
	if len(b.Lists) < 1 || len(b.Lists) > 2 {
		return nil, fail(400, "Expected one or two lists")
	}
	ids := b.Ids
	// Each filter read as sent: a null projectId item means rows outside projects, which the typed list cannot hold.
	var raw struct {
		Lists []struct {
			Filter doc `json:"filter"`
		} `json:"lists"`
	}
	json.Unmarshal(q.body, &raw)
	out := []doc{}
	for i, l := range b.Lists {
		f := doc{}
		if i < len(raw.Lists) && raw.Lists[i].Filter != nil {
			f = raw.Lists[i].Filter
		}
		group, sort := optAny(l.Group), optAny(l.Sort)
		switch l.Name {
		case "tasks":
			rs, err := taskMembership(q, f, group, sort, ids)
			if err != nil {
				return nil, err
			}
			counts, groups, err := taskSummary(q, f, group, false)
			if err != nil {
				return nil, err
			}
			out = append(out, doc{"name": "tasks", "rows": rs, "counts": counts, "groups": groups})
		case "sessions":
			o, err := sessionOrderOf(group, sort)
			if err != nil {
				return nil, err
			}
			p := []any{}
			c, err := sessionWhere(f, &p, true)
			if err != nil {
				return nil, err
			}
			p = append(p, ids)
			c = append(c, fmt.Sprintf("x.id=ANY($%d)", len(p)))
			rs, err := rowsOf(q, sessionBase+" SELECT *,"+o.cols()+" FROM x WHERE "+strings.Join(c, " AND ")+" ORDER BY "+o.by(), p...)
			if err != nil {
				return nil, err
			}
			rows := []doc{}
			for _, r := range rs {
				rows = append(rows, sessionRow(r))
			}
			counts, groups, err := sessionSummary(q, f, o)
			if err != nil {
				return nil, err
			}
			out = append(out, doc{"name": "sessions", "rows": rows, "counts": counts, "groups": groups})
		default:
			return nil, fail(400, "Unknown list")
		}
	}
	return doc{"lists": out}, nil
}

// sessionUnarchive: undo session_archive (owner or full access). The teardown already sent is not undone: a removed
// worktree is prepared again at the next start.
func sessionUnarchive(q *Request, r *contract.SessionUnarchiveRequest) (contract.SessionUnarchiveReply, error) {
	var none contract.SessionUnarchiveReply
	sid, reason := r.SessionId, strings.TrimSpace(r.Reason)
	if reason == "" {
		return none, fail(400, "Expected reason")
	}
	s, err := get(q, sid, "session")
	if err != nil {
		return none, err
	}
	if s == nil {
		return none, fail(404, "Unknown session")
	}
	at, archived := s["archivedAt"]
	if !archived {
		return none, fail(409, "Session is not archived")
	}
	delete(s, "archivedAt")
	s["id"] = sid
	if err := put(q, "session", s); err != nil {
		return none, err
	}
	// A teardown no host has fetched yet would remove the worktree of a live session again.
	pending, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"})
	if err != nil {
		return none, err
	}
	for _, x := range pending {
		x["status"] = "stopped"
		if err := put(q, "command", x); err != nil {
			return none, err
		}
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "session:" + sid, "field": "archivedAt", "previous": at, "value": nil, "reason": reason, "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
		return none, err
	}
	return contract.SessionUnarchiveReply{SessionId: sid, TeardownsWithdrawn: int64(len(pending))}, nil
}

// Conversation records (migration 77, docs/designs/fleet-standard.md S1): the kinds a conversation is made of live in
// their own tables. A record field a query filters, joins, orders or a state machine owns is a column; payload holds the
// record's other fields. all/get/put (records.go) serve these kinds through convTables, so a caller still reads and
// writes the record shape while each field has one column. A field whose value does not have its column's type stays
// in payload.
type convCol struct{ field, col, typ string } // typ: text, bool, int, count (int, absent as 0), ms (epoch ms as timestamptz)

type convTable struct {
	table string
	cols  []convCol
	// keep: the columns a put never overwrites while the project stays (their own writers update them in place: the
	// session title fields of session_rename and the title pass). A command's status is commandTransition's (commandPut).
	keep []string
	// delta: the table holds two kinds (event and event-delta), told apart by the delta column.
	delta *bool
	// state: the table's state machine column (typed_records.go): set on insert, moved only by its owner afterwards.
	state *convState
}

var convFalse, convTrue = false, true

var convTables = map[string]*convTable{
	"session": {table: "session", cols: []convCol{{"projectId", "project_id", "text"}, {"taskId", "task_id", "text"}, {"hostId", "host_id", "text"},
		{"actor", "actor", "text"}, {"directedBy", "directed_by", "text"}, {"directorAccessRevokedAt", "director_access_revoked_at", "ms"},
		{"parentExecutionId", "parent_execution_id", "text"}, {"createdByExecutionId", "created_by_execution_id", "text"},
		{"createdBySessionId", "created_by_session_id", "text"}, {"sourceSessionId", "source_session_id", "text"},
		{"pipelineRunId", "pipeline_run_id", "text"}, {"createdAt", "created_at", "ms"}, {"archivedAt", "archived_at", "ms"},
		{"title", "title", "text"}, {"titleBy", "title_by", "text"}, {"titleCall", "title_call", "text"},
		{"titleAttemptAt", "title_attempt_at", "ms"}, {"titleAttempts", "title_attempts", "count"}, {"titleUnknownCall", "title_unknown_call", "text"}},
		keep: []string{"title", "title_by", "title_call", "title_attempt_at", "title_attempts", "title_unknown_call"}},
	"message": {table: "message", cols: []convCol{{"sessionId", "session_id", "text"}, {"executionId", "execution_id", "text"}, {"projectId", "project_id", "text"},
		{"requestId", "request_id", "text"}, {"text", "text", "text"}, {"sentBy", "sent_by", "text"}, {"source", "source", "text"}}},
	"event":       {table: "event", cols: convEventCols, delta: &convFalse},
	"event-delta": {table: "event", cols: convEventCols, delta: &convTrue},
	"queued-message": {table: "queued_message", cols: []convCol{{"sessionId", "session_id", "text"}, {"projectId", "project_id", "text"}, {"taskId", "task_id", "text"},
		{"requestId", "request_id", "text"}, {"text", "text", "text"}, {"state", "state", "text"}, {"executionId", "execution_id", "text"},
		{"steerCommandId", "steer_command_id", "text"}, {"sentBy", "sent_by", "text"}, {"source", "source", "text"}, {"createdAt", "created_at", "ms"}}},
	"command": {table: "command", cols: []convCol{{"executionId", "execution_id", "text"}, {"sessionId", "session_id", "text"}, {"hostId", "host_id", "text"},
		{"projectId", "project_id", "text"}, {"kind", "kind", "text"}, {"status", "status", "status"}, {"stopDetail", "stop_detail", "text"},
		{"handedOut", "handed_out", "bool"}, {"traceparent", "traceparent", "text"}}},
	"handoff":            {table: "handoff", cols: []convCol{{"sessionId", "session_id", "text"}, {"projectId", "project_id", "text"}, {"state", "state", "text"}}},
	"session-fork":       {table: "session_fork", cols: []convCol{{"sessionId", "session_id", "text"}, {"sourceSessionId", "source_session_id", "text"}, {"projectId", "project_id", "text"}, {"requestId", "request_id", "text"}}},
	"session-transition": {table: "session_transition", cols: []convCol{{"sessionId", "session_id", "text"}, {"projectId", "project_id", "text"}}},
	"worker-done":        {table: "worker_done", cols: []convCol{{"executionId", "execution_id", "text"}, {"projectId", "project_id", "text"}}},
}

// convEventAny: the event table with both kinds (eventsWhere).
var convEventAny = &convTable{table: "event", cols: convEventCols}

var convEventCols = []convCol{{"executionId", "execution_id", "text"}, {"sessionId", "session_id", "text"}, {"projectId", "project_id", "text"},
	{"seq", "event_seq", "int"}, {"type", "type", "text"}, {"timestamp", "at", "ms"}}

// convFieldOf: the record field a conversation table's column holds (a reason names what the record shows), else col.
func convFieldOf(table, col string) string {
	for _, t := range convTables {
		if t.table == table {
			for _, c := range t.cols {
				if c.col == col {
					return c.field
				}
			}
		}
	}
	return col
}

// record: the SQL expression of a row as its record.
func (t *convTable) record() string {
	parts := []string{"'id',id"}
	for _, c := range t.cols {
		x := c.col
		switch c.typ {
		case "ms":
			x = "(extract(epoch FROM " + c.col + ")*1000)::bigint"
		case "count":
			x = "nullif(" + c.col + ",0)"
		case "status":
			x = c.col + "::text"
		case "hoststate":
			x = "nullif(" + c.col + ",'" + hostStateActive + "')"
		}
		parts = append(parts, "'"+c.field+"',"+x)
	}
	return "(payload||jsonb_strip_nulls(jsonb_build_object(" + strings.Join(parts, ",") + ")))"
}

func (t *convTable) kindWhere() string {
	if t.delta == nil {
		return "true"
	}
	if *t.delta {
		return "delta"
	}
	return "NOT delta"
}

// colValue: v as the column's parameter, ok false when v does not have the column's type.
func (c convCol) value(v any) (any, bool) {
	switch c.typ {
	case "text", "status", "hoststate":
		s, ok := v.(string)
		return s, ok
	case "bool":
		b, ok := v.(bool)
		return b, ok
	case "int", "count", "ms":
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int64:
			f = float64(n)
		case int:
			f = float64(n)
		case json.Number:
			x, err := n.Float64()
			if err != nil {
				return nil, false
			}
			f = x
		default:
			return nil, false
		}
		if c.typ == "ms" {
			return f, true
		}
		if f != float64(int64(f)) {
			return nil, false
		}
		return int64(f), true
	}
	return nil, false
}

func (c convCol) param(n int) string {
	p := fmt.Sprintf("$%d", n)
	switch c.typ {
	case "ms":
		return "to_timestamp(" + p + "::float8/1000)"
	case "int":
		return p + "::bigint"
	case "count":
		return "coalesce(" + p + "::int,0)"
	case "bool":
		return p + "::boolean"
	case "status":
		return p + "::command_status"
	}
	return p + "::text"
}

func convAll(q *Request, t *convTable, f doc) ([]doc, error) {
	where, args := []string{t.kindWhere()}, []any{}
	rest := doc{}
	for _, k := range sortedKeysOf(f) {
		if k == "id" {
			args = append(args, f[k])
			where = append(where, fmt.Sprintf("id=$%d::text", len(args)))
			continue
		}
		c, found := t.col(k)
		if !found {
			rest[k] = f[k]
			continue
		}
		v, ok := c.value(f[k])
		if !ok {
			return []doc{}, nil // no column value equals a value of another type
		}
		args = append(args, v)
		where = append(where, c.col+"="+c.param(len(args)))
	}
	if len(rest) > 0 {
		args = append(args, js(rest))
		where = append(where, fmt.Sprintf("payload @> $%d::jsonb", len(args)))
	}
	return rows(q, "SELECT "+t.record()+" FROM "+t.table+" WHERE "+strings.Join(where, " AND ")+" ORDER BY seq", args...)
}

func (t *convTable) col(field string) (convCol, bool) {
	for _, c := range t.cols {
		if c.field == field {
			return c, true
		}
	}
	return convCol{}, false
}

func convGet(q *Request, t *convTable, id string, forUpdate bool) (doc, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	return one(q, "SELECT "+t.record()+" FROM "+t.table+" WHERE id=$1 AND "+t.kindWhere()+lock, id)
}

// convPut writes a record: every column from its field (absent: NULL), payload from the other fields. A put of an
// existing row keeps the keep columns while the project stays (a new project clears the session title fields, as a
// title belongs to its project's conversation); a command's status moves only through commandTransition.
func convPut(q *Request, kind string, t *convTable, v doc) error {
	id, _ := v["id"].(string)
	if id == "" {
		return fmt.Errorf("%s record without an id", kind)
	}
	rest := doc{}
	for k, x := range v {
		if _, found := t.col(k); !found && k != "id" {
			rest[k] = x
		}
	}
	if t.table == "command" {
		return commandPut(q, t, v, rest)
	}
	prevState, wantState := "", ""
	if t.state != nil {
		var err error
		if prevState, err = t.state.of(q, id); err != nil {
			return err
		}
		wantState, _ = v[t.state.field].(string)
		if c, _ := t.col(t.state.field); wantState == "" && c.typ == "hoststate" {
			wantState = hostStateActive
		}
	}
	cols, vals, args := []string{"id"}, []string{"$1::text"}, []any{id}
	for _, c := range t.cols {
		x, given := v[c.field]
		if !given || x == nil {
			cols, vals = append(cols, c.col), append(vals, "NULL")
			if c.typ == "count" {
				vals[len(vals)-1] = "0"
			}
			if c.typ == "hoststate" {
				vals[len(vals)-1] = "'" + hostStateActive + "'"
			}
			continue
		}
		p, ok := c.value(x)
		if !ok {
			rest[c.field] = x
			cols, vals = append(cols, c.col), append(vals, "NULL")
			if c.typ == "count" {
				vals[len(vals)-1] = "0"
			}
			continue
		}
		args = append(args, p)
		cols, vals = append(cols, c.col), append(vals, c.param(len(args)))
	}
	args = append(args, js(rest))
	cols, vals = append(cols, "payload"), append(vals, fmt.Sprintf("$%d::jsonb", len(args)))
	if t.delta != nil {
		cols, vals = append(cols, "delta"), append(vals, fmt.Sprint(*t.delta))
	}
	sets := []string{"payload=excluded.payload", "updated_at=now()"}
	for _, c := range cols[1:] {
		switch {
		case c == "payload" || c == "delta":
		case t.state != nil && c == t.state.col:
		case slices.Contains(t.keep, c):
			reset := "NULL"
			if c == "title_attempts" {
				reset = "0"
			}
			sets = append(sets, fmt.Sprintf("%s=CASE WHEN %s.project_id IS NOT DISTINCT FROM excluded.project_id THEN %s.%s ELSE %s END", c, t.table, t.table, c, reset))
		default:
			sets = append(sets, c+"=excluded."+c)
		}
	}
	guard := ""
	if t.delta != nil {
		guard = " WHERE " + t.table + ".delta=excluded.delta"
	}
	if _, err := q.tx.Exec(q.ctx(), "INSERT INTO "+t.table+"("+strings.Join(cols, ",")+") VALUES("+strings.Join(vals, ",")+") ON CONFLICT(id) DO UPDATE SET "+strings.Join(sets, ",")+guard, args...); err != nil {
		return err
	}
	if prevState != "" && wantState != "" && wantState != prevState {
		if err := t.state.move(q, id, wantState); err != nil {
			return err
		}
	}
	switch kind {
	case "queued-message", "host", "managed-account":
		if q.app != nil {
			afterCommit(q, func() { q.app.wakeDispatch() })
		}
	}
	return nil
}

// commandPut: a command record through its state owner (commands.go): a new command is created with its status, an
// existing one keeps its status unless the record names another, which commandTransition moves it to.
func commandPut(q *Request, t *convTable, v doc, rest doc) error {
	id, _ := v["id"].(string)
	text := func(field string) *string {
		if c, ok := t.col(field); ok {
			if x, ok := c.value(v[field]); ok {
				s := x.(string)
				return &s
			}
		}
		return nil
	}
	kind, status := text("kind"), text("status")
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
	if b, ok := v["handedOut"].(bool); ok {
		handed = &b
	}
	if err := commandCreate(q, db.CommandUpsertParams{ID: id, ExecutionID: text("executionId"), SessionID: text("sessionId"), HostID: text("hostId"),
		ProjectID: text("projectId"), Kind: *kind, Status: db.CommandStatus(*status), StopDetail: text("stopDetail"), HandedOut: handed,
		Traceparent: text("traceparent"), Payload: jsonBytes(rest)}); err != nil {
		return err
	}
	if prev != "" && prev != *status {
		if err := commandTransition(q, id, *status, text("stopDetail")); err != nil {
			return err
		}
	} else if prev != "" {
		if err := commandDetail(q, id, text("stopDetail")); err != nil {
			return err
		}
	}
	if hid := text("hostId"); *status == "pending" && hid != nil && q.app != nil {
		afterCommit(q, func() { q.app.wakeHost(*hid) })
	}
	return nil
}
