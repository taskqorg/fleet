package main

// Subjects of bot work (docs/designs/fleet-conversations.md#rules): a trigger groups its changes by subject and sends
// one message to the bot's conversation for each subject (triggers.go, bots_runtime.go botConversation). Each
// conversation keeps its own native session, so a turn about one item never carries another item's context.

import (
	"fleet/contract"
)

// changeThread: the subject a change row belongs to (watch.go classify names the same row;
// fleet-conversations.md#rules): a task, a question or a delivery keeps its own; a question or a run of a task belongs
// to the task. A run is never a subject: a run without a task belongs to its host, so repeated failures of one host
// are one line of work. Limit signals share one Fleet subject.
func changeThread(c contract.Change) string {
	switch k := string(c.Kind); k {
	case "question", "execution":
		if c.TaskId != nil && *c.TaskId != "" {
			return "task:" + *c.TaskId
		}
		if k == "execution" {
			if c.HostId != nil && *c.HostId != "" {
				return "host:" + *c.HostId
			}
			return "fleet"
		}
		return k + ":" + c.Id
	case "host":
		return "host:" + c.Id
	case "task", "delivery":
		return k + ":" + c.Id
	case "":
		return "fleet"
	default:
		return "fleet:" + k
	}
}

// threadPart: one change line and the thread it goes to.
type threadPart struct {
	Thread, Text string
}

// threadGroups: the change lines grouped by thread in the order their threads first appear; several changes of one
// item in one check go to that item's one turn.
func threadGroups(parts []threadPart) (keys []string, lines map[string][]string) {
	lines = map[string][]string{}
	for _, p := range parts {
		if _, ok := lines[p.Thread]; !ok {
			keys = append(keys, p.Thread)
		}
		lines[p.Thread] = append(lines[p.Thread], p.Text)
	}
	return keys, lines
}

// texts: the change lines alone.
func texts(parts []threadPart) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.Text
	}
	return out
}
