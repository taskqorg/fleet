// Queue advance: a queued task that nothing has started gets a worker session (fleet.md "the queue advances
// automatically"; docs/designs/fleet-pm-agent.md §4a).
package main

import (
	"fmt"
	"log"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// noWorkerRole: the wait reason of a queued task whose project maps no profile to the worker role.
const noWorkerRole = "no worker role"

// workerStartRefused: the wait reason of a queued task whose worker session_start was refused (the log has why); the
// next explicit dispatch pass tries again.
const workerStartRefused = "worker start refused"

// taskAutoStart starts a worker session for every queued task with no primary execution, no session and no open
// blocking dependency. The session is the one session_start {projectId, taskId, role: worker} makes, directed by the
// task's director, with the task as its first message; the dispatch pass that follows places its execution under the
// ordinary placement rules. requestId autostart:<task> makes a repeat a replay. A project without a worker role leaves
// the task queued with wait reason "no worker role"; any other refusal leaves it queued with "worker start refused" and
// is logged once, and the other tasks still start. A task with either reason is retried on explicit passes only.
func taskAutoStart(q *Request) error {
	rows, err := q.db().DispStartableTasks(q.ctx())
	if err != nil || len(rows) == 0 {
		return err
	}
	for _, t := range rows {
		if !profileForRole(projectSettingsOf(q, t.ProjectID), "worker") {
			if err := q.db().DispTaskSetWaitReason(q.ctx(), db.DispTaskSetWaitReasonParams{ID: t.ID, WaitReason: ptr(noWorkerRole)}); err != nil {
				return err
			}
			continue
		}
		if err := exec(q, "SAVEPOINT task_autostart"); err != nil {
			return err
		}
		if err := autoStartTask(q, t); err != nil {
			if e := exec(q, "ROLLBACK TO SAVEPOINT task_autostart"); e != nil {
				return e
			}
			if t.WaitReason != workerStartRefused {
				log.Printf("task autostart %s: %v", firstString(t.Code, t.ID), err)
			}
			if err := q.db().DispTaskSetWaitReason(q.ctx(), db.DispTaskSetWaitReasonParams{ID: t.ID, WaitReason: ptr(workerStartRefused)}); err != nil {
				return err
			}
			continue
		}
		if err := exec(q, "RELEASE SAVEPOINT task_autostart"); err != nil {
			return err
		}
	}
	return nil
}

func profileForRole(settings rawObject, role string) bool {
	return settings != nil && presetSettingsOf(settings).Roles[role] != ""
}

func autoStartTask(q *Request, t db.DispStartableTasksRow) error {
	director := q.peopleSnapshot().person(t.DirectedBy)
	if director == nil {
		director = q.peopleSnapshot().person(ownerEmailOf(q.app))
	}
	text := autoStartText(t)
	r := contract.SessionStartRequest{ProjectId: &t.ProjectID, TaskId: &t.ID, Role: ptr("worker"), RequestId: ptr("autostart:" + t.ID), Text: &text}
	person, body := q.person, q.body
	q.person, q.body = director, jsonBytes(r)
	defer func() { q.person, q.body = person, body }()
	if _, err := sessionStart(q, &r); err != nil {
		return err
	}
	if t.WaitReason != "" {
		return q.db().DispTaskSetWaitReason(q.ctx(), db.DispTaskSetWaitReasonParams{ID: t.ID})
	}
	return nil
}

// autoStartText: the worker's first message, the task as written.
func autoStartText(t db.DispStartableTasksRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\n", firstString(t.Code, t.ID), t.Title)
	if s := strings.TrimSpace(t.ExpectedOutcome); s != "" {
		fmt.Fprintf(&b, "\nExpected outcome:\n%s\n", s)
	}
	b.WriteString("\nFleet started this session because the task was queued and nothing had started it. Do the task under the project rules.")
	return b.String()
}
