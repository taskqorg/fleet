// Status answers, commands, Needs you questions and milestones of the Telegram channel (FLT-34,
// docs/designs/fleet-telegram.md): everything here is read from Fleet state, never from a worker turn.
//
//   - "So?", "??", /status: the conductor answers where a room's work stands (telegramFacts) without waking its session.
//   - /now, /needs, /status: commands of the registry (telegram_commands.go) without a model.
//   - telegram-watch (everyWake): open questions of a work item's or a home conversation go to its chat with buttons;
//     answered or withdrawn ones are edited to say by whom; delivery and task milestones go to the work item's room once.
//   - A tap on a question's button or a Telegram reply to it answers through questions_answer as the person.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		everyWake(ctx, "telegram-watch", 5*time.Second, nil, false, func(ctx context.Context, _ bool) error { return telegramWatch(ctx, a) })
	})
}

// --- facts ---

// telegramFacts: where a conversation stands, one short line each: its newest run, open questions, its task and the
// delivery of its newest pull request. run is the run line; rest the others.
func telegramFacts(q *Request, sessionID, lang string, now time.Time) (run string, rest []string, err error) {
	r, err := q.db().TelegramSessionRun(q.ctx(), &sessionID)
	if err != nil {
		return "", nil, err
	}
	ago := ""
	if r.RunAt.Valid {
		ago = sinceWords(lang, now.Sub(r.RunAt.Time))
	}
	switch r.RunState {
	case "":
		run = say(lang, "runNone", "")
	case "queued", "preparing", "running", "starting":
		run = say(lang, "runWorking", ago)
	case "waiting":
		run = say(lang, "runWaiting", ago)
	case "succeeded":
		run = say(lang, "runDone", ago)
	case "failed":
		run = say(lang, "runFailedAgo", ago)
	default:
		run = say(lang, "runStopped", ago)
	}
	if r.OpenQuestions > 0 {
		rest = append(rest, say(lang, "openQuestions", strconv.FormatInt(r.OpenQuestions, 10)))
	}
	if s, err := sessionMaybe(q, sessionID); err != nil {
		return "", nil, err
	} else if s != nil {
		tasks, err := sessionTasks(q, *s, nil, nil)
		if err != nil {
			return "", nil, err
		}
		for i, t := range tasks {
			if i == 2 {
				break
			}
			rest = append(rest, say(lang, "taskLine", taskRef(t.Code, t.Id)+": "+clip(t.Title, 60)+" · "+stateWords(lang, t.State)))
		}
	}
	if pr, stage := telegramDelivery(q, r.ExecutionIds); pr != nil {
		rest = append(rest, say(lang, "prLine", fmt.Sprintf("#%d", pr.Number)+" · "+stageWords(lang, stage)))
	}
	return run, rest, nil
}

func taskRef(code *string, id string) string {
	if deref(code) != "" {
		return *code
	}
	return clip(id, 8)
}

// telegramDelivery: the newest pull request the conversation's recent runs published, and its stage.
func telegramDelivery(q *Request, executions []string) (*contract.PublishingHostRecord, contract.ThreadDeliveryStage) {
	if len(executions) == 0 {
		return nil, ""
	}
	rs, err := q.db().ThreadPublishingResults(q.ctx(), db.ThreadPublishingResultsParams{Ids: executions, Changed: []string{}})
	if err != nil {
		return nil, ""
	}
	order := map[string]int{}
	for i, id := range executions {
		order[id] = i
	}
	best, bestAt := -1, len(executions)
	for i, r := range rs {
		if n, ok := order[r.ExecutionID]; ok && n < bestAt {
			best, bestAt = i, n
		}
	}
	if best < 0 {
		return nil, ""
	}
	var rec contract.PublishingHostRecord
	if json.Unmarshal(rs[best].Value, &rec) != nil || rec.Number == 0 {
		return nil, ""
	}
	stage, err := threadDeliveryStage(rs[best].Value)
	if err != nil || stage == nil {
		return &rec, contract.ThreadDeliveryStageUnknown
	}
	return &rec, *stage
}

func sinceWords(lang string, d time.Duration) string {
	switch {
	case d < time.Minute:
		return say(lang, "justNow", "")
	case d < time.Hour:
		return say(lang, "minAgo", strconv.Itoa(int(d/time.Minute)))
	case d < 48*time.Hour:
		return say(lang, "hAgo", strconv.Itoa(int(d/time.Hour)))
	}
	return say(lang, "dAgo", strconv.Itoa(int(d/(24*time.Hour))))
}

func stateWords(lang, state string) string {
	if w := say(lang, "task_"+state, ""); w != "" {
		return w
	}
	return state
}

func stageWords(lang string, s contract.ThreadDeliveryStage) string {
	if w := say(lang, "stage_"+string(s), ""); w != "" {
		return w
	}
	return string(s)
}

// telegramWorkText: a work item's title in bold and its facts; first replaces the run line when not empty.
func telegramWorkText(q *Request, w db.TelegramWork, lang, first string, now time.Time) (string, error) {
	run, rest, err := telegramFacts(q, w.SessionID, lang, now)
	if err != nil {
		return "", err
	}
	if first != "" {
		run = first
	}
	return "**" + workLabel(q, w) + "**\n" + strings.Join(append([]string{run}, rest...), "\n"), nil
}

// workLabel: the conversation's own short title once it has one, else the work item's first line (owner-tester F7).
func workLabel(q *Request, w db.TelegramWork) string {
	if t, err := q.db().TelegramSessionTitle(q.ctx(), w.SessionID); err == nil && strings.TrimSpace(t) != "" {
		return clip(strings.TrimSpace(t), 60)
	}
	return w.Title
}

// --- "So?" and commands ---

var statusPing = regexp.MustCompile(`^(so+|and|well|news|any news|status|ну+|и|что там|как там|как дела|статус|новости|is it live|is it done|so all is live|so it is live|все\s+live|уже\s+live)$`)

// isStatusPing: a short message that only asks how things stand ("So?", "??", "ну?").
func isStatusPing(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if len([]rune(t)) > 30 {
		return false
	}
	core := strings.TrimSpace(strings.TrimRight(t, "?!. …"))
	if core == "" {
		return strings.Contains(t, "?")
	}
	return statusPing.MatchString(core)
}

// telegramStatusAnswer answers where a room's work stands: one work item (a reply target) or every open one.
func telegramStatusAnswer(q *Request, now time.Time, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, only *db.TelegramWork) error {
	works := []db.TelegramWork{}
	if only != nil {
		works = append(works, *only)
	} else {
		open, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
		if err != nil {
			return err
		}
		works = open
	}
	parts := []string{}
	for i, w := range works {
		if i == 4 {
			break
		}
		t, err := telegramWorkText(q, w, tp.Language, "", now)
		if err != nil {
			return err
		}
		parts = append(parts, t)
	}
	body := strings.Join(parts, "\n\n")
	if body == "" {
		body = say(tp.Language, "noWork", "")
	}
	return telegramOutbox(q, db.TelegramOutboxAddParams{ID: "so:" + row.ID, ChatID: room.ChatID, ThreadID: room.ThreadID, Kind: "text", ReplyTo: &m.MessageID, Text: body})
}

// telegramGlance (/now): in a group room its open work; in a private chat the room's current conversation and the work
// items the person opened elsewhere.
func telegramGlance(q *Request, now time.Time, room db.TelegramRoom, tp db.TelegramPerson) (string, error) {
	works, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
	if err != nil {
		return "", err
	}
	parts := []string{}
	if room.Kind == "private" {
		if len(works) > 0 {
			run, rest, err := telegramFacts(q, works[0].SessionID, tp.Language, now)
			if err != nil {
				return "", err
			}
			parts = append(parts, "**"+say(tp.Language, "home", "")+"**\n"+strings.Join(append([]string{run}, rest...), "\n"))
		}
		all, err := q.db().TelegramWorkList(q.ctx())
		if err != nil {
			return "", err
		}
		works = works[:0]
		for _, w := range all {
			if w.CreatedBy == tp.Email && w.RoomID != room.ID {
				works = append(works, w)
			}
		}
	}
	// What runs or waits leads; finished work is one line (owner-tester F10).
	done := []string{}
	for _, w := range works {
		r, err := q.db().TelegramSessionRun(q.ctx(), &w.SessionID)
		if err != nil {
			return "", err
		}
		if r.RunState != "running" && r.RunState != "preparing" && r.RunState != "waiting" && r.RunState != "queued" && r.OpenQuestions == 0 {
			done = append(done, workLabel(q, w))
			continue
		}
		if len(parts) == 6 {
			continue
		}
		t, err := telegramWorkText(q, w, tp.Language, "", now)
		if err != nil {
			return "", err
		}
		parts = append(parts, t)
	}
	if len(done) > 0 {
		parts = append(parts, say(tp.Language, "finished", strconv.Itoa(len(done)))+": "+clip(strings.Join(done, "; "), 300))
	}
	if len(parts) == 0 {
		return say(tp.Language, "noWork", ""), nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// telegramTaskStatus (/status <task>): a task the person may see, by code or id.
func telegramTaskStatus(q *Request, tp db.TelegramPerson, ref string) (string, error) {
	p := q.app.peopleSnapshot().person(tp.Email)
	if p == nil {
		return say(tp.Language, "noTask", ref), nil
	}
	id, err := q.db().TelegramTaskByRef(q.ctx(), ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return say(tp.Language, "noTask", ref), nil
	}
	if err != nil {
		return "", err
	}
	if !mayAccessProjectID(p, deref(id.ProjectID)) {
		return say(tp.Language, "noTask", ref), nil
	}
	line := say(tp.Language, "taskLine", taskRef(id.Code, id.ID)+": "+clip(id.Title, 80)+" · "+stateWords(tp.Language, id.State))
	if id.WaitReason != nil && *id.WaitReason != "" {
		line += "\n" + clip(*id.WaitReason, 200)
	}
	return line, nil
}

// --- questions ---

// tgChoices: a question's answer options when they are a list of strings (options: [..] or {choices: [..]}).
func tgChoices(options []byte) []string {
	var list []string
	if json.Unmarshal(options, &list) == nil {
		return list
	}
	var obj struct {
		Choices []string `json:"choices"`
	}
	_ = json.Unmarshal(options, &obj)
	return obj.Choices
}

// telegramQuestionMessage queues one question with its buttons. A key request only links to Fleet: keys are never
// typed in chat.
// telegramQuestionBody: a question as Telegram shows it. An approval card is in the reader's language: the scope as the
// agent wrote it, Fleet's labels translated; the edit after an answer keeps it so (owner-tester F13b).
func telegramQuestionBody(q *Request, questionID, text, lang string) (string, error) {
	a, err := q.db().ApprovalOf(q.ctx(), questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "❓ " + text, nil
	}
	if err != nil {
		return "", err
	}
	var sc approvalScope
	_ = json.Unmarshal(a.Approval, &sc)
	body := "⚠️ " + say(lang, "approvalNeeded", sc.What) + "\n" + say(lang, "approvalWhere", sc.Where)
	if sc.Version != "" {
		body += "\n" + say(lang, "approvalVersion", sc.Version)
	}
	if sc.Rollback != "" {
		body += "\n" + say(lang, "approvalRollback", sc.Rollback)
	}
	return body + "\n" + say(lang, "approvalWaiting", strings.Join(approverNames(approvers(q, a.ProjectID, sc.Action)), ", ")), nil
}

func telegramQuestionMessage(q *Request, id, questionID, kind, text, lang string, chat, thread int64, workID, sessionID string, replyTo *int64) error {
	rows := [][]map[string]string{}
	body, err := telegramQuestionBody(q, questionID, text, lang)
	if err != nil {
		return err
	}
	if kind == "secret" {
		body = "🔑 " + text + "\n" + say(lang, "keyInFleet", "")
	}
	if kind != "secret" {
		row := []map[string]string{}
		qr, err := q.db().TelegramQuestionGet(q.ctx(), questionID)
		if err != nil {
			return err
		}
		for i, c := range tgChoices(qr.Options) {
			if i == 6 {
				break
			}
			label := c
			if c == approvalYes || c == approvalNo {
				label = say(lang, "btn"+strings.ReplaceAll(c, " ", ""), "")
			}
			row = append(row, map[string]string{"text": clip(label, 30), "callback_data": "q:" + questionID + ":" + strconv.Itoa(i)})
			if len(row) == 2 {
				rows, row = append(rows, row), []map[string]string{}
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
		body += "\n" + say(lang, "replyToAnswer", "")
	}
	if link := conversationLink(sessionID); link != "" && sessionID != "" {
		rows = append(rows, []map[string]string{{"text": say(lang, "open", ""), "url": link}})
	}
	p := db.TelegramOutboxAddParams{ID: id, ChatID: chat, ThreadID: thread, Kind: "text", Text: body, ReplyTo: replyTo}
	if workID != "" {
		p.WorkID = &workID
	}
	if sessionID != "" {
		p.SessionID = &sessionID
	}
	if len(rows) > 0 {
		p.Payload = jsonBytes(tgMarkup{InlineKeyboard: rows})
	}
	return telegramOutbox(q, p)
}

// telegramNeeds (/needs): the open questions of the room's work (a group) or of the person's home conversation and
// work items (a private chat), each as its own message that a reply or a tap answers.
func telegramNeeds(q *Request, row *db.TelegramUpdate, room db.TelegramRoom, tp db.TelegramPerson) (int, error) {
	if room.Kind == "private" {
		// A private chat shows the person's whole Needs you: every open question of their projects (owner-tester F9).
		p := q.app.peopleSnapshot().person(tp.Email)
		all, err := q.db().TelegramNeedsAll(q.ctx())
		if err != nil {
			return 0, err
		}
		n := 0
		for _, x := range all {
			if n == 10 || p == nil {
				break
			}
			if x.ProjectID == "" && !fullAccess(p) || x.ProjectID != "" && !mayAccessProjectID(p, x.ProjectID) {
				continue
			}
			if err := telegramQuestionMessage(q, "qn:"+x.ID+":"+row.ID, x.ID, x.Kind, x.Text, tp.Language, room.ChatID, 0, "", deref(x.SessionID), nil); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	}
	qs, err := q.db().TelegramOpenQuestions(q.ctx(), db.TelegramOpenQuestionsParams{RoomID: room.ID, Email: tp.Email, Private: false})
	if err != nil {
		return 0, err
	}
	for _, x := range qs {
		if err := telegramQuestionMessage(q, "qn:"+x.ID+":"+row.ID, x.ID, x.Kind, x.Text, tp.Language, room.ChatID, room.ThreadID, "", deref(x.SessionID), nil); err != nil {
			return 0, err
		}
	}
	return len(qs), nil
}

// telegramAnswer answers a question as the person who tapped or replied, through question_answer in their scope.
// tgAnswerAt: where an answer was given in Telegram, so the run the reply starts answers there too.
type tgAnswerAt struct {
	chat, thread, message int64
	roomID, email         string
}

func telegramAnswer(q *Request, c *kitClaim, p *Person, questionID, answer string, at tgAnswerAt) {
	a := q.app
	c.Call = func(ctx context.Context) any {
		var out tgDelivered
		// Chat never carries a key, and a person answers only within their projects.
		var qr db.TelegramQuestionGetRow
		if err := a.store.Read(ctx, nil, func(rq *Request) (err error) { qr, err = rq.db().TelegramQuestionGet(rq.ctx(), questionID); return err }); err != nil {
			out.err = err
			return out
		}
		if qr.Kind == "secret" {
			out.err = fail(403, "keys are added in Fleet, never in chat")
			return out
		}
		if !mayAccessProjectID(p, deref(qr.ProjectID)) && deref(qr.ProjectID) != "" {
			out.err = fail(403, "no access to project "+deref(qr.ProjectID))
			return out
		}
		// An approval card takes Approve or Not now; free text maps onto them ("go live", "not now").
		if isApprovalCard(qr.Options) && answer != approvalYes && answer != approvalNo {
			switch {
			case isApprovalText(answer):
				answer = approvalYes
			case isDeclineText(answer):
				answer = approvalNo
			default:
				out.err = fail(400, "tap Approve or Not now")
				return out
			}
		}
		pq := scopedRequest(ctx, a, p)
		out.err = a.store.Write(ctx, pq, []string{GLOBAL}, func(pq *Request) error {
			if err := refreshRequestAccess(pq); err != nil {
				return err
			}
			pq.body = jsonBytes(contract.QuestionsAnswerRequest{QuestionId: questionID, Answer: answer})
			if _, err := operations["questions_answer"].h(pq); err != nil { // as the web app: an agent's question gets the reply delivered
				return err
			}
			if qr.SessionID == nil || qr.Kind != "agent_question" {
				return nil
			}
			// The reply starts (or joins) a run of the asking session: its answer comes back where the person answered,
			// as for any message they send (owner-tester F12).
			var work *string
			if w, err := pq.db().TelegramWorkBySession(pq.ctx(), db.TelegramWorkBySessionParams{RoomID: at.roomID, SessionID: *qr.SessionID}); err == nil {
				work = &w.ID
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := pq.db().TelegramTurnAdd(pq.ctx(), db.TelegramTurnAddParams{ID: "ans:" + questionID, SessionID: *qr.SessionID, RequestID: "reply:" + questionID,
				WorkID: work, ChatID: at.chat, ThreadID: at.thread, MessageID: at.message, Email: at.email}); err != nil {
				return err
			}
			return kitNotify(pq, "telegram-reply")
		})
		return out
	}
}

// telegramWatch: one pass of questions and milestones.
func telegramWatch(ctx context.Context, a *App) error {
	if ready, err := telegramReady(ctx, a); err != nil || !ready {
		return err
	}
	return a.store.Write(ctx, nil, nil, func(q *Request) error {
		now := time.Now()
		push, err := q.db().TelegramQuestionsToPush(q.ctx())
		if err != nil {
			return err
		}
		for _, x := range push {
			lang := ""
			if w, err := q.db().TelegramWorkGet(q.ctx(), x.WorkID); err == nil {
				if tp, err := q.db().TelegramPersonGet(q.ctx(), w.CreatedBy); err == nil {
					lang = tp.Language
				}
			}
			if err := telegramQuestionMessage(q, "q:"+x.ID, x.ID, x.Kind, x.Text, lang, x.ChatID, x.ThreadID, x.WorkID, x.SessionID, nil); err != nil {
				return err
			}
		}
		ended, err := q.db().TelegramQuestionsEnded(q.ctx())
		if err != nil {
			return err
		}
		for _, x := range ended {
			// The answerer reads the edit first: their language; a withdrawn question keeps the default.
			lang := ""
			if tp, err := q.db().TelegramPersonGet(q.ctx(), x.AnsweredBy); err == nil {
				lang = tp.Language
			}
			body, err := telegramQuestionBody(q, x.ID, x.Text, lang)
			if err != nil {
				return err
			}
			line := "— " + say(lang, "withdrawn", "")
			if x.State == "answered" {
				who := x.AnsweredBy
				if pp := q.app.peopleSnapshot().person(who); pp != nil && pp.Name != "" {
					who = pp.Name
				}
				answer := x.Answer
				if answer == approvalYes || answer == approvalNo {
					answer = say(lang, "btn"+strings.ReplaceAll(answer, " ", ""), "")
				}
				line = "✓ " + who + ": " + clip(answer, 300)
			}
			if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "qend:" + x.ID, ChatID: x.ChatID, Kind: "edit", EditMessageID: x.TelegramMessageID,
				Text: body + "\n" + line}); err != nil {
				return err
			}
		}
		if err := telegramMilestones(q, now); err != nil {
			return err
		}
		return telegramOverlaps(q)
	})
}

// telegramOverlaps: when another Fleet conversation (any host) runs on a work item's task, the room hears once, with
// [Join it] (later messages go to that conversation) and [Work separately].
func telegramOverlaps(q *Request) error {
	works, err := q.db().TelegramWorkActive(q.ctx())
	if err != nil {
		return err
	}
	for _, w := range works {
		s, err := sessionMaybe(q, w.SessionID)
		if err != nil {
			return err
		}
		if s == nil {
			continue
		}
		tasks, err := sessionTasks(q, *s, nil, nil)
		if err != nil {
			return err
		}
		ids := []string{}
		for _, t := range tasks {
			ids = append(ids, t.Id)
		}
		if len(ids) == 0 {
			continue
		}
		others, err := q.db().TelegramOverlaps(q.ctx(), db.TelegramOverlapsParams{TaskIds: ids, SessionID: &w.SessionID})
		if err != nil {
			return err
		}
		lang := ""
		if tp, err := q.db().TelegramPersonGet(q.ctx(), w.CreatedBy); err == nil {
			lang = tp.Language
		}
		room, err := q.db().TelegramRoomGet(q.ctx(), w.RoomID)
		if err != nil {
			return err
		}
		for _, o := range others {
			// callback_data holds at most 64 bytes: the outbox id carries the pair, the buttons point at it.
			id := "overlap:" + w.ID + ":" + o.SessionID
			title := o.Title
			if title == "" {
				title = clip(o.SessionID, 8)
			}
			where := ""
			if o.HostID != "" {
				where = " · " + o.HostID
			}
			key := clip(o.SessionID, 36)
			rows := [][]map[string]string{{{"text": say(lang, "join", ""), "callback_data": "j:" + clip(w.ID, 24) + ":" + key},
				{"text": say(lang, "separate", ""), "callback_data": "sep:" + clip(w.ID, 20) + ":" + key}}}
			if link := conversationLink(o.SessionID); link != "" {
				rows = append(rows, []map[string]string{{"text": say(lang, "open", ""), "url": link}})
			}
			if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: id, ChatID: room.ChatID, ThreadID: room.ThreadID, Kind: "text",
				Text: "**" + w.Title + "**\n" + say(lang, "overlap", title+where), WorkID: &w.ID, SessionID: &w.SessionID,
				Payload: jsonBytes(tgMarkup{InlineKeyboard: rows})}); err != nil {
				return err
			}
		}
	}
	return nil
}

// telegramOverlapTap: [Join it] moves the work item to the other conversation; [Work separately] keeps it. Either
// edits the notice; only a linked person who may see the project counts.
func telegramOverlapTap(q *Request, row *db.TelegramUpdate, cb *tgCallback, join bool, workPrefix, session string) error {
	tp, err := q.db().TelegramPersonByUser(q.ctx(), cb.From.ID)
	if err != nil || cb.Message == nil {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	o, err := q.db().TelegramOutboxBySent(q.ctx(), db.TelegramOutboxBySentParams{ChatID: cb.Message.Chat.ID, TelegramMessageID: &cb.Message.MessageID})
	if err != nil || o.WorkID == nil || !strings.HasPrefix(o.ID, "overlap:") || !strings.HasPrefix(*o.WorkID, workPrefix) {
		return telegramDone(q, row.ID, "tap-stale", "")
	}
	other := strings.TrimPrefix(o.ID, "overlap:"+*o.WorkID+":")
	if !strings.HasPrefix(other, session) {
		return telegramDone(q, row.ID, "tap-stale", "")
	}
	w, err := q.db().TelegramWorkGet(q.ctx(), *o.WorkID)
	if err != nil {
		return telegramDone(q, row.ID, "tap-stale", "")
	}
	p := q.app.peopleSnapshot().person(tp.Email)
	room, err := q.db().TelegramRoomGet(q.ctx(), w.RoomID)
	if err != nil {
		return err
	}
	if p == nil || (room.ProjectHint != "" && !mayAccessProjectID(p, room.ProjectHint)) {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	line := say(tp.Language, "separately", "")
	if join {
		if _, err := q.db().TelegramWorkJoin(q.ctx(), db.TelegramWorkJoinParams{ID: w.ID, SessionID: other}); err != nil {
			return err
		}
		line = say(tp.Language, "joined", "")
	}
	if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "overlapped:" + o.ID, ChatID: o.ChatID, Kind: "edit", EditMessageID: o.TelegramMessageID,
		Text: o.Text + "\n" + line + " (" + personName(p) + ")"}); err != nil {
		return err
	}
	return telegramDone(q, row.ID, "tap", w.ID)
}

// telegramMilestones: a work item's room hears once when its pull request opens, fails its checks or merges, and when
// its task completes or fails; the status message follows.
func telegramMilestones(q *Request, now time.Time) error {
	works, err := q.db().TelegramWorkActive(q.ctx())
	if err != nil {
		return err
	}
	for _, w := range works {
		lang := ""
		if tp, err := q.db().TelegramPersonGet(q.ctx(), w.CreatedBy); err == nil {
			lang = tp.Language
		}
		events := []struct{ key, text string }{}
		r, err := q.db().TelegramSessionRun(q.ctx(), &w.SessionID)
		if err != nil {
			return err
		}
		if pr, stage := telegramDelivery(q, r.ExecutionIds); pr != nil {
			switch stage {
			case contract.ThreadDeliveryStagePrOpen, contract.ThreadDeliveryStageChecksFailed, contract.ThreadDeliveryStageMerged:
				events = append(events, struct{ key, text string }{fmt.Sprintf("pr:%d:%s", pr.Number, stage),
					say(lang, "prLine", fmt.Sprintf("[#%d](%s)", pr.Number, pr.Url)+" · "+stageWords(lang, stage))})
			}
		}
		if s, err := sessionMaybe(q, w.SessionID); err != nil {
			return err
		} else if s != nil && s.TaskID != "" {
			if t, err := q.db().TelegramTaskByRef(q.ctx(), s.TaskID); err == nil && (t.State == "completed" || t.State == "failed") {
				events = append(events, struct{ key, text string }{"task:" + t.ID + ":" + t.State,
					say(lang, "taskLine", taskRef(t.Code, t.ID)+" · "+stateWords(lang, t.State))})
			}
		}
		for _, e := range events {
			id := "ms:" + w.ID + ":" + e.key
			if _, err := q.db().TelegramOutboxGet(q.ctx(), id); err == nil {
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			room, err := q.db().TelegramRoomGet(q.ctx(), w.RoomID)
			if err != nil {
				return err
			}
			if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: id, ChatID: room.ChatID, ThreadID: room.ThreadID, Kind: "text",
				Text: "**" + w.Title + "**\n" + e.text, WorkID: &w.ID, SessionID: &w.SessionID}); err != nil {
				return err
			}
			if err := telegramWorkStatus(q, w.ID, "ms:"+e.key, lang, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// telegramQuestionTap: a tap on a question's option answers it as the tapping person; Fleet decides whether they may.
func telegramQuestionTap(q *Request, c *kitClaim, row *db.TelegramUpdate, cb *tgCallback, questionID, n string) error {
	tp, err := q.db().TelegramPersonByUser(q.ctx(), cb.From.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	if err != nil {
		return err
	}
	p := q.app.peopleSnapshot().person(tp.Email)
	if p == nil {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	if res, ok := c.Result.(tgDelivered); ok {
		if res.err != nil && cb.Message != nil {
			room := db.TelegramRoom{ChatID: cb.Message.Chat.ID, ThreadID: cb.Message.thread()}
			if err := telegramNotice(q, room, cb.Message.MessageID, "fail:"+row.ID, tp.Language, "notDelivered", plainError(res.err)); err != nil {
				return err
			}
			return telegramDone(q, row.ID, "failed", "")
		}
		return telegramDone(q, row.ID, "answered", "")
	}
	qr, err := q.db().TelegramQuestionGet(q.ctx(), questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return telegramDone(q, row.ID, "tap-stale", "")
	}
	if err != nil {
		return err
	}
	choices := tgChoices(qr.Options)
	i, err := strconv.Atoi(n)
	if qr.State != "open" || err != nil || i < 0 || i >= len(choices) {
		return telegramDone(q, row.ID, "tap-stale", "")
	}
	at := tgAnswerAt{email: tp.Email}
	if cb.Message != nil {
		at = tgAnswerAt{chat: cb.Message.Chat.ID, thread: cb.Message.thread(), message: cb.Message.MessageID,
			roomID: telegramRoomID(cb.Message.Chat.ID, cb.Message.thread()), email: tp.Email}
	}
	telegramAnswer(q, c, p, questionID, choices[i], at)
	return nil
}

func init() {
	for lang, words := range map[string]map[string]string{
		"en": {
			"runNone":       "No run yet",
			"runWorking":    "⏳ Working, started %s",
			"runWaiting":    "❓ Waiting for an answer (%s)",
			"runDone":       "✓ Last run done %s",
			"runFailedAgo":  "✗ Last run failed %s",
			"runStopped":    "Stopped %s",
			"openQuestions": "❓ Open questions: %s",
			"taskLine":      "Task %s",
			"prLine":        "PR %s",
			"justNow":       "just now",
			"minAgo":        "%s min ago",
			"hAgo":          "%s h ago",
			"dAgo":          "%s d ago",
			"task_queued":   "queued", "task_in_progress": "in progress", "task_waiting": "waiting for an answer", "task_held": "held",
			"task_paused": "paused", "task_completed": "completed", "task_failed": "failed", "task_cancelled": "cancelled",
			"stage_pr_open": "open", "stage_checks": "checks running", "stage_checks_failed": "checks failed",
			"stage_merging": "merging", "stage_merged": "merged", "stage_unknown": "state unknown",
			"noNeeds":        "No open questions here.",
			"noWork":         "No open work here.",
			"home":           "Your conversation",
			"noTask":         "No task %s that you can see.",
			"keyInFleet":     "Keys are never typed in chat: add it in Fleet (Open in Fleet).",
			"replyToAnswer":  "Reply to this message to answer.",
			"withdrawn":      "withdrawn",
			"approvalNeeded": "Approval needed: %s", "approvalWhere": "Where: %s", "approvalVersion": "Version: %s",
			"approvalRollback": "Rollback: %s", "approvalWaiting": "Waiting for: %s", "btnApprove": "Approve", "btnNotnow": "Not now",
			"finished":   "Finished (%s)",
			"overlap":    "Another Fleet conversation works on the same task: %s",
			"join":       "Join it",
			"separate":   "Work separately",
			"joined":     "Joined: messages go to that conversation now",
			"separately": "Working separately",
		},
		"ru": {
			"runNone":       "Запусков ещё не было",
			"runWorking":    "⏳ В работе, начато %s",
			"runWaiting":    "❓ Ждёт ответа (%s)",
			"runDone":       "✓ Последний запуск завершён %s",
			"runFailedAgo":  "✗ Последний запуск упал %s",
			"runStopped":    "Остановлено %s",
			"openQuestions": "❓ Открытых вопросов: %s",
			"taskLine":      "Задача %s",
			"prLine":        "PR %s",
			"justNow":       "только что",
			"minAgo":        "%s мин назад",
			"hAgo":          "%s ч назад",
			"dAgo":          "%s дн назад",
			"task_queued":   "в очереди", "task_in_progress": "в работе", "task_waiting": "ждёт ответа", "task_held": "отложена",
			"task_paused": "на паузе", "task_completed": "готова", "task_failed": "упала", "task_cancelled": "отменена",
			"stage_pr_open": "открыт", "stage_checks": "идут проверки", "stage_checks_failed": "проверки упали",
			"stage_merging": "вливается", "stage_merged": "влит", "stage_unknown": "состояние неизвестно",
			"noNeeds":        "Открытых вопросов здесь нет.",
			"noWork":         "Открытой работы здесь нет.",
			"home":           "Ваш разговор",
			"noTask":         "Задачи %s среди доступных вам нет.",
			"keyInFleet":     "Ключи не вводятся в чат: добавьте его во Fleet (Открыть во Fleet).",
			"replyToAnswer":  "Ответьте на это сообщение, чтобы ответить.",
			"withdrawn":      "отозван",
			"approvalNeeded": "Нужно согласие: %s", "approvalWhere": "Где: %s", "approvalVersion": "Версия: %s",
			"approvalRollback": "Откат: %s", "approvalWaiting": "Ждёт: %s", "btnApprove": "Одобрить", "btnNotnow": "Не сейчас",
			"finished":   "Завершено (%s)",
			"overlap":    "Над той же задачей работает другой разговор Fleet: %s",
			"join":       "Присоединиться",
			"separate":   "Работать отдельно",
			"joined":     "Присоединено: сообщения теперь идут в тот разговор",
			"separately": "Работаем отдельно",
		},
	} {
		for k, v := range words {
			telegramWords[lang][k] = v
		}
	}
}

// isApprovalCard: a question whose options are exactly the approval card's.
func isApprovalCard(options []byte) bool {
	c := tgChoices(options)
	return len(c) == 2 && c[0] == approvalYes && c[1] == approvalNo
}

var (
	approvalWords = regexp.MustCompile(`go live|live it|can live|i allow|approve|ship it|release it|deploy it|выкатыв|одобр|разреш`)
	approvalShort = regexp.MustCompile(`^(yes|y|ok|okay|go|go ahead|да|ок|давай|можно|согласен|поехали)$`)
	negation      = regexp.MustCompile(`\bnot\b|n't|\bdont\b|\bwait\b|(^|\s)(не|нельзя|рано)(\s|$)`)
	declineWords  = regexp.MustCompile(`^(no|not now|nope|stop|wait|cancel|нет|не сейчас|стоп|подожди|отмена)$`)
)

func approvalCore(text string) string {
	return strings.TrimSpace(strings.TrimRight(strings.ToLower(strings.TrimSpace(text)), "!.?,… "))
}

// isApprovalText: free text that approves ("good u can live", "Go live", "live it, works", "давай").
func isApprovalText(text string) bool {
	t := approvalCore(text)
	if len([]rune(t)) > 60 || declineWords.MatchString(t) || negation.MatchString(t) {
		return false
	}
	return approvalShort.MatchString(t) || approvalWords.MatchString(t)
}

func isDeclineText(text string) bool { return declineWords.MatchString(approvalCore(text)) }

// telegramApprovalText: free text that approves, while approval cards are open in the room's work (a private chat's is
// its current conversation). One card: answered as the author (Fleet checks they may). More: each is listed with its
// buttons to choose. None: false, the message is routed as usual.
func telegramApprovalText(q *Request, c *kitClaim, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, p *Person) (bool, error) {
	// Only explicit approval words count here; a bare "ok" or "yes" may be about any work, and approves only as a
	// Telegram reply to the card itself (telegramAnswer).
	if !isApprovalText(messageText(m)) || !approvalWords.MatchString(approvalCore(messageText(m))) {
		return false, nil
	}
	sessions := []string{}
	open, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
	if err != nil {
		return false, err
	}
	for _, w := range open {
		sessions = append(sessions, w.SessionID)
	}
	cards := []string{}
	for _, s := range sessions {
		ids, err := q.db().ApprovalOpenForSession(q.ctx(), &s)
		if err != nil {
			return false, err
		}
		cards = append(cards, ids...)
	}
	switch len(cards) {
	case 0:
		return false, nil
	case 1:
		telegramAnswer(q, c, p, cards[0], approvalYes, tgAnswerAt{chat: m.Chat.ID, thread: m.thread(), message: m.MessageID, roomID: room.ID, email: tp.Email})
		return true, nil
	}
	for _, id := range cards {
		qr, err := q.db().TelegramQuestionGet(q.ctx(), id)
		if err != nil {
			return false, err
		}
		if err := telegramQuestionMessage(q, "qn:"+id+":"+row.ID, id, qr.Kind, qr.Text, tp.Language, room.ChatID, room.ThreadID, "", deref(qr.SessionID), &m.MessageID); err != nil {
			return false, err
		}
	}
	return true, telegramDone(q, row.ID, "asked-approval", "")
}
