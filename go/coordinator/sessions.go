// Sessions (docs/designs/fleet-operator-app.md §6): create, profile, read with its queue, questions, handoffs and
// delegations, archive, message submit, the Conversations list and list membership.
package main

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
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

// related: a delegation record with the states of its parent, child and continuation runs.
func related(q *Request, d rawObject) (rawObject, error) {
	out := rawObject{}
	maps.Copy(out, d)
	for _, r := range []struct{ key, field string }{{"parentState", "parentExecutionId"}, {"childState", "childExecutionId"}, {"continuationState", "continuationExecutionId"}} {
		e, err := recMaybe[rawObject](q, recExecution, d.text(r.field))
		if err != nil {
			return nil, err
		}
		out[r.key] = json.RawMessage("null")
		if e != nil {
			if st, ok := (*e)["state"]; ok {
				out[r.key] = st
			}
		} else if r.key != "continuationState" && d.has(r.field) {
			return nil, errNotFound
		}
	}
	return out, nil
}

// delegationsOf: the delegations read (SessionDelegations, ExecutionDelegations), each with the states of its runs.
func delegationsOf(q *Request, ds [][]byte, err error) ([]rawObject, error) {
	if err != nil {
		return nil, err
	}
	out := []rawObject{}
	for _, raw := range ds {
		var d rawObject
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		r, err := related(q, d)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func sessionGet(q *Request, sid string) (rawObject, error) {
	s, err := recGet[rawObject](q, recSession, sid)
	if err != nil {
		return nil, err
	}
	v, err := sessionView(q, sid)
	if err != nil {
		return nil, err
	}
	s["queued"], s["questions"], s["handoffs"] = jsonBytes(v.Queued), jsonBytes(v.Questions), jsonBytes(v.Handoffs)
	s["transitions"], s["forks"] = jsonBytes(v.Transitions), jsonBytes(v.Forks)
	s["sessionId"] = jsonBytes(sid)
	code, err := q.db().SessionCode(q.ctx(), sid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	s["code"] = jsonBytes(code)
	ds, err := q.db().SessionDelegations(q.ctx(), sid)
	delegations, err := delegationsOf(q, ds, err)
	if err != nil {
		return nil, err
	}
	s["delegations"] = jsonBytes(delegations)
	messages, err := recAll[rawObject](q, recMessage, agentFilter{SessionID: &sid})
	if err != nil {
		return nil, err
	}
	s["messages"] = jsonBytes(messages)
	executions, err := recAll[rawObject](q, recExecution, agentFilter{SessionID: &sid})
	if err != nil {
		return nil, err
	}
	s["executions"] = jsonBytes(executions)
	res, err := sessionResourceRollup(q, sid)
	if err != nil {
		return nil, err
	}
	s["cost"], s["resources"] = jsonBytes(res.Total.Cost), jsonBytes(res)
	return s, nil
}

func sessionProfile(q *Request, r *contract.SessionProfileRequest) (contract.SessionProfileReply, error) {
	var none contract.SessionProfileReply
	sid := r.SessionId
	s, err := recGet[agentSession](q, recSession, sid)
	if err != nil {
		return none, err
	}
	pid := r.ProfileId
	if a, err := agentActive(q, sid); err != nil || a != nil {
		if err == nil {
			err = fail(409, "Execution active")
		}
		return none, err
	}
	if r.Account != nil && !accountPattern.MatchString(*r.Account) {
		return none, fail(400, "Invalid named host account")
	}
	if w := r.WorkingDirectory; w != nil {
		if !strings.HasPrefix(*w, "/") || strings.Contains(*w, "..") {
			return none, fail(400, "Invalid working directory")
		}
		s.WorkingDirectory = *w
		if err := recPut(q, recSession, &s); err != nil {
			return none, err
		}
	}
	if s.NativeSessionID != "" {
		args := agentTransitionArgs{SessionID: sid, ProfileID: pid}
		if r.Account != nil {
			args.Account = jsonBytes(*r.Account)
		}
		t, err := agentTransition(q, args)
		if err != nil {
			return none, err
		}
		return asT[contract.SessionProfileReply](t)
	}
	p, err := readTrustedProfile(q, pid)
	if err != nil {
		return none, err
	}
	s.Account = deref(r.Account)
	if r.Account == nil {
		delete(s.rest, "account")
	}
	s.AgentConfig = jsonBytes(p)
	if err := recPut(q, recSession, &s); err != nil {
		return none, err
	}
	profileID := p.text("id")
	return contract.SessionProfileReply{SessionId: sid, ProfileId: profileID, Account: r.Account}, nil
}

// teardownFilter: a session's teardown commands no host has fetched yet.
func teardownFilter(sid string) agentFilter {
	return agentFilter{SessionID: &sid, Kind: ptr("teardown"), Status: ptr("pending")}
}

// lastOnHost: the last of a session's executions that ran on a host, nil when none did.
func lastOnHost(es []agentExecution) *agentExecution {
	var last *agentExecution
	for i := range es {
		if deref(es[i].HostID) != "" {
			last = &es[i]
		}
	}
	return last
}

// sessionArchived: session_archive's answer when no execution ran on a host.
type sessionArchived struct {
	SessionID  string    `json:"sessionId"`
	ArchivedAt int64     `json:"archivedAt"`
	Teardown   *struct{} `json:"teardown"`
}

// sessionArchive: one teardown command on the last execution's host.
func sessionArchive(q *Request, sid string) (any, error) {
	s, err := recGet[agentSession](q, recSession, sid)
	if err != nil {
		return nil, err
	}
	es, err := agentExecutions(q, sid)
	if err != nil {
		return nil, err
	}
	for i := range es {
		if !es[i].terminal() && !es[i].unconfirmed() {
			return nil, fail(409, "Execution active")
		}
	}
	last := lastOnHost(es)
	// A teardown still on its way is the answer; a finished one (kept, or sent on delivery) does not stop a new one.
	if old, err := recAll[agentCommand](q, recCommand, teardownFilter(sid)); err != nil || len(old) > 0 {
		if err != nil {
			return nil, err
		}
		return old[0], nil
	}
	at := nowMs()
	s.ArchivedAt = &at
	if err := recPut(q, recSession, &s); err != nil {
		return nil, err
	}
	if err := audit(q, "session:"+sid, "archivedAt", nil, at, auditExtra{}); err != nil {
		return nil, err
	}
	if last == nil {
		return sessionArchived{SessionID: sid, ArchivedAt: at}, nil
	}
	return teardownCommand(q, s, *last, false)
}

// sessionDeleteArchive: a deleted session is archived (object_delete, cleanup.go tombstone) and its worktree torn down
// on the host of its last execution, unless it is archived already or a teardown is on its way; torn: one was queued.
func sessionDeleteArchive(q *Request, sid string) (bool, error) {
	s, err := recGet[agentSession](q, recSession, sid)
	if err != nil {
		return false, err
	}
	if s.ArchivedAt != nil {
		return false, nil
	}
	if pending, err := recAll[agentCommand](q, recCommand, teardownFilter(sid)); err != nil || len(pending) > 0 {
		return false, err
	}
	es, err := agentExecutions(q, sid)
	if err != nil {
		return false, err
	}
	last := lastOnHost(es)
	at := nowMs()
	s.ArchivedAt = &at
	if err := recPut(q, recSession, &s); err != nil {
		return false, err
	}
	if last == nil {
		return false, nil
	}
	// Queued after the stop: the executor keeps a worktree with uncommitted or unpushed work (cleanup.keepWhen).
	t, err := teardownCommand(q, s, *last, false)
	_, torn := t.(agentCommand)
	return err == nil && torn, err
}

// teardownSkipped: no teardown was queued (cleanup skipped or unknown) and why.
type teardownSkipped struct {
	SessionID string `json:"sessionId"`
	Cleanup   string `json:"cleanup"`
	Reason    string `json:"reason"`
}

// teardownPayload: a teardown command's payload: the worktree to remove and how.
type teardownPayload struct {
	WorkspaceKey     json.RawMessage `json:"workspaceKey,omitempty"`
	DeleteBranch     bool            `json:"deleteBranch,omitempty"`
	ProjectID        string          `json:"projectId,omitempty"`
	Project          json.RawMessage `json:"project,omitempty"`
	WorkingDirectory string          `json:"workingDirectory,omitempty"`
	Branch           *bool           `json:"branch,omitempty"`
}

// teardownCommand: the teardown of a session's worktree on the host of its last execution (an agentCommand), or why
// none was queued (teardownSkipped). deleteBranch: the work was delivered (its task accepted), so the local branch goes
// even when squash-merged (git branch -D); the executor still keeps the worktree for any cleanup.keepWhen reason
// (uncommitted, unpushed, teardown-failed).
func teardownCommand(q *Request, s agentSession, last agentExecution, deleteBranch bool) (any, error) {
	sid, host := s.ID, deref(last.HostID)
	// A removed host never fetches it: its worktrees went with it, and a pending command would keep purge waiting.
	if live, err := knownHost(q, host); err != nil || !live {
		return teardownSkipped{SessionID: sid, Cleanup: "skipped", Reason: "host-gone"}, err
	}
	var payload teardownPayload
	d := rawObject{}
	if last.Dispatch != nil {
		json.Unmarshal(jsonBytes(last.Dispatch), &d)
	}
	if d.has("workspaceKey") {
		payload.WorkspaceKey = d["workspaceKey"]
	}
	workingDirectory := d.val("workingDirectory")
	payload.DeleteBranch = deleteBranch
	if p := d.text("projectId"); p != "" {
		workspace := rawObject(last.rest).object("workspaceSnapshot")
		snapshots := rawObject(s.rest).object("workspaceSnapshots")
		if workspace == nil {
			workspace = snapshots.object(host + ":" + p + ":" + d.text("workspaceKey"))
		}
		if workspace == nil {
			workspace = snapshots.object(host)
		}
		if workspace == nil || (workspace.has("projectId") && workspace.val("projectId") != any(p)) {
			return teardownSkipped{SessionID: sid, Cleanup: "unknown", Reason: "cleanup_context_unknown: original workspace settings unavailable"}, nil
		}
		payload.ProjectID, payload.Project = p, json.RawMessage("null")
		if workspace.has("project") {
			payload.Project = workspace["project"]
		}
		workingDirectory = workspace.val("workingDirectory")
	}
	if w, _ := workingDirectory.(string); w != "" {
		payload.WorkingDirectory = w
	}
	if s.Branch != nil && !*s.Branch { // a session started without a branch (presets.go): its worktree was detached
		payload.Branch = s.Branch
	}
	c := agentCommand{ID: newID(), ExecutionID: last.ID, SessionID: sid, HostID: host, Kind: "teardown", Payload: jsonBytes(payload), Status: "pending"}
	err := recPut(q, recCommand, &c)
	return c, err
}

// deliveredTeardown: an accepted task's sessions get their worktree teardown (fleet-system-management.md, Worktree on
// delivery). A session with a live execution, or a teardown still pending, is left alone; archiving it later sends one.
func deliveredTeardown(q *Request, taskID string) error {
	sids, err := q.db().TaskSessionIDs(q.ctx(), &taskID)
	if err != nil {
		return err
	}
	for _, sid := range sids {
		s, err := recGet[agentSession](q, recSession, sid)
		if errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if old, err := recAll[agentCommand](q, recCommand, teardownFilter(sid)); err != nil || len(old) > 0 {
			if err != nil {
				return err
			}
			continue
		}
		es, err := agentExecutions(q, sid)
		if err != nil {
			return err
		}
		active := false
		for i := range es {
			active = active || (!es[i].terminal() && !es[i].unconfirmed())
		}
		last := lastOnHost(es)
		if active || last == nil {
			continue
		}
		if _, err := teardownCommand(q, s, *last, true); err != nil {
			return err
		}
	}
	return nil
}

// errSessionCreateKeys: session_create takes an empty object.
var errSessionCreateKeys = fail(400, "session_create takes no identity or linkage fields")

func sessionCreate(q *Request) (contract.SessionCreateReply, error) {
	s := agentSession{ID: newID(), CreatedAt: nowMs(), DirectedBy: director(q)}
	if callerReadOnly(q) { // an agent of a read-only conversation creates read-only sessions (presets.go)
		s.Access, s.Branch = "read", ptr(false)
	}
	if err := recWrite(q, recSession, s); err != nil {
		return contract.SessionCreateReply{}, err
	}
	out, err := asT[contract.SessionCreateReply](s)
	out.SessionId = s.ID
	return out, err
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
		if _, err := recGet[rawObject](q, recExecution, id); err != nil {
			return nil, err
		}
		raw, err := q.db().ExecutionDelegations(q.ctx(), id)
		ds, err := delegationsOf(q, raw, err)
		return struct {
			Delegations []rawObject `json:"delegations"`
		}{ds}, err
	})
	op("session_create", func(q *Request, r *contract.SessionCreateRequest) (contract.SessionCreateReply, error) {
		if len(*r) > 0 {
			return contract.SessionCreateReply{}, errSessionCreateKeys
		}
		return sessionCreate(q)
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
		r.SessionId = sid
		return sessionProfile(q, r)
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
	s, err := recGet[agentSession](q, recSession, sid)
	if err != nil {
		return none, err
	}
	if s.ArchivedAt == nil {
		return none, fail(409, "Session is not archived")
	}
	at := *s.ArchivedAt
	s.ArchivedAt, s.ID = nil, sid
	if err := recPut(q, recSession, &s); err != nil {
		return none, err
	}
	// A teardown no host has fetched yet would remove the worktree of a live session again.
	pending, err := recAll[agentCommand](q, recCommand, teardownFilter(sid))
	if err != nil {
		return none, err
	}
	for _, x := range pending {
		x.Status = "stopped"
		if err := recPut(q, recCommand, &x); err != nil {
			return none, err
		}
	}
	if err := audit(q, "session:"+sid, "archivedAt", at, nil, auditExtra{Reason: reason}); err != nil {
		return none, err
	}
	return contract.SessionUnarchiveReply{SessionId: sid, TeardownsWithdrawn: int64(len(pending))}, nil
}
