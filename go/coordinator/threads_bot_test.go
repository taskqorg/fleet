package main

import (
	"encoding/json"
	"slices"
	"testing"

	"fleet/contract"
)

// Risk: a bot's turns about different work items share one native session, so each turn carries every other item's
// context (HAL0 2026-11-25: 248 turns a day in one thread; owner pick b2, one turn per item).
func TestThreadGroupsOneTurnPerItem(t *testing.T) {
	task := func(id string) *string { return &id }
	parts := []threadPart{
		{changeThread(contract.Change{Kind: "task", Id: "t1"}), "task t1 now held"},
		{changeThread(contract.Change{Kind: "question", Id: "q1", TaskId: task("t1")}), "question q1 on task t1"},
		{changeThread(contract.Change{Kind: "question", Id: "q2"}), "question q2 on task none"},
		{changeThread(contract.Change{Kind: "execution", Id: "e1", TaskId: task("t2")}), "execution e1 failed"},
		{changeThread(contract.Change{Kind: "task", Id: "t1"}), "task t1 now in_progress"},
		{changeThread(contract.Change{Kind: "host", Id: "pds18"}), "host pds18 offline"},
	}
	keys, lines := threadGroups(parts)
	if want := []string{"task:t1", "question:q2", "task:t2", "fleet:host"}; !slices.Equal(keys, want) {
		t.Fatalf("threads %v, want %v", keys, want)
	}
	if want := []string{"task t1 now held", "question q1 on task t1", "task t1 now in_progress"}; !slices.Equal(lines["task:t1"], want) {
		t.Fatalf("one item's changes in one turn: %v", lines["task:t1"])
	}
}

func TestThreadNativeSession(t *testing.T) {
	if threadContinuation("codex::fleet::", "") != "codex::fleet::" || threadContinuation("codex::fleet::", "task:t1") != "codex::fleet::#task:t1" {
		t.Fatal("a thread's continuation identity extends the session's; the main thread keeps it")
	}
	s := dispatchSession{rest: recordRest{"nativeSessions": json.RawMessage(`{"codex::fleet::":{"nativeSessionId":"main"},"codex::fleet::#task:t1":{"nativeSessionId":"n1"}}`)}}
	if n := s.threadNative("codex::fleet::#task:t1"); n == nil || *n != "n1" {
		t.Fatalf("an idle thread resumes its own native session, got %v", n)
	}
	if n := s.threadNative("codex::fleet::#task:t2"); n != nil {
		t.Fatalf("a new thread starts fresh, got %v", *n)
	}
}
