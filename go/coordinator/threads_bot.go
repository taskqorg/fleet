package main

// Work-item threads of a conversation (Conversations v2, owner pick b2; docs/designs/fleet-conversations.md#work-item-threads).
// A bot's Home session holds one thread per work item: a task, a question, a pull request, or a topic a person started.
// The thread key travels on the queued and sent message and on the execution (field thread); each thread continues its
// own native session (continuation key + "#" + thread), so a turn about one item never carries another item's context.
// A thread with no new event is idle: it simply has no turn; the next event on its item resumes its native session.
// ("Park" in Fleet means only host stickiness: a session keeps the host it started on, dispatch.go.)
// The empty key is the session's main thread, as before.

import (
	"encoding/json"

	"fleet/contract"
)

// threadContinuation: the continuation identity of a thread's turns.
func threadContinuation(key, thread string) string {
	if thread == "" {
		return key
	}
	return key + "#" + thread
}

// threadNative: the native session a thread continues, from nativeSessions (nil when the thread has none yet).
func (s *dispatchSession) threadNative(key string) *string {
	var all map[string]agentNative
	if raw, ok := s.rest["nativeSessions"]; !ok || json.Unmarshal(raw, &all) != nil {
		return nil
	}
	if n, ok := all[key]; ok && n.NativeSessionID != "" {
		return &n.NativeSessionID
	}
	return nil
}

// changeThread: the work item a change row belongs to (watch.go classify names the same row): a task, a question, a
// pull request or an execution keeps its own thread; host and limit signals share one Fleet thread.
func changeThread(c contract.Change) string {
	switch k := string(c.Kind); k {
	case "question", "execution": // a question or a run of a task belongs to the task's thread
		if c.TaskId != nil && *c.TaskId != "" {
			return "task:" + *c.TaskId
		}
		return k + ":" + c.Id
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
