package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"fleet/contract"
	"fleet/coordinator/db"
)

// telegramFake answers the telegram connection's calls: get_updates hands out the queued updates once each.
type telegramFake struct {
	mu      sync.Mutex
	updates []string
	calls   []string // tool + " " + args
	nextID  int64
}

func (f *telegramFake) call(_ context.Context, _ *App, tool string, args any) contract.CallLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool+" "+string(jsonBytes(args)))
	ok := func(result string) contract.CallLine {
		return contract.CallLine{Outcome: contract.CallOutcomeOk, Output: json.RawMessage(`{"ok":true,"result":` + result + `}`)}
	}
	switch tool {
	case "get_me":
		return ok(`{"id":777,"is_bot":true,"username":"fleet_test_bot"}`)
	case "get_updates":
		out := "[" + strings.Join(f.updates, ",") + "]"
		f.updates = nil
		return ok(out)
	case "send_message":
		f.nextID++
		return ok(`{"message_id":` + jsonText64(9000+f.nextID) + `}`)
	}
	return ok(`true`)
}

func jsonText64(n int64) string { return string(jsonBytes(n)) }

func (f *telegramFake) sent(tool string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		if strings.HasPrefix(c, tool+" ") {
			out = append(out, c)
		}
	}
	return out
}

func telegramTestEnv(t *testing.T) (*testCoordEnv, *telegramFake) {
	e := openTestCoordinator(t, false)
	f := &telegramFake{}
	prevCall, prevReady, prevChoose := telegramCall, telegramReady, conductorChoose
	telegramCall = f.call
	telegramReady = func(context.Context, *App) (bool, error) { return true, nil }
	t.Cleanup(func() { telegramCall, telegramReady, conductorChoose = prevCall, prevReady, prevChoose })
	return e, f
}

func (e *testCoordEnv) telegramPass(t *testing.T, queue string) {
	t.Helper()
	if err := kitOf(e.app).passQueue(e.ctx, kitQueueNamed(queue)); err != nil {
		t.Fatalf("%s: %v", queue, err)
	}
}

func (e *testCoordEnv) telegramUpdate(t *testing.T, id string) db.TelegramUpdate {
	t.Helper()
	var u db.TelegramUpdate
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) { u, err = q.db().TelegramUpdateGet(q.ctx(), id); return err }); err != nil {
		t.Fatal(err)
	}
	return u
}

func tgText(update int64, chat int64, chatType string, thread, from, message int64, text string) string {
	m := map[string]any{"message_id": message, "from": map[string]any{"id": from, "first_name": "Al", "language_code": "ru"},
		"chat": map[string]any{"id": chat, "type": chatType, "title": "Room"}, "text": text}
	if thread != 0 {
		m["message_thread_id"], m["is_topic_message"] = thread, true
	}
	return string(jsonBytes(map[string]any{"update_id": update, "message": m}))
}

// A linked person's DM reaches their home conversation as sent by them with source telegram, once, even when the
// update arrives again; a stranger gets no reply.
func TestTelegramDMReachesHomeConversationOnce(t *testing.T) {
	e, f := telegramTestEnv(t)
	var linked contract.TelegramPersonSetReply
	e.opInto(t, "telegram_person_set", map[string]any{"telegramUserId": 42, "language": "ru"}, e.ownerHeaders(), &linked)
	if linked.Person == nil || linked.Person.Email != testOwnerEmail {
		t.Fatalf("link: %+v", linked)
	}
	f.updates = []string{tgText(100, 42, "private", 0, 42, 1, "привет, Fleet"), tgText(101, 99, "private", 0, 99, 1, "who are you")}
	if err := telegramPoll(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	// The same update again (a lost confirmation): the journal keeps one row.
	f.updates = []string{tgText(100, 42, "private", 0, 42, 1, "привет, Fleet")}
	if err := telegramPoll(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if got := f.sent("get_updates"); !strings.Contains(got[1], `"offset":102`) {
		t.Fatalf("second poll confirms what the journal holds: %v", got)
	}
	e.telegramPass(t, "telegram-intake")
	if u := e.telegramUpdate(t, "tg:100"); u.Outcome != "delivered" {
		t.Fatalf("outcome %q", u.Outcome)
	}
	if u := e.telegramUpdate(t, "tg:101"); u.Outcome != "unknown-sender" {
		t.Fatalf("stranger outcome %q", u.Outcome)
	}
	var tp db.TelegramPerson
	var msgs []agentMessage
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) {
		if tp, err = q.db().TelegramPersonGet(q.ctx(), testOwnerEmail); err != nil || tp.HomeSessionID == nil {
			return err
		}
		msgs, err = messagesBySession(q, *tp.HomeSessionID, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if tp.HomeSessionID == nil || len(msgs) != 1 || msgs[0].Text != "привет, Fleet" || msgs[0].SentBy != testOwnerEmail {
		t.Fatalf("home %v messages %+v", tp.HomeSessionID, msgs)
	}
	e.telegramPass(t, "telegram-send")
	if r := f.sent("set_message_reaction"); len(r) != 1 || !strings.Contains(r[0], "👀") {
		t.Fatalf("receipt: %v", r)
	}
	if s := f.sent("send_message"); len(s) != 0 {
		t.Fatalf("nothing else reaches the chat yet: %v", s)
	}
	// The run is still working: the chat shows typing, no text.
	e.telegramPass(t, "telegram-reply")
	if s := f.sent("send_chat_action"); len(s) != 1 {
		t.Fatalf("typing: %v", f.calls)
	}
}

// In a group topic the conductor opens a work item for the first job, asks with one tap when unsure, and the tap
// sends the message to the chosen item.
func TestTelegramConductorOpensAsksAndAttaches(t *testing.T) {
	e, f := telegramTestEnv(t)
	var linked contract.TelegramPersonSetReply
	e.opInto(t, "telegram_person_set", map[string]any{"telegramUserId": 42, "language": "en"}, e.ownerHeaders(), &linked)
	f.updates = []string{tgText(200, -5, "supergroup", 7, 42, 10, "Black Friday mockups please")}
	if err := telegramPoll(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	e.telegramPass(t, "telegram-intake")
	first := e.telegramUpdate(t, "tg:200")
	if first.Outcome != "delivered" || first.WorkID == nil {
		t.Fatalf("first job: %+v", first)
	}
	conductorChoose = func(context.Context, *App, []db.TelegramWork, string, string) conductorDecision {
		return conductorDecision{Choice: "ask"}
	}
	f.updates = []string{tgText(201, -5, "supergroup", 7, 42, 11, "show mobile also")}
	if err := telegramPoll(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	e.telegramPass(t, "telegram-intake")
	if u := e.telegramUpdate(t, "tg:201"); u.Outcome != "asked" {
		t.Fatalf("unsure: %q", u.Outcome)
	}
	for range 6 { // one item per chat per pass, in order
		e.telegramPass(t, "telegram-send")
	}
	asks := f.sent("send_message")
	ask := ""
	for _, s := range asks {
		if strings.Contains(s, "Is this about") {
			ask = s
		}
	}
	if !strings.Contains(ask, `"callback_data":"w:tg:201:0"`) || !strings.Contains(ask, `"message_thread_id":7`) {
		t.Fatalf("one-tap question in the topic: %v", asks)
	}
	tap := string(jsonBytes(map[string]any{"update_id": 202, "callback_query": map[string]any{"id": "cb1", "from": map[string]any{"id": 42},
		"data": "w:tg:201:0", "message": map[string]any{"message_id": 9001, "chat": map[string]any{"id": -5, "type": "supergroup"}, "message_thread_id": 7, "is_topic_message": true}}}))
	f.updates = []string{tap}
	if err := telegramPoll(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	e.telegramPass(t, "telegram-intake")
	if u := e.telegramUpdate(t, "tg:201"); u.Outcome != "delivered" || deref(u.WorkID) != deref(first.WorkID) {
		t.Fatalf("tap attaches to the first job: %+v", u)
	}
	var msgs []agentMessage
	var queued []agentQueued
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		w, err := q.db().TelegramWorkGet(q.ctx(), *first.WorkID)
		if err != nil {
			return err
		}
		if msgs, err = messagesBySession(q, w.SessionID, nil); err != nil {
			return err
		}
		// The first job's run is still working, so the follow-up waits in its queue.
		queued, err = queuedMessages(q, db.QueuedMessagesParams{SessionID: &w.SessionID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	texts := []string{}
	for _, m := range msgs {
		texts = append(texts, m.Text)
	}
	for _, m := range queued {
		if m.SentBy != testOwnerEmail || m.Source != "telegram" {
			t.Fatalf("queued as %q from %q", m.SentBy, m.Source)
		}
		texts = append(texts, m.Text)
	}
	if !strings.Contains(strings.Join(texts, "|"), "show mobile also") {
		t.Fatalf("work item messages: %v", texts)
	}
}

func TestTelegramChunksKeepEveryRune(t *testing.T) {
	long := strings.Repeat("строка текста\n", 700)
	parts := chunks(long, telegramChunk)
	if len(parts) < 2 {
		t.Fatal("not split")
	}
	total := 0
	for _, p := range parts {
		if n := len([]rune(p)); n > telegramChunk {
			t.Fatalf("part of %d runes", n)
		}
		total += len([]rune(strings.ReplaceAll(p, "\n", "")))
	}
	if total != len([]rune(strings.ReplaceAll(long, "\n", ""))) {
		t.Fatal("runes lost")
	}
}

// The owner sets a room up before the bot has seen it: telegram_room_set with chatId creates it.
func TestTelegramRoomSeededAhead(t *testing.T) {
	e, _ := telegramTestEnv(t)
	var out contract.TelegramRoomSetReply
	e.opInto(t, "telegram_room_set", map[string]any{"chatId": -100, "threadId": 3, "projectHint": "", "title": "pilot"}, e.ownerHeaders(), &out)
	if out.Room.Id != "-100:3" || out.Room.Kind != "supergroup" || out.Room.Title != "pilot" {
		t.Fatalf("seeded room: %+v", out.Room)
	}
}
