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

// botExecutionContext is pinned onto each execution in a bot's conversations so dispatch uses the exact settings
// revision, memory revision and execution project of that turn. The execution is the bot run: Cause is the wake that
// started it (manual, schedule, needs_you; empty: a message) and Quiet, once a wake's run succeeded, whether it changed
// nothing (settleBotRun).
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
	Cause          string `json:"cause,omitempty"`
	Quiet          *bool  `json:"quiet,omitempty"`
}

func botContextOf(b *botFull, cause string) *botExecutionContext {
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
		Cause:          cause,
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
	header := fmt.Sprintf("Bot %s (id %s, settings rev %d, memory rev %d, project %s)", bc.Name, bc.BotID, bc.BotRevision, bc.MemoryRevision, bc.ProjectID)
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

// botConversation: the bot's conversation for a subject (fleet-conversations.md#rules), created when it has none and
// opened again when archived. The empty subject is the bot's newest conversation without one: person messages and
// direct runs.
// botSubjectLock: the lock of the one conversation per (bot, subject); a caller waking several subjects in one
// transaction takes them all before the first session lock (triggerSend).
func botSubjectLock(botID, subject string) string { return "bot-subject:" + botID + ":" + subject }

func botConversation(q *Request, b *botFull, subject string) (agentSession, error) {
	if err := acquireTxLock(q, botSubjectLock(b.Id, subject)); err != nil { // one conversation per (bot, subject)
		return agentSession{}, err
	}
	id, err := q.db().BotConversationBySubject(q.ctx(), db.BotConversationBySubjectParams{BotID: &b.Id, Subject: strPtr(subject)})
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
	// A wake over several subjects holds earlier subjects' session locks already (triggerSend); this session is new.
	if err := acquireNewObjectLock(q, "session:"+s.ID); err != nil {
		return agentSession{}, err
	}
	title, titleBy := b.Settings.Name, "bot"
	if subject != "" {
		if err := q.db().SessionSetSubject(q.ctx(), db.SessionSetSubjectParams{ID: s.ID, Subject: &subject}); err != nil {
			return agentSession{}, err
		}
		if title, err = subjectTitle(q, subject); err != nil {
			return agentSession{}, err
		}
		titleBy = "subject"
	}
	if _, err := q.db().TitleRename(q.ctx(), db.TitleRenameParams{ID: s.ID, Title: title, TitleBy: titleBy}); err != nil {
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

// prepareBotSessionForTurn rechecks bot authority and aligns the bot's conversation before queuing or launching a turn.
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
	workspaceSessionChanged := false
	if liveSession.row.Subject != nil {
		workspaceSession, err := q.db().BotNativeWorkspaceSession(q.ctx(), db.BotNativeWorkspaceSessionParams{
			SessionID: &liveSession.ID,
			NativeID:  deref(liveSession.NativeSessionID),
		})
		if err != nil {
			return err
		}
		workspaceSessionChanged = workspaceSession != "" && workspaceSession != liveSession.ID
	}
	if !projectChanged && !hostChanged && !workspaceChanged && !workspaceSessionChanged && !profileChanged && !accountChanged {
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
	case workspaceSessionChanged:
		source = "conversation workspace change"
	case workspaceChanged:
		source = "workspace change"
	}
	h, err := makeHandoff(q, &sess, turns, "bot_compatibility_handoff", source)
	if err != nil {
		return err
	}
	liveSession.NativeSessionID = nil
	delete(liveSession.rest, "nativeSessionId")
	if workspaceSessionChanged {
		liveSession.ForkNativeSessionID = ""
		delete(liveSession.rest, "forkNativeSessionId")
	}
	if projectChanged {
		delete(liveSession.rest, "nativeSessions")
	}
	if h != nil {
		liveSession.PendingHandoffID = h.ID
	}
	return dispatchSessionPut(q, liveSession)
}

func botRunRowToContract(r db.BotRunsListRow, linkedTaskIDs []string) contract.BotRunRecord {
	if linkedTaskIDs == nil {
		linkedTaskIDs = []string{}
	}
	var failure *contract.Failure
	if r.State == "failed" || r.State == "cancelled" {
		failure = contract.PlainCause(deref(r.Problem), r.Reason)
	}
	var automation *contract.BotAutomationRef
	if r.TriggerID != nil {
		automation = &contract.BotAutomationRef{Id: *r.TriggerID, Revision: int64(r.TriggerRevision), Name: r.TriggerName}
	}
	return contract.BotRunRecord{
		Id:             r.ID,
		BotId:          r.BotID,
		ProjectId:      r.ProjectID,
		BotRevision:    r.BotRevision,
		MemoryRevision: r.MemoryRevision,
		SessionId:      r.SessionID,
		ExecutionId:    r.ID,
		Cause:          contract.BotRunCause(r.Cause),
		State:          contract.BotRunState(r.State),
		Observation:    contract.BotRunObservation(r.Observation),
		Quiet:          r.Quiet,
		Reason:         optStr(r.Reason),
		SentBy:         optStr(r.SentBy),
		Source:         optStr(r.Source),
		HostId:         r.HostID,
		Problem:        r.Problem,
		Failure:        failure,
		CreatedAt:      r.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		StartedAt:      formatTimePtr(r.StartedAt),
		EndedAt:        formatTimePtr(r.EndedAt),
		LinkedTaskIds:  linkedTaskIDs,
		Automation:     automation,
	}
}

// readBotRun: the bot run of execution id (errNotFound when it is no run of a bot's conversation).
func readBotRun(q *Request, id string) (contract.BotRunRecord, error) {
	rows, err := q.db().BotRunsList(q.ctx(), db.BotRunsListParams{ID: &id, Lim: 1})
	if err != nil {
		return contract.BotRunRecord{}, err
	}
	if len(rows) == 0 {
		return contract.BotRunRecord{}, errNotFound
	}
	links, err := q.db().BotTasksByRunIDs(q.ctx(), []string{id})
	if err != nil {
		return contract.BotRunRecord{}, err
	}
	taskIDs := make([]string, 0, len(links))
	for _, l := range links {
		taskIDs = append(taskIDs, l.TaskID)
	}
	return botRunRowToContract(rows[0], taskIDs), nil
}

type botWakeReq struct {
	Cause      string
	DedupKey   string
	Text       string
	SentBy     string
	Source     string
	CheckPause bool
	Trigger    *db.Trigger
	Thread     string // the subject the wake belongs to (threads_bot.go changeThread); "" the bot's conversation without one
}

// admitBotWake: a wake's turn in the bot's conversation for its subject (Run now, a trigger). A busy conversation
// queues the message as its next turn (fleet-conversations.md#rules); a paused bot's trigger wake is logged in
// trigger_log and not sent. Reply: the run (nil when queued or skipped), the conversation, and whether the wake was
// sent (a run started or a message queued). A dedup key already used returns its run, not sent.
func admitBotWake(q *Request, b *botFull, req botWakeReq) (*contract.BotRunRecord, string, bool, error) {
	if req.DedupKey != "" {
		id, err := q.db().BotRunByRequest(q.ctx(), db.BotRunByRequestParams{BotID: &b.Id, RequestID: &req.DedupKey})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", false, err
		}
		if err == nil {
			rec, err := readBotRun(q, id)
			return &rec, deref(rec.SessionId), false, err
		}
	}
	text, triggerID := req.Text, ""
	if t := req.Trigger; t != nil {
		triggerID = t.ID
		text += fmt.Sprintf("\n\nTrigger %s (revision %d)", t.Name, t.Revision)
		if t.Text != "" {
			text += "\n" + t.Text
		}
	}
	if req.CheckPause && b.Paused {
		if req.Trigger == nil {
			return nil, "", false, nil
		}
		return nil, "", false, q.db().TriggerLogSkip(q.ctx(), db.TriggerLogSkipParams{TriggerID: triggerID,
			RequestID: firstString(req.DedupKey, newID()), ProjectID: &b.ProjectId, Reason: "paused"})
	}
	s, err := botConversation(q, b, req.Thread)
	if err != nil {
		return nil, "", false, err
	}
	if err := acquireTxLock(q, "session:"+s.ID); err != nil {
		return nil, "", false, err
	}
	if s, err = sessionLocked(q, s.ID); err != nil {
		return nil, "", false, err
	}
	rid := firstString(req.DedupKey, "bot:"+newID())
	// One turn at a time per conversation; a bot's conversations run in parallel (fleet-conversations.md#rules).
	activeExec, err := agentActive(q, s.ID)
	if err != nil {
		return nil, "", false, err
	}
	if activeExec != nil {
		_, err := agentSubmitFrom(q, &contract.MessageSendRequest{SessionId: &s.ID, RequestId: &rid, Text: &text}, req.Source, triggerID)
		return nil, s.ID, err == nil, err
	}
	if err := botAuthorityCheck(q, b); err != nil {
		return nil, "", false, err
	}
	if err := syncBotSessionCompatibility(q, &s, b); err != nil {
		return nil, "", false, err
	}
	e, err := agentEnqueue(q, &s, text, rid, nil, struct {
		BotContext *botExecutionContext `json:"botContext,omitempty"`
	}{botContextOf(b, req.Cause)}, "", &agentProvenance{SentBy: req.SentBy, Source: req.Source, TriggerID: triggerID})
	if err != nil {
		return nil, "", false, err
	}
	if q.app != nil {
		afterCommit(q, func() { q.app.wakeDispatch() })
	}
	rec, err := readBotRun(q, e.ID)
	return &rec, s.ID, true, err
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
		if saved.PresetID == youtrackPreset {
			return out, fail(409, "YouTrack triggers run only for verified YouTrack activity")
		}
		if dedupKey != "" {
			dedupKey = "trigger:" + saved.ID + ":" + dedupKey
		}
		trigger = &saved
	}
	subject := ""
	if trigger != nil {
		subject = "trigger:" + trigger.ID // a trigger's Run now: its own conversation, as its timed wakes
	}
	run, sessionID, admitted, err := admitBotWake(q, &b, botWakeReq{
		Trigger:  trigger,
		Cause:    "manual",
		DedupKey: dedupKey,
		Text:     text,
		SentBy:   director(q),
		Source:   "bot_run",
		Thread:   subject,
	})
	if err != nil {
		return out, err
	}
	fresh, err := readBot(q, b.Id)
	if err != nil {
		return out, err
	}
	return contract.BotRunReply{Bot: fresh, Run: run, SessionId: sessionID, Admitted: admitted}, nil
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
	execID, err := q.db().BotRunActive(q.ctx(), &b.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	stopRep, err := requestExecutionStop(q, execID, false)
	if err != nil {
		return out, err
	}
	if err := settleBotRun(q, execID); err != nil {
		return out, err
	}
	out.Stopped = true
	out.Stop = &stopRep.Stop
	rec, err := readBotRun(q, execID)
	if err != nil {
		return out, err
	}
	out.Run = &rec
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
		if _, err := readBotRun(q, parts[1]); err != nil {
			if errors.Is(err, errNotFound) {
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
			linkedByRun[l.ExecutionID] = append(linkedByRun[l.ExecutionID], l.TaskID)
		}
	}
	for _, row := range rows {
		if q.person != nil && !mayAccessProjectID(q.person, row.ProjectID) {
			continue
		}
		out.Runs = append(out.Runs, botRunRowToContract(row, linkedByRun[row.ID]))
	}
	if out.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next := fmt.Sprintf("%d:%s", last.Seq, last.ID)
		out.NextAfter = &next
	}
	return out, nil
}

func linkBotTask(q *Request, botID, taskID, projectID, kind, createdBy string, sessionID, executionID *string) error {
	if botID == "" || taskID == "" {
		return nil
	}
	return q.db().BotTaskUpsert(q.ctx(), db.BotTaskUpsertParams{
		BotID:       botID,
		TaskID:      taskID,
		ProjectID:   projectID,
		LinkKind:    kind,
		CreatedBy:   createdBy,
		SessionID:   sessionID,
		ExecutionID: executionID,
	})
}

func botTaskLinkToContract(botID, botName, taskID, projectID string, sessionID, execID *string, kind, createdBy string, createdAt time.Time) contract.BotTaskLink {
	return contract.BotTaskLink{
		BotId:       botID,
		BotName:     botName,
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
	var sessionID, execID *string
	if id := deref(r.ExecutionId); id != "" {
		run, err := readBotRun(q, id)
		if err != nil {
			return out, err
		}
		if run.BotId != b.Id {
			return out, fail(400, "Bot run does not belong to bot")
		}
		sessionID, execID = run.SessionId, &run.Id
	} else if callerExec := strings.TrimPrefix(q.agent, "execution:"); callerExec != "" && q.agent != callerExec {
		if run, err := readBotRun(q, callerExec); err == nil && run.BotId == b.Id {
			sessionID, execID = run.SessionId, &run.Id
		} else if err != nil && !errors.Is(err, errNotFound) {
			return out, err
		}
	}
	if err := linkBotTask(q, b.Id, t.ID, b.ProjectId, kind, actor(q, director(q)), sessionID, execID); err != nil {
		return out, err
	}
	row, err := q.db().BotTaskGet(q.ctx(), db.BotTaskGetParams{BotID: b.Id, TaskID: t.ID})
	if err != nil {
		return out, err
	}
	out.Link = botTaskLinkToContract(row.BotID, row.BotName, row.TaskID, row.ProjectID, row.SessionID, row.ExecutionID, row.LinkKind, row.CreatedBy, row.CreatedAt)
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
