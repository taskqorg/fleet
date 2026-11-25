package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// Risk: a stopped request stays in the native session unanswered, and the next turn resumes it and does it anyway
// (F21, Telegram DM 2026-11-25: "/stop" on a list request, then the next answer appended that list).
func TestNextTurnSeesStoppedRequest(t *testing.T) {
	e := openTestCoordinator(t, false)
	sid := "stop-note-session"
	put := func(q *Request, id, state string, stopped bool, thread, text string) {
		t.Helper()
		x := agentExecution{ID: id, ExecutionID: id, SessionID: sid, State: state, CreatedAt: nowMs(), Thread: thread}
		if stopped {
			x.StopRequested = ptr(true)
		}
		if err := executionPut(q, &x); err != nil {
			t.Fatal(err)
		}
		m := agentMessage{ID: "m-" + id, SessionID: sid, ExecutionID: id, RequestID: "r-" + id, Text: text, Thread: thread}
		if err := messagePut(q, &m); err != nil {
			t.Fatal(err)
		}
	}
	note := func(q *Request, id string) string {
		t.Helper()
		p := contract.StartPayload{Text: ptr("Что в Needs you?")}
		if err := noteStoppedRequest(q, id, &p); err != nil {
			t.Fatal(err)
		}
		return deref(p.Text)
	}
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		s := agentSession{ID: sid, AgentConfig: []byte(`{"harness":"codex"}`)}
		if err := sessionPut(q, &s); err != nil {
			return err
		}
		put(q, "e1", "succeeded", false, "", "Привет")
		put(q, "e2", "cancelled", true, "", "Перечисли все завершённые задачи за ноябрь")
		put(q, "e3", "preparing", false, "", "Что в Needs you?")
		got := note(q, "e3")
		if !strings.HasPrefix(got, "[Fleet: the person stopped the previous request") || !strings.Contains(got, "Перечисли все завершённые задачи за ноябрь") ||
			!strings.HasSuffix(got, "\n\nЧто в Needs you?") {
			t.Fatalf("the turn after a stop names the stopped request first, got %q", got)
		}
		put(q, "e4", "succeeded", false, "", "ok")
		put(q, "e5", "preparing", false, "", "дальше")
		if got := note(q, "e5"); got != "Что в Needs you?" {
			t.Fatalf("only the turn right after the stop carries the note, got %q", got)
		}
		put(q, "e6", "cancelled", true, "task:t1", "работа t1")
		put(q, "e7", "preparing", false, "", "главный тред")
		if got := note(q, "e7"); got != "Что в Needs you?" {
			t.Fatalf("a stop on another thread does not reach this one, got %q", got)
		}
		put(q, "e8", "preparing", false, "task:t1", "снова t1")
		if got := note(q, "e8"); !strings.Contains(got, "работа t1") {
			t.Fatalf("the thread's own next turn sees its stop, got %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
