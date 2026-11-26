package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: a held task records who held it (fleet-pm-agent.md §4): a held create and task_hold set the holder,
// task_get and task_page return it, and release clears it with the held reason. Risk: the row names no holder, or a
// released task keeps a stale one.
func TestTaskHolder(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	var created contract.TaskDetail
	e.opInto(t, "task_create", map[string]any{"title": "held at create", "held": true}, h, &created)
	if deref(created.PauseReason) != "held" || deref(created.HeldBy) == "" || deref(created.HeldByName) == "" {
		t.Fatalf("held create: %v %v %v", created.PauseReason, created.HeldBy, created.HeldByName)
	}

	var task contract.TaskDetail
	e.opInto(t, "task_create", map[string]any{"title": "held later"}, h, &task)
	if task.HeldBy != nil {
		t.Fatalf("queued task has a holder: %v", *task.HeldBy)
	}
	var held contract.TaskDetail
	e.opInto(t, "task_hold", map[string]any{"taskId": task.Id}, h, &held)
	if deref(held.HeldBy) != deref(created.HeldBy) || deref(held.HeldByName) != deref(created.HeldByName) {
		t.Fatalf("task_hold: %v %v, want %v %v", held.HeldBy, held.HeldByName, created.HeldBy, created.HeldByName)
	}
	var page contract.TaskPageReply
	e.opInto(t, "task_page", map[string]any{"filter": map[string]any{"state": []string{"held"}}}, h, &page)
	if len(page.Tasks) != 2 || deref(page.Tasks[0].HeldByName) != deref(created.HeldByName) || deref(page.Tasks[1].HeldByName) != deref(created.HeldByName) {
		t.Fatalf("task_page held rows: %+v", page.Tasks)
	}

	var released contract.TaskDetail
	e.opInto(t, "task_release", map[string]any{"taskId": task.Id}, h, &released)
	if released.PauseReason != nil || released.HeldBy != nil || released.HeldByName != nil {
		t.Fatalf("release keeps the holder: %v %v %v", released.PauseReason, released.HeldBy, released.HeldByName)
	}
}
