package main

import (
	"strings"
	"testing"

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
