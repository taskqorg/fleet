package main

import (
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
		{changeThread(contract.Change{Kind: "execution", Id: "e2", HostId: task("pds18")}), "execution e2 failed"},
		{changeThread(contract.Change{Kind: "execution", Id: "e3", HostId: task("pds18")}), "execution e3 failed"},
	}
	keys, lines := threadGroups(parts)
	if want := []string{"task:t1", "question:q2", "task:t2", "host:pds18"}; !slices.Equal(keys, want) {
		t.Fatalf("threads %v, want %v", keys, want)
	}
	if want := []string{"task t1 now held", "question q1 on task t1", "task t1 now in_progress"}; !slices.Equal(lines["task:t1"], want) {
		t.Fatalf("one item's changes in one turn: %v", lines["task:t1"])
	}
	// A run is never a subject: taskless failures on one host join that host's line of work.
	if len(lines["host:pds18"]) != 3 {
		t.Fatalf("host subject: %v", lines["host:pds18"])
	}
	if threadTitle("host:pds18", "") != "Host pds18" || threadTitle("topic:9f2c", "Release P90 grew") != "Release P90 grew" || threadTitle("topic:9f2c", "") != "Conversation" {
		t.Fatal("a raw key is never a title")
	}
}
