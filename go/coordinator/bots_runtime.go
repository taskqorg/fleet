// Persistent bot runtime: shared wake admission, Home conversation lifecycle, daily schedule & Needs you triggers,
// pause/stop lifecycle controls, and Runs observability.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

func initBotWakeCursor(q *Request, botID, projectID string) error {
	h, err := head(q.ctx(), q.tx)
	if err != nil {
		return err
	}
	cur, err := q.db().BotWakeCursorGet(q.ctx(), botID)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.db().BotWakeCursorUpsert(q.ctx(), db.BotWakeCursorUpsertParams{
			BotID:             botID,
			ProjectID:         projectID,
			NeedsYouChangeSeq: h,
		})
	}
	if err != nil {
		return err
	}
	return q.db().BotWakeCursorUpsert(q.ctx(), db.BotWakeCursorUpsertParams{
		BotID:             botID,
		ProjectID:         projectID,
		NeedsYouChangeSeq: h,
		LastScheduleKey:   cur.LastScheduleKey,
		LastScheduleAt:    cur.LastScheduleAt,
	})
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
	Automation  *db.BotAutomation
	ManualInput string
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
			if req.Cause == "manual" && req.Automation == nil && req.Text != "" && p.Text != "" && p.Text != req.Text {
				return none, false, fail(409, "Request identity conflict")
			}
			if req.Automation != nil && req.Cause == "manual" && p.ManualInput != req.ManualInput {
				return none, false, fail(409, "Request identity conflict")
			}
			rec, err := readBotRun(q, existingID)
			if err != nil {
				return none, false, err
			}
			return rec, false, nil
		}
	}

	s, err := ensureBotHomeSession(q, b)
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
	var automationID *string
	if automation := req.Automation; automation != nil {
		automationID = &automation.ID
		payload.Automation = &contract.BotAutomationRef{
			Id: automation.ID, Revision: int64(automation.Revision), Name: automation.Name,
		}
		req.Text += fmt.Sprintf("\n\nAutomation %s (revision %d)", automation.Name, automation.Revision)
		if automation.Instructions != "" {
			req.Text += "\n" + automation.Instructions
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
			AutomationID:   automationID,
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

	activeRun, err := q.db().BotRunActiveForBot(q.ctx(), b.Id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, false, err
	}
	hasActiveRun := err == nil && activeRun.ID != ""
	activeExec, err := agentActive(q, s.ID)
	if err != nil {
		return none, false, err
	}
	if hasActiveRun || activeExec != nil {
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
		AutomationID:   automationID,
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
	var automation *db.BotAutomation
	if r.AutomationId != nil {
		saved, err := getBotAutomation(q, b.Id, *r.AutomationId)
		if err != nil {
			return out, err
		}
		if dedupKey != "" {
			dedupKey = automationDedup(saved.ID, dedupKey)
		}
		automation = &saved
	}
	run, admitted, err := admitBotWake(q, &b, botWakeReq{
		Automation:  automation,
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
		AutomationID:      r.AutomationId,
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

func scheduleDueSlot(b *botFull, lastKey string, now time.Time) (slotKey string, due bool) {
	if b.Settings.ScheduleTime == "" || b.Settings.ScheduleZone == "" {
		return "", false
	}
	loc, err := time.LoadLocation(b.Settings.ScheduleZone)
	if err != nil {
		return "", false
	}
	t, err := time.Parse("15:04", b.Settings.ScheduleTime)
	if err != nil {
		return "", false
	}
	localNow := now.In(loc)
	scheduledAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	effectiveSince := b.RevisionCreatedAt.In(loc).Truncate(time.Minute)
	if b.ResumedAt != nil {
		if r := b.ResumedAt.In(loc).Truncate(time.Minute); r.After(effectiveSince) {
			effectiveSince = r
		}
	}
	slotKey = localNow.Format("2006-01-02") + "|" + b.Settings.ScheduleTime + "|" + b.Settings.ScheduleZone
	if localNow.Before(scheduledAt) || scheduledAt.Before(effectiveSince) || lastKey == slotKey {
		return slotKey, false
	}
	return slotKey, true
}

func problemDedupHash(problem string) string {
	sum := sha256.Sum256([]byte(problem))
	return hex.EncodeToString(sum[:6])
}

func botTriggerPass(ctx context.Context, a *App, now time.Time) error {
	type botSnap struct {
		bot    db.BotListForTriggersRow
		cursor db.BotWakeCursor
		head   int64
	}
	var snaps []botSnap
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		bots, err := q.db().BotListForTriggers(q.ctx())
		if err != nil {
			return err
		}
		if len(bots) == 0 {
			return nil
		}
		h, err := head(q.ctx(), q.tx)
		if err != nil {
			return err
		}
		for _, b := range bots {
			cur, err := q.db().BotWakeCursorGet(q.ctx(), b.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				cur = db.BotWakeCursor{BotID: b.ID, ProjectID: b.ProjectID}
			} else if err != nil {
				return err
			}
			snaps = append(snaps, botSnap{bot: b, cursor: cur, head: h})
		}
		return nil
	}); err != nil {
		log.Printf("bots trigger read: %v", err)
		return err
	}
	for _, sn := range snaps {
		full := botFull{
			BotRecord: contract.BotRecord{
				Id:             sn.bot.ID,
				ProjectId:      sn.bot.ProjectID,
				Director:       sn.bot.Director,
				Revision:       int64(sn.bot.Revision),
				MemoryRevision: int64(sn.bot.MemoryRevision),
				HomeSessionId:  sn.bot.HomeSessionID,
				Paused:         sn.bot.Paused,
				PausedAt:       formatTimePtr(sn.bot.PausedAt),
				Memory:         sn.bot.Memory,
				Settings: contract.BotSettings{
					Name:           sn.bot.Name,
					Purpose:        sn.bot.Purpose,
					ProfileId:      sn.bot.ProfileID,
					Instructions:   sn.bot.Instructions,
					Soul:           sn.bot.Soul,
					ScheduleTime:   sn.bot.ScheduleTime,
					ScheduleZone:   sn.bot.ScheduleZone,
					WakeOnNeedsYou: sn.bot.WakeOnNeedsYou,
				},
			},
			RevisionCreatedAt: sn.bot.RevisionCreatedAt,
			ResumedAt:         sn.bot.ResumedAt,
		}
		_, dueSched := scheduleDueSlot(&full, sn.cursor.LastScheduleKey, now)
		needsChangeCheck := sn.bot.WakeOnNeedsYou && sn.cursor.NeedsYouChangeSeq != sn.head
		if !dueSched && !needsChangeCheck {
			continue
		}
		if err := a.store.Write(ctx, nil, []string{"bot:" + sn.bot.ID}, func(q *Request) error {
			return botProcessTriggersOne(q, sn.bot.ID, now)
		}); err != nil {
			log.Printf("bot %s trigger: %v", sn.bot.ID, err)
		}
	}
	return botAutomationTriggerPass(ctx, a, now)
}

func botProcessTriggersOne(q *Request, botID string, now time.Time) error {
	b, err := readBotFull(q, botID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	cur, err := q.db().BotWakeCursorGet(q.ctx(), botID)
	if errors.Is(err, pgx.ErrNoRows) {
		cur = db.BotWakeCursor{BotID: botID, ProjectID: b.ProjectId}
	} else if err != nil {
		return err
	}

	s, err := ensureBotHomeSession(q, &b)
	if err != nil {
		return err
	}

	// 1. Scheduled daily wake
	if slotKey, due := scheduleDueSlot(&b, cur.LastScheduleKey, now); due {
		cur.LastScheduleKey = slotKey
		cur.LastScheduleAt = &now
		if err := q.db().BotWakeCursorUpsert(q.ctx(), db.BotWakeCursorUpsertParams{
			BotID:             b.Id,
			ProjectID:         b.ProjectId,
			NeedsYouChangeSeq: cur.NeedsYouChangeSeq,
			LastScheduleKey:   cur.LastScheduleKey,
			LastScheduleAt:    cur.LastScheduleAt,
		}); err != nil {
			return err
		}
		prompt := fmt.Sprintf("Fleet: scheduled daily wake (%s %s) for %s in project %s.", b.Settings.ScheduleTime, b.Settings.ScheduleZone, b.Settings.Name, b.ProjectId)
		if _, _, err := admitBotWake(q, &b, botWakeReq{
			Cause:      "schedule",
			DedupKey:   "schedule:" + b.Id + ":" + slotKey,
			Text:       prompt,
			SentBy:     "Fleet",
			Source:     "schedule",
			CheckPause: true,
		}); err != nil {
			return err
		}
	}

	// 2. Needs you trigger
	if !b.Settings.WakeOnNeedsYou {
		return nil
	}
	return botProcessNeedsYou(q, &b, s, cur, nil)
}

func botProcessNeedsYou(q *Request, b *botFull, s agentSession, cur db.BotWakeCursor, automation *db.BotAutomation) error {
	var automationID *string
	var enabledSince pgtype.Timestamptz
	event := "all"
	dedup := func(key string) string { return key }
	if automation != nil {
		automationID = &automation.ID
		event = automation.Event
		enabledSince = pgtype.Timestamptz{Time: automation.EffectiveSince, Valid: true}
		dedup = func(key string) string { return automationDedup(automation.ID, key) }
	}
	cursor := cur.NeedsYouChangeSeq
	var qids, eids []string
	qseen, eseen := map[string]bool{}, map[string]bool{}
	resynced := false
	held := false

	ownActors := map[string]bool{}
	ownExecIDs := map[string]bool{}
	ownExecList, err := q.db().BotOwnExecutions(q.ctx(), &s.ID)
	if err != nil {
		return err
	}
	for _, eid := range ownExecList {
		ownExecIDs[eid] = true
		ownActors["execution:"+eid] = true
	}
	childSessions := map[string]bool{}
	cs, err := q.db().ChildSessionIDs(q.ctx(), &s.ID)
	if err != nil {
		return err
	}
	for _, cid := range cs {
		childSessions[cid] = true
	}

	for {
		page, next, resync, pageHeld, err := since(q.ctx(), q.tx, cursor, 500)
		if err != nil {
			return err
		}
		if pageHeld {
			held = true
		}
		if resync {
			cursor = next
			resynced = true
			break
		}
		for _, c := range page {
			if c.ProjectID == nil || *c.ProjectID != b.ProjectId {
				continue
			}
			if (c.Actor != nil && ownActors[*c.Actor]) || (c.SessionId != nil && (*c.SessionId == s.ID || childSessions[*c.SessionId])) {
				continue
			}
			switch c.Kind {
			case "question":
				if !qseen[c.Id] {
					qseen[c.Id] = true
					qids = append(qids, c.Id)
				}
			case "execution":
				if !eseen[c.Id] {
					eseen[c.Id] = true
					eids = append(eids, c.Id)
				}
			}
		}
		cursor = next
		if len(page) < 500 {
			break
		}
	}

	type triggerCandidate struct {
		item     contract.BotRunWakeItem
		dedupKey string
	}
	var candidates []triggerCandidate

	handledRows, err := q.db().BotHandledDedupPayloads(q.ctx(), db.BotHandledDedupPayloadsParams{BotID: b.Id, AutomationID: automationID})
	if err != nil {
		return err
	}
	handled := map[string]bool{}
	for _, hr := range handledRows {
		if hr.DedupKey != nil && *hr.DedupKey != "" {
			handled[*hr.DedupKey] = true
		}
		if len(hr.Payload) > 0 {
			var p botRunPayload
			if json.Unmarshal(hr.Payload, &p) == nil {
				for _, dk := range p.DedupKeys {
					if dk != "" {
						handled[dk] = true
					}
				}
			}
		}
	}

	if event != "execution" && (resynced || len(qids) > 0) {
		var argIDs []string
		if !resynced {
			argIDs = qids
		}
		qs, err := q.db().BotNeedsYouQuestions(q.ctx(), db.BotNeedsYouQuestionsParams{ProjectID: b.ProjectId, Ids: argIDs, EnabledSince: enabledSince})
		if err != nil {
			return err
		}
		for _, qu := range qs {
			if qu.SessionID != nil && (*qu.SessionID == s.ID || childSessions[*qu.SessionID]) {
				continue
			}
			if qu.ExecutionID != nil && ownExecIDs[*qu.ExecutionID] {
				continue
			}
			if qu.AskedBy != nil && (ownActors[*qu.AskedBy] || ownExecIDs[*qu.AskedBy]) {
				continue
			}
			dk := dedup("needs_you:question:" + qu.ID)
			if handled[dk] {
				continue
			}
			candidates = append(candidates, triggerCandidate{
				item: contract.BotRunWakeItem{
					Kind:      "question",
					Id:        qu.ID,
					ProjectId: qu.ProjectID,
					Summary:   &qu.Text,
				},
				dedupKey: dk,
			})
		}
	}

	if event != "question" && (resynced || len(eids) > 0) {
		var argIDs []string
		if !resynced {
			argIDs = eids
		}
		es, err := q.db().BotNeedsYouExecutions(q.ctx(), db.BotNeedsYouExecutionsParams{ProjectID: b.ProjectId, Ids: argIDs, EnabledSince: enabledSince})
		if err != nil {
			return err
		}
		for _, ex := range es {
			if ex.SessionID != nil && (*ex.SessionID == s.ID || childSessions[*ex.SessionID]) {
				continue
			}
			if ownExecIDs[ex.ID] {
				continue
			}
			prob := deref(ex.Problem)
			dk := dedup("needs_you:execution:" + ex.ID + ":" + problemDedupHash(prob))
			if handled[dk] {
				continue
			}
			candidates = append(candidates, triggerCandidate{
				item: contract.BotRunWakeItem{
					Kind:      "execution",
					Id:        ex.ID,
					ProjectId: ex.ProjectID,
					Summary:   &prob,
				},
				dedupKey: dk,
			})
		}
	}

	advance := func() error {
		if cursor == cur.NeedsYouChangeSeq {
			return nil
		}
		if automation != nil {
			return q.db().BotAutomationEventAdvance(q.ctx(), db.BotAutomationEventAdvanceParams{ID: automation.ID, EventCursor: cursor})
		}
		return q.db().BotWakeCursorUpsert(q.ctx(), db.BotWakeCursorUpsertParams{
			BotID: b.Id, ProjectID: b.ProjectId, NeedsYouChangeSeq: cursor,
			LastScheduleKey: cur.LastScheduleKey, LastScheduleAt: cur.LastScheduleAt,
		})
	}
	if len(candidates) == 0 {
		if err := advance(); err != nil {
			return err
		}
		if held && q.app != nil {
			afterCommit(q, func() { q.app.wakeBots() })
		}
		return nil
	}

	triggers := make([]contract.BotRunWakeItem, 0, len(candidates))
	dedupKeys := make([]string, 0, len(candidates))
	summaries := make([]string, 0, len(candidates))
	for _, c := range candidates {
		triggers = append(triggers, c.item)
		dedupKeys = append(dedupKeys, c.dedupKey)
		summaries = append(summaries, fmt.Sprintf("%s %s: %s", c.item.Kind, c.item.Id, deref(c.item.Summary)))
	}
	primaryDedup := candidates[0].dedupKey
	prompt := fmt.Sprintf("Fleet: %d new Needs you entry(s) in %s: %s", len(triggers), b.ProjectId, strings.Join(summaries, "; "))

	run, _, err := admitBotWake(q, b, botWakeReq{
		Automation: automation,
		Cause:      "needs_you",
		DedupKey:   primaryDedup,
		DedupKeys:  dedupKeys,
		Text:       prompt,
		SentBy:     "Fleet",
		Source:     "needs_you",
		Triggers:   triggers,
		CheckPause: true,
	})
	if err != nil || deref(run.Reason) == "active_run" {
		return err
	}
	return advance()
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
