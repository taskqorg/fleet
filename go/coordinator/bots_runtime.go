// Persistent bot runtime: shared wake admission, Home conversation lifecycle, daily schedule & Needs you triggers,
// pause/stop lifecycle controls, and Runs observability.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func (a *App) wakeBots() {
	if a == nil {
		return
	}
	a.ensureRuntime()
	select {
	case a.botWakeCh <- struct{}{}:
	default:
	}
}

// botExecutionContext is pinned onto each execution in a bot's Home conversation so dispatch uses the exact
// settings revision, memory revision and execution project of that turn.
type botExecutionContext struct {
	BotID          string `json:"botId"`
	BotRevision    int64  `json:"botRevision"`
	MemoryRevision int64  `json:"memoryRevision"`
	ProjectID      string `json:"projectId"`
	Name           string `json:"name"`
	Purpose        string `json:"purpose,omitempty"`
	Instructions   string `json:"instructions,omitempty"`
	Soul           string `json:"soul,omitempty"`
	Memory         string `json:"memory,omitempty"`
	BotRunID       string `json:"botRunId,omitempty"`
}

type botRunPayload struct {
	Text        string                     `json:"text,omitempty"`
	Triggers    []contract.BotRunWakeItem  `json:"triggers,omitempty"`
	DedupKeys   []string                   `json:"dedupKeys,omitempty"`
	Automation  *contract.BotAutomationRef `json:"automation,omitempty"`
	ManualInput string                     `json:"manualInput,omitempty"`
}

func botContextOf(b *botFull, runID string) *botExecutionContext {
	return &botExecutionContext{
		BotID:          b.Id,
		BotRevision:    b.Revision,
		MemoryRevision: b.MemoryRevision,
		ProjectID:      b.ProjectId,
		Name:           b.Settings.Name,
		Purpose:        b.Settings.Purpose,
		Instructions:   b.Settings.Instructions,
		Soul:           b.Settings.Soul,
		Memory:         b.Memory,
		BotRunID:       runID,
	}
}

func formatBotInstructions(base string, bc *botExecutionContext) string {
	if bc == nil {
		return base
	}
	parts := []string{}
	if s := strings.TrimSpace(base); s != "" {
		parts = append(parts, s)
	}
	header := fmt.Sprintf("Bot %s (id %s, settings rev %d, memory rev %d, project %s", bc.Name, bc.BotID, bc.BotRevision, bc.MemoryRevision, bc.ProjectID)
	if bc.BotRunID != "" {
		header += fmt.Sprintf(", run %s", bc.BotRunID)
	}
	header += ")"
	if strings.TrimSpace(bc.Purpose) != "" {
		header += ": " + strings.TrimSpace(bc.Purpose)
	}
	parts = append(parts, header)
	if strings.TrimSpace(bc.Instructions) != "" {
		parts = append(parts, "Bot instructions:\n"+strings.TrimSpace(bc.Instructions))
	}
	if strings.TrimSpace(bc.Soul) != "" {
		parts = append(parts, "Bot SOUL:\n"+strings.TrimSpace(bc.Soul))
	}
	if strings.TrimSpace(bc.Memory) != "" {
		parts = append(parts, fmt.Sprintf("Bot project memory (%s):\n%s", bc.ProjectID, strings.TrimSpace(bc.Memory)))
	}
	return strings.Join(parts, "\n\n")
}

func resolvePersonLive(q *Request, email string) (*Person, error) {
	if q.tx != nil {
		var p *Person
		var err error
		if value, ok := txLockStates.Load(q.tx); ok && value.(*txLockState).write {
			p, err = resolveAccessShareTx(q.ctx(), q.tx, email)
		} else {
			p, err = resolveAccessTx(q.ctx(), q.tx, email)
		}
		var he *HTTPError
		if asHTTP(err, &he) && he.Code == 403 {
			return nil, nil
		}
		return p, err
	}
	if q.app != nil && q.app.store != nil {
		p, err := q.app.resolveAccess(q.ctx(), email)
		var he *HTTPError
		if asHTTP(err, &he) && he.Code == 403 {
			return nil, nil
		}
		return p, err
	}
	return q.peopleSnapshot().person(email), nil
}

func verifyPersonProjectAccess(q *Request, email, projectID, missingMsg, revokedMsg string) error {
	dir := q.peopleSnapshot().person(email)
	if dir == nil {
		return fail(403, missingMsg)
	}
	if projectID != "" && !mayAccessProjectID(dir, projectID) {
		return fail(403, revokedMsg)
	}
	liveDir, err := resolvePersonLive(q, email)
	if err != nil {
		return err
	}
	if liveDir == nil {
		return fail(403, missingMsg)
	}
	if projectID != "" && !mayAccessProjectID(liveDir, projectID) {
		return fail(403, revokedMsg)
	}
	return nil
}

func botAuthorityCheck(q *Request, b *botFull) error {
	if err := verifyPersonProjectAccess(q, b.Director, b.ProjectId,
		"Bot director is no longer a person of this Fleet",
		"Bot director no longer has access to project "+b.ProjectId,
	); err != nil {
		return err
	}
	if _, err := readTrustedProfile(q, b.Settings.ProfileId); err != nil {
		return err
	}
	return nil
}

func botRunIDForExecution(q *Request, execID *string) (*string, error) {
	if execID == nil || *execID == "" {
		return nil, nil
	}
	rid, err := unscoped(q, func() (string, error) {
		return q.db().BotRunIDByExecution(q.ctx(), execID)
	})
	if errors.Is(err, pgx.ErrNoRows) || rid == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rid, nil
}

func verifyQueuedBotAuthority(q *Request, botID, projectID, directedBy string) error {
	if botID != "" {
		b, err := unscoped(q, func() (botFull, error) {
			return readBotFull(q, botID)
		})
		if errors.Is(err, errNotFound) {
			return fail(403, "Bot no longer exists")
		}
		if err != nil {
			return err
		}
		if err := botAuthorityCheck(q, &b); err != nil {
			return err
		}
		if projectID != "" && b.ProjectId != projectID {
			return fail(403, "Bot execution project mismatch")
		}
	}
	if directedBy != "" {
		if err := verifyPersonProjectAccess(q, directedBy, projectID,
			"Bot director no longer has project access",
			"Bot director no longer has project access",
		); err != nil {
			return err
		}
	}
	return nil
}

// ensureBotHomeSession ensures the bot has one stable, live, unarchived Home conversation in Conversations.
func ensureBotHomeSession(q *Request, b *botFull) (agentSession, error) {
	if sid := deref(b.HomeSessionId); sid != "" {
		s, err := sessionMaybe(q, sid)
		if err != nil {
			return agentSession{}, err
		}
		if s != nil {
			if s.ArchivedAt != nil {
				s.ArchivedAt = nil
				if err := sessionPut(q, s); err != nil {
					return agentSession{}, err
				}
			}
			return *s, nil
		}
	}
	profile, err := readTrustedProfile(q, b.Settings.ProfileId)
	if err != nil {
		return agentSession{}, err
	}
	sid := newID()
	branch := true
	s := agentSession{
		ID:          sid,
		CreatedAt:   nowMs(),
		AgentConfig: jsonBytes(profile),
		Access:      "write",
		Branch:      &branch,
		ProjectID:   b.ProjectId,
		DirectedBy:  b.Director,
		BotID:       b.Id,
	}
	if err := sessionPut(q, &s); err != nil {
		return agentSession{}, err
	}
	if _, err := q.db().TitleRename(q.ctx(), db.TitleRenameParams{ID: sid, Title: b.Settings.Name, TitleBy: "bot"}); err != nil {
		return agentSession{}, err
	}
	if err := q.db().BotSetHomeSession(q.ctx(), db.BotSetHomeSessionParams{ID: b.Id, HomeSessionID: &sid}); err != nil {
		return agentSession{}, err
	}
	b.HomeSessionId = &sid
	return s, nil
}

// botConversation: the bot's conversation for a subject (fleet-conversations.md#rules), created when it has none and
// opened again when archived. The empty subject is the bot's Home: person messages and direct runs.
func botConversation(q *Request, b *botFull, subject string) (agentSession, error) {
	if subject == "" {
		return ensureBotHomeSession(q, b)
	}
	if err := acquireTxLock(q, "bot-subject:"+b.Id+":"+subject); err != nil { // one conversation per (bot, subject)
		return agentSession{}, err
	}
	id, err := q.db().BotConversationBySubject(q.ctx(), db.BotConversationBySubjectParams{BotID: &b.Id, Subject: &subject})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return agentSession{}, err
	}
	if err == nil {
		s, err := sessionMaybe(q, id)
		if err != nil || s == nil {
			return agentSession{}, err
		}
		if s.ArchivedAt != nil {
			s.ArchivedAt = nil
			if err := sessionPut(q, s); err != nil {
				return agentSession{}, err
			}
		}
		return *s, nil
	}
	profile, err := readTrustedProfile(q, b.Settings.ProfileId)
	if err != nil {
		return agentSession{}, err
	}
	branch := true
	s := agentSession{ID: newID(), CreatedAt: nowMs(), AgentConfig: jsonBytes(profile), Access: "write", Branch: &branch,
		ProjectID: b.ProjectId, DirectedBy: b.Director, BotID: b.Id}
	if err := sessionPut(q, &s); err != nil {
		return agentSession{}, err
	}
	if err := q.db().SessionSetSubject(q.ctx(), db.SessionSetSubjectParams{ID: s.ID, Subject: &subject}); err != nil {
		return agentSession{}, err
	}
	title, err := subjectTitle(q, subject)
	if err != nil {
		return agentSession{}, err
	}
	if _, err := q.db().TitleRename(q.ctx(), db.TitleRenameParams{ID: s.ID, Title: title, TitleBy: "subject"}); err != nil {
		return agentSession{}, err
	}
	return s, nil
}

// subjectTitle: a new conversation's title from its subject: the task's title, the question's text, Host <id>, the
// trigger's name; never the raw key.
func subjectTitle(q *Request, subject string) (string, error) {
	kind, id, _ := strings.Cut(subject, ":")
	var p db.SessionViewThreadItemsParams
	switch kind {
	case "task":
		p.TaskIds = []string{id}
	case "question":
		p.QuestionIds = []string{id}
	case "trigger":
		if t, err := q.db().TriggerGet(q.ctx(), id); err == nil && t.Name != "" {
			return t.Name, nil
		}
		return "Trigger", nil
	default:
		return threadTitle(subject, ""), nil
	}
	rows, err := q.db().SessionViewThreadItems(q.ctx(), p)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Title != "" {
			line, _, _ := strings.Cut(strings.TrimSpace(r.Title), "\n")
			return clip(line, 120), nil
		}
	}
	return threadTitle(subject, ""), nil
}

// syncBotSessionCompatibility continues the native session when project, workspace, host, profile and account are
// compatible, and otherwise creates a retained handoff scoped to the bot's execution project.
func syncBotSessionCompatibility(q *Request, s *agentSession, b *botFull) error {
	curProf, hasProf := s.profile()
	if !hasProf || curProf.ID != b.Settings.ProfileId || s.ProjectID != b.ProjectId {
		priorPerson := q.person
		q.person = q.peopleSnapshot().person(b.Director)
		if q.person == nil {
			q.person = priorPerson
			return fail(403, "Bot director is no longer a person of this Fleet")
		}
		_, err := agentTransition(q, agentTransitionArgs{
			SessionID: s.ID,
			ProfileID: b.Settings.ProfileId,
			ProjectID: jsonBytes(b.ProjectId),
		})
		q.person = priorPerson
		if err != nil {
			return err
		}
		fresh, err := sessionLocked(q, s.ID)
		if err != nil {
			return err
		}
		*s = fresh
		return nil
	}
	return refreshProfile(q, s)
}

// prepareBotSessionForTurn rechecks bot authority and aligns the Home session before queuing or launching a turn.
func prepareBotSessionForTurn(q *Request, s *agentSession) (botFull, error) {
	b, err := unscoped(q, func() (botFull, error) {
		return readBotFull(q, s.BotID)
	})
	if err != nil {
		return botFull{}, err
	}
	if err := botAuthorityCheck(q, &b); err != nil {
		return b, err
	}
	if err := syncBotSessionCompatibility(q, s, &b); err != nil {
		return b, err
	}
	return b, nil
}

func effectiveBotWorkingDir(explicitDir string, projectRaw json.RawMessage, host string) string {
	if explicitDir != "" {
		return explicitDir
	}
	if len(projectRaw) == 0 || host == "" {
		return ""
	}
	var rs runSettings
	if json.Unmarshal(projectRaw, &rs) != nil {
		return ""
	}
	for _, env := range rs.Environments {
		if env.HostID == host && env.CheckoutPath != "" {
			return env.CheckoutPath
		}
	}
	return ""
}

// checkBotDispatchCompatibility verifies authority and native session project/workspace/host/profile/account compatibility at dispatch time.
func checkBotDispatchCompatibility(q *Request, e *dispatchExecution, liveSession *dispatchSession, host, effectiveAccount string, d dispatchSpec) error {
	isBot := e.BotContext != nil || deref(liveSession.BotID) != ""
	if !isBot {
		return nil
	}
	pid := deref(d.ProjectID)
	botID := deref(liveSession.BotID)
	if botID == "" && e.BotContext != nil {
		botID = e.BotContext.BotID
	}
	if err := verifyQueuedBotAuthority(q, botID, pid, deref(e.DirectedBy)); err != nil {
		return err
	}
	if deref(liveSession.NativeSessionID) == "" {
		return nil
	}
	var snap projectSnapshot
	if jsonGiven(e.ProjectSnapshot) {
		_ = lenientDecode(e.ProjectSnapshot, &snap)
	}
	last, err := q.db().BotLastSessionDispatch(q.ctx(), db.BotLastSessionDispatchParams{SessionID: &liveSession.ID, ID: e.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	projectChanged, hostChanged, workspaceChanged, profileChanged, accountChanged := false, false, false, false, false
	if errors.Is(err, pgx.ErrNoRows) {
		hostChanged = deref(d.Last) != "" && deref(d.Last) != host
	} else {
		projectChanged = last.ProjectID != "" && pid != "" && last.ProjectID != pid
		hostChanged = (last.HostID != "" && last.HostID != host) || (deref(d.Last) != "" && deref(d.Last) != host)
		prevDir := effectiveBotWorkingDir(last.WorkingDirectory, last.WorkspaceProject, last.HostID)
		currDir := effectiveBotWorkingDir(deref(d.WorkingDirectory), snap.Settings, host)
		workspaceChanged = last.WorkspaceKey != deref(d.WorkspaceKey) || prevDir != currDir
		profileChanged = last.Harness != "" && d.Harness != "" && last.Harness != d.Harness
		accountChanged = last.AccountBinding != effectiveAccount
	}
	if !projectChanged && !hostChanged && !workspaceChanged && !profileChanged && !accountChanged {
		return nil
	}
	turns, err := transcriptForProject(q, liveSession.ID, pid)
	if err != nil {
		return err
	}
	sess := agentSession{
		ID:               liveSession.ID,
		AgentConfig:      liveSession.AgentConfig,
		Account:          deref(liveSession.Account),
		ProjectID:        pid,
		WorkingDirectory: deref(d.WorkingDirectory),
		WorkspaceKey:     deref(d.WorkspaceKey),
	}
	source := "host " + firstString(last.HostID, deref(d.Last))
	switch {
	case projectChanged:
		source = "project " + last.ProjectID
	case profileChanged:
		source = "profile change"
	case accountChanged:
		source = "account change"
	case workspaceChanged:
		source = "workspace change"
	}
	h, err := makeHandoff(q, &sess, turns, "bot_compatibility_handoff", source)
	if err != nil {
		return err
	}
	liveSession.NativeSessionID = nil
	delete(liveSession.rest, "nativeSessionId")
	if projectChanged {
		delete(liveSession.rest, "nativeSessions")
	}
	if h != nil {
		liveSession.PendingHandoffID = h.ID
	}
	return dispatchSessionPut(q, liveSession)
}

func botRunRowToContract(id string, botID string, projectID string, botRev, memRev int32, sessionID, execID, qmsgID *string, cause string, dedupKey *string, state, obs string, quiet bool, reason, sentBy, source string, hostID, problem *string, createdAt time.Time, startedAt, endedAt *time.Time, payload []byte, linkedTaskIDs []string) contract.BotRunRecord {
	var p botRunPayload
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &p)
	}
	triggers := p.Triggers
	if triggers == nil {
		triggers = []contract.BotRunWakeItem{}
	}
	if linkedTaskIDs == nil {
		linkedTaskIDs = []string{}
	}
	var failure *contract.Failure
	if state == "failed" || state == "cancelled" {
		failure = contract.PlainCause(deref(problem), reason)
	}
	return contract.BotRunRecord{
		Id:              id,
		BotId:           botID,
		ProjectId:       projectID,
		BotRevision:     int64(botRev),
		MemoryRevision:  int64(memRev),
		SessionId:       sessionID,
		ExecutionId:     execID,
		QueuedMessageId: qmsgID,
		Cause:           contract.BotRunCause(cause),
		DedupKey:        dedupKey,
		State:           contract.BotRunState(state),
		Observation:     contract.BotRunObservation(obs),
		Quiet:           quiet,
		Reason:          optStr(reason),
		SentBy:          optStr(sentBy),
		Source:          optStr(source),
		HostId:          hostID,
		Problem:         problem,
		Failure:         failure,
		CreatedAt:       createdAt.UTC().Format(time.RFC3339Nano),
		StartedAt:       formatTimePtr(startedAt),
		EndedAt:         formatTimePtr(endedAt),
		Triggers:        triggers,
		LinkedTaskIds:   linkedTaskIDs,
		Automation:      p.Automation,
	}
}

func readBotRun(q *Request, id string) (contract.BotRunRecord, error) {
	r, err := q.db().BotRunGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.BotRunRecord{}, errNotFound
	}
	if err != nil {
		return contract.BotRunRecord{}, err
	}
	links, err := q.db().BotTasksByRunIDs(q.ctx(), []string{id})
	if err != nil {
		return contract.BotRunRecord{}, err
	}
	taskIDs := make([]string, 0, len(links))
	for _, l := range links {
		taskIDs = append(taskIDs, l.TaskID)
	}
	return botRunRowToContract(r.ID, r.BotID, r.ProjectID, r.BotRevision, r.MemoryRevision, r.SessionID, r.ExecutionID, r.QueuedMessageID, r.Cause, r.DedupKey, r.State, r.Observation, r.Quiet, r.Reason, r.SentBy, r.Source, r.HostID, r.Problem, r.CreatedAt, r.StartedAt, r.EndedAt, r.Payload, taskIDs), nil
}

type botWakeReq struct {
	Cause       string
	DedupKey    string
	DedupKeys   []string
	Text        string
	SentBy      string
	Source      string
	Triggers    []contract.BotRunWakeItem
	CheckPause  bool
	Trigger     *db.Trigger
	ManualInput string
	Thread      string // the subject the wake belongs to (threads_bot.go changeThread); "" the bot's Home
}

// admitBotWake runs the shared admission flow for Run now, scheduled wakes and Needs you triggers.
func admitBotWake(q *Request, b *botFull, req botWakeReq) (contract.BotRunRecord, bool, error) {
	var none contract.BotRunRecord
	if req.DedupKey != "" {
		existingID, err := q.db().BotRunByDedup(q.ctx(), db.BotRunByDedupParams{BotID: b.Id, DedupKey: &req.DedupKey})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return none, false, err
		}
		if err == nil {
			existingRow, err := q.db().BotRunGet(q.ctx(), existingID)
			if err != nil {
				return none, false, err
			}
			var p botRunPayload
			if len(existingRow.Payload) > 0 {
				_ = json.Unmarshal(existingRow.Payload, &p)
			}
			if req.Cause == "manual" && req.Trigger == nil && req.Text != "" && p.Text != "" && p.Text != req.Text {
				return none, false, fail(409, "Request identity conflict")
			}
			if req.Trigger != nil && req.Cause == "manual" && p.ManualInput != req.ManualInput {
				return none, false, fail(409, "Request identity conflict")
			}
			rec, err := readBotRun(q, existingID)
			if err != nil {
				return none, false, err
			}
			return rec, false, nil
		}
	}

	s, err := botConversation(q, b, req.Thread)
	if err != nil {
		return none, false, err
	}
	if err := acquireTxLock(q, "session:"+s.ID); err != nil {
		return none, false, err
	}
	if s, err = sessionLocked(q, s.ID); err != nil {
		return none, false, err
	}

	runID := newID()
	payload := botRunPayload{Text: req.Text, Triggers: req.Triggers, DedupKeys: req.DedupKeys, ManualInput: req.ManualInput}
	var triggerID *string
	if t := req.Trigger; t != nil {
		triggerID = &t.ID
		payload.Automation = &contract.BotAutomationRef{Id: t.ID, Revision: int64(t.Revision), Name: t.Name}
		req.Text += fmt.Sprintf("\n\nTrigger %s (revision %d)", t.Name, t.Revision)
		if t.Text != "" {
			req.Text += "\n" + t.Text
		}
		payload.Text = req.Text
	}
	payloadBytes := jsonBytes(payload)

	recordTerminalSkip := func(state, reason string, dedup *string) (contract.BotRunRecord, bool, error) {
		inserted, err := botRunInsert(q, db.BotRunInsertParams{
			ID:             runID,
			BotID:          b.Id,
			ProjectID:      b.ProjectId,
			BotRevision:    int32(b.Revision),
			MemoryRevision: int32(b.MemoryRevision),
			SessionID:      &s.ID,
			Cause:          req.Cause,
			DedupKey:       dedup,
			State:          state,
			Observation:    "confirmed",
			Reason:         reason,
			SentBy:         req.SentBy,
			Source:         req.Source,
			Payload:        payloadBytes,
			TriggerID:      triggerID,
		})
		if err != nil {
			return none, false, err
		}
		if !inserted && dedup != nil {
			if id, err := q.db().BotRunByDedup(q.ctx(), db.BotRunByDedupParams{BotID: b.Id, DedupKey: dedup}); err == nil {
				rec, err := readBotRun(q, id)
				return rec, false, err
			}
		}
		rec, err := readBotRun(q, runID)
		return rec, false, err
	}

	if req.CheckPause && b.Paused {
		return recordTerminalSkip("skipped", "paused", optStr(req.DedupKey))
	}

	// One turn at a time per conversation; a bot's conversations run in parallel (fleet-conversations.md#rules). A
	// trigger's message to a busy conversation waits in its queue.
	activeExec, err := agentActive(q, s.ID)
	if err != nil {
		return none, false, err
	}
	if activeExec != nil && req.Trigger != nil && req.DedupKey != "" {
		rid := req.DedupKey
		_, err := agentSubmitFrom(q, &contract.MessageSendRequest{SessionId: &s.ID, RequestId: &rid, Text: &req.Text}, req.Source, req.Trigger.ID, "")
		return none, err == nil, err
	}
	if activeExec != nil {
		dedupKey := req.DedupKey
		if req.Cause == "needs_you" {
			// Keep the real event identity available for admission after the active run ends.
			dedupKey = "active_skip:" + dedupKey
		}
		return recordTerminalSkip("skipped", "active_run", optStr(dedupKey))
	}

	if err := botAuthorityCheck(q, b); err != nil {
		return recordTerminalSkip("failed", err.Error(), optStr(req.DedupKey))
	}
	if err := syncBotSessionCompatibility(q, &s, b); err != nil {
		return none, false, err
	}

	e, err := agentEnqueue(q, &s, req.Text, "bot:"+runID, nil, struct {
		BotContext *botExecutionContext `json:"botContext,omitempty"`
	}{botContextOf(b, runID)}, "", &agentProvenance{SentBy: req.SentBy, Source: req.Source})
	if err != nil {
		return none, false, err
	}
	inserted, err := botRunInsert(q, db.BotRunInsertParams{
		ID:             runID,
		BotID:          b.Id,
		ProjectID:      b.ProjectId,
		BotRevision:    int32(b.Revision),
		MemoryRevision: int32(b.MemoryRevision),
		SessionID:      &s.ID,
		ExecutionID:    &e.ID,
		Cause:          req.Cause,
		DedupKey:       optStr(req.DedupKey),
		State:          "queued",
		Observation:    "confirmed",
		SentBy:         req.SentBy,
		Source:         req.Source,
		Payload:        payloadBytes,
		TriggerID:      triggerID,
	})
	if err != nil {
		return none, false, err
	}
	if !inserted {
		return recordTerminalSkip("skipped", "active_run", nil)
	}
	if q.app != nil {
		afterCommit(q, func() { q.app.wakeDispatch() })
	}
	rec, err := readBotRun(q, runID)
	return rec, true, err
}

func recordHomeMessageRun(q *Request, s *agentSession, b *botFull, e *agentExecution, m *agentQueued, text, requestID, sentBy, source string) error {
	runID := newID()
	var execID, qmsgID *string
	if e != nil {
		execID = &e.ID
	}
	if m != nil {
		qmsgID = &m.ID
	}
	dedup := "message:" + requestID
	payloadBytes := jsonBytes(botRunPayload{Text: text})
	_, err := botRunInsert(q, db.BotRunInsertParams{
		ID:              runID,
		BotID:           b.Id,
		ProjectID:       b.ProjectId,
		BotRevision:     int32(b.Revision),
		MemoryRevision:  int32(b.MemoryRevision),
		SessionID:       &s.ID,
		ExecutionID:     execID,
		QueuedMessageID: qmsgID,
		Cause:           "message",
		DedupKey:        &dedup,
		State:           "queued",
		Observation:     "confirmed",
		SentBy:          sentBy,
		Source:          source,
		Payload:         payloadBytes,
	})
	return err
}

func attachBotRunExecution(q *Request, queuedMsgID string, e *agentExecution, b *botFull) error {
	br, err := unscoped(q, func() (db.BotRunByQueuedMessageRow, error) {
		return q.db().BotRunByQueuedMessage(q.ctx(), &queuedMsgID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = unscoped(q, func() (struct{}, error) {
		return struct{}{}, q.db().BotRunAttachExecution(q.ctx(), db.BotRunAttachExecutionParams{
			ID:             br.ID,
			ExecutionID:    &e.ID,
			BotRevision:    int32(b.Revision),
			MemoryRevision: int32(b.MemoryRevision),
			ProjectID:      b.ProjectId,
		})
	})
	return err
}

func transitionBotQueuedRun(q *Request, queuedMsgID, state, reason string) error {
	br, err := unscoped(q, func() (db.BotRunByQueuedMessageRow, error) {
		return q.db().BotRunByQueuedMessage(q.ctx(), &queuedMsgID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if botRunEnds(br.State) {
		return nil
	}
	_, err = unscoped(q, func() (bool, error) {
		return botRunTransition(q, br.ID, botRunEvent{To: state, Reason: optStr(reason)})
	})
	return err
}

func botRunOp(q *Request, r *contract.BotRunRequest) (contract.BotRunReply, error) {
	var out contract.BotRunReply
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	dedupKey := ""
	if rid := strings.TrimSpace(deref(r.RequestId)); rid != "" {
		dedupKey = "manual:" + rid
	}
	text := strings.TrimSpace(deref(r.Text))
	if text == "" {
		text = fmt.Sprintf("Fleet: manual run requested for %s (%s). Inspect your project state and handle any open work.", b.Settings.Name, b.ProjectId)
	}
	var trigger *db.Trigger
	if r.AutomationId != nil {
		saved, err := getBotTrigger(q, b.Id, *r.AutomationId)
		if err != nil {
			return out, err
		}
		if dedupKey != "" {
			dedupKey = "trigger:" + saved.ID + ":" + dedupKey
		}
		trigger = &saved
	}
	run, admitted, err := admitBotWake(q, &b, botWakeReq{
		Trigger:     trigger,
		ManualInput: strings.TrimSpace(deref(r.Text)),
		Cause:       "manual",
		DedupKey:    dedupKey,
		Text:        text,
		SentBy:      director(q),
		Source:      "bot_run",
	})
	if err != nil {
		return out, err
	}
	fresh, err := readBot(q, b.Id)
	if err != nil {
		return out, err
	}
	return contract.BotRunReply{Bot: fresh, Run: run, Admitted: admitted}, nil
}

func botPauseOp(q *Request, r *contract.BotPauseRequest) (contract.BotPauseReply, error) {
	var out contract.BotPauseReply
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	prev := b.Paused
	if prev != r.Paused {
		by := actor(q, "fleet")
		if err := q.db().BotSetPaused(q.ctx(), db.BotSetPausedParams{ID: b.Id, Paused: r.Paused, PausedBy: &by}); err != nil {
			return out, err
		}
		if err := audit(q, "bot:"+b.Id, "paused", prev, r.Paused, auditExtra{ProjectID: &b.ProjectId, Reason: deref(r.Reason)}); err != nil {
			return out, err
		}
		if !r.Paused && q.app != nil {
			afterCommit(q, func() { q.app.wakeBots() })
		}
	}
	fresh, err := readBot(q, b.Id)
	return contract.BotPauseReply{Bot: fresh}, err
}

func botStopOp(q *Request, r *contract.BotStopRequest) (contract.BotStopReply, error) {
	out := contract.BotStopReply{BotId: r.BotId}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	active, err := q.db().BotRunActiveForBot(q.ctx(), b.Id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var execID string
	var runID string
	if err == nil {
		runID = active.ID
		execID = deref(active.ExecutionID)
	}
	if execID == "" && b.HomeSessionId != nil {
		if ae, err := agentActive(q, *b.HomeSessionId); err != nil {
			return out, err
		} else if ae != nil {
			execID = ae.ID
		}
	}
	if execID == "" {
		return out, nil
	}
	stopRep, err := requestExecutionStop(q, execID, false)
	if err != nil {
		return out, err
	}
	if err := syncBotRunFromExecution(q, execID); err != nil {
		return out, err
	}
	out.Stopped = true
	out.Stop = &stopRep.Stop
	if runID != "" {
		rec, err := readBotRun(q, runID)
		if err != nil {
			return out, err
		}
		out.Run = &rec
	}
	return out, nil
}

func botRunsOp(q *Request, r *contract.BotRunsRequest) (contract.BotRunsReply, error) {
	out := contract.BotRunsReply{Runs: []contract.BotRunRecord{}}
	if r.BotId != nil && *r.BotId != "" {
		if _, err := readBot(q, *r.BotId); err != nil {
			return out, err
		}
	}
	if r.ProjectId != nil && *r.ProjectId != "" && projectSettingsOf(q, *r.ProjectId) == nil {
		return out, errNotFound
	}
	var afterSeq *int64
	var afterID *string
	if r.After != nil && *r.After != "" {
		parts := strings.SplitN(*r.After, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return out, fail(400, "Invalid bot runs cursor")
		}
		seq, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return out, fail(400, "Invalid bot runs cursor")
		}
		if _, err := q.db().BotRunGet(q.ctx(), parts[1]); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, fail(400, "Invalid bot runs cursor")
			}
			return out, err
		}
		afterSeq = &seq
		afterID = &parts[1]
	}
	limit := int64(50)
	if r.Limit != nil {
		limit = *r.Limit
		if limit < 1 {
			return out, fail(400, "Expected limit >= 1")
		}
		if limit > 200 {
			limit = 200
		}
	}
	var state, cause, obs *string
	if r.State != nil {
		s := string(*r.State)
		state = &s
	}
	if r.Cause != nil {
		c := string(*r.Cause)
		cause = &c
	}
	if r.Observation != nil {
		o := string(*r.Observation)
		obs = &o
	}
	rows, err := q.db().BotRunsList(q.ctx(), db.BotRunsListParams{
		BotID:             r.BotId,
		TriggerID:         r.AutomationId,
		ProjectID:         r.ProjectId,
		ProjectIds:        r.ProjectIds,
		ExcludeProjectIds: r.ExcludeProjectIds,
		State:             state,
		Cause:             cause,
		Observation:       obs,
		Quiet:             r.Quiet,
		AfterSeq:          afterSeq,
		AfterID:           afterID,
		Lim:               int32(limit + 1),
	})
	if err != nil {
		return out, err
	}
	if int64(len(rows)) > limit {
		out.HasMore = true
		rows = rows[:limit]
	}
	runIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if q.person != nil && !mayAccessProjectID(q.person, row.ProjectID) {
			continue
		}
		runIDs = append(runIDs, row.ID)
	}
	linkedByRun := map[string][]string{}
	if len(runIDs) > 0 {
		links, err := q.db().BotTasksByRunIDs(q.ctx(), runIDs)
		if err != nil {
			return out, err
		}
		for _, l := range links {
			if l.BotRunID != nil {
				linkedByRun[*l.BotRunID] = append(linkedByRun[*l.BotRunID], l.TaskID)
			}
		}
	}
	for _, row := range rows {
		if q.person != nil && !mayAccessProjectID(q.person, row.ProjectID) {
			continue
		}
		out.Runs = append(out.Runs, botRunRowToContract(
			row.ID, row.BotID, row.ProjectID, row.BotRevision, row.MemoryRevision,
			row.SessionID, row.ExecutionID, row.QueuedMessageID, row.Cause, row.DedupKey,
			row.State, row.Observation, row.Quiet, row.Reason, row.SentBy, row.Source,
			row.HostID, row.Problem, row.CreatedAt, row.StartedAt, row.EndedAt, row.Payload,
			linkedByRun[row.ID],
		))
	}
	if out.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next := fmt.Sprintf("%d:%s", last.Seq, last.ID)
		out.NextAfter = &next
	}
	return out, nil
}

func linkBotTask(q *Request, botID, taskID, projectID, kind, createdBy string, botRunID, sessionID, executionID *string) error {
	if botID == "" || taskID == "" {
		return nil
	}
	return q.db().BotTaskUpsert(q.ctx(), db.BotTaskUpsertParams{
		BotID:       botID,
		TaskID:      taskID,
		ProjectID:   projectID,
		LinkKind:    kind,
		CreatedBy:   createdBy,
		BotRunID:    botRunID,
		SessionID:   sessionID,
		ExecutionID: executionID,
	})
}

func botTaskLinkToContract(botID, botName, taskID, projectID string, botRunID, sessionID, execID *string, kind, createdBy string, createdAt time.Time) contract.BotTaskLink {
	return contract.BotTaskLink{
		BotId:       botID,
		BotName:     botName,
		BotRunId:    botRunID,
		CreatedAt:   createdAt.UTC().Format(time.RFC3339Nano),
		CreatedBy:   createdBy,
		ExecutionId: execID,
		LinkKind:    contract.BotTaskLinkKind(kind),
		ProjectId:   projectID,
		SessionId:   sessionID,
		TaskId:      taskID,
	}
}

func botTaskRowToContract(r db.BotTasksListRow) contract.BotTaskRecord {
	var open []json.RawMessage
	if len(r.OpenQuestions) > 0 {
		_ = json.Unmarshal(r.OpenQuestions, &open)
	}
	byState := map[string]int64{}
	if len(r.ExecutionsByState) > 0 {
		_ = json.Unmarshal(r.ExecutionsByState, &byState)
	}
	if byState == nil {
		byState = map[string]int64{}
	}
	var execCount int64
	if r.ExecutionCount != nil {
		execCount = *r.ExecutionCount
	}
	return contract.BotTaskRecord{
		AcceptedAt:        formatTimePtr(r.AcceptedAt),
		AcceptedBy:        r.AcceptedBy,
		BotId:             r.BotID,
		BotName:           r.BotName,
		BotRunId:          r.BotRunID,
		CreatedAt:         r.CreatedAt.UTC().Format(time.RFC3339Nano),
		CreatedBy:         r.CreatedBy,
		ExecutionCount:    execCount,
		ExecutionId:       r.ExecutionID,
		ExecutionsByState: byState,
		LinkKind:          contract.BotTaskLinkKind(r.LinkKind),
		OpenQuestions:     int64(len(open)),
		PauseReason:       r.PauseReason,
		Priority:          r.Priority,
		ProjectId:         r.ProjectID,
		SessionId:         r.SessionID,
		State:             r.State,
		TaskCode:          r.TaskCode,
		TaskId:            r.TaskID,
		Title:             r.Title,
		UpdatedAt:         r.UpdatedAt.UTC().Format(time.RFC3339Nano),
		WaitReason:        r.WaitReason,
	}
}

func botTaskLinkOp(q *Request, r *contract.BotTaskLinkRequest) (contract.BotTaskLinkReply, error) {
	var out contract.BotTaskLinkReply
	if r.BotId == "" || r.TaskId == "" {
		return out, fail(400, "Expected botId and taskId")
	}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if q.agent != "" {
		if err := botAuthorityCheck(q, &b); err != nil {
			return out, err
		}
	}
	t, err := taskLoad(q, r.TaskId)
	if err != nil {
		return out, err
	}
	if deref(t.ProjectID) != b.ProjectId {
		return out, fail(400, "Task project does not match bot project")
	}
	kind := string(contract.BotTaskLinkKindManaged)
	if r.LinkKind != nil {
		if !r.LinkKind.Valid() {
			return out, fail(400, "Invalid linkKind")
		}
		kind = string(*r.LinkKind)
	}
	botRunID := r.BotRunId
	sessionID := b.HomeSessionId
	var execID *string
	if botRunID != nil && *botRunID != "" {
		run, err := q.db().BotRunGet(q.ctx(), *botRunID)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errNotFound
		}
		if err != nil {
			return out, err
		}
		if run.BotID != b.Id {
			return out, fail(400, "Bot run does not belong to bot")
		}
		if run.SessionID != nil {
			sessionID = run.SessionID
		}
		execID = run.ExecutionID
	} else if callerExec := strings.TrimPrefix(q.agent, "execution:"); callerExec != "" && q.agent != callerExec {
		rid, err := botRunIDForExecution(q, &callerExec)
		if err != nil {
			return out, err
		}
		if rid != nil {
			botRunID = rid
			execID = &callerExec
		}
	}
	if err := linkBotTask(q, b.Id, t.ID, b.ProjectId, kind, actor(q, director(q)), botRunID, sessionID, execID); err != nil {
		return out, err
	}
	row, err := q.db().BotTaskGet(q.ctx(), db.BotTaskGetParams{BotID: b.Id, TaskID: t.ID})
	if err != nil {
		return out, err
	}
	out.Link = botTaskLinkToContract(row.BotID, row.BotName, row.TaskID, row.ProjectID, row.BotRunID, row.SessionID, row.ExecutionID, row.LinkKind, row.CreatedBy, row.CreatedAt)
	return out, nil
}

func botTasksOp(q *Request, r *contract.BotTasksRequest) (contract.BotTasksReply, error) {
	out := contract.BotTasksReply{Tasks: []contract.BotTaskRecord{}}
	if r.BotId == "" {
		return out, fail(400, "Expected botId")
	}
	b, err := readBot(q, r.BotId)
	if err != nil {
		return out, err
	}
	if q.person != nil && !mayAccessProjectID(q.person, b.ProjectId) {
		return out, errNotFound
	}
	var afterSeq *int64
	var afterTaskID *string
	if r.After != nil && *r.After != "" {
		parts := strings.SplitN(*r.After, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return out, fail(400, "Invalid bot tasks cursor")
		}
		seq, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || seq < 0 {
			return out, fail(400, "Invalid bot tasks cursor")
		}
		afterSeq = &seq
		afterTaskID = &parts[1]
	}
	limit := int64(50)
	if r.Limit != nil {
		limit = *r.Limit
		if limit < 1 {
			limit = 1
		}
		if limit > 200 {
			limit = 200
		}
	}
	rows, err := q.db().BotTasksList(q.ctx(), db.BotTasksListParams{
		BotID:       b.Id,
		State:       r.State,
		AfterSeq:    afterSeq,
		AfterTaskID: afterTaskID,
		Lim:         int32(limit + 1),
	})
	if err != nil {
		return out, err
	}
	if int64(len(rows)) > limit {
		out.HasMore = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		if q.person != nil && !mayAccessProjectID(q.person, row.ProjectID) {
			continue
		}
		out.Tasks = append(out.Tasks, botTaskRowToContract(row))
	}
	if out.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next := fmt.Sprintf("%d:%s", last.Seq, last.TaskID)
		out.NextAfter = &next
	}
	return out, nil
}

// getBotTrigger: the trigger id of the bot, errNotFound when it is another target's or absent.
func getBotTrigger(q *Request, botID, id string) (db.Trigger, error) {
	t, err := q.db().TriggerGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deref(t.BotID) != botID) {
		return t, errNotFound
	}
	return t, err
}
