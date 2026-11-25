// The Telegram channel's commands (docs/designs/fleet-telegram.md, Commands): one registry, telegramCommands, is the
// only source of what a command is called, where it works, what it does and how it reads in each language. Dispatch
// (telegramCommand), /help and the bot's command menu (setMyCommands, per language and chat kind) are all built from
// it, so help never drifts from what the channel does; TestTelegramCommandRegistry checks every command has every
// language. PilotQ built /help from such a catalogue (catalog.rs); Fleet adds the menu.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// tgCommand: one command of the channel.
type tgCommand struct {
	name   string
	where  string // "any", "private" or "group": the chats it works in and is offered in
	hidden bool   // works, but neither /help nor the menu lists it
	words  map[string]tgCommandWords
	run    func(x *tgCommandCall) error
}

// tgCommandWords: a command in one language: its argument hint and what it does, for /help and the menu.
type tgCommandWords struct{ args, does string }

// tgCommandCall: one command a person sent.
type tgCommandCall struct {
	q     *Request
	now   time.Time
	c     *kitClaim
	row   *db.TelegramUpdate
	room  db.TelegramRoom
	m     *tgMessage
	tp    db.TelegramPerson
	p     *Person
	args  []string
	lang  string
	reply func(body string) error
}

var telegramCommands []tgCommand

func init() {
	telegramCommands = []tgCommand{
		{name: "help", where: "any", run: func(x *tgCommandCall) error { return x.reply(telegramHelp(x.lang, x.room.Kind)) },
			words: map[string]tgCommandWords{"en": {"", "what works here"}, "ru": {"", "что здесь работает"}}},
		{name: "start", where: "any", hidden: true, run: func(x *tgCommandCall) error { return x.reply(telegramHelp(x.lang, x.room.Kind)) },
			words: map[string]tgCommandWords{"en": {"", "what works here"}, "ru": {"", "что здесь работает"}}},
		{name: "now", where: "any", run: func(x *tgCommandCall) error {
			body, err := telegramGlance(x.q, x.now, x.room, x.tp)
			if err != nil {
				return err
			}
			return x.reply(body)
		}, words: map[string]tgCommandWords{"en": {"", "where the work here stands"}, "ru": {"", "как дела у работы здесь"}}},
		{name: "needs", where: "any", run: func(x *tgCommandCall) error {
			n, err := telegramNeeds(x.q, x.row, x.room, x.tp)
			if err != nil || n > 0 {
				return err
			}
			return x.reply(say(x.lang, "noNeeds", ""))
		}, words: map[string]tgCommandWords{"en": {"", "open questions, with buttons"}, "ru": {"", "открытые вопросы с кнопками"}}},
		{name: "status", where: "any", run: func(x *tgCommandCall) error {
			if len(x.args) > 0 {
				body, err := telegramTaskStatus(x.q, x.tp, x.args[0])
				if err != nil {
					return err
				}
				return x.reply(body)
			}
			return telegramStatusAnswer(x.q, x.now, x.row, x.room, x.m, x.tp, nil)
		}, words: map[string]tgCommandWords{"en": {"[task]", "where a task or the work here stands"}, "ru": {"[задача]", "как дела у задачи или у работы здесь"}}},
		{name: "stop", where: "any", run: telegramStop,
			words: map[string]tgCommandWords{"en": {"", "stop the runs answering this chat"}, "ru": {"", "остановить запуски, которые отвечают в этот чат"}}},
		{name: "new", where: "private", run: func(x *tgCommandCall) error {
			if err := x.q.db().TelegramPersonSetHome(x.q.ctx(), db.TelegramPersonSetHomeParams{Email: x.tp.Email}); err != nil {
				return err
			}
			return x.reply(say(x.lang, "newHome", ""))
		}, words: map[string]tgCommandWords{"en": {"", "start a new conversation"}, "ru": {"", "начать новый разговор"}}},
		{name: "version", where: "any", run: func(x *tgCommandCall) error { return x.reply(say(x.lang, "version", version)) },
			words: map[string]tgCommandWords{"en": {"", "the running Fleet version"}, "ru": {"", "запущенная версия Fleet"}}},
	}
	for lang, words := range map[string]map[string]string{
		"en": {
			"helpIntro": "Write as usual: each message goes to its piece of work, and an agent answers here.",
			"helpTail":  "So? or ?? answers from Fleet without waking anyone. Reply to a question to answer it.",
			"version":   "Fleet `%s`",
			"noRun":     "Nothing is running for this chat.",
			"stopped":   "Stop requested.",
		},
		"ru": {
			"helpIntro": "Пишите как обычно: каждое сообщение попадает в свою работу, агент отвечает здесь.",
			"helpTail":  "«Ну?» или «??» отвечаются из Fleet, никого не будя. Ответьте на вопрос реплаем, чтобы ответить.",
			"version":   "Fleet `%s`",
			"noRun":     "Для этого чата ничего не запущено.",
			"stopped":   "Остановка запрошена.",
		},
	} {
		for k, v := range words {
			telegramWords[lang][k] = v
		}
	}
}

// telegramCommandFor: the registry entry a chat kind may use, or nil.
func telegramCommandFor(name, kind string) *tgCommand {
	for i := range telegramCommands {
		c := &telegramCommands[i]
		if c.name == name && tgCommandIn(c, kind) {
			return c
		}
	}
	return nil
}

func tgCommandIn(c *tgCommand, kind string) bool {
	switch c.where {
	case "private":
		return kind == "private"
	case "group":
		return kind != "private"
	}
	return true
}

// tgWordsOf: a command's words in a language, English when it has none.
func tgWordsOf(c *tgCommand, lang string) tgCommandWords {
	if w, ok := c.words[lang]; ok {
		return w
	}
	return c.words["en"]
}

// telegramHelp renders /help for a language and chat kind from the registry.
func telegramHelp(lang, kind string) string {
	lines := []string{say(lang, "helpIntro", ""), ""}
	for i := range telegramCommands {
		c := &telegramCommands[i]
		if c.hidden || !tgCommandIn(c, kind) {
			continue
		}
		w := tgWordsOf(c, lang)
		head := "/" + c.name
		if w.args != "" {
			head += " " + w.args
		}
		lines = append(lines, "- "+head+": "+w.does)
	}
	return strings.Join(append(lines, "", say(lang, "helpTail", "")), "\n")
}

// telegramCommand runs a command of the registry; false when the text is no command of the channel (it then goes to
// the work as any message).
func telegramCommand(q *Request, now time.Time, c *kitClaim, row *db.TelegramUpdate, room db.TelegramRoom, m *tgMessage, tp db.TelegramPerson, p *Person, text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false, nil
	}
	name, bot, _ := strings.Cut(strings.TrimPrefix(fields[0], "/"), "@")
	telegramHealth.Lock()
	me := telegramHealth.botUsername
	telegramHealth.Unlock()
	if bot != "" && !strings.EqualFold(bot, me) {
		return false, nil
	}
	cmd := telegramCommandFor(strings.ToLower(name), room.Kind)
	if cmd == nil {
		return false, nil
	}
	x := &tgCommandCall{q: q, now: now, c: c, row: row, room: room, m: m, tp: tp, p: p, args: fields[1:], lang: tp.Language}
	x.reply = func(body string) error {
		return telegramOutbox(q, db.TelegramOutboxAddParams{ID: "cmd:" + row.ID, ChatID: room.ChatID, ThreadID: room.ThreadID, Kind: "text", ReplyTo: &m.MessageID, Text: body})
	}
	return true, cmd.run(x)
}

// telegramStop (/stop): a stop request, as the person, for each unfinished run answering a message of this chat (a
// group room's topic, or the private chat). The confirmation follows the stops (telegramIntakeStep, tgDelivered.notice).
func telegramStop(x *tgCommandCall) error {
	turns, err := x.q.db().TelegramTurnsInRoom(x.q.ctx(), db.TelegramTurnsInRoomParams{ChatID: x.room.ChatID, ThreadID: x.room.ThreadID})
	if err != nil {
		return err
	}
	runs := map[string]bool{}
	for _, t := range turns {
		ms, err := messagesBySession(x.q, t.SessionID, &t.RequestID)
		if err != nil {
			return err
		}
		if len(ms) == 0 || ms[0].ExecutionID == "" {
			continue
		}
		e, err := getExecution(x.q, ms[0].ExecutionID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && e.terminal() {
			continue
		} else if err != nil {
			return err
		}
		runs[e.ID] = true
	}
	if len(runs) == 0 {
		return x.reply(say(x.lang, "noRun", ""))
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	a, p := x.q.app, x.p
	x.c.Call = func(ctx context.Context) any {
		out := tgDelivered{notice: "stopped"}
		pq := scopedRequest(ctx, a, p)
		out.err = a.store.Write(ctx, pq, []string{GLOBAL}, func(pq *Request) error {
			if err := refreshRequestAccess(pq); err != nil {
				return err
			}
			for _, id := range ids {
				pq.body = jsonBytes(contract.ExecutionStopRequest{ExecutionId: id})
				if _, err := operations["execution_stop"].h(pq); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
			}
			return nil
		})
		return out
	}
	return nil
}

// telegramMenu sets the bot's command menu from the registry: for private chats and for groups, in each language
// Fleet speaks (English also as the default for other languages). Once per coordinator start, after getMe.
func telegramMenu(ctx context.Context, a *App) error {
	langs := make([]string, 0, len(telegramWords))
	for lang := range telegramWords {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, kind := range []string{"private", "group"} {
		scope := map[string]string{"type": "all_private_chats"}
		if kind == "group" {
			scope["type"] = "all_group_chats"
		}
		for _, lang := range append([]string{""}, langs...) {
			commands := []map[string]string{}
			for i := range telegramCommands {
				c := &telegramCommands[i]
				if c.hidden || !tgCommandIn(c, kind) {
					continue
				}
				w := tgWordsOf(c, or(lang, "en"))
				commands = append(commands, map[string]string{"command": c.name, "description": w.does})
			}
			args := tgArgs{Commands: commands, Scope: scope, LanguageCode: lang}
			if _, err := tgParse(telegramCall(ctx, a, "set_my_commands", args)); err != nil {
				return err
			}
		}
	}
	return nil
}
