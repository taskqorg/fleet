// Agent tools: /api/hosts/agent-tools MCP server, tool binding, director/focus scoping, and agent-callable operations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/mcpserve"

	"github.com/jackc/pgx/v5"
)

func agentAsk(q *Request, r *contract.AskRequest) (contract.AskReply, error) {
	eid := deref(r.ExecutionId)
	if eid == "" {
		return nil, fail(400, "Expected executionId")
	}
	e, err := getExecution(q, eid)
	if err != nil {
		return nil, err
	}
	if rid := deref(r.Resume); rid != "" {
		if r.Question != nil || r.RequestId != nil || r.TaskId != nil {
			return nil, fail(400, "Use question + requestId for a fresh ask, or resume=<questionId> alone to read an existing answer; resume is not continuation prose")
		}
		qu, err := agentQuestionGet(q, rid)
		if err != nil {
			return nil, err
		}
		if qu.ExecutionID != e.ID && qu.SessionID != e.SessionID {
			return nil, fail(403, "Question belongs to another session")
		}
		return recOf(qu), nil
	}
	rid := deref(r.RequestId)
	if rid == "" {
		return nil, fail(400, "Expected requestId")
	}
	question := deref(r.Question)
	if question == "" {
		return nil, fail(400, "Expected question")
	}
	link, err := q.db().AgentExecutionLink(q.ctx(), eid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	taskID, projectID := deref(link.TaskID), deref(link.ProjectID)
	explicit := r.TaskId != nil || bodyKeys(q)["taskId"]
	if explicit {
		tid := deref(r.TaskId)
		if tid == "" {
			return nil, fail(400, "Expected taskId")
		}
		t, err := taskRow(q, tid)
		if err != nil {
			return nil, err
		}
		tp := agentView[struct {
			ProjectID string `json:"project_id"`
		}](t).ProjectID
		if !mayAccessProjectID(q.person, tp) {
			return nil, fail(403, "Task project is outside caller grants")
		}
		if (taskID != "" && taskID != tid) || (projectID != "" && projectID != tp) {
			return nil, fail(409, "Question task conflicts with the execution task or project")
		}
		taskID, projectID = tid, tp
	}
	if old, err := agentQuestionsList(q, db.AgentQuestionsParams{ExecutionID: &eid, RequestID: &rid}); err != nil {
		return nil, err
	} else if len(old) > 0 {
		if old[0].Question != question || deref(old[0].TaskID) != taskID {
			return nil, fail(409, "Question identity conflict")
		}
		return recOf(old[0]), nil
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		projectID = s.ProjectID
	}
	if explicit {
		if err := withdrawIdleDecision(q, taskID); err != nil {
			return nil, err
		}
	}
	qu := agentQuestion{ID: newID(), ExecutionID: eid, SessionID: e.SessionID, ProjectID: agentOpt(projectID), TaskID: agentOpt(taskID), RequestID: rid, Question: question, State: "open", CreatedAt: nowMs()}
	if err := agentQuestionPut(q, &qu); err != nil {
		return nil, err
	}
	out := recOf(qu) // a fresh question shows its missing project and task as null
	out["projectId"], out["taskId"] = agentNullText(projectID), agentNullText(taskID)
	return out, nil
}

// agentReplyTo: an answer to an agent's question delivered to its session (the reply tool, a task decision's answer).
func agentReplyTo(q *Request, r *contract.ReplyRequest) (contract.ReplyReply, error) {
	qid := deref(r.QuestionId)
	if qid == "" {
		return nil, fail(400, "Expected questionId")
	}
	qu, err := agentQuestionGet(q, qid)
	if err != nil {
		return nil, err
	}
	if err := approvalMayAnswer(q, qid); err != nil { // an approval card: approvers only, never an agent (approvals.go)
		return nil, err
	}
	answer, _ := jsonText(r.Answer)
	if answer == "" {
		return nil, fail(400, "Expected answer")
	}
	if qu.State == "answered" {
		if qu.Answer != answer {
			return nil, fail(409, "Question already answered differently")
		}
		return recOf(qu), nil
	}
	if qu.ProjectID == nil { // a question asked before it carried its session's project
		s, err := sessionByID(q, qu.SessionID)
		if err != nil {
			return nil, err
		}
		qu.ProjectID = agentOpt(s.ProjectID)
	}
	// The answer is the answering person's, by email, as for every other question (not "operator").
	qu.State, qu.Answer, qu.AnsweredAt, qu.AnsweredBy = "answered", answer, nowMs(), actor(q, "operator")
	d, err := submitQuestionReply(q, qu.SessionID, qid, qu.Question, answer)
	if err != nil {
		return nil, err
	}
	if d.queued != nil {
		qu.Delivery = &agentQuestionDelivery{QueueID: d.queued.ID, State: d.queued.State}
	} else {
		qu.Delivery = &agentQuestionDelivery{ExecutionID: d.execution.ID}
	}
	err = agentQuestionPut(q, &qu)
	return recOf(qu), err
}

func submitQuestionReply(q *Request, sid, qid, question, answer string) (agentSubmitted, error) {
	return agentSubmit(q, &contract.MessageSendRequest{SessionId: &sid, RequestId: ptr("reply:" + qid),
		Text: ptr(fmt.Sprintf("Fleet reply to your question %s (\"%s\"):\n%s", qid, question, answer))}, "")
}

// continueQuestionAsker admits the answer as an ordinary message only when the asking session needs a turn.
func continueQuestionAsker(q *Request, question db.Question, answer string) error {
	sid, eid := deref(question.SessionID), deref(question.ExecutionID)
	if question.Kind != db.QuestionKindOperatorDecision || sid == "" || eid == "" ||
		deref(question.AskedBy) != "execution:"+eid {
		return nil
	}
	// Use the same lock as message_send and queue promotion so none can admit a turn between this check and submit.
	if err := acquireTxLock(q, "session:"+sid); err != nil {
		return err
	}
	s, err := sessionMaybe(q, sid)
	if err != nil || s == nil || s.ArchivedAt != nil {
		return err
	}
	if tid := firstString(s.DeliveryID, s.TaskID, deref(question.TaskID)); tid != "" {
		state, err := q.db().TaskStateForShare(q.ctx(), tid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || !openTask[string(state)] || state == db.TaskStatePaused {
			return err
		}
	}
	asking, err := executionMaybe(q, eid)
	if err != nil || asking == nil || asking.SessionID != sid || !asking.terminal() {
		return err
	}
	if active, err := agentActive(q, sid); err != nil || active != nil {
		return err
	}
	steering, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, State: ptr("steering")})
	if err != nil || len(steering) != 0 {
		return err
	}
	_, err = submitQuestionReply(q, sid, question.ID, question.Text, answer)
	return err
}

type workerDonePayload struct {
	Action    string                 `json:"action"`
	At        *int64                 `json:"at,omitempty"`
	Code      *string                `json:"code,omitempty"`
	Duplicate *bool                  `json:"duplicate,omitempty"`
	Reason    *string                `json:"reason,omitempty"`
	Report    *contract.WorkerReport `json:"report,omitempty"`
}

func workerDone(q *Request, b *contract.WorkerDoneRequest) (contract.WorkerDoneReply, error) {
	var none contract.WorkerDoneReply
	eid := b.ExecutionId
	if eid == "" {
		return none, fail(400, "Expected executionId")
	}
	e, err := getExecution(q, eid)
	if err != nil {
		return none, err
	}
	record := func(r contract.WorkerDoneReply) (contract.WorkerDoneReply, error) {
		if deref(r.Id) == "" {
			return r, fmt.Errorf("worker-done record without an id")
		}
		p := workerDonePayload{Action: r.Action, At: r.At, Code: r.Code, Duplicate: r.Duplicate, Reason: r.Reason, Report: r.Report}
		return r, q.db().WorkerDonePut(q.ctx(), db.WorkerDonePutParams{ID: *r.Id, ExecutionID: strPtr(r.ExecutionId), Payload: jsonBytes(p)})
	}
	reject := func(code, reason string) (contract.WorkerDoneReply, error) {
		id, at := newID(), nowMs()
		return record(contract.WorkerDoneReply{Id: &id, ExecutionId: eid, Action: "rejected", Code: &code, Reason: &reason, At: &at})
	}
	var outcome string
	json.Unmarshal(b.Outcome, &outcome) // a non-string outcome is rejected below, as before
	var summary *string
	json.Unmarshal(b.Summary, &summary) // a non-string summary is stored as null
	if outcome != "succeeded" && outcome != "failed" {
		return reject("invalid_outcome", "worker_done requires outcome=succeeded or outcome=failed.")
	}
	es, err := agentExecutions(q, e.SessionID)
	if err != nil {
		return none, err
	}
	if latest := es[len(es)-1]; latest.ID != eid {
		return reject("stale_dispatch", fmt.Sprintf("Execution %s is superseded by %s.", eid, latest.ID))
	}
	if r := e.Report; r != nil {
		if r.Outcome == outcome && (r.Summary == nil) == (summary == nil) && deref(r.Summary) == deref(summary) {
			report, yes := *r, true
			return contract.WorkerDoneReply{Action: "accepted", ExecutionId: eid, Report: &report, Duplicate: &yes}, nil
		}
		return reject("already_reported", "A different worker_done was already accepted for this execution.")
	}
	if n := deref(b.NativeSessionId); n != "" {
		known, err := agentKnownNative(q, &e)
		if err != nil {
			return none, err
		}
		if known != "" && known != n {
			return reject("native_identity_conflict", "Reported native session differs from the captured one.")
		}
	}
	report := contract.WorkerReport{Outcome: outcome, Summary: summary, At: nowMs()}
	e.Report = &report
	if err := executionPut(q, &e); err != nil {
		return none, err
	}
	if ds, err := delegationsList(q, nil, &eid); err != nil {
		return none, err
	} else if len(ds) > 0 {
		ds[0].Report = &report
		if err := delegationPut(q, &ds[0]); err != nil {
			return none, err
		}
	}
	if e.terminal() {
		link, err := q.db().AgentExecutionLink(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return none, err
		}
		if taskID := deref(link.TaskID); taskID != "" {
			if _, err := settleIdleTask(q, taskID); err != nil {
				return none, err
			}
		}
	}
	id, at := newID(), nowMs()
	return record(contract.WorkerDoneReply{Id: &id, ExecutionId: eid, Action: "accepted", Report: &report, At: &at})
}

// agentTable: an operation the coordinator binds to the calling session for a host agent (agent: bound:session in
// contracts/ops-*.schema.json; boundArguments). The same operations serve /api/ops and /api/tools unbound.
func agentTable(name string) bool {
	_, ok := operations[name]
	return ok && contract.Operations[name].Agent == "bound:session"
}

// hostAgentPerson checks whose grants a host can use for a native tool call.
func hostAgentPerson(q *Request, director, takenFrom string) (*Person, error) {
	person := q.peopleSnapshot().person(director)
	if person == nil {
		return nil, fail(403, "Director "+director+" is not a person of this Fleet")
	}
	var host authHost
	if q.host != nil {
		host = *q.host
	}
	if holder := q.peopleSnapshot().person(host.Holder); director != host.Holder && (holder == nil || !holder.Owner) {
		return nil, fail(403, "Host "+host.HostID+" is held by "+host.Holder+", not the director "+director)
	}
	// Taken work uses the holder's subscription and the original requester's grants.
	if takenFrom != "" {
		person = q.peopleSnapshot().person(takenFrom)
		if person == nil {
			return nil, fail(403, "Requester "+takenFrom+" is not a person of this Fleet")
		}
	}
	return person, nil
}

func init() {
	// /api/hosts/agent-tools: an executor's agent calls a tool (mcpserve); every call is bound to the calling execution's
	// session and runs through runOp, under the director's person gate.
	hostRoute(contract.RouteAgentTools, func(q *Request, b *contract.AgentToolsRequest) (any, error) {
		executionID := ""
		if b.ExecutionId != nil {
			executionID = *b.ExecutionId
		}
		rpc := mcpserve.Parse(b.Request)
		// The director's rights, the execution as actor (docs/designs/fleet-pm-agent.md §1).
		q.agent = "execution:" + executionID
		q.r = q.r.WithContext(context.WithValue(q.ctx(), actorKey{}, q.agent))
		var focus []string                   // nil: no focus or "*"
		sessionProject, focused := "", false // the calling session's own project; whether it has any focus
		prove := func() error {
			var e agentExecution
			var sessionDirector string
			if executionID != "" {
				var err error
				if e, err = getExecution(q, executionID); err != nil {
					return err
				}
				if deref(e.HostID) != b.HostId {
					return fail(403, "Execution rejected")
				}
			}
			c, err := commandGet(q, b.CommandId)
			if err != nil {
				return err
			}
			if c.ExecutionID != executionID || c.HostID != b.HostId {
				return fail(403, "Command rejected")
			}
			if rpc.Method == "tools/call" {
				bindToolHTTPSpan(q, executionID, e.ProjectID, rpc.Params.Name)
			}
			if sid := e.SessionID; sid != "" {
				if s, err := sessionMaybe(q, sid); err != nil {
					return err
				} else if s != nil {
					focus, sessionProject, focused = focusIDsOf(s.Focus), s.ProjectID, s.Focus != nil
					sessionDirector = s.DirectedBy
					if s.BotID != "" {
						bot, err := unscoped(q, func() (botFull, error) {
							return readBotFull(q, s.BotID)
						})
						if errors.Is(err, errNotFound) {
							return fail(403, "Bot no longer exists")
						}
						if err != nil {
							return err
						}
						if err := botAuthorityCheck(q, &bot); err != nil {
							return err
						}
					}
				}
			}
			// A call without an execution (initialize, tools/list) acts for the host's holder; never an implicit owner.
			holder := ""
			if q.host != nil {
				holder = q.host.Holder
			}
			q.person, err = hostAgentPerson(q, firstString(e.DirectedBy, sessionDirector, holder), e.TakenFrom)
			return err
		}
		if name := rpc.Params.Name; rpc.Method == "tools/call" && !agentTable(name) {
			if agentDenied(name) {
				return nil, fail(403, "Only a person may do this")
			}
			if executionID == "" {
				return nil, fail(403, "Execution rejected")
			}
		}
		isReadOp := func(name string) bool {
			reg, ok := operations[name]
			return ok && reg.read && longOperations[name] == nil
		}
		receipt := func(payload any) *toolReceipt {
			return &toolReceipt{key: b.AgentToolKey, execution: executionID, payload: payload}
		}
		focusBound := func() []string {
			if focused && focus != nil {
				return focus
			}
			if !focused && sessionProject != "" {
				return []string{sessionProject}
			}
			return nil
		}
		checkBoundProjectID := func(bound []string, raw json.RawMessage) error {
			pid, ok := jsonText(raw)
			if !ok || !slices.Contains(bound, pid) {
				return fail(403, "Project outside the session focus")
			}
			return nil
		}
		// prepareTable: bind agent-table arguments to the caller's own session inside the proof read transaction.
		prepareTable := func(q *Request, name string, raw json.RawMessage) (json.RawMessage, *toolReceipt, error) {
			var args map[string]json.RawMessage
			json.Unmarshal(raw, &args)
			var err error
			args, err = agentBoundArguments(q, name, args, executionID)
			if err != nil {
				return nil, nil, err
			}
			bound := jsonValue(jsonBytes(args))
			return json.RawMessage(js(bound)), receipt(bound), nil
		}
		// prepareOp: validate and bind focus/project arguments for a non-table operation inside the proof read transaction.
		prepareOp := func(q *Request, name string, raw json.RawMessage) (json.RawMessage, error) {
			if isResourceRead(name) && !resourceReadsEnabled {
				return nil, fail(503, "Prepared project reads disabled")
			}
			if !agentMay(q.person, name) {
				return nil, fail(400, "Unknown agent tool")
			}
			var a map[string]json.RawMessage
			json.Unmarshal(raw, &a)
			if a == nil {
				a = map[string]json.RawMessage{}
			}
			if name == "task_page" && focus != nil {
				var f map[string]json.RawMessage
				json.Unmarshal(a["filter"], &f) // a filter that is not an object reads as none
				if f == nil || !jsonGiven(f["projectId"]) {
					if f == nil {
						f = map[string]json.RawMessage{}
					}
					f["projectId"] = jsonBytes(focus) // focus filters reads without a project (§2)
					a["filter"] = jsonBytes(f)
				}
			}
			if name == "analytics_query" {
				if bound := focusBound(); bound != nil {
					if len(bound) == 0 {
						return nil, fail(403, "Project outside the session focus")
					}
					if jsonGiven(a["projectId"]) {
						if err := checkBoundProjectID(bound, a["projectId"]); err != nil {
							return nil, err
						}
					}
					var f map[string]json.RawMessage
					json.Unmarshal(a["filters"], &f)
					for _, fk := range []string{"project", "projectId"} {
						if raw, ok := f[fk]; ok && jsonGiven(raw) {
							for _, pid := range jsonStrings(raw) {
								if !slices.Contains(bound, pid) {
									return nil, fail(403, "Project outside the session focus")
								}
							}
						}
					}
					if !jsonGiven(a["projectId"]) && len(jsonStrings(f["project"])) == 0 && len(jsonStrings(f["projectId"])) == 0 {
						if f == nil {
							f = map[string]json.RawMessage{}
						}
						f["projectId"] = jsonBytes(bound)
						a["filters"] = jsonBytes(f)
					}
				}
			}
			if name == "spend_summary" {
				if bound := focusBound(); bound != nil {
					if len(bound) == 0 {
						return nil, fail(403, "Project outside the session focus")
					}
					if jsonGiven(a["projectId"]) {
						if err := checkBoundProjectID(bound, a["projectId"]); err != nil {
							return nil, err
						}
					}
					var pids []string
					if jsonGiven(a["projectIds"]) {
						var xs []json.RawMessage
						if err := json.Unmarshal(a["projectIds"], &xs); err != nil {
							return nil, fail(400, "Invalid projectIds")
						}
						pids = jsonStrings(a["projectIds"])
						if len(pids) != len(xs) {
							return nil, fail(400, "Invalid projectIds")
						}
						for _, pid := range pids {
							if !slices.Contains(bound, pid) {
								return nil, fail(403, "Project outside the session focus")
							}
						}
					}
					if !jsonGiven(a["projectId"]) && len(pids) == 0 {
						a["projectIds"] = jsonBytes(bound)
					}
				}
			}
			// An operation that requires projectId, called without the key, gets the one focus project, or the session's
			// own project when it has no focus (§2). Optional projectIds stay absent (scope-wide reads), an explicit null
			// stays null, and the operation's own gates still refuse a project the director cannot reach.
			if _, given := a["projectId"]; !given && projectRequired[name] {
				if len(focus) == 1 {
					a["projectId"] = jsonBytes(focus[0])
				} else if !focused && sessionProject != "" {
					a["projectId"] = jsonBytes(sessionProject)
				}
			}
			if name == "task_create" && !jsonGiven(a["projectId"]) && len(focus) == 1 {
				a["projectId"] = jsonBytes(focus[0]) // one focus project is the default (§2)
			}
			if pid, _ := jsonText(a["projectId"]); isResourceRead(name) && pid != "" && !mayAccessProjectID(q.person, pid) {
				return nil, fail(404, "Unknown project")
			}
			if name == "publishing_capabilities" || name == "publishing_status" {
				if bound := focusBound(); bound != nil {
					if len(bound) == 0 {
						return nil, fail(403, "Project outside the session focus")
					}
					if jsonGiven(a["projectId"]) {
						if err := checkBoundProjectID(bound, a["projectId"]); err != nil {
							return nil, err
						}
					}
					if jsonGiven(a["projectIds"]) {
						var xs []json.RawMessage
						if err := json.Unmarshal(a["projectIds"], &xs); err != nil {
							return nil, fail(400, "Invalid projectIds")
						}
						pids := jsonStrings(a["projectIds"])
						if len(pids) != len(xs) {
							return nil, fail(400, "Invalid projectIds")
						}
						for _, pid := range pids {
							if !slices.Contains(bound, pid) {
								return nil, fail(403, "Project outside the session focus")
							}
						}
					} else if !jsonGiven(a["projectId"]) && name == "publishing_status" {
						a["projectIds"] = jsonBytes(bound)
					}
				}
				if !jsonGiven(a["projectId"]) && !jsonGiven(a["projectIds"]) {
					if len(focus) == 1 {
						a["projectId"] = jsonBytes(focus[0])
					} else if !focused && sessionProject != "" {
						a["projectId"] = jsonBytes(sessionProject)
					}
				}
			}
			if name == "project_update" || name == "project_context" || name == "project_readiness" || isResourceRead(name) {
				out, err := projectAgentArguments(q, a, executionID, isResourceRead(name))
				if err != nil {
					return nil, err
				}
				a = out
			}
			return json.RawMessage(js(jsonValue(jsonBytes(a)))), nil
		}
		// Combine proof, director access refresh, argument binding, and (for read ops) the operation itself in one read snapshot.
		if rpc.Method != "tools/call" || isReadOp(rpc.Params.Name) {
			var out any
			err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
				if err := prove(); err != nil {
					return err
				}
				if err := refreshRequestAccess(q); err != nil {
					return err
				}
				var handleErr error
				out, handleErr = mcpserve.Server{Name: "fleet-agent", Tools: agentTools(q.person), Call: func(name string, raw json.RawMessage) (any, error) {
					if agentTable(name) {
						bound, rc, err := prepareTable(q, name, raw)
						if err != nil {
							return nil, err
						}
						return q.app.runOp(q, name, bound, rc)
					}
					bound, err := prepareOp(q, name, raw)
					if err != nil {
						return nil, err
					}
					return q.app.runOp(q, name, bound, receipt(nil))
				}}.Handle(b.Request)
				return handleErr
			})
			if errors.Is(err, mcpserve.ErrUnknownMethod) {
				return nil, fail(400, "Unknown tool")
			}
			return out, err
		}
		name := rpc.Params.Name
		var bound json.RawMessage
		var rc *toolReceipt
		if err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
			if err := prove(); err != nil {
				return err
			}
			if err := refreshRequestAccess(q); err != nil {
				return err
			}
			if agentTable(name) {
				var err error
				bound, rc, err = prepareTable(q, name, rpc.Params.Arguments)
				return err
			}
			var err error
			bound, err = prepareOp(q, name, rpc.Params.Arguments)
			rc = receipt(nil)
			return err
		}); err != nil {
			return nil, err
		}
		opOut, err := q.app.runOp(q, name, bound, rc)
		if err != nil {
			return nil, err
		}
		return mcpserve.Result(rpc.ID, opOut), nil
	})
}

// focusIDsOf: the focus projects; nil for no focus or "*" (every project in scope).
func focusIDsOf(f *agentFocus) []string {
	if f == nil || string(f.ProjectIDs) == `"*"` {
		return nil
	}
	return jsonStrings(f.ProjectIDs)
}

// focusProjectOf: the project of a call in a focused session: the given one (inside focus and the director's grants),
// or the single focus project. settings: the project's settings must load too (the legacy focus resolution, which
// keeps its configuration validation; prepared reads skip it so a configuration failure cannot take down the resource
// projection).
func focusProjectOf(q *Request, f *agentFocus, p string, settings bool) (string, error) {
	ids, all := focusIDsOf(f), f != nil && string(f.ProjectIDs) == `"*"`
	if p == "" {
		if all || len(ids) != 1 {
			return "", fail(400, "Expected projectId: the session focus holds several projects")
		}
		p = ids[0]
	}
	if !all && !slices.Contains(ids, p) {
		return "", fail(403, "Project outside the session focus")
	}
	if !mayAccessProjectID(q.person, p) {
		return "", fail(404, "Unknown project")
	}
	exists, err := q.db().AgentProjectLive(q.ctx(), p)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fail(404, "Unknown project")
	}
	if settings && projectSettingsOf(q, p) == nil {
		return "", fail(404, "Unknown project")
	}
	return p, nil
}

// sessionDirects: a person changes only sessions they direct (the owner any); an agent call is already bound to its own
// session tree by boundArguments().
func sessionDirects(q *Request, s *agentSession) error {
	if q.agent != "" || q.person == nil || fullAccess(q.person) || s.DirectedBy == q.person.Email {
		return nil
	}
	return fail(403, "Session is directed by someone else")
}

// focusSet: a session's focus, a list of projects in the caller's grants or "*" (docs/designs/fleet-pm-agent.md §2).
func focusSet(q *Request, r *contract.FocusSetRequest) (contract.FocusSetReply, error) {
	var none contract.FocusSetReply
	sid := r.SessionId
	if sid == "" {
		return none, fail(400, "Expected sessionId")
	}
	s, err := sessionByID(q, sid)
	if err != nil {
		return none, err
	}
	if err := sessionDirects(q, &s); err != nil {
		return none, err
	}
	v := json.RawMessage(`"*"`)
	if t, _ := jsonText(r.ProjectIds); t != "*" {
		var xs []json.RawMessage
		list := len(r.ProjectIds) > 0 && r.ProjectIds[0] == '[' && json.Unmarshal(r.ProjectIds, &xs) == nil
		ids := jsonStrings(r.ProjectIds)
		if !list || len(ids) != len(xs) {
			return none, fail(400, `Expected projectIds: a list of project ids or "*"`)
		}
		if err := projectSetCheck(q, ids); err != nil {
			return none, err
		}
		v = jsonBytes(ids)
	}
	s.Focus = &agentFocus{ProjectIDs: v}
	if err := sessionPut(q, &s); err != nil {
		return none, err
	}
	return contract.FocusSetReply{SessionId: sid, Focus: contract.Focus{ProjectIds: v}}, nil
}

// projectSetCheck: every id names a project of this Fleet in the caller's grants (a conversation's project set).
func projectSetCheck(q *Request, ids []string) error {
	for _, id := range ids {
		if projectSettingsOf(q, id) == nil || (q.person != nil && !mayAccessProjectID(q.person, id)) {
			return fail(404, "Unknown project")
		}
	}
	return nil
}

// sessionProjects: a conversation's project set (fleet-conversations.md#entities): its focus list, else its checkout
// project; a focus of every project ("*") is the checkout project too. sql_builder.go sessionModelCols reads the same.
func sessionProjects(s *agentSession) []string {
	if s.Focus != nil && len(s.Focus.ProjectIDs) > 0 && s.Focus.ProjectIDs[0] == '[' {
		return orEmpty(jsonStrings(s.Focus.ProjectIDs))
	}
	if s.ProjectID != "" {
		return []string{s.ProjectID}
	}
	return []string{}
}

// sessionUpdate: a conversation's project set (projectIds), by the rights of session_rename: a person who directs it,
// the owner, or an agent bound to it. The set is the focus focus_set writes; the checkout project stays.
func sessionUpdate(q *Request, r *contract.SessionUpdateRequest) (contract.SessionUpdateReply, error) {
	var none contract.SessionUpdateReply
	sid := deref(r.SessionId)
	if sid == "" {
		return none, fail(400, "Expected sessionId")
	}
	if !bodyKeys(q)["projectIds"] {
		return none, fail(400, "Expected projectIds")
	}
	s, err := sessionByID(q, sid)
	if err != nil {
		return none, err
	}
	if err := sessionDirects(q, &s); err != nil {
		return none, err
	}
	ids := orEmpty(r.ProjectIds)
	if err := projectSetCheck(q, ids); err != nil {
		return none, err
	}
	// An empty set drops the focus: the conversation is back to its checkout project alone.
	s.Focus = nil
	if len(ids) > 0 {
		s.Focus = &agentFocus{ProjectIDs: jsonBytes(ids)}
	}
	if err := sessionPut(q, &s); err != nil {
		return none, err
	}
	return contract.SessionUpdateReply{SessionId: sid, ProjectIds: sessionProjects(&s)}, nil
}

// agentDenied: operations only a person may call (agent: no; take, host secrets, key values); docs/designs/fleet-pm-agent.md
// §1. Secrets never enter a model's context: the host secret and gateway token operations return one.
func agentDenied(name string) bool { return contract.Operations[name].Agent == "no" }

// agentMay: an operation outside the agent table offered to the director: one whose declared auth is architect needs
// the architect role; every other one is offered and runOp's declaredAuth decides.
func agentMay(p *Person, name string) bool {
	if !resourceOperationAdvertised(name) {
		return false
	}
	if agentDenied(name) || ((name == "lab_promote" || name == "lab_personal_promote") && !hasRole(p, "architect")) {
		return false
	}
	_, op := operations[name]
	_, long := longOperations[name]
	switch auth := contract.Operations[name].Auth; {
	case !op && !long:
		return false
	case fullAccess(p):
		return true
	case auth == "architect":
		return hasRole(p, "architect")
	}
	// Owner, holder and project rules stay offered: runOp answers 403 or 404 for the object named.
	return true
}

// agentTools: the agent table plus every operation the director may call, as published by tools/list.
func agentTools(p *Person) []string {
	out := []string{}
	for n := range operations {
		if agentTable(n) || agentMay(p, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// agentAccountArg: an account argument as AccountTakesWork reads it (a string, else its printed value).
func agentAccountArg(raw json.RawMessage) string {
	if s, ok := jsonText(raw); ok {
		return s
	}
	return fmt.Sprint(jsonValue(raw))
}

// agentBoundArguments: host-run agents act only on their own session (or one they created); lists never default to all
// sessions. a: the tool's arguments as sent, an open object that goes on to the named operation.
func agentBoundArguments(q *Request, name string, a map[string]json.RawMessage, executionID string) (map[string]json.RawMessage, error) {
	if a == nil {
		a = map[string]json.RawMessage{}
	}
	e, err := getExecution(q, executionID)
	if err != nil {
		return nil, err
	}
	own := e.SessionID
	ownProject := func() (string, error) {
		s, err := sessionByID(q, own)
		if err != nil {
			return "", err
		}
		given, isText := jsonText(a["projectId"])
		if s.Focus != nil {
			return focusProjectOf(q, s.Focus, given, !isResourceRead(name))
		}
		p := s.ProjectID
		if isResourceRead(name) && p == "" {
			return "", fail(400, "Expected projectId")
		}
		if _, ok := a["projectId"]; p == "" || (ok && (!isText || given != p)) {
			return "", fail(403, "Project belongs to another agent")
		}
		return p, nil
	}
	mine := func(sid string) error {
		if sid == own && sid != "" {
			return nil
		}
		if sid != "" {
			s, err := sessionByID(q, sid)
			if err != nil {
				return err
			}
			if s.CreatedBySessionID == own {
				return nil
			}
			// A bot acts on its own sessions and on the work of the tasks it created or manages: on 2026-11-27 HAL0's
			// problem conversation could not resume T-FLT-79's worker conversation and asked the owner instead.
			x, err := q.db().ExecutionByID(q.ctx(), executionID)
			if err != nil {
				return err
			}
			if caller := deref(x.BotID); caller != "" {
				if s.BotID == caller {
					return nil
				}
				if s.TaskID != "" {
					managed, err := q.db().TaskManagedBy(q.ctx(), db.TaskManagedByParams{TaskID: s.TaskID, BotID: caller})
					if err != nil {
						return err
					}
					if managed {
						return nil
					}
				}
			}
		}
		return fail(403, "Session belongs to another agent")
	}
	sessionArg, _ := jsonText(a["sessionId"])
	switch {
	case name == "execution_status" && jsonGiven(a["sessionId"]):
		s, err := sessionMaybe(q, sessionArg)
		if err != nil {
			return nil, err
		}
		if s == nil || s.ReadinessOfSessionID != own {
			if err := mine(sessionArg); err != nil {
				return nil, err
			}
		}
		es, err := agentExecutions(q, sessionArg)
		if err != nil {
			return nil, err
		}
		if len(es) == 0 {
			return nil, fail(404, "No execution")
		}
		a["executionId"] = jsonBytes(es[len(es)-1].ID)
		delete(a, "sessionId")
	case name == "execution_status" && jsonGiven(a["executionId"]):
		// A readiness check the caller started (project_readiness: a host job with no session); else the caller's own run.
		xid, _ := jsonText(a["executionId"])
		if x, err := getExecution(q, xid); err != nil || x.ReadinessOfSessionID == "" || x.ReadinessOfSessionID != own {
			a["executionId"] = jsonBytes(executionID)
		}
	case name == "ask" || name == "worker_done" || name == "execution_status":
		a["executionId"] = jsonBytes(executionID)
	case name == "reply":
		qid, _ := jsonText(a["questionId"])
		if qid == "" {
			return nil, fail(400, "Expected questionId")
		}
		qu, err := agentQuestionGet(q, qid)
		if err != nil {
			return nil, err
		}
		if err := mine(qu.SessionID); err != nil {
			return nil, err
		}
	case name == "reconcile":
		return nil, fail(403, "Not available to host agents")
	case name == "project_readiness" || name == "project_context" || name == "project_update" || name == "session_presets" || name == "question_ask" || isResourceRead(name):
		// Bound to the caller's own project: another project's readiness is not the agent's to read.
		p, err := ownProject()
		if err != nil {
			return nil, err
		}
		a["projectId"] = jsonBytes(p)
		if name == "project_readiness" {
			a["callerSessionId"] = jsonBytes(own)
		}
	case strings.HasPrefix(name, "publishing_"):
		p, err := ownProject()
		if err != nil {
			return nil, err
		}
		a["projectId"] = jsonBytes(p)
		if !jsonGiven(a["sourceExecutionId"]) && name != "publishing_capabilities" && name != "publishing_status" {
			a["sourceExecutionId"] = jsonBytes(executionID)
		}
		if sid, ok := jsonText(a["sourceExecutionId"]); ok && sid != "" && sid != executionID {
			se, err := getExecution(q, sid)
			if err != nil {
				return nil, err
			}
			if se.ProjectID != "" && se.ProjectID != p {
				return nil, fail(403, "Source execution belongs to another project")
			}
		}
	case name == "session_start":
		p, err := ownProject()
		if err != nil {
			return nil, err
		}
		a["projectId"], a["callerSessionId"], a["callerExecutionId"] = jsonBytes(p), jsonBytes(own), jsonBytes(executionID)
		if pid, ok := a["profileId"]; ok {
			id, _ := jsonText(pid)
			callerProfile := agentView[agentProfile](e.AgentConfig).ID
			if callerProfile == "" && own != "" {
				if s, err := sessionMaybe(q, own); err == nil && s != nil {
					if cp, ok := s.profile(); ok {
						callerProfile = cp.ID
					}
				}
			}
			allowSwarmWorker := callerProfile == "swarm-coordinator" && (id == "swarm-worker" || id == "swarm-verifier-claude")
			if !allowSwarmWorker && !profileInRoles(projectSettingsOf(q, p), id) {
				return nil, fail(403, "Profile not in project roles")
			}
		}
		if acct, ok := a["account"]; ok {
			if ok, err := q.db().AccountTakesWork(q.ctx(), agentAccountArg(acct)); err != nil || !ok {
				if err == nil {
					err = fail(403, "Account not available")
				}
				return nil, err
			}
		}
	default:
		if !jsonGiven(a["sessionId"]) {
			a["sessionId"], sessionArg = jsonBytes(own), own
		}
		if err := mine(sessionArg); err != nil {
			return nil, err
		}
		if name == "session_fork" {
			a["callerSessionId"] = jsonBytes(own)
		}
		if name == "session_task_attach" {
			delete(a, "projectId")
			p, err := ownProject()
			if err != nil {
				return nil, err
			}
			a["boundProject"] = jsonBytes(p)
		}
	}
	if name == "session_transition" {
		target, err := sessionByID(q, sessionArg)
		if err != nil {
			return nil, err
		}
		current, _ := target.profile()
		requested, _ := jsonText(a["profileId"])
		pid := firstString(requested, current.ID)
		if _, err := readTrustedProfile(q, pid); err != nil || pid == "" {
			return nil, fail(403, "Unknown profile")
		}
		// ponytail: the account must be a registered managed account; its binding readiness on this host is the accounts area's resolve.
		if acct, ok := a["account"]; ok && jsonGiven(acct) {
			if ok, err := q.db().AccountTakesWork(q.ctx(), agentAccountArg(acct)); err != nil || !ok {
				if err == nil {
					err = fail(403, "Account not available")
				}
				return nil, err
			}
		}
	}
	return a, nil
}

// agentOperation: a tool of the executor's tool list (fleetOperations), keys in the order the list wrote them.
type agentOperation struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Name        string          `json:"name"`
}

// fleetOperations: name and description of every tool the director may call, for the
// executor's tool list of a profile with fleetOperations (its MCP server forwards them to /api/hosts/agent-tools).
func fleetOperations(p *Person) []agentOperation {
	out := []agentOperation{}
	for _, t := range mcpserve.List(agentTools(p)) { // the agent table too: the executor's own list predates focus_set and watch_*
		out = append(out, agentOperation{Name: t.Name, InputSchema: t.InputSchema, Description: t.Description})
	}
	return out
}

// projectRequired: the operations whose input schema requires projectId (read once from the contract).
var projectRequired = func() map[string]bool {
	out := map[string]bool{}
	for n, o := range contract.Operations {
		var sc struct {
			Required []string `json:"required"`
		}
		// a revoke or an archive must name its project: an agent never does either by omission.
		if json.Unmarshal(o.InputSchema, &sc) == nil && slices.Contains(sc.Required, "projectId") && n != "project_initial_access_revoke" && n != "project_archive" {
			out[n] = true
		}
	}
	return out
}()
