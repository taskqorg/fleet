package main

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"fleet/coordinator/db"
)

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

func TestTelegramMessageLink(t *testing.T) {
	for _, c := range []struct {
		chat, thread, msg int64
		want              string
	}{
		{-1003995096980, 3, 42, "https://t.me/c/3995096980/3/42"},
		{-1003995096980, 0, 42, "https://t.me/c/3995096980/42"},
		{134142744, 0, 42, ""},
	} {
		if got := telegramMessageLink(c.chat, c.thread, c.msg); got != c.want {
			t.Errorf("%d/%d/%d: %q, want %q", c.chat, c.thread, c.msg, got, c.want)
		}
	}
}

func TestTelegramStatusPing(t *testing.T) {
	for text, want := range map[string]bool{"So?": true, "??": true, "sooo?": true, "ну?": true, "Что там?": true, "is it live?": true,
		"so all is live?": true, "make mockup": false, "show mobile also": false, "so ull do it or?": false, "ok": false} {
		if isStatusPing(text) != want {
			t.Errorf("%q: %v", text, !want)
		}
	}
}

func TestTelegramPlainHasNoMarks(t *testing.T) {
	if got := tgPlain("**1.261125.63** see [PR](https://x/1) and `code`"); got != "1.261125.63 see PR (https://x/1) and code" {
		t.Fatal(got)
	}
}

func TestTelegramApprovalText(t *testing.T) {
	for text, want := range map[string]bool{"good u can live": true, "Go live": true, "live it, works": true, "use it i allow": true, "давай": true,
		"выкатывай": true, "not now": false, "don't release it yet": false, "не выкатывай": false, "make mockup": false, "show mobile also": false} {
		if isApprovalText(text) != want {
			t.Errorf("%q: %v", text, !want)
		}
	}
}

// One registry drives dispatch, /help and the menu: every command reads in every language Fleet speaks, fits the
// menu's limits, and /help lists exactly the commands a chat kind offers.
func TestTelegramCommandRegistry(t *testing.T) {
	name := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	seen := map[string]bool{}
	for _, c := range telegramCommands {
		if !name.MatchString(c.name) || seen[c.name] || c.run == nil {
			t.Errorf("command %q: bad name, repeated or without a handler", c.name)
		}
		seen[c.name] = true
		for lang := range telegramWords {
			w, ok := c.words[lang]
			if !ok || w.does == "" || utf8.RuneCountInString(w.does) > 256 {
				t.Errorf("command %s: no or too long words in %s", c.name, lang)
			}
		}
	}
	for lang := range telegramWords {
		for _, key := range []string{"helpIntro", "helpTail", "noRun", "stopped", "version", "newHome", "noNeeds"} {
			if telegramWords[lang][key] == "" {
				t.Errorf("%s: no word %s", lang, key)
			}
		}
		for _, kind := range []string{"private", "supergroup"} {
			help := telegramHelp(lang, kind)
			for _, c := range telegramCommands {
				listed := strings.Contains(help, "- /"+c.name)
				if listed != (!c.hidden && tgCommandIn(&c, kind)) {
					t.Errorf("%s %s: /help lists /%s = %v", lang, kind, c.name, listed)
				}
				if telegramCommandFor(c.name, kind) == nil == tgCommandIn(&c, kind) {
					t.Errorf("%s: dispatch of /%s disagrees with where it works", kind, c.name)
				}
			}
		}
	}
	if !strings.Contains(telegramHelp("ru", "private"), "/new: начать новый разговор") || strings.Contains(telegramHelp("ru", "supergroup"), "/new") {
		t.Error("ru help: /new is offered only in private chats")
	}
}
