// Agent turn lifecycle: execution creation, message submit, queuing, steering, command receipts, start decoration, stop outcome, and reconciliation.
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// agentExecutions: the executions of a session, in order.
func agentExecutions(q *Request, sid string) ([]agentExecution, error) {
	return recAll[agentExecution](q, recExecution, agentFilter{SessionID: &sid})
}

func agentActive(q *Request, sid string) (*agentExecution, error) {
	es, err := agentExecutions(q, sid)
	for i := range es {
		if !es[i].terminal() {
			return &es[i], err
		}
	}
	return nil, err
}

// agentProvenance: who sent a message and its trace (a queued message carries both to its turn).
type agentProvenance struct {
	SentBy      string `json:"sentBy,omitempty"`
	Source      string `json:"source,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
	TraceActor  string `json:"traceActor,omitempty"`
}

// agentEnqueue: a queued execution and its message. Late choice: host and subscription are picked at dispatch.
// execution: a struct of the fields another area adds to the execution record (a pipeline step's); nil: none.
func agentEnqueue(q *Request, s *agentSession, input, requestID string, payload map[string]json.RawMessage, execution any, host string, prov *agentProvenance) (agentExecution, error) {
	var none agentExecution
	extra := agentView[struct {
		DeliveryID string `json:"deliveryId"`
	}](execution)
	if err := taskAllowsWork(q, firstString(extra.DeliveryID, s.DeliveryID, s.TaskID)); err != nil {
		return none, err
	}
	if s.PendingTransition != nil {
		priorPerson := q.person
		q.person = q.peopleSnapshot().person(s.DirectedBy)
		if q.person == nil {
			q.person = priorPerson
			return none, fail(403, "Session director unavailable")
		}
		_, transitionErr := agentTransition(q, *s.PendingTransition)
		q.person = priorPerson
		if transitionErr != nil {
			return none, transitionErr
		}
		fresh, err := sessionByID(q, s.ID)
		if err != nil {
			return none, err
		}
		*s = fresh
	}
	if s.DirectedBy == "" {
		d := ""
		for _, id := range []string{s.CreatedByExecutionID, s.ParentExecutionID} {
			if x, err := recMaybe[agentExecution](q, recExecution, id); err != nil {
				return none, err
			} else if d == "" && x != nil {
				d = x.DirectedBy
			}
		}
		if d == "" && s.PipelineRunID != "" {
			if r, err := pipelineRun(q, s.PipelineRunID); err == nil {
				d = agentView[struct {
					RequestedBy struct {
						Email string `json:"email"`
					} `json:"requestedBy"`
				}](r).RequestedBy.Email
			}
		}
		s.DirectedBy = firstString(d, director(q))
		if err := sessionPut(q, s); err != nil {
			return none, err
		}
	}
	es, err := agentExecutions(q, s.ID)
	if err != nil {
		return none, err
	}
	last := ""
	for _, e := range es {
		if h := deref(e.HostID); h != "" {
			last = h
		}
	}
	if host == "" {
		host = s.HostID
	}
	if host != "" {
		if ok, err := knownHost(q, host); err != nil || !ok {
			if err == nil {
				err = fail(400, "Unknown host "+host)
			}
			return none, err
		}
	}
	ac, hasProfile := s.profile()
	if _, given := payload["skills"]; !given && len(ac.Skills) > 0 { // a session profile's skills, as pipeline steps get them
		skills, err := profileSkills(s.AgentConfig)
		if err != nil {
			return none, err
		}
		if payload == nil {
			payload = map[string]json.RawMessage{}
		}
		payload["skills"] = jsonBytes(skills)
	}
	var payloadRaw json.RawMessage
	if payload != nil {
		payloadRaw = jsonBytes(payload)
	}
	id := newID()
	e := agentExecution{ID: id, ExecutionID: id, SessionID: s.ID, State: "preparing", CreatedAt: nowMs(), DirectedBy: s.DirectedBy, Model: "gpt-6.1-sol", Effort: jsonBytes("low"),
		Dispatch: &agentDispatch{Harness: s.harness(), ProjectID: agentOpt(s.ProjectID), WorkingDirectory: agentOpt(s.WorkingDirectory), WorkspaceKey: agentOpt(s.WorkspaceKey), Account: agentOpt(s.Account),
			Access: agentOpt(s.Access), Branch: s.Branch, Host: agentOpt(host), Last: agentOpt(last), Input: input, Payload: payloadRaw}}
	if hasProfile {
		e.AgentConfig, e.ProfileSha256, e.Model, e.Effort = s.AgentConfig, frameHash(jsonValue(s.AgentConfig)), firstString(ac.Model, "gpt-6.1-sol"), ac.Effort
		ref, err := definitionRef(q, "agent", ac.ID, jsonValue(s.AgentConfig))
		if err != nil {
			return none, err
		}
		e.Definitions = jsonBytes([]contract.DefinitionRef{ref})
	}
	if execution != nil {
		var d, add rawObject
		if err := json.Unmarshal(jsonBytes(e), &d); err != nil {
			return none, err
		}
		if err := json.Unmarshal(jsonBytes(execution), &add); err != nil {
			return none, err
		}
		maps.Copy(d, add)
		if e, err = asT[agentExecution](d); err != nil {
			return none, err
		}
	}
	if pid := s.ProjectID; pid != "" {
		snap, err := projectContext(q, pid)
		if err != nil {
			return none, err
		}
		snapRaw := json.RawMessage(jsonBytes(snap))
		if s.PipelineRunID != "" {
			parent, err := pipelineRun(q, s.PipelineRunID)
			if err != nil {
				return none, err
			}
			if frozen := agentView[struct {
				ProjectSnapshot json.RawMessage `json:"projectSnapshot"`
			}](parent).ProjectSnapshot; jsonIsObject(frozen) {
				snapRaw = frozen
			}
		}
		e.ProjectSnapshot = snapRaw
		version := agentView[struct {
			Revision   json.RawMessage `json:"revision"`
			ContentSha json.RawMessage `json:"contentSha"`
		}](snapRaw)
		var refs []json.RawMessage
		json.Unmarshal(e.Definitions, &refs)
		refs = append(refs, jsonBytes(struct {
			Kind       string          `json:"kind"`
			ID         string          `json:"id"`
			Revision   json.RawMessage `json:"revision"`
			ContentSha json.RawMessage `json:"contentSha"`
		}{"project", pid, version.Revision, version.ContentSha}))
		e.Definitions = jsonBytes(refs)
	}
	if prov != nil && validTraceparent(prov.Traceparent) {
		e.Traceparent, e.TraceActor = prov.Traceparent, prov.TraceActor
	}
	if err := recPut(q, recExecution, &e); err != nil {
		return none, err
	}
	m := agentMessage{ID: newID(), SessionID: s.ID, ExecutionID: id, RequestID: requestID, Text: input, ProjectID: s.ProjectID, SentBy: director(q)}
	var attached []json.RawMessage
	json.Unmarshal(payload["attachments"], &attached)
	if len(attached) > 0 {
		m.Attachments = payload["attachments"]
		json.Unmarshal(payload["attachmentRequest"], &m.AttachmentRequest)
	}
	if prov != nil {
		if prov.SentBy != "" {
			m.SentBy = prov.SentBy
		}
		if prov.Source != "" {
			m.Source = prov.Source
		}
	}
	return e, messagePut(q, &m)
}

// queueView: a queued message as message_send and queue_list show it: its record, queued and its place in the queue.
func queueView(q *Request, m agentQueued) (map[string]json.RawMessage, error) {
	qs, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &m.SessionID, State: ptr("queued")})
	if err != nil {
		return nil, err
	}
	v := recOf(m)
	v["queued"], v["position"] = json.RawMessage("true"), json.RawMessage("null")
	for i, x := range qs {
		if x.ID == m.ID {
			v["position"] = jsonBytes(i + 1)
		}
	}
	return v, nil
}

// agentSubmitted: what a message became: the execution it started (or that a repeated request started), or its queue
// entry.
type agentSubmitted struct {
	execution *agentExecution
	queued    *agentQueued
	view      map[string]json.RawMessage
}

// reply: the message_send reply (the execution record, or the queue view).
func (r agentSubmitted) reply() map[string]json.RawMessage {
	if r.execution != nil {
		return recOf(*r.execution)
	}
	return r.view
}

// attachmentIDs: the artifact ids of a message, in order, without repeats; checked before any read (as attachmentArg).
func attachmentIDs(xs []string) ([]string, error) {
	if xs == nil {
		return nil, nil
	}
	ids, seen := []string{}, map[string]bool{}
	for _, id := range xs {
		if id == "" || len(id) > 200 {
			return nil, fail(400, "Expected attachments: a list of artifact ids")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// agentSubmit: a message starts a turn, or is queued (or steered) behind an active execution; nothing is refused for
// being busy. source is internal provenance, never taken from the caller's arguments.
func agentSubmit(q *Request, r *contract.MessageSendRequest, source string) (agentSubmitted, error) {
	var none agentSubmitted
	sid := deref(r.SessionId)
	if sid == "" {
		return none, fail(400, "Expected sessionId")
	}
	s, err := sessionLocked(q, sid)
	if err != nil {
		return none, err
	}
	requestID := deref(r.RequestId)
	if requestID == "" {
		return none, fail(400, "Expected requestId")
	}
	ids, err := attachmentIDs(r.Attachments)
	if err != nil {
		return none, err
	}
	text := deref(r.Text)
	if text == "" && len(ids) == 0 {
		return none, fail(400, "Expected text")
	}
	if len(r.Setup) > 0 {
		setup, _ := jsonText(r.Setup)
		if setup != "run" && setup != "skip" {
			return none, fail(400, "Expected setup run|skip")
		}
		s.Setup = setup
		if err := sessionPut(q, &s); err != nil {
			return none, err
		}
	}
	if sent, err := messagesBySession(q, sid, &requestID); err != nil {
		return none, err
	} else if len(sent) > 0 {
		if sent[0].Text != text || strings.Join(sent[0].AttachmentRequest, ",") != strings.Join(ids, ",") {
			return none, fail(409, "Request identity conflict")
		}
		e, err := recGet[agentExecution](q, recExecution, sent[0].ExecutionID)
		return agentSubmitted{execution: &e}, err
	}
	if queued, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, RequestID: &requestID}); err != nil {
		return none, err
	} else if len(queued) > 0 {
		if queued[0].Text != text || strings.Join(queued[0].AttachmentRequest, ",") != strings.Join(ids, ",") {
			return none, fail(409, "Request identity conflict")
		}
		v, err := queueView(q, queued[0])
		return agentSubmitted{queued: &queued[0], view: v}, err
	}
	if err := taskAllowsWork(q, firstString(s.DeliveryID, s.TaskID)); err != nil {
		return none, err
	}
	// A repeated request answered above without checking its files again; a new one checks them now.
	attachments, err := messageAttachments(q, ids, &s)
	if err != nil {
		return none, err
	}
	active, err := agentActive(q, sid)
	if err != nil {
		return none, err
	}
	waiting, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, State: ptr("queued")})
	if err != nil {
		return none, err
	}
	sender := director(q)
	if source == "watch" || source == "wake" || source == "transition" {
		sender = "Fleet"
	}
	if active == nil && len(waiting) == 0 {
		if err := refreshProfile(q, &s); err != nil {
			return none, err
		}
		payload := attachmentPayload(attachments, ids)
		e, err := agentEnqueue(q, &s, text, requestID, payload, nil, "", &agentProvenance{SentBy: sender, Source: source})
		if err != nil {
			return none, err
		}
		return agentSubmitted{execution: &e}, nil
	}
	m := agentQueued{ID: newID(), SessionID: sid, ProjectID: s.ProjectID, TaskID: s.TaskID, RequestID: requestID, Text: text, State: "queued", CreatedAt: nowMs(), SentBy: sender, Source: source}
	if len(attachments) > 0 {
		m.Attachments, m.AttachmentRequest = jsonBytes(attachments), ids
	}
	// A message with attachments is queued, never steered: its files are staged before a turn starts.
	if deref(r.Delivery) == "steer" && active != nil && active.State == "running" && len(attachments) == 0 {
		ok, err := capable(q, "steer")
		if err != nil {
			return none, err
		}
		if ok {
			payload := struct {
				Text            string `json:"text"`
				NativeSessionID string `json:"nativeSessionId,omitempty"`
			}{text, s.NativeSessionID}
			c := agentCommand{ID: newID(), ExecutionID: active.ID, SessionID: sid, HostID: deref(active.HostID), ProjectID: s.ProjectID, Kind: "steer", Payload: jsonBytes(payload), Status: "pending"}
			if err := commandPut(q, &c); err != nil {
				return none, err
			}
			m.State, m.SteerCommandID, m.ExecutionID = "steering", c.ID, active.ID
		}
	}
	if err := queuedMessagePut(q, &m); err != nil {
		return none, err
	}
	v, err := queueView(q, m)
	return agentSubmitted{queued: &m, view: v}, err
}

// agentPromote starts the oldest queued message of every idle session (call after an execution ends).
// It acquires canonical session:<id> locks in order to serialize with concurrent message_send calls.
func agentPromote(q *Request) error {
	queued, err := queuedMessages(q, db.QueuedMessagesParams{State: ptr("queued")})
	if err != nil || len(queued) == 0 {
		return err
	}
	var orderable []string
	deferred := false
	seen := map[string]bool{}
	var candidates []string
	for _, m := range queued {
		if sid := m.SessionID; sid != "" && !seen[sid] {
			seen[sid] = true
			candidates = append(candidates, "session:"+sid)
		}
	}
	sorted, err := sortLocks(candidates)
	if err != nil {
		return err
	}
	for _, k := range sorted {
		if canAcquireTxLock(q.tx, k) {
			orderable = append(orderable, k)
		} else {
			deferred = true
		}
	}
	if len(orderable) > 0 {
		if err := acquireTxLock(q, orderable...); err != nil {
			return err
		}
		queued, err = queuedMessages(q, db.QueuedMessagesParams{State: ptr("queued")})
		if err != nil {
			return err
		}
	}
	if deferred {
		onEnd(q.tx, func(committed bool) {
			if committed && q.app != nil {
				q.app.wakeDispatch()
			}
		})
	}
	for i := range queued {
		m := queued[i]
		sid := m.SessionID
		if !txHasLock(q.tx, "session:"+sid) {
			continue
		}
		earlier := false
		for _, x := range queued[:i] {
			if x.SessionID == m.SessionID && x.State == "queued" && x.CreatedAt < m.CreatedAt {
				earlier = true
			}
		}
		if earlier {
			continue
		}
		if a, err := agentActive(q, sid); err != nil {
			return err
		} else if a != nil {
			continue
		}
		e, err := func() (agentExecution, error) {
			s, err := sessionLocked(q, sid)
			if err != nil {
				return agentExecution{}, err
			}
			if err := refreshProfile(q, &s); err != nil {
				return agentExecution{}, err
			}
			var attachments []any
			json.Unmarshal(m.Attachments, &attachments)
			payload := attachmentPayload(attachments, m.AttachmentRequest)
			return agentEnqueue(q, &s, m.Text, m.RequestID, payload, nil, "", &agentProvenance{SentBy: m.SentBy, Source: m.Source, Traceparent: m.Traceparent, TraceActor: m.TraceActor})
		}()
		if err != nil {
			if pgCode(err) != "" {
				return err
			}
			m.Error = err.Error()
		} else {
			m.State, m.ExecutionID, m.Error = "started", e.ID, ""
		}
		if err := queuedMessagePut(q, &m); err != nil {
			return err
		}
	}
	return nil
}

// agentCommandReceipt: steer queue state, the completion wake and handoff delivery state for a receipt (status legacy).
func agentCommandReceipt(q *Request, c *agentCommand, e *agentExecution, status string) error {
	if c.Kind == "steer" {
		ms, err := queuedMessages(q, db.QueuedMessagesParams{SteerCommandID: &c.ID})
		if err != nil || len(ms) == 0 {
			return err
		}
		m := ms[0]
		if m.State == "cancelled" {
			return nil
		}
		switch status {
		case "completed":
			m.State = "steered"
		case "failed":
			m.State, m.SteerCommandID = "queued", ""
		case "unknown":
			m.State = "steer_unknown"
		}
		return queuedMessagePut(q, &m)
	}
	if c.Kind != "start" {
		return nil
	}
	if status == "completed" || status == "failed" || status == "stopped" {
		if err := wake(q, e, status); err != nil {
			return err
		}
	}
	if e.HandoffID == "" {
		return nil
	}
	h, err := handoffGet(q, e.HandoffID)
	if err != nil || h.State != "sent" {
		return err
	}
	switch status {
	case "started", "completed":
		h.State = "delivered"
	case "unknown":
		h.State = "unconfirmed"
	case "failed", "stopped":
		h.State = "pending"
		s, err := sessionByID(q, e.SessionID)
		if err != nil {
			return err
		}
		if s.PendingHandoffID == "" && !jsonGiven(e.StartedAt) {
			s.PendingHandoffID = h.ID
			if err := sessionPut(q, &s); err != nil {
				return err
			}
		}
	}
	return handoffPut(q, &h)
}

// wake: a child session's finished turn becomes the next message of the session that created it.
func wake(q *Request, e *agentExecution, status string) error {
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return err
	}
	parentSid := s.CreatedBySessionID
	if parentSid == "" {
		return nil
	}
	if canAcquireTxLock(q.tx, "session:"+parentSid) {
		if err := acquireTxLock(q, "session:"+parentSid); err != nil {
			return err
		}
	}
	fresh, err := recGet[agentExecution](q, recExecution, e.ID)
	if err != nil {
		return err
	}
	role, report := "", ""
	if r := s.Role; r != "" {
		role = " (" + r + ")"
	}
	if r := fresh.Report; r != nil {
		report = fmt.Sprintf(" worker_done %s: %s", r.Outcome, deref(r.Summary))
	}
	_, err = agentSubmit(q, &contract.MessageSendRequest{SessionId: &parentSid, RequestId: ptr("wake:" + e.ID),
		Text: ptr(fmt.Sprintf(`Fleet: your session %s%s finished execution %s: %s.%s Read it with execution_status {sessionId:"%s"}.`, s.ID, role, e.ID, status, report, s.ID))}, "wake")
	if pgCode(err) != "" {
		return err
	}
	return nil
}

// agentDecorate: agentDecorateStart for dispatch.go, which holds the session and execution as open records: the fields
// it changes are written back into them.
func agentDecorate(q *Request, s, e rawObject, payload *contract.StartPayload) error {
	sess, err := asT[agentSession](s)
	if err != nil {
		return err
	}
	ex, err := asT[agentExecution](e)
	if err != nil {
		return err
	}
	err = agentDecorateStart(q, &sess, &ex, payload)
	e["continuationKey"] = jsonBytes(ex.ContinuationKey)
	if ex.ForkOfNativeSessionID != "" {
		e["forkOfNativeSessionId"] = jsonBytes(ex.ForkOfNativeSessionID)
	}
	if ex.HandoffID != "" {
		e["handoffId"] = jsonBytes(ex.HandoffID)
	}
	if sess.PendingHandoffID == "" {
		delete(s, "pendingHandoffId")
	}
	return err
}

// agentDecorateStart: inline handoff delivery and the native fork request on a start command (call before storing it).
func agentDecorateStart(q *Request, s *agentSession, e *agentExecution, payload *contract.StartPayload) error {
	e.ContinuationKey = s.continuationKey()
	if f := s.ForkNativeSessionID; f != "" && s.NativeSessionID == "" {
		payload.Fork = recOf(struct {
			NativeSessionID string `json:"nativeSessionId"`
			Argv            any    `json:"argv"`
		}{f, forkArgv(s.harness(), f)})
		e.ForkOfNativeSessionID = f
	}
	if hid := s.PendingHandoffID; hid != "" {
		h, err := handoffGet(q, hid)
		if err != nil {
			return err
		}
		text := h.Text + "\n\nCurrent request:\n" + deref(payload.Text)
		payload.Text = &text
		h.State, h.ExecutionID = "sent", e.ID
		if err := handoffPut(q, &h); err != nil {
			return err
		}
		e.HandoffID = h.ID
		fresh, err := sessionByID(q, s.ID)
		if err != nil {
			return err
		}
		fresh.PendingHandoffID, s.PendingHandoffID = "", ""
		return sessionPut(q, &fresh)
	}
	return nil
}

// agentStopOutcome: proven only when a host receipt ended the execution; otherwise unknown.
func agentStopOutcome(q *Request, e *agentExecution) (contract.StopOutcome, error) {
	cs, err := commandsList(q, db.CommandsParams{ExecutionID: &e.ID, Kind: ptr("stop")})
	if err != nil {
		return contract.StopOutcome{}, err
	}
	var stop *agentCommand
	if len(cs) > 0 {
		stop = &cs[0]
	}
	if e.terminal() {
		detail := "already_finished"
		if isTrue(e.StopRequested) && (e.State == "cancelled" || (stop != nil && stop.Status == "stopped")) {
			detail = "stopped"
		}
		return contract.StopOutcome{Outcome: "proven", Detail: detail, State: e.State}, nil
	}
	o := contract.StopOutcome{Outcome: "unknown", Detail: "unconfirmed", State: e.State}
	if stop != nil {
		o.StopCommand = &contract.StopCommandRef{Id: stop.ID, Status: stop.Status}
	}
	return o, nil
}

func executionStatus(q *Request, r *contract.ExecutionStatusRequest) (contract.ExecutionStatusReply, error) {
	var rep contract.ExecutionStatusReply
	eid := r.ExecutionId
	if eid == "" {
		return rep, fail(400, "Expected executionId")
	}
	e, err := recGet[agentExecution](q, recExecution, eid)
	if err != nil {
		return rep, err
	}
	next := "wait_or_steer"
	switch {
	case e.terminal():
		next = "message"
	case e.unconfirmed():
		next = "session_fork"
	case isTrue(e.StopRequested):
		next = "execution_status"
	}
	// Host events carry no project: read them unscoped, the execution itself was read in the person's scope above.
	readiness, err := unscoped(q, func() ([][]byte, error) {
		return q.db().AgentReadinessData(q.ctx(), eid)
	})
	if err != nil {
		return rep, err
	}
	observation := "confirmed"
	var reason *string
	if e.unconfirmed() {
		observation = "unconfirmed"
		text := firstString(e.Error, e.Problem, "Execution outcome is unconfirmed; the host has not established its current state")
		reason = &text
	}
	rep = contract.ExecutionStatusReply{ExecutionId: eid, SessionId: agentOpt(e.SessionID), State: e.State, Observation: observation, Reason: reason, Report: jsonBytes(e.Report), Next: next}
	if isTrue(e.StopRequested) {
		stop, err := agentStopOutcome(q, &e)
		if err != nil {
			return rep, err
		}
		rep.Stop = &stop
	}
	resume, err := sessionResume(q, deref(rep.SessionId))
	if err != nil {
		return rep, err
	}
	rep.Resume = recOf(resume)
	for _, d := range readiness {
		r, err := sortedJSON(d)
		if err != nil {
			return rep, err
		}
		rep.Readiness = append(rep.Readiness, r)
	}
	return rep, nil
}

// The items of a reconcile report, one type per kind.
type (
	agentSteerRequeued struct {
		Kind    string `json:"kind"`
		QueueID string `json:"queueId"`
	}
	agentHandoffRepending struct {
		Kind      string `json:"kind"`
		HandoffID string `json:"handoffId"`
	}
	agentExecutionItem struct {
		Kind           string          `json:"kind"`
		ExecutionID    string          `json:"executionId"`
		SessionID      json.RawMessage `json:"sessionId"`
		State          string          `json:"state"`
		Classification string          `json:"classification"`
		Next           string          `json:"next"`
	}
	agentQuestionOpen struct {
		Kind       string          `json:"kind"`
		QuestionID string          `json:"questionId"`
		AskerState json.RawMessage `json:"askerState"`
	}
)

// agentReconcile settles coordinator-owned queue and handoff state and reports unproven executions; nothing is replayed.
func agentReconcile(q *Request) (contract.ReconcileReply, error) {
	items := []any{}
	steering, err := queuedMessages(q, db.QueuedMessagesParams{State: ptr("steering")})
	if err != nil {
		return nil, err
	}
	for _, m := range steering {
		c, err := commandGet(q, m.SteerCommandID)
		if err != nil {
			return nil, err
		}
		e, err := recGet[agentExecution](q, recExecution, m.ExecutionID)
		if err != nil {
			return nil, err
		}
		if e.terminal() && c.Status == "pending" {
			m.State, m.SteerCommandID = "queued", ""
			if err := queuedMessagePut(q, &m); err != nil {
				return nil, err
			}
			items = append(items, agentSteerRequeued{"steer_requeued", m.ID})
		}
	}
	sent, err := handoffsByState(q, "sent")
	if err != nil {
		return nil, err
	}
	for _, h := range sent {
		e, err := recGet[agentExecution](q, recExecution, h.ExecutionID)
		if err != nil {
			return nil, err
		}
		if e.terminal() && !jsonGiven(e.StartedAt) {
			h.State = "pending"
			if err := handoffPut(q, &h); err != nil {
				return nil, err
			}
			s, err := sessionByID(q, h.SessionID)
			if err != nil {
				return nil, err
			}
			if s.PendingHandoffID == "" {
				s.PendingHandoffID = h.ID
				if err := sessionPut(q, &s); err != nil {
					return nil, err
				}
			}
			items = append(items, agentHandoffRepending{"handoff_repending", h.ID})
		}
	}
	if err := agentPromote(q); err != nil {
		return nil, err
	}
	es, err := recAll[agentExecution](q, recExecution, agentFilter{})
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.terminal() {
			continue
		}
		starts, err := commandsList(q, db.CommandsParams{ExecutionID: &e.ID, Kind: ptr("start")})
		if err != nil {
			return nil, err
		}
		class, next := "in_flight_unproven", "wait"
		if len(starts) == 0 || starts[0].Status == "pending" {
			class = "awaiting_host"
		}
		if e.unconfirmed() {
			class, next = "unknown", "session_fork"
		}
		items = append(items, agentExecutionItem{"execution", e.ID, agentNullText(e.SessionID), e.State, class, next})
	}
	open, err := recAll[agentQuestion](q, recQuestion, agentFilter{State: ptr("open")})
	if err != nil {
		return nil, err
	}
	for _, qu := range open {
		asker := json.RawMessage("null")
		if e, _ := recMaybe[agentExecution](q, recExecution, qu.ExecutionID); e != nil {
			asker = agentNullText(e.State)
		}
		items = append(items, agentQuestionOpen{"question_open", qu.ID, asker})
	}
	id := "reconcile:" + newID()
	r := recOf(struct {
		ID    string `json:"id"`
		At    int64  `json:"at"`
		Items []any  `json:"items"`
	}{id, nowMs(), items})
	return r, q.db().DispReconciliationPut(q.ctx(), db.DispReconciliationPutParams{ID: id, Items: jsonBytes(items)})
}
