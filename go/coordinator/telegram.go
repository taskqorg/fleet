// The Telegram channel (docs/designs/fleet-telegram.md): people talk to Fleet sessions from Telegram through the
// telegram connection, whose bot token only fleet-gateway holds. Three work kit queues and one poll:
//
//   - telegram-poll (everyWake): long-polls getUpdates and commits every update into telegram_update before the next
//     poll's offset confirms it to Telegram, so a restart neither loses nor repeats an update (pilotq L15, L16).
//   - telegram-intake: routes one update. A linked person's message goes verbatim to one Fleet session as sent by that
//     person with source telegram: the person's home conversation in a private chat, a room's work item in a group
//     (the conductor, telegramConduct). Anyone not linked gets no reply and an audit row.
//   - telegram-reply: waits for the run a delivered message started and puts its final reply into the outbox.
//   - telegram-send: sends the outbox, one item at a time per chat, in order; a send whose outcome is unknown is never
//     repeated (pilotq L11).
//
// Only replies, questions and Fleet's own short notices reach the chat; tool output, heartbeats and progress stay in
// Fleet (FLT-32).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

const telegramConn = "telegram"

// telegramLongPoll: how long one getUpdates waits for an update (below the gateway's 30 s request timeout).
const telegramLongPoll = 20 * time.Second

// telegramChunk: Telegram's text limit is 4096 characters; a reply longer than this goes out in parts.
const telegramChunk = 4000

func init() {
	op("telegram_status", telegramStatus)
	op("telegram_link_code", telegramLinkCode)
	op("telegram_person_set", telegramPersonSet)
	op("telegram_people", telegramPeople)
	op("telegram_rooms", telegramRooms)
	op("telegram_room_set", telegramRoomSet)
	op("telegram_work_close", telegramWorkClose)
	kitRegister(&kitQueue{Name: "telegram-intake", Period: time.Second, Limit: 8, Due: telegramIntakeDue, Step: telegramIntakeStep,
		Retry: kitRetry{Table: "telegram_update", Key: "id", Guard: `telegram_update.attempts=$5::int`, Class: func(error) string { return "telegram_intake_failed" }}})
	kitRegister(&kitQueue{Name: "telegram-reply", Period: time.Second, Limit: 16, Workers: 4, Due: telegramReplyDue, Step: telegramReplyStep,
		Retry: kitRetry{Table: "telegram_turn", Key: "id", Guard: `telegram_turn.attempts=$5::int`, Class: func(error) string { return "telegram_reply_failed" }}})
	kitRegister(&kitQueue{Name: "telegram-send", Period: time.Second, Limit: 16, Workers: 4, Due: telegramSendDue, Step: telegramSendStep,
		Retry: kitRetry{Table: "telegram_outbox", Key: "id", Guard: `telegram_outbox.attempts=$5::int`, Class: func(error) string { return "telegram_send_failed" }}})
	onStart(func(ctx context.Context, a *App) {
		everyWake(ctx, "telegram-poll", 2*time.Second, nil, true, func(ctx context.Context, _ bool) error { return telegramPoll(ctx, a) })
		// A healthy poll waits up to telegramLongPoll for updates before it completes, so the absence rule (rules.go,
		// rulePassMisses periods) measures it against the wait plus the long poll, not the 2 s wait alone (a
		// healthy poller opened Q-ALL-1368).
		passIntervals.Store("telegram-poll", 2*time.Second+telegramLongPoll)
	})
}

// --- Telegram wire shapes (only the fields Fleet reads) ---

type tgUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

type tgChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type tgMessage struct {
	MessageID       int64      `json:"message_id"`
	MessageThreadID int64      `json:"message_thread_id"`
	IsTopicMessage  bool       `json:"is_topic_message"`
	From            *tgUser    `json:"from"`
	Chat            tgChat     `json:"chat"`
	Text            string     `json:"text"`
	Caption         string     `json:"caption"`
	ReplyToMessage  *tgMessage `json:"reply_to_message"`
	Photo           []struct{} `json:"photo"`
	Document        *struct{}  `json:"document"`
	ForumTopicNew   *struct {
		Name string `json:"name"`
	} `json:"forum_topic_created"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	EditedMessage *tgMessage  `json:"edited_message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

// thread: the forum topic a message is in; 0 outside topics (a private chat, a plain group, General).
func (m *tgMessage) thread() int64 {
	if m.IsTopicMessage {
		return m.MessageThreadID
	}
	return 0
}

func telegramRoomID(chat, thread int64) string {
	return strconv.FormatInt(chat, 10) + ":" + strconv.FormatInt(thread, 10)
}

// --- health of the poll (in memory: one coordinator polls; telegram_status reads it) ---

var telegramHealth struct {
	sync.Mutex
	polledAt    int64
	pollError   string
	botID       int64
	botUsername string
}

func telegramNote(err string) {
	telegramHealth.Lock()
	telegramHealth.polledAt, telegramHealth.pollError = nowMs(), err
	telegramHealth.Unlock()
}

// telegramCall: one call of the telegram connection as the coordinator. A variable so tests answer without a gateway.
var telegramCall = func(ctx context.Context, a *App, tool string, args any) contract.CallLine {
	token, release, err := readTokenFor(ctx, a, telegramConn, "")
	if err != nil {
		return contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: ptr("no-token")}
	}
	defer release()
	t, ok := gatewayProviders[telegramConn].Tools[tool]
	return gatewayCall(ctx, token, telegramConn, tool, args, ok && t.Idempotent)
}

// telegramReady: the gateway is configured and the telegram connection takes calls. A variable so tests switch it.
var telegramReady = func(ctx context.Context, a *App) (bool, error) {
	if gatewayURL() == "" {
		return false, nil
	}
	ok := false
	err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		ok, err = q.db().ConnTakesCalls(q.ctx(), telegramConn)
		return err
	})
	return ok, err
}

// tgReply: Telegram's answer envelope.
type tgReply struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func tgParse(line contract.CallLine) (tgReply, error) {
	var r tgReply
	if len(line.Output) > 0 {
		_ = json.Unmarshal(line.Output, &r)
	}
	if line.Outcome == contract.CallOutcomeOk && r.OK {
		return r, nil
	}
	cause := deref(line.ErrorCode)
	if r.Description != "" {
		cause += ": " + r.Description
	}
	if cause == "" {
		cause = string(line.Outcome)
	}
	return r, errors.New("telegram: " + cause)
}

// telegramPoll: one long poll. The offset confirms every update the journal already holds; the new ones are committed
// before the next poll confirms them.
func telegramPoll(ctx context.Context, a *App) error {
	ready, err := telegramReady(ctx, a)
	if err != nil || !ready {
		return err
	}
	telegramHealth.Lock()
	known := telegramHealth.botID != 0
	telegramHealth.Unlock()
	if !known {
		r, err := tgParse(telegramCall(ctx, a, "get_me", struct{}{}))
		if err != nil {
			telegramNote(err.Error())
			return nil // a refused token shows on System › Access and in telegram_status; the next pass tries again
		}
		var me tgUser
		_ = json.Unmarshal(r.Result, &me)
		telegramHealth.Lock()
		telegramHealth.botID, telegramHealth.botUsername = me.ID, me.Username
		telegramHealth.Unlock()
	}
	var offset int64
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		offset, err = q.db().TelegramUpdateNext(q.ctx())
		return err
	}); err != nil {
		return err
	}
	args := tgArgs{Timeout: ptr(int64(telegramLongPoll / time.Second)), AllowedUpdates: []string{"message", "edited_message", "callback_query"}}
	if offset > 0 {
		args.Offset = &offset
	}
	r, err := tgParse(telegramCall(ctx, a, "get_updates", args))
	if err != nil {
		telegramNote(err.Error())
		return nil
	}
	telegramNote("")
	var raws []json.RawMessage
	if err := json.Unmarshal(r.Result, &raws); err != nil || len(raws) == 0 {
		return nil
	}
	return a.store.Write(ctx, nil, nil, func(q *Request) error {
		for _, raw := range raws {
			var u tgUpdate
			if json.Unmarshal(raw, &u) != nil {
				continue
			}
			p := db.TelegramUpdateAddParams{ID: "tg:" + strconv.FormatInt(u.UpdateID, 10), UpdateID: u.UpdateID, Payload: raw}
			if m := firstMessage(&u); m != nil {
				p.ChatID, p.MessageID = &m.Chat.ID, &m.MessageID
				if m.From != nil {
					p.FromID = &m.From.ID
				}
			} else if u.CallbackQuery != nil {
				p.FromID = &u.CallbackQuery.From.ID
			}
			if _, err := q.db().TelegramUpdateAdd(q.ctx(), p); err != nil {
				return err
			}
		}
		return kitNotify(q, "telegram-intake")
	})
}

func firstMessage(u *tgUpdate) *tgMessage {
	if u.Message != nil {
		return u.Message
	}
	return u.EditedMessage
}

// --- intake ---

func telegramIntakeDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().TelegramUpdateDue(q.ctx(), db.TelegramUpdateDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// tgDelivered: what a delivery call reports back to its step.
type tgDelivered struct {
	err    error
	workID string
}

// tgDecided: what the conductor's model call reports back to its step.
type tgDecided struct {
	decision conductorDecision
}

// tgTarget: where one message goes: an existing session (work item or home), or a new work item / home conversation.
type tgTarget struct {
	sessionID string
	workID    string
	newWork   string // the new work item's title
	newHome   bool
}

func telegramIntakeStep(q *Request, now time.Time, c *kitClaim) error {
	row, err := q.db().TelegramUpdateGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && row.Outcome != "" {
		return nil
	}
	if err != nil {
		return err
	}
	var u tgUpdate
	if err := json.Unmarshal(row.Payload, &u); err != nil {
		return telegramDone(q, row.ID, "unreadable", "")
	}
	if u.CallbackQuery != nil {
		return telegramTap(q, now, c, &row, u.CallbackQuery)
	}
	if u.Message == nil {
		// An edit reaches nobody yet: the session already has the first text (the design keeps edits for FLT-36).
		return telegramDone(q, row.ID, "edited", "")
	}
	m := u.Message
	if m.From == nil || m.From.IsBot {
		return telegramDone(q, row.ID, "ignored", "")
	}
	if code, ok := strings.CutPrefix(strings.TrimSpace(m.Text), "/start "); ok && c.Result == nil {
		return telegramStart(q, &row, m, strings.TrimSpace(code))
	}
	tp, err := q.db().TelegramPersonByUser(q.ctx(), m.From.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Ignored: no reply, no session, no question (owner's rule, 2026-11-25). The journal keeps the outcome for the
		// channel's health and telegram_people.unknown, nothing else.
		return telegramDone(q, row.ID, "unknown-sender", "")
	}
	if err != nil {
		return err
	}
	p := q.app.peopleSnapshot().person(tp.Email)
	if p == nil {
		return telegramDone(q, row.ID, "no-person", "")
	}
	room, err := q.db().TelegramRoomUpsert(q.ctx(), db.TelegramRoomUpsertParams{ID: telegramRoomID(m.Chat.ID, m.thread()), ChatID: m.Chat.ID,
		ThreadID: m.thread(), Kind: m.Chat.Type, Title: roomTitle(m)})
	if err != nil {
		return err
	}
	switch res := c.Result.(type) {
	case tgDelivered:
		if res.err != nil {
			if err := telegramNotice(q, room, m.MessageID, "fail:"+row.ID, tp.Language, "notDelivered", plainError(res.err)); err != nil {
				return err
			}
			return telegramDone(q, row.ID, "failed", "")
		}
		return telegramDone(q, row.ID, "delivered", res.workID)
	case tgDecided:
		return telegramApply(q, c, &row, room, m, tp, p, res.decision)
	}
	text := messageText(m)
	if text == "" {
		if err := telegramNotice(q, room, m.MessageID, "nofile:"+row.ID, tp.Language, "filesLater", ""); err != nil {
			return err
		}
		return telegramDone(q, row.ID, "no-text", "")
	}
	// The receipt: the message is committed (pilotq L01).
	if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "seen:" + row.ID, ChatID: m.Chat.ID, ThreadID: m.thread(), Kind: "reaction",
		EditMessageID: &m.MessageID, Payload: jsonBytes([]map[string]string{{"type": "emoji", "emoji": "👀"}})}); err != nil {
		return err
	}
	if m.Chat.Type == "private" {
		if strings.TrimSpace(text) == "/new" {
			if err := q.db().TelegramPersonSetHome(q.ctx(), db.TelegramPersonSetHomeParams{Email: tp.Email}); err != nil {
				return err
			}
			if err := telegramNotice(q, room, m.MessageID, "new:"+row.ID, tp.Language, "newHome", ""); err != nil {
				return err
			}
			return telegramDone(q, row.ID, "new-home", "")
		}
		t := tgTarget{newHome: true}
		if tp.HomeSessionID != nil {
			if s, err := sessionMaybe(q, *tp.HomeSessionID); err != nil {
				return err
			} else if s != nil && s.ArchivedAt == nil {
				t = tgTarget{sessionID: s.ID}
			}
		}
		return telegramDeliver(q, c, &row, room, m, tp, p, t)
	}
	return telegramConduct(q, c, &row, room, m, tp, p)
}

func roomTitle(m *tgMessage) string {
	if m.ForumTopicNew != nil {
		return m.ForumTopicNew.Name
	}
	if m.Chat.Type == "private" {
		return ""
	}
	return m.Chat.Title
}

func messageText(m *tgMessage) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

func plainError(err error) string {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Msg
	}
	return "Fleet could not deliver it"
}

func telegramDone(q *Request, id, outcome, workID string) error {
	var w *string
	if workID != "" {
		w = &workID
	}
	return q.db().TelegramUpdateDone(q.ctx(), db.TelegramUpdateDoneParams{ID: id, Outcome: outcome, WorkID: w})
}

func telegramOutbox(q *Request, p db.TelegramOutboxAddParams) error {
	if err := q.db().TelegramOutboxAdd(q.ctx(), p); err != nil {
		return err
	}
	return kitNotify(q, "telegram-send")
}

// telegramStart: /start <code> links the sender to the person who made the code (telegram_link_code).
func telegramStart(q *Request, row *db.TelegramUpdate, m *tgMessage, code string) error {
	email, err := q.db().TelegramLinkTake(q.ctx(), code)
	if errors.Is(err, pgx.ErrNoRows) {
		return telegramDone(q, row.ID, "unknown-sender", "") // a wrong or used code answers nothing, like any stranger
	}
	if err != nil {
		return err
	}
	if err := q.db().TelegramPersonUnlinkUser(q.ctx(), db.TelegramPersonUnlinkUserParams{TelegramUserID: m.From.ID, Email: email}); err != nil {
		return err
	}
	lang := languageOf(m.From.LanguageCode)
	if err := q.db().TelegramPersonLink(q.ctx(), db.TelegramPersonLinkParams{Email: email, TelegramUserID: m.From.ID, Username: m.From.Username, Language: lang}); err != nil {
		return err
	}
	if err := audit(q, "telegram-person:"+email, "telegramUserId", nil, m.From.ID, auditExtra{Holder: email, Reason: "linked by /start code"}); err != nil {
		return err
	}
	tp, err := q.db().TelegramPersonGet(q.ctx(), email)
	if err != nil {
		return err
	}
	name := email
	if p := q.app.peopleSnapshot().person(email); p != nil && p.Name != "" {
		name = p.Name
	}
	room := db.TelegramRoom{ChatID: m.Chat.ID, ThreadID: m.thread()}
	if err := telegramNotice(q, room, m.MessageID, "linked:"+row.ID, tp.Language, "linked", name); err != nil {
		return err
	}
	return telegramDone(q, row.ID, "linked", "")
}

// languageOf: Telegram's IETF tag reduced to its language (ru-RU: ru); Fleet's notices know en and ru.
func languageOf(tag string) string {
	l, _, _ := strings.Cut(strings.ToLower(tag), "-")
	return l
}

// telegramDeliver: the message goes verbatim to its target as sent by the person, source telegram, in the person's
// scope (a Call: it runs in its own transaction after this step). A repeat is the same request (requestId tg:<update>),
// so a crash between the call and its record delivers once.
func telegramDeliver(q *Request, c *kitClaim, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, p *Person, t tgTarget) error {
	a, text, rid := q.app, messageText(m), row.ID
	c.Call = func(ctx context.Context) any {
		var out tgDelivered
		pq := scopedRequest(ctx, a, p)
		out.err = a.store.Write(ctx, pq, []string{GLOBAL}, func(pq *Request) error {
			if err := refreshRequestAccess(pq); err != nil {
				return err
			}
			sid, request := t.sessionID, rid
			if t.newHome || t.newWork != "" {
				r := contract.SessionStartRequest{RequestId: &rid, Preset: ptr("discuss"), Text: &text}
				if room.ProjectHint != "" && !t.newHome {
					r.ProjectId = &room.ProjectHint
				}
				pq.body = jsonBytes(r)
				started, err := sessionStart(pq, &r)
				if err != nil {
					return err
				}
				sid, request = started.SessionId, "start:"+rid
				if t.newHome {
					if err := pq.db().TelegramPersonSetHome(pq.ctx(), db.TelegramPersonSetHomeParams{Email: tp.Email, SessionID: &sid}); err != nil {
						return err
					}
				} else {
					t.workID = "work:" + rid
					if err := pq.db().TelegramWorkAdd(pq.ctx(), db.TelegramWorkAddParams{ID: t.workID, RoomID: room.ID, SessionID: sid, Title: t.newWork, CreatedBy: tp.Email}); err != nil {
						return err
					}
					if err := telegramOutbox(pq, db.TelegramOutboxAddParams{ID: "notice:newwork:" + rid, ChatID: room.ChatID, ThreadID: room.ThreadID,
						Kind: "text", Text: say(tp.Language, "newWork", t.newWork), WorkID: &t.workID}); err != nil {
						return err
					}
				}
			} else if _, err := agentSubmit(pq, &contract.MessageSendRequest{SessionId: &sid, RequestId: &rid, Text: &text}, "telegram"); err != nil {
				return err
			}
			var work *string
			if t.workID != "" {
				work = &t.workID
				if err := pq.db().TelegramWorkTouch(pq.ctx(), t.workID); err != nil {
					return err
				}
			}
			if err := pq.db().TelegramTurnAdd(pq.ctx(), db.TelegramTurnAddParams{ID: rid, SessionID: sid, RequestID: request, WorkID: work,
				ChatID: m.Chat.ID, ThreadID: m.thread(), MessageID: m.MessageID, Email: tp.Email}); err != nil {
				return err
			}
			return kitNotify(pq, "telegram-reply")
		})
		out.workID = t.workID
		return out
	}
	return nil
}

// --- the conductor (FLT-33): which work item of a group room a message belongs to ---

// conductorDecision: the conductor's answer for one message.
type conductorDecision struct {
	Choice string `json:"choice"` // a work item id, "new", or "ask"
	Title  string `json:"title"`  // the new work item's title
	err    error
}

// telegramConduct: fast paths first (a reply to a work item's message, a room with no open work); otherwise the
// conductor's model call chooses, and when it is not sure the person chooses with one tap.
func telegramConduct(q *Request, c *kitClaim, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, p *Person) error {
	if r := m.ReplyToMessage; r != nil {
		work := ""
		if o, err := q.db().TelegramOutboxBySent(q.ctx(), db.TelegramOutboxBySentParams{ChatID: m.Chat.ID, TelegramMessageID: &r.MessageID}); err == nil && o.WorkID != nil {
			work = *o.WorkID
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if work == "" {
			if u, err := q.db().TelegramUpdateByMessage(q.ctx(), db.TelegramUpdateByMessageParams{ChatID: &m.Chat.ID, MessageID: &r.MessageID}); err == nil {
				work = deref(u.WorkID)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if work != "" {
			if w, err := q.db().TelegramWorkGet(q.ctx(), work); err == nil && w.ClosedAt == nil && w.RoomID == room.ID {
				return telegramDeliver(q, c, row, room, m, tp, p, tgTarget{sessionID: w.SessionID, workID: w.ID})
			}
		}
	}
	open, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return telegramDeliver(q, c, row, room, m, tp, p, tgTarget{newWork: workTitle(messageText(m))})
	}
	a, text, author := q.app, messageText(m), tp.Email
	if pp := q.app.peopleSnapshot().person(tp.Email); pp != nil && pp.Name != "" {
		author = pp.Name
	}
	c.Call = func(ctx context.Context) any { return tgDecided{decision: conductorChoose(ctx, a, open, author, text)} }
	return nil
}

// telegramApply: the conductor's choice for a message: attach, open new work, or ask with one tap.
func telegramApply(q *Request, c *kitClaim, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, p *Person, d conductorDecision) error {
	open, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
	if err != nil {
		return err
	}
	for _, w := range open {
		if d.Choice == w.ID {
			return telegramDeliver(q, c, row, room, m, tp, p, tgTarget{sessionID: w.SessionID, workID: w.ID})
		}
	}
	if d.Choice == "new" {
		title := strings.TrimSpace(d.Title)
		if title == "" {
			title = workTitle(messageText(m))
		}
		return telegramDeliver(q, c, row, room, m, tp, p, tgTarget{newWork: title})
	}
	// Not sure: "Is this about: [A] [B] [new]". The tap (telegramTap) routes the message.
	buttons := [][]map[string]string{}
	for i, w := range open {
		if i == 4 {
			break
		}
		buttons = append(buttons, []map[string]string{{"text": clip(w.Title, 40), "callback_data": "w:" + row.ID + ":" + strconv.Itoa(i)}})
	}
	buttons = append(buttons, []map[string]string{{"text": say(tp.Language, "newButton", ""), "callback_data": "w:" + row.ID + ":new"}})
	if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "ask:" + row.ID, ChatID: m.Chat.ID, ThreadID: m.thread(), Kind: "text",
		ReplyTo: &m.MessageID, Text: say(tp.Language, "which", ""), Payload: jsonBytes(tgMarkup{InlineKeyboard: buttons})}); err != nil {
		return err
	}
	return telegramDone(q, row.ID, "asked", "")
}

// telegramTap: a button tap. w:<update>:<n|new> routes an asked message; only its author's tap counts.
func telegramTap(q *Request, now time.Time, c *kitClaim, row *db.TelegramUpdate, cb *tgCallback) error {
	if c.Result == nil {
		if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "tap:" + row.ID, ChatID: chatOf(cb), Kind: "callback", Payload: jsonBytes(map[string]string{"callback_query_id": cb.ID})}); err != nil {
			return err
		}
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 4 || parts[0] != "w" {
		return telegramDone(q, row.ID, "tap-unknown", "")
	}
	asked, err := q.db().TelegramUpdateGet(q.ctx(), parts[1]+":"+parts[2])
	if errors.Is(err, pgx.ErrNoRows) {
		return telegramDone(q, row.ID, "tap-unknown", "")
	}
	if err != nil {
		return err
	}
	var u tgUpdate
	if json.Unmarshal(asked.Payload, &u) != nil || u.Message == nil || u.Message.From == nil || u.Message.From.ID != cb.From.ID || asked.Outcome != "asked" {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	m := u.Message
	tp, err := q.db().TelegramPersonByUser(q.ctx(), m.From.ID)
	if err != nil {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	p := q.app.peopleSnapshot().person(tp.Email)
	room, err := q.db().TelegramRoomGet(q.ctx(), telegramRoomID(m.Chat.ID, m.thread()))
	if p == nil || err != nil {
		return telegramDone(q, row.ID, "tap-ignored", "")
	}
	if res, ok := c.Result.(tgDelivered); ok {
		if res.err != nil {
			if err := telegramNotice(q, room, m.MessageID, "fail:"+asked.ID, tp.Language, "notDelivered", plainError(res.err)); err != nil {
				return err
			}
			if err := telegramDone(q, asked.ID, "failed", ""); err != nil {
				return err
			}
			return telegramDone(q, row.ID, "tap", "")
		}
		if err := telegramDone(q, asked.ID, "delivered", res.workID); err != nil {
			return err
		}
		return telegramDone(q, row.ID, "tap", res.workID)
	}
	open, err := q.db().TelegramWorkOpen(q.ctx(), room.ID)
	if err != nil {
		return err
	}
	t, label := tgTarget{newWork: workTitle(messageText(m))}, ""
	if n, err := strconv.Atoi(parts[3]); err == nil {
		if n < 0 || n >= len(open) {
			return telegramDone(q, row.ID, "tap-stale", "")
		}
		t, label = tgTarget{sessionID: open[n].SessionID, workID: open[n].ID}, open[n].Title
	} else {
		label = t.newWork
	}
	if cb.Message != nil {
		if err := telegramOutbox(q, db.TelegramOutboxAddParams{ID: "asked:" + asked.ID, ChatID: cb.Message.Chat.ID, ThreadID: cb.Message.thread(), Kind: "edit",
			EditMessageID: &cb.Message.MessageID, Text: "→ " + label}); err != nil {
			return err
		}
	}
	// The asked message is delivered under its own request id, so the tap's repeat cannot deliver it twice.
	return telegramDeliver(q, c, &asked, room, m, tp, p, t)
}

func chatOf(cb *tgCallback) int64 {
	if cb.Message != nil {
		return cb.Message.Chat.ID
	}
	return cb.From.ID
}

func workTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return clip(line, 60)
}


// conductorModel: the conductor's model connection and model (FLEET_TELEGRAM_CONDUCTOR, "<connection>/<model>");
// by default the first OpenAI connection open to every project.
func conductorModel(ctx context.Context, a *App) (string, string) {
	if v := strings.TrimSpace(os.Getenv("FLEET_TELEGRAM_CONDUCTOR")); v != "" {
		conn, model, _ := strings.Cut(v, "/")
		return conn, model
	}
	conn := ""
	_ = a.store.Read(ctx, nil, func(q *Request) error {
		cs, err := apiConnectionAll(q)
		for _, c := range cs {
			if c.Provider == "openai" && slices.Contains(c.Access.Projects, "*") && conn == "" {
				if ok, _ := q.db().ConnTakesCalls(q.ctx(), c.Name); ok {
					conn = c.Name
				}
			}
		}
		return err
	})
	return conn, "gpt-6.2-sol"
}

// conductorChoose asks the conductor model which open work item a message belongs to. Any failure is "ask": the
// person then chooses with one tap, so a model outage never misroutes a message.
var conductorChoose = func(ctx context.Context, a *App, open []db.TelegramWork, author, text string) conductorDecision {
	conn, model := conductorModel(ctx, a)
	if conn == "" {
		return conductorDecision{Choice: "ask"}
	}
	var items strings.Builder
	for _, w := range open {
		fmt.Fprintf(&items, "- id %s: %s\n", w.ID, w.Title)
	}
	instructions := "You are the conductor of a team chat room. People write short messages about several pieces of work. " +
		"Decide which open work item the new message belongs to. Answer only JSON {\"choice\": <work item id | \"new\" | \"ask\">, \"title\": <short title for new work, else \"\">}. " +
		"Choose \"new\" only when the message clearly starts different work; choose \"ask\" when it could belong to more than one item or you are unsure. " +
		"Short follow-ups (\"so?\", \"show mobile too\", \"ok go\") belong to the most recent item unless they name another."
	input := "Open work items, most recent first:\n" + items.String() + "\nNew message from " + author + ":\n" + clip(text, 2000)
	token, release, err := readTokenFor(ctx, a, conn, "")
	if err != nil {
		return conductorDecision{Choice: "ask", err: err}
	}
	defer release()
	line := gatewayCall(ctx, token, conn, "create_response", struct {
		Model           string            `json:"model"`
		Instructions    string            `json:"instructions"`
		Input           string            `json:"input"`
		MaxOutputTokens int               `json:"max_output_tokens"`
		Reasoning       map[string]string `json:"reasoning"`
	}{model, instructions, input, 300, map[string]string{"effort": "low"}}, false)
	if line.Outcome != contract.CallOutcomeOk {
		return conductorDecision{Choice: "ask", err: errors.New(deref(line.ErrorCode))}
	}
	var resp struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	_ = json.Unmarshal(line.Output, &resp)
	for _, o := range resp.Output {
		for _, ct := range o.Content {
			if ct.Type != "output_text" {
				continue
			}
			body := strings.TrimSpace(ct.Text)
			body = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(body, "```json"), "```"), "```")
			var d conductorDecision
			if json.Unmarshal([]byte(strings.TrimSpace(body)), &d) == nil && d.Choice != "" {
				return d
			}
		}
	}
	return conductorDecision{Choice: "ask"}
}

// --- replies ---

func telegramReplyDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().TelegramTurnDue(q.ctx(), db.TelegramTurnDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// telegramReplyStep: while the run works the chat shows typing (pilotq L02); when it ends its final reply, and only
// that, goes to the chat as a reply to the person's message (L03, L12).
func telegramReplyStep(q *Request, now time.Time, c *kitClaim) error {
	t, err := q.db().TelegramTurnGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Result != nil {
		if err := q.db().TelegramTurnTyped(q.ctx(), t.ID); err != nil {
			return err
		}
		return kitSchedule(q, "telegram-reply", t.ID, now.Add(3*time.Second))
	}
	ms, err := messagesBySession(q, t.SessionID, &t.RequestID)
	if err != nil {
		return err
	}
	if len(ms) == 0 || ms[0].ExecutionID == "" {
		queued, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &t.SessionID, RequestID: &t.RequestID})
		if err != nil {
			return err
		}
		if len(queued) == 0 || queued[0].State != "queued" {
			return q.db().TelegramTurnDelete(q.ctx(), t.ID) // withdrawn before it ran: nothing will answer it
		}
		return telegramTyping(q, now, c, t)
	}
	e, err := getExecution(q, ms[0].ExecutionID)
	if err != nil {
		return err
	}
	if !e.terminal() {
		return telegramTyping(q, now, c, t)
	}
	tp, err := q.db().TelegramPersonGet(q.ctx(), t.Email)
	lang := ""
	if err == nil {
		lang = tp.Language
	}
	texts, err := replyText(q, e.ID)
	if err != nil {
		return err
	}
	final := ""
	for i := len(texts) - 1; i >= 0 && final == ""; i-- {
		final = strings.TrimSpace(texts[i])
	}
	if final == "" {
		key := "noReply"
		if e.State != "succeeded" {
			key = "runFailed"
		}
		final = say(lang, key, "")
	}
	parts := chunks(final, telegramChunk)
	for i, part := range parts {
		p := db.TelegramOutboxAddParams{ID: fmt.Sprintf("reply:%s:%d", e.ID, i), ChatID: t.ChatID, ThreadID: t.ThreadID, Kind: "text", Text: part,
			WorkID: t.WorkID, SessionID: &t.SessionID}
		if i == 0 {
			p.ReplyTo = &t.MessageID
		}
		if i == len(parts)-1 {
			if link := conversationLink(t.SessionID); link != "" {
				p.Payload = jsonBytes(tgMarkup{InlineKeyboard: [][]map[string]string{{{"text": say(lang, "open", ""), "url": link}}}})
			}
		}
		if err := telegramOutbox(q, p); err != nil {
			return err
		}
	}
	return q.db().TelegramTurnDelete(q.ctx(), t.ID)
}

// telegramTyping: typing at most every 4 s while the run works (Telegram shows it about 5 s), then look again.
func telegramTyping(q *Request, now time.Time, c *kitClaim, t db.TelegramTurn) error {
	if t.TypingAt != nil && now.Sub(*t.TypingAt) < 4*time.Second {
		return kitSchedule(q, "telegram-reply", t.ID, now.Add(2*time.Second))
	}
	a := q.app
	args := tgArgs{ChatID: t.ChatID, Action: "typing"}
	if t.ThreadID != 0 {
		args.MessageThreadID = &t.ThreadID
	}
	c.Call = func(ctx context.Context) any { return telegramCall(ctx, a, "send_chat_action", args) }
	return nil
}

func conversationLink(sessionID string) string {
	if browserOrigin == "" {
		return ""
	}
	return strings.TrimRight(browserOrigin, "/") + "/conversations/" + sessionID
}

// chunks splits text into parts of at most n runes, at a line break where one is near the end of a part.
func chunks(text string, n int) []string {
	out := []string{}
	r := []rune(text)
	for len(r) > n {
		cut := n
		for i := n; i > n*3/4; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		if part := strings.TrimSpace(string(r[:cut])); part != "" {
			out = append(out, part)
		}
		r = r[cut:]
	}
	if part := strings.TrimSpace(string(r)); part != "" || len(out) == 0 {
		out = append(out, part)
	}
	return out
}

// --- outbox ---

func telegramSendDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().TelegramOutboxDue(q.ctx(), db.TelegramOutboxDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

func telegramSendStep(q *Request, now time.Time, c *kitClaim) error {
	o, err := q.db().TelegramOutboxGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && o.State != "pending" {
		return nil
	}
	if err != nil {
		return err
	}
	if line, ok := c.Result.(contract.CallLine); ok {
		return telegramSent(q, now, o, line)
	}
	tool, args := "send_message", tgArgs{ChatID: o.ChatID}
	switch o.Kind {
	case "text":
		args.Text, args.LinkPreview = tgFormatted(o, &args), map[string]bool{"is_disabled": true}
		if o.ThreadID != 0 {
			args.MessageThreadID = &o.ThreadID
		}
		if o.ReplyTo != nil {
			args.ReplyParameters = &tgReplyTo{MessageID: *o.ReplyTo, AllowSendingWithoutReply: true}
		}
		if len(o.Payload) > 0 {
			args.ReplyMarkup = json.RawMessage(o.Payload)
		}
	case "edit":
		tool, args.MessageID = "edit_message_text", o.EditMessageID
		args.Text = tgFormatted(o, &args)
	case "reaction":
		tool, args.MessageID, args.Reaction = "set_message_reaction", o.EditMessageID, json.RawMessage(o.Payload)
	case "callback":
		var cb struct {
			ID string `json:"callback_query_id"`
		}
		_ = json.Unmarshal(o.Payload, &cb)
		tool, args = "answer_callback_query", tgArgs{CallbackQueryID: cb.ID}
	}
	a := q.app
	c.Call = func(ctx context.Context) any { return telegramCall(ctx, a, tool, args) }
	return nil
}

// tgFormatted: the item's text as Telegram HTML (tgHTML), or as plain text once Telegram refused its formatting.
func tgFormatted(o db.TelegramOutbox, args *tgArgs) *string {
	if deref(o.Error) == "html-refused" {
		return &o.Text
	}
	args.ParseMode = "HTML"
	return ptr(tgHTML(o.Text))
}

// tgArgs: the arguments of the Bot API calls Fleet makes; an absent field is omitted.
type tgArgs struct {
	ChatID          int64           `json:"chat_id,omitempty"`
	MessageThreadID *int64          `json:"message_thread_id,omitempty"`
	MessageID       *int64          `json:"message_id,omitempty"`
	Text            *string         `json:"text,omitempty"`
	ParseMode       string          `json:"parse_mode,omitempty"`
	ReplyParameters *tgReplyTo      `json:"reply_parameters,omitempty"`
	ReplyMarkup     json.RawMessage `json:"reply_markup,omitempty"`
	LinkPreview     map[string]bool `json:"link_preview_options,omitempty"`
	Reaction        json.RawMessage `json:"reaction,omitempty"`
	Action          string          `json:"action,omitempty"`
	CallbackQueryID string          `json:"callback_query_id,omitempty"`
	Offset          *int64          `json:"offset,omitempty"`
	Timeout         *int64          `json:"timeout,omitempty"`
	AllowedUpdates  []string        `json:"allowed_updates,omitempty"`
}

type tgReplyTo struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

type tgMarkup struct {
	InlineKeyboard [][]map[string]string `json:"inline_keyboard"`
}

// telegramSent records one send. A refused message (HTTP 4xx) fails for good, a flood limit waits its retry_after, a
// send whose outcome is unknown is never repeated (a second copy is worse than a gap, pilotq L11); a refusal before
// Telegram (no key, gateway down) retries through the kit.
func telegramSent(q *Request, now time.Time, o db.TelegramOutbox, line contract.CallLine) error {
	r, err := tgParse(line)
	settle := func(state string, id *int64, cause *string) error {
		return q.db().TelegramOutboxSettle(q.ctx(), db.TelegramOutboxSettleParams{ID: o.ID, State: state, TelegramMessageID: id, Error: cause})
	}
	if err == nil {
		var sent struct {
			MessageID int64 `json:"message_id"`
		}
		_ = json.Unmarshal(r.Result, &sent)
		var id *int64
		if sent.MessageID != 0 {
			id = &sent.MessageID
		} else if o.Kind == "edit" {
			id = o.EditMessageID
		}
		return settle("sent", id, nil)
	}
	code := deref(line.ErrorCode)
	cause := err.Error()
	switch {
	case code == "http-429" && r.Parameters.RetryAfter > 0:
		return kitSchedule(q, "telegram-send", o.ID, now.Add(time.Duration(r.Parameters.RetryAfter)*time.Second))
	case code == "http-400" && strings.Contains(r.Description, "can't parse entities") && deref(o.Error) != "html-refused":
		// Formatting Telegram refuses: the same text again, plain, at once.
		if err := q.db().TelegramOutboxMarkPlain(q.ctx(), o.ID); err != nil {
			return err
		}
		return kitSchedule(q, "telegram-send", o.ID, now)
	case o.Kind == "edit" && strings.Contains(r.Description, "message is not modified"):
		return settle("sent", o.EditMessageID, nil)
	case strings.HasPrefix(code, "http-4"):
		return settle("failed", nil, &cause)
	case line.Outcome == contract.CallOutcomeUnknown && o.Kind == "text":
		return settle("unknown", nil, &cause)
	}
	return err
}

// --- Fleet's own notices, in the person's language ---

var telegramWords = map[string]map[string]string{
	"en": {
		"linked":       "Linked to Fleet as %s. Write here to talk to Fleet.",
		"notDelivered": "Not delivered: %s",
		"filesLater":   "Files without text do not reach Fleet yet; add a line of text.",
		"newHome":      "The next message starts a new conversation.",
		"newWork":      "New: %s",
		"which":        "Is this about:",
		"newButton":    "Something new",
		"noReply":      "Done, without a text answer.",
		"runFailed":    "The run failed without an answer.",
		"open":         "Open in Fleet",
	},
	"ru": {
		"linked":       "Telegram привязан к Fleet: %s. Пишите сюда, чтобы говорить с Fleet.",
		"notDelivered": "Не доставлено: %s",
		"filesLater":   "Файлы без текста пока не доходят до Fleet; добавьте строку текста.",
		"newHome":      "Следующее сообщение начнёт новый разговор.",
		"newWork":      "Новое: %s",
		"which":        "Это про:",
		"newButton":    "Новое",
		"noReply":      "Готово, без текстового ответа.",
		"runFailed":    "Запуск завершился ошибкой без ответа.",
		"open":         "Открыть во Fleet",
	},
}

func say(lang, key, arg string) string {
	words, ok := telegramWords[lang]
	if !ok {
		words = telegramWords["en"]
	}
	if strings.Contains(words[key], "%s") {
		return fmt.Sprintf(words[key], arg)
	}
	return words[key]
}

// telegramNotice queues one of Fleet's own short messages into a room (a reply to replyTo when it is not 0).
func telegramNotice(q *Request, room db.TelegramRoom, replyTo int64, id, lang, key, arg string) error {
	p := db.TelegramOutboxAddParams{ID: "notice:" + id, ChatID: room.ChatID, ThreadID: room.ThreadID, Kind: "text", Text: say(lang, key, arg)}
	if replyTo != 0 {
		p.ReplyTo = &replyTo
	}
	return telegramOutbox(q, p)
}

// --- operations ---

func telegramStatus(q *Request, _ *contract.TelegramStatusRequest) (contract.TelegramStatusReply, error) {
	var out contract.TelegramStatusReply
	ok, err := q.db().ConnTakesCalls(q.ctx(), telegramConn)
	if err != nil {
		return out, err
	}
	out.Connected = ok
	telegramHealth.Lock()
	if telegramHealth.botID != 0 {
		out.BotId, out.BotUsername = ptr(telegramHealth.botID), ptr(telegramHealth.botUsername)
	}
	if telegramHealth.polledAt != 0 {
		out.PolledAt = ptr(telegramHealth.polledAt)
	}
	if telegramHealth.pollError != "" {
		out.PollError = ptr(telegramHealth.pollError)
	}
	telegramHealth.Unlock()
	if out.Pending.Intake, err = q.db().TelegramUpdatePending(q.ctx()); err != nil {
		return out, err
	}
	if out.Pending.Replies, err = q.db().TelegramTurnPending(q.ctx()); err != nil {
		return out, err
	}
	if out.Pending.Outbox, err = q.db().TelegramOutboxPending(q.ctx()); err != nil {
		return out, err
	}
	tp, err := q.db().TelegramPersonGet(q.ctx(), director(q))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Me = contract.TelegramMe{Linked: true, TelegramUserId: &tp.TelegramUserID, Username: &tp.Username, Language: &tp.Language, HomeSessionId: tp.HomeSessionID}
	return out, nil
}

var linkAlphabet = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

func telegramLinkCode(q *Request, _ *contract.TelegramLinkCodeRequest) (contract.TelegramLinkCodeReply, error) {
	if q.person == nil {
		return contract.TelegramLinkCodeReply{}, fail(409, "A person links Telegram")
	}
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return contract.TelegramLinkCodeReply{}, err
	}
	code := linkAlphabet.EncodeToString(b)
	if err := q.db().TelegramLinkAdd(q.ctx(), db.TelegramLinkAddParams{Code: code, Email: q.person.Email}); err != nil {
		return contract.TelegramLinkCodeReply{}, err
	}
	out := contract.TelegramLinkCodeReply{Code: code, Command: "/start " + code, ExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli()}
	telegramHealth.Lock()
	if u := telegramHealth.botUsername; u != "" {
		out.Url = ptr("https://t.me/" + u + "?start=" + code)
	}
	telegramHealth.Unlock()
	return out, nil
}

func telegramPersonSet(q *Request, r *contract.TelegramPersonSetRequest) (contract.TelegramPersonSetReply, error) {
	var out contract.TelegramPersonSetReply
	email := director(q)
	if r.Email != nil && *r.Email != "" {
		email = *r.Email
	}
	owner := q.person != nil && fullAccess(q.person)
	if (email != director(q) || r.TelegramUserId != nil) && !owner {
		return out, fail(403, "Only the owner links another person or a Telegram user id")
	}
	target := q.app.peopleSnapshot().person(email)
	if target == nil {
		return out, errNotFound
	}
	// Full access manages other people's links, never the owner's.
	if target.Owner && email != director(q) && (q.person == nil || !q.person.Owner) {
		return out, fail(403, "Only the owner changes the owner's Telegram link")
	}
	// Linking lives on the person record and is set through Fleet: the owner, a full-access person, or an agent acting
	// for one (owner's rule, 2026-11-25). The bot confirms a link in the user's private chat.
	confirmChat := int64(0)
	if r.TelegramUserId != nil {
		confirmChat = *r.TelegramUserId // Telegram refuses it until the user has started the bot
	}
	if r.Unlink != nil && *r.Unlink {
		n, err := q.db().TelegramPersonDelete(q.ctx(), email)
		if err == nil && n > 0 {
			err = audit(q, "telegram-person:"+email, "telegramUserId", "linked", nil, auditExtra{Holder: email})
		}
		out.Unlinked = ptr(n > 0)
		return out, err
	}
	lang := ""
	if r.Language != nil {
		if lang = languageOf(*r.Language); lang == "" || len(lang) > 8 {
			return out, fail(400, "language: a language tag such as en or ru")
		}
	}
	if r.TelegramUserId != nil {
		if *r.TelegramUserId <= 0 {
			return out, fail(400, "telegramUserId: a Telegram user id")
		}
		if err := q.db().TelegramPersonUnlinkUser(q.ctx(), db.TelegramPersonUnlinkUserParams{TelegramUserID: *r.TelegramUserId, Email: email}); err != nil {
			return out, err
		}
		if err := q.db().TelegramPersonLink(q.ctx(), db.TelegramPersonLinkParams{Email: email, TelegramUserID: *r.TelegramUserId, Username: deref(r.Username), Language: lang}); err != nil {
			return out, err
		}
		if err := audit(q, "telegram-person:"+email, "telegramUserId", nil, *r.TelegramUserId, auditExtra{Holder: email}); err != nil {
			return out, err
		}
		// The bot says in chat who the Telegram user now speaks as, so a wrong link is seen at once.
		tp, err := q.db().TelegramPersonGet(q.ctx(), email)
		if err != nil {
			return out, err
		}
		name := target.Name
		if name == "" {
			name = email
		}
		if err := telegramNotice(q, db.TelegramRoom{ChatID: confirmChat}, 0, "linked:"+email+":"+strconv.FormatInt(tp.LinkedAt.UnixMilli(), 10), tp.Language, "linked", name); err != nil {
			return out, err
		}
	} else if lang != "" {
		n, err := q.db().TelegramPersonSetLanguage(q.ctx(), db.TelegramPersonSetLanguageParams{Email: email, Language: lang})
		if err != nil {
			return out, err
		}
		if n == 0 {
			return out, fail(404, "Not linked to Telegram yet: telegram_link_code")
		}
	}
	tp, err := q.db().TelegramPersonGet(q.ctx(), email)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(404, "Not linked to Telegram yet: telegram_link_code")
	}
	if err != nil {
		return out, err
	}
	out.Person = ptr(telegramPersonOf(tp))
	return out, nil
}

func telegramPersonOf(tp db.TelegramPerson) contract.TelegramPerson {
	return contract.TelegramPerson{Email: tp.Email, TelegramUserId: tp.TelegramUserID, Username: tp.Username, Language: tp.Language,
		HomeSessionId: tp.HomeSessionID, LinkedAt: tp.LinkedAt.UnixMilli()}
}

func telegramPeople(q *Request, _ *contract.TelegramPeopleRequest) (contract.TelegramPeopleReply, error) {
	out := contract.TelegramPeopleReply{People: []contract.TelegramPerson{}, Unknown: []contract.TelegramUnknownSender{}}
	ps, err := q.db().TelegramPersonList(q.ctx())
	if err != nil {
		return out, err
	}
	for _, p := range ps {
		out.People = append(out.People, telegramPersonOf(p))
	}
	us, err := q.db().TelegramUnknownSenders(q.ctx())
	for _, u := range us {
		out.Unknown = append(out.Unknown, contract.TelegramUnknownSender{TelegramUserId: u.TelegramUserID, Username: u.Username, Name: u.Name,
			LastAt: u.LastAt.Time.UnixMilli(), Messages: u.Messages})
	}
	return out, err
}

func telegramRooms(q *Request, _ *contract.TelegramRoomsRequest) (contract.TelegramRoomsReply, error) {
	out := contract.TelegramRoomsReply{Rooms: []contract.TelegramRoom{}}
	rs, err := q.db().TelegramRoomList(q.ctx())
	if err != nil {
		return out, err
	}
	ws, err := q.db().TelegramWorkList(q.ctx())
	if err != nil {
		return out, err
	}
	for _, r := range rs {
		room := telegramRoomOf(r)
		for _, w := range ws {
			if w.RoomID == r.ID {
				room.Works = append(room.Works, contract.TelegramWork{Id: w.ID, Title: w.Title, SessionId: w.SessionID, CreatedBy: w.CreatedBy, LastAt: w.LastAt.UnixMilli()})
			}
		}
		out.Rooms = append(out.Rooms, room)
	}
	return out, nil
}

func telegramRoomOf(r db.TelegramRoom) contract.TelegramRoom {
	return contract.TelegramRoom{Id: r.ID, ChatId: r.ChatID, ThreadId: r.ThreadID, Kind: r.Kind, Title: r.Title, ProjectHint: r.ProjectHint,
		SeenAt: r.SeenAt.UnixMilli(), Works: []contract.TelegramWork{}}
}

func telegramRoomSet(q *Request, r *contract.TelegramRoomSetRequest) (contract.TelegramRoomSetReply, error) {
	if r.ProjectHint != "" && projectSettingsOf(q, r.ProjectHint) == nil {
		return contract.TelegramRoomSetReply{}, fail(400, "unknown project "+r.ProjectHint)
	}
	if r.RoomId == nil && r.ChatId == nil {
		return contract.TelegramRoomSetReply{}, fail(400, "Expected roomId or chatId")
	}
	if r.RoomId == nil {
		kind, thread := deref(r.Kind), int64(0)
		if r.ThreadId != nil {
			thread = *r.ThreadId
		}
		if kind == "" {
			kind = "private"
			if *r.ChatId < 0 {
				kind = "supergroup"
			}
		}
		room, err := q.db().TelegramRoomUpsert(q.ctx(), db.TelegramRoomUpsertParams{ID: telegramRoomID(*r.ChatId, thread), ChatID: *r.ChatId,
			ThreadID: thread, Kind: kind, Title: deref(r.Title)})
		if err != nil {
			return contract.TelegramRoomSetReply{}, err
		}
		r.RoomId = &room.ID
	}
	prev, err := q.db().TelegramRoomGet(q.ctx(), *r.RoomId)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.TelegramRoomSetReply{}, errNotFound
	}
	if err != nil {
		return contract.TelegramRoomSetReply{}, err
	}
	if _, err := q.db().TelegramRoomSetHint(q.ctx(), db.TelegramRoomSetHintParams{ID: *r.RoomId, ProjectHint: r.ProjectHint}); err != nil {
		return contract.TelegramRoomSetReply{}, err
	}
	if err := audit(q, "telegram-room:"+*r.RoomId, "projectHint", prev.ProjectHint, r.ProjectHint, auditExtra{}); err != nil {
		return contract.TelegramRoomSetReply{}, err
	}
	prev.ProjectHint = r.ProjectHint
	return contract.TelegramRoomSetReply{Room: telegramRoomOf(prev)}, nil
}

func telegramWorkClose(q *Request, r *contract.TelegramWorkCloseRequest) (contract.TelegramWorkCloseReply, error) {
	n, err := q.db().TelegramWorkClose(q.ctx(), r.WorkId)
	if err != nil {
		return contract.TelegramWorkCloseReply{}, err
	}
	if n == 0 {
		if _, err := q.db().TelegramWorkGet(q.ctx(), r.WorkId); errors.Is(err, pgx.ErrNoRows) {
			return contract.TelegramWorkCloseReply{}, errNotFound
		}
	}
	return contract.TelegramWorkCloseReply{Closed: n > 0}, nil
}
