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

// sessionListDBRow: a sessions_list and list_membership row (sqlSessionListCols) with its group and sort keys.
type sessionListDBRow struct {
	ID            string   `db:"id"`
	Code          *string  `db:"code"`
	Title         *string  `db:"title"`
	FirstMessage  *string  `db:"first_message"`
	State         string   `db:"state"`
	Harness       *string  `db:"harness"`
	AgentHarness  *string  `db:"agent_harness"`
	ProfileID     *string  `db:"profile_id"`
	ProjectID     *string  `db:"project_id"`
	CreatedAt     *float64 `db:"created_ms"`
	ArchivedAt    *float64 `db:"archived_ms"`
	ActivityAt    *string  `db:"activity_at"`
	Executions    int64    `db:"executions"`
	OpenQuestions int64    `db:"open_questions"`
	G             string   `db:"g"`
	Sk            float64  `db:"sk"`
}

func (x sessionListDBRow) row() contract.SessionRow {
	// The stored title (session_rename, or the generated one: session_titles.go); until it exists, the first message.
	var title *string
	if t := []rune(firstString(deref(x.Title), deref(x.FirstMessage))); len(t) > 0 {
		title = ptr(string(t[:min(200, len(t))]))
	}
	harness := x.Harness
	if harness == nil {
		harness = x.AgentHarness
	}
	return contract.SessionRow{Id: x.ID, Code: x.Code, Title: title, State: x.State, Harness: harness, ProfileId: x.ProfileID, ProjectId: x.ProjectID,
		CreatedAt: x.CreatedAt, ActivityAt: x.ActivityAt, Executions: x.Executions, OpenQuestions: x.OpenQuestions, ArchivedAt: x.ArchivedAt,
		SortKey: sortKey(x.G, x.Sk, x.ID)}
}

// sortKey: a list row's position, [group key, sort key, id].
func sortKey(g string, sk float64, id string) []contract.SortKeyPart {
	return []contract.SortKeyPart{jsonBytes(g), jsonBytes(sk), jsonBytes(id)}
}

// sessionFilter: a sessions list's filter as sent (rowFilterOf).
func sessionFilter(raw json.RawMessage) (rowFilter, error) {
	return rowFilterOf(raw, "projectId", "excludeProjectId", "state", "search", "archived")
}

// sessionListRows: the session rows of an assembled list statement.
func sessionListRows(q *Request, sql string, p []any, err error) ([]contract.SessionRow, error) {
	if err != nil {
		return nil, err
	}
	rs, err := typedRows[sessionListDBRow](q, sql, p)
	out := make([]contract.SessionRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.row())
	}
	return out, err
}

// sessionSummary: counts per state (without the state filter, for the quick filter) and the groups of the filtered list,
// in one pass over the sessions: rows match the filter without states, and the groups count those that also match the states.
func sessionSummary(q *Request, f rowFilter, o sessionOrder) (map[string]int64, []contract.ListGroup, error) {
	sql, p, err := sqlSessionSummary(f, o)
	if err != nil {
		return nil, nil, err
	}
	type summaryRow struct {
		State string  `db:"state"`
		G     string  `db:"g"`
		Label *string `db:"label"`
		N     int64   `db:"n"`
		Ng    int64   `db:"ng"`
	}
	rs, err := typedRows[summaryRow](q, sql, p)
	if err != nil {
		return nil, nil, err
	}
	counts, groups := zeroCounts(sessionStates), []contract.ListGroup{}
	for _, r := range rs {
		counts[r.State] += r.N
		if o.name == "none" || r.Ng == 0 {
			continue
		}
		// Rows come ordered by group key, so a group's rows (one per state) are adjacent.
		if n := len(groups); n > 0 && groups[n-1].Key == r.G {
			groups[n-1].Count += r.Ng
			if groups[n-1].Label == nil {
				groups[n-1].Label = r.Label
			}
			continue
		}
		groups = append(groups, contract.ListGroup{Key: r.G, Label: r.Label, Count: r.Ng})
	}
	return counts, groups, nil
}

// listAfter: a page request's after, the sort key of the last row it has: [group key, sort key, id].
func listAfter(after []json.RawMessage) ([]any, error) {
	if after == nil {
		return nil, nil
	}
	var a []any
	if json.Unmarshal(jsonBytes(after), &a) != nil || len(a) != 3 {
		return nil, fail(400, "Invalid after")
	}
	return a, nil
}

func sessionsPage(q *Request, r *contract.SessionsListRequest) (contract.SessionsListReply, error) {
	var out contract.SessionsListReply
	// Read as sent: a null projectId item means sessions outside projects, which the typed list cannot hold.
	var raw struct {
		Filter json.RawMessage `json:"filter"`
	}
	json.Unmarshal(q.body, &raw)
	f, err := sessionFilter(raw.Filter)
	if err != nil {
		return out, err
	}
	o, err := sessionOrderOf(r.Group, r.Sort)
	if err != nil {
		return out, err
	}
	limit := 100
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 100)
	}
	after, err := listAfter(r.After)
	if err != nil {
		return out, err
	}
	sql, p, err := sqlSessionPage(f, o, after, limit+1)
	rs, err := sessionListRows(q, sql, p, err)
	if err != nil {
		return out, err
	}
	if out.Counts, out.Groups, err = sessionSummary(q, f, o); err != nil {
		return out, err
	}
	out.Sessions = rs[:min(limit, len(rs))]
	if len(rs) > limit {
		out.NextAfter = jsonBytes(out.Sessions[len(out.Sessions)-1].SortKey)
	}
	out.Total = total(out.Counts)
	return out, nil
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
	if err := audit(q, "session:"+sid, "archivedAt", nil, s["archivedAt"], auditExtra{}); err != nil {
		return nil, err
	}
	if last == nil {
		return doc{"sessionId": sid, "archivedAt": s["archivedAt"], "teardown": nil}, nil
	}
	return teardownCommand(q, s, last, false)
}

// sessionDeleteArchive: a deleted session is archived (object_delete, cleanup.go tombstone) and its worktree torn down
// on the host of its last execution, unless it is archived already or a teardown is on its way; torn: one was queued.
func sessionDeleteArchive(q *Request, sid string) (bool, error) {
	s, err := get(q, sid, "session")
	if err != nil {
		return false, err
	}
	if s["archivedAt"] != nil {
		return false, nil
	}
	if pending, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"}); err != nil || len(pending) > 0 {
		return false, err
	}
	es, err := sessionExecutions(q, sid)
	if err != nil {
		return false, err
	}
	var last doc
	for _, e := range es {
		if astr(e["hostId"]) != "" {
			last = e
		}
	}
	s["archivedAt"] = nowMs()
	if err := put(q, "session", s); err != nil {
		return false, err
	}
	if last == nil {
		return false, nil
	}
	// Queued after the stop: the executor keeps a worktree with uncommitted or unpushed work (cleanup.keepWhen).
	t, err := teardownCommand(q, s, last, false)
	return err == nil && t["kind"] == "teardown", err
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
		return sessionsPage(q, r)
	})
	op("list_membership", func(q *Request, b *contract.ListMembershipRequest) (contract.ListMembershipReply, error) {
		return listMembership(q, b)
	})
	// /api/tools: the operation table as MCP tools, each call in its own transaction.
	longRoute("POST /api/tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet", operations) })
}

// listMembership: which of ids each named list (tasks, sessions) holds, with the list's counts and groups.
func listMembership(q *Request, b *contract.ListMembershipRequest) (contract.ListMembershipReply, error) {
	out := contract.ListMembershipReply{Lists: []contract.MembershipAnswer{}}
	if len(b.Ids) > 1000 {
		return out, fail(400, "Expected at most 1000 ids")
	}
	if len(b.Lists) < 1 || len(b.Lists) > 2 {
		return out, fail(400, "Expected one or two lists")
	}
	// Each filter read as sent: a null projectId item means rows outside projects, which the typed list cannot hold.
	var raw struct {
		Lists []struct {
			Filter json.RawMessage `json:"filter"`
		} `json:"lists"`
	}
	json.Unmarshal(q.body, &raw)
	for i, l := range b.Lists {
		var filter json.RawMessage
		if i < len(raw.Lists) {
			filter = raw.Lists[i].Filter
		}
		a := contract.MembershipAnswer{Name: l.Name, Rows: []contract.ListRow{}}
		switch l.Name {
		case "tasks":
			f, err := taskFilter(filter)
			if err != nil {
				return out, err
			}
			rs, err := taskMembership(q, f, l.Group, l.Sort, b.Ids)
			if err != nil {
				return out, err
			}
			for _, r := range rs {
				a.Rows = append(a.Rows, jsonBytes(r))
			}
			if a.Counts, a.Groups, err = taskSummary(q, f, l.Group, false); err != nil {
				return out, err
			}
		case "sessions":
			f, err := sessionFilter(filter)
			if err != nil {
				return out, err
			}
			o, err := sessionOrderOf(l.Group, l.Sort)
			if err != nil {
				return out, err
			}
			sql, p, err := sqlSessionRows(f, o, b.Ids)
			rs, err := sessionListRows(q, sql, p, err)
			if err != nil {
				return out, err
			}
			for _, r := range rs {
				a.Rows = append(a.Rows, jsonBytes(r))
			}
			if a.Counts, a.Groups, err = sessionSummary(q, f, o); err != nil {
				return out, err
			}
		default:
			return out, fail(400, "Unknown list")
		}
		out.Lists = append(out.Lists, a)
	}
	return out, nil
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
	if err := audit(q, "session:"+sid, "archivedAt", at, nil, auditExtra{Reason: reason}); err != nil {
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
		wantState, _ = v[t.state.field].(string) // a record without its state keeps the stored one
	}
	cols, vals, args := []string{"id"}, []string{"$1::text"}, []any{id}
	for _, c := range t.cols {
		x, given := v[c.field]
		if t.state != nil && c.col == t.state.col {
			if _, ok := x.(string); !ok { // a new row without a state starts in the column's default state
				cols, vals = append(cols, c.col), append(vals, "DEFAULT")
				continue
			}
		}
		if !given || x == nil {
			cols, vals = append(cols, c.col), append(vals, "NULL")
			if c.typ == "count" {
				vals[len(vals)-1] = "0"
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
